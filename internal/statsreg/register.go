package statsreg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
)

// Registration stages, recorded in the receipt so an interrupted run can resume.
const (
	stagePrepared    = "prepared"    // device transaction sent to COMMIT; outcome unknown
	stageDevices     = "devices"     // devices/interfaces committed, no checkpoints yet
	stageCheckpoints = "checkpoints" // everything committed
)

type Result struct {
	ID            string  `json:"id"`
	GroupID       int64   `json:"groupId"`
	DeviceIDs     []int64 `json:"deviceIds"`
	InterfaceIDs  []int64 `json:"interfaceIds"`
	CheckpointIDs []int64 `json:"checkpointIds"`
	// NewDeviceIDs: devices created by this run (all of DeviceIDs for a new
	// registration; only new servers' devices for an update).
	NewDeviceIDs []int64 `json:"newDeviceIds"`
	Update       bool    `json:"update"`
	Stage        string  `json:"stage"`
	Committed    bool    `json:"committed"`
	Notified     bool    `json:"notified"`
	// Collection check on liz.checkvalue.topic after the checkpoint notification.
	Verified  bool    `json:"verified"`
	Collected int     `json:"collected"`
	Missing   []int64 `json:"missing"`
	Message   string  `json:"message"`
}
type receipt struct {
	Result    Result
	DBHost    string
	DBPort    int
	Brokers   string
	GroupName string
	// Single-transaction receipts written before the two-phase split.
	Messages []json.RawMessage `json:",omitempty"`
	Sent     int               `json:",omitempty"`

	TwoPhase          bool
	Hosts             []string `json:",omitempty"` // Hosts[i] owns DeviceIDs[i] and InterfaceIDs[i]
	Points            []Point  `json:",omitempty"` // selected points, inserted in the second phase
	DeviceMessages    []json.RawMessage
	DeviceSent        int
	DevicesNotifiedAt time.Time
	PointMessages     []json.RawMessage
	PointSent         int
}

func (s *Service) session(id string) (session, error) {
	v, ok := s.sessions[id]
	if !ok || time.Now().After(v.Expires) {
		return v, fmt.Errorf("연결 분석이 만료됐습니다. 다시 연결 확인하세요")
	}
	return v, nil
}
func activeServers(sel Selection) []Server {
	used := map[string]bool{}
	for _, p := range sel.Points {
		if p.Selected {
			used[p.Host] = true
		}
	}
	var out []Server
	for _, h := range sel.Servers {
		if used[h.Hostname] {
			out = append(out, h)
		}
	}
	return out
}

// updateCluster makes an update's new devices use the group's collector cluster.
func updateCluster(ctx context.Context, q queryer, sel Selection, upd *ExistingGroup) (Selection, error) {
	if upd == nil {
		return sel, nil
	}
	c, e := groupCluster(ctx, q, upd.GroupID)
	if e != nil {
		return sel, e
	}
	if c != 0 {
		sel.ClusterID = c
	}
	return sel, nil
}
func checkLocation(ctx context.Context, q queryer, sel Selection) (int64, error) {
	var dc int64
	if e := q.QueryRowContext(ctx, "SELECT data_center_id FROM device_explorer WHERE id=?", sel.ExplorerID).Scan(&dc); e != nil {
		return 0, fmt.Errorf("장비 탐색기를 확인하세요: %w", e)
	}
	var cluster int64
	if e := q.QueryRowContext(ctx, "SELECT id FROM liz_cluster WHERE id=? AND cluster_type=3", sel.ClusterID).Scan(&cluster); e != nil {
		return 0, fmt.Errorf("Collector 클러스터를 확인하세요: %w", e)
	}
	return dc, nil
}

func (s *Service) Validate(ctx context.Context, sel Selection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.session(sel.PlanID)
	if e != nil {
		return e
	}
	if e = validateSelection(v.Plan, sel); e != nil {
		return e
	}
	db, e := openDB(ctx, v.Config)
	if e != nil {
		return e
	}
	defer db.Close()
	_, e = checkLocation(ctx, db, sel)
	return e
}
func insert(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	r, e := tx.ExecContext(ctx, query, args...)
	if e != nil {
		return 0, e
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return 0, fmt.Errorf("INSERT 대상이 없거나 여러 개입니다 (rows=%d, error=%v)", n, err)
	}
	return r.LastInsertId()
}

type auditShape struct {
	columns []string
	base    map[string]bool
}

// Copy entity snapshots into the existing Envers audit tables, inside the same
// transaction. Only observed schema column names are used as SQL identifiers.
func audit(ctx context.Context, tx *sql.Tx, cache map[string]auditShape, table string, id, rev int64, revtype int, entity string) error {
	shape, cached := cache[table]
	cols, base := shape.columns, shape.base
	if !cached {
		rows, e := tx.QueryContext(ctx, "SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='liz' AND TABLE_NAME=? ORDER BY ORDINAL_POSITION", table+"_aud")
		if e != nil {
			return e
		}
		for rows.Next() {
			var c string
			if e = rows.Scan(&c); e != nil {
				rows.Close()
				return e
			}
			cols = append(cols, c)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(cols) == 0 {
			return fmt.Errorf("감사 테이블이 없습니다: %s_aud", table)
		}
		rows, e = tx.QueryContext(ctx, "SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='liz' AND TABLE_NAME=?", table)
		if e != nil {
			return e
		}
		base = map[string]bool{}
		for rows.Next() {
			var c string
			if e = rows.Scan(&c); e != nil {
				rows.Close()
				return e
			}
			base[c] = true
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		cache[table] = auditShape{cols, base}
	}
	quoted := []string{}
	expr := []string{}
	args := []any{}
	for _, c := range cols {
		quoted = append(quoted, "`"+c+"`")
		switch {
		case c == "rev":
			expr = append(expr, "?")
			args = append(args, rev)
		case c == "revtype":
			expr = append(expr, "?")
			args = append(args, revtype)
		case base[c]:
			expr = append(expr, "`"+c+"`")
		case strings.HasSuffix(c, "_mod"):
			if revtype == 1 && c != "devices_mod" && c != "device_interfaces_mod" {
				expr = append(expr, "0")
			} else {
				expr = append(expr, "1")
			}
		default:
			expr = append(expr, "NULL")
		}
	}
	args = append(args, id)
	_, e := tx.ExecContext(ctx, "INSERT INTO `"+table+"_aud` ("+strings.Join(quoted, ",")+") SELECT "+strings.Join(expr, ",")+" FROM `"+table+"` WHERE id=?", args...)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO revchanges (rev,entityname) SELECT ?,? WHERE NOT EXISTS (SELECT 1 FROM revchanges WHERE rev=? AND entityname=?)", rev, entity, rev, entity)
	return e
}

func (s *Service) Register(ctx context.Context, sel Selection) (Result, error) {
	return s.register(ctx, sel, false)
}

// Registration runs in the same order as the web console, because the collector
// is not safe against the one-shot variant: when devices already carry all of
// their checkpoints at DEVICE_ADDED time, the collector starts lazy-loading their
// labels and the CHECKPOINT_UPDATED reload a moment later collided with that
// (c3p0 "Marking a ResultSet inactive..." InternalError killed the MK119 Cache
// worker for good, 2026-09-21). So:
//
//  1. devices + interfaces commit, then SPECIFY_RULE_UPDATED + DEVICE_ADDED
//  2. wait for the collector to apply that while the devices are still empty
//  3. all checkpoints commit in one transaction, then CHECKPOINT_UPDATED + SPECIFY_RULE_UPDATED
//  4. watch liz.checkvalue.topic until every new checkpoint produced values
//
// Kafka ACKs only mean the broker has the message, so step 2 is a wait, not a
// confirmation. This lowers the chance of the collision; it does not fix the
// collector, which can still hit it on any other configuration change.
//
// rollbackOnly is used by opt-in integration tests against the MK119 schema.
func (s *Service) register(ctx context.Context, sel Selection, rollbackOnly bool) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.session(sel.PlanID)
	if e != nil {
		return Result{}, e
	}
	if e = validateSelection(v.Plan, sel); e != nil {
		return Result{}, e
	}
	db, e := openDB(ctx, v.Config)
	if e != nil {
		return Result{}, e
	}
	defer db.Close()
	if e = checkKafka(ctx, v.Config); e != nil {
		return Result{}, e
	}
	r, e := openRedis(ctx, v.Config)
	if e != nil {
		return Result{}, e
	}
	defer r.Close()
	for _, p := range sel.Points {
		if p.Selected {
			typ, err := r.Type(ctx, p.Key).Result()
			if err != nil {
				return Result{}, err
			}
			if typ != "string" {
				return Result{}, fmt.Errorf("Redis 키가 사라졌거나 타입이 변경됐습니다: %s", p.Key)
			}
		}
	}
	cat, e := loadCatalog(ctx, db)
	if e != nil {
		return Result{}, e
	}
	if cat.TypeID != v.Catalog.TypeID || cat.InterfaceID != v.Catalog.InterfaceID || cat.DriverID != v.Catalog.DriverID {
		return Result{}, fmt.Errorf("카탈로그가 변경됐습니다. 다시 분석하세요")
	}
	tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if e != nil {
		return Result{}, e
	}
	defer tx.Rollback()
	upd, e := updateTarget(v.Plan, sel)
	if e != nil {
		return Result{}, e
	}
	if sel, e = updateCluster(ctx, tx, sel, upd); e != nil {
		return Result{}, e
	}
	dc, e := checkLocation(ctx, tx, sel)
	if e != nil {
		return Result{}, e
	}
	points := selectedPoints(sel)
	// The tool never registers a key twice: re-checked here, inside the transaction.
	keys := make([]string, len(points))
	for i, p := range points {
		keys[i] = p.Key
	}
	if n, e := registeredCount(ctx, tx, cat.DriverID, keys); e != nil || n > 0 {
		if e != nil {
			return Result{}, e
		}
		return Result{}, fmt.Errorf("선택한 키 %d개가 이미 등록돼 있습니다(분석 이후 등록됨) — 다시 조회하세요", n)
	}
	res := Result{ID: uuid.NewString(), DeviceIDs: []int64{}, InterfaceIDs: []int64{}, CheckpointIDs: []int64{}, NewDeviceIDs: []int64{}, Missing: []int64{}}
	var hosts []string
	var refs []audRef
	groupName := sel.GroupName
	if upd == nil {
		hosts, refs, e = insertDevices(ctx, tx, sel, cat, dc, &res)
	} else {
		groupName = upd.Name
		hosts, refs, e = addToGroup(ctx, tx, sel, cat, dc, upd, &res)
	}
	if e != nil {
		return res, e
	}
	if len(refs) > 0 {
		related := []audRef{{"support_device_driver", "SupportDeviceDriver", cat.DriverID}, {"device_type", "DeviceType", cat.TypeID}, {"liz_cluster", "LizCluster", sel.ClusterID}}
		if e = writeAudits(ctx, tx, refs, related); e != nil {
			return res, e
		}
	}
	if rollbackOnly {
		// Exercise the second phase's SQL too, inside the same transaction.
		ids, pointRefs, e := insertCheckpoints(ctx, tx, points, hosts, res.DeviceIDs, res.InterfaceIDs, cat.DriverID)
		if e != nil {
			return res, e
		}
		res.CheckpointIDs = ids
		if e = writeAudits(ctx, tx, pointRefs, nil); e != nil {
			return res, e
		}
		return res, tx.Rollback()
	}
	res.Stage = stagePrepared
	rec := receipt{Result: res, DBHost: v.Config.DBHost, DBPort: v.Config.DBPort, Brokers: v.Config.Brokers, GroupName: groupName,
		TwoPhase: true, Hosts: hosts, Points: points, DeviceMessages: []json.RawMessage{}}
	if len(res.NewDeviceIDs) > 0 {
		rec.DeviceMessages = deviceNotifications(res)
	}
	if e = s.saveReceipt(rec); e != nil {
		return res, fmt.Errorf("복구 기록 저장 실패 — 등록 취소: %w", e)
	}
	delete(s.sessions, sel.PlanID)
	s.progress("1/4 새 장비 %d개·인터페이스 저장 중…", len(res.NewDeviceIDs))
	if e = tx.Commit(); e != nil {
		rec.Result.Message = "장비 저장(커밋) 결과를 확인해야 합니다. 「이어서 진행」이 저장 여부부터 확인합니다: " + e.Error()
		return rec.Result, nil
	}
	rec.Result.Stage = stageDevices
	// A stale "prepared" receipt is still resumable: Retry finds the group and moves on.
	_ = s.saveReceipt(rec)
	out := s.proceed(ctx, v.Config, db, rec)
	if out.Committed {
		// Remember the choices, unselected placed keys included. Never fails the registration.
		if ev, e := s.learnPatterns(sel.Points, "registration "+out.ID); e != nil {
			out.Message += " (등록 패턴 저장 실패: " + e.Error() + ")"
		} else if n := len(ev.Changes); n > 0 {
			out.Message += fmt.Sprintf(" 등록 패턴 %d건을 기억했습니다.", n)
		}
	}
	return out, nil
}

type audRef struct {
	table, entity string
	id            int64
}

func selectedPoints(sel Selection) []Point {
	out := []Point{}
	for _, p := range sel.Points {
		if p.Selected {
			out = append(out, p)
		}
	}
	return out
}

// First phase: specify rule, group, and one device + interface per server.
func insertDevices(ctx context.Context, tx *sql.Tx, sel Selection, cat catalog, dc int64, res *Result) ([]string, []audRef, error) {
	rule, e := insert(ctx, tx, "INSERT INTO device_specify_rule (name,description,specify_type,specify_join_type) VALUES ('-','-',1,NULL)")
	if e != nil {
		return nil, nil, e
	}
	res.GroupID, e = insert(ctx, tx, "INSERT INTO device_group (name,description,depth,explorer_id,device_specify_rule_id) VALUES (?,?,1,?,?)", sel.GroupName, sel.GroupName, sel.ExplorerID, rule)
	if e != nil {
		return nil, nil, e
	}
	refs := []audRef{{"device_specify_rule", "DeviceSpecifyRule", rule}, {"device_group", "DeviceGroup", res.GroupID}}
	hosts := []string{}
	for _, h := range activeServers(sel) {
		devRefs, e := newDevice(ctx, tx, h.Name, cat, sel.ClusterID, dc, rule, res)
		if e != nil {
			return nil, nil, e
		}
		hosts = append(hosts, h.Hostname)
		refs = append(refs, devRefs...)
	}
	return hosts, refs, nil
}

// newDevice inserts one device + interface and puts it into the group's specify rule.
func newDevice(ctx context.Context, tx *sql.Tx, name string, cat catalog, clusterID, dc, rule int64, res *Result) ([]audRef, error) {
	device, e := insert(ctx, tx, "INSERT INTO device (entity_type,name,description,device_type_id,cluster_id,data_center_id,check_alarm,enable_control,enable_device_accessory,enable_maintenance_alarm,enable_monitor,save_checkvalue,save_device_event) VALUES ('generic',?,'',?,?,?,1,1,0,0,1,1,1)", name, cat.TypeID, clusterID, dc)
	if e != nil {
		return nil, e
	}
	iface, e := insert(ctx, tx, "INSERT INTO device_interface (device_id,device_driver_id,interface_type_id) VALUES (?,?,?)", device, cat.DriverID, cat.InterfaceID)
	if e != nil {
		return nil, e
	}
	mapping, e := insert(ctx, tx, "INSERT INTO device_specify_rule_mapping_device (device_id,device_specify_rule_id) VALUES (?,?)", device, rule)
	if e != nil {
		return nil, e
	}
	res.DeviceIDs = append(res.DeviceIDs, device)
	res.InterfaceIDs = append(res.InterfaceIDs, iface)
	res.NewDeviceIDs = append(res.NewDeviceIDs, device)
	return []audRef{{"device", "Device", device}, {"device_interface", "DeviceInterface", iface}, {"device_specify_rule_mapping_device", "DeviceSpecifyRuleMappingDevice", mapping}}, nil
}

// addToGroup is the first phase of an update: the group's existing device is
// reused per host, a new server gets a new device in the group. Nothing
// existing is modified or deleted.
func addToGroup(ctx context.Context, tx *sql.Tx, sel Selection, cat catalog, dc int64, g *ExistingGroup, res *Result) ([]string, []audRef, error) {
	res.GroupID, res.Update = g.GroupID, true
	var rule sql.NullInt64
	if e := tx.QueryRowContext(ctx, "SELECT device_specify_rule_id FROM device_group WHERE id=? FOR UPDATE", g.GroupID).Scan(&rule); e != nil {
		return nil, nil, fmt.Errorf("업데이트할 그룹을 찾을 수 없습니다(ID %d): %w", g.GroupID, e)
	}
	hosts, refs := []string{}, []audRef{}
	for _, h := range activeServers(sel) {
		if d := g.deviceFor(h.Hostname); d != nil {
			var n int
			if e := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM device_interface i JOIN device d ON d.id=i.device_id WHERE i.id=? AND d.id=? AND i.flag=1 AND d.flag=1", d.InterfaceID, d.DeviceID).Scan(&n); e != nil || n != 1 {
				return nil, nil, fmt.Errorf("기존 장비 %s(ID %d)를 확인할 수 없습니다 — 다시 조회하세요", d.Name, d.DeviceID)
			}
			res.DeviceIDs = append(res.DeviceIDs, d.DeviceID)
			res.InterfaceIDs = append(res.InterfaceIDs, d.InterfaceID)
			hosts = append(hosts, h.Hostname)
			continue
		}
		if !rule.Valid {
			return nil, nil, fmt.Errorf("그룹 %s에 장비 규칙이 없어 새 장비(%s)를 넣을 수 없습니다", g.Name, h.Hostname)
		}
		devRefs, e := newDevice(ctx, tx, h.Name, cat, sel.ClusterID, dc, rule.Int64, res)
		if e != nil {
			return nil, nil, e
		}
		hosts = append(hosts, h.Hostname)
		refs = append(refs, devRefs...)
	}
	return hosts, refs, nil
}

func inList(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// registeredCount counts live checkpoints of the driver that already use one of keys.
func registeredCount(ctx context.Context, q queryer, driverID int64, keys []string) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	args := []any{driverID}
	for _, k := range keys {
		args = append(args, k)
	}
	var n int
	e := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM checkpoint c JOIN device_interface i ON i.id=c.device_interface_id WHERE i.device_driver_id=? AND c.flag=1 AND c.request_command IN ("+inList(len(keys))+")", args...).Scan(&n)
	return n, e
}

// Second phase: every checkpoint (and template labels) of every device at once.
func insertCheckpoints(ctx context.Context, tx *sql.Tx, points []Point, hosts []string, devices, ifaces []int64, driverID int64) ([]int64, []audRef, error) {
	if len(hosts) != len(devices) || len(hosts) != len(ifaces) {
		return nil, nil, fmt.Errorf("장비/인터페이스 기록이 맞지 않습니다")
	}
	ids := []int64{}
	refs := []audRef{}
	for i, host := range hosts {
		device, iface := devices[i], ifaces[i]
		for _, p := range points {
			if p.Host != host {
				continue
			}
			var templateID any
			builtin := 0
			if p.TemplateID != 0 {
				templateID = p.TemplateID
				builtin = 1
			}
			var point int64
			var e error
			if p.TemplateID != 0 {
				point, e = insert(ctx, tx, "INSERT INTO checkpoint (name,display_name,builtin,check_interval,keep_interval,check_time_to_live,do_blink,driver_code,request_command,enable_monitor,enable_stats,enable_subscribe,convert_expression,data_format,measure,enable_data_validator,request_code,request_size,request_type,slot_id,save_value,device_id,device_interface_id,support_checkpoint_id,checkpoint_type_id,internal_checkpoint_type,aggregated) SELECT ?,?,1,?,?,0,0,?,?,1,1,0,COALESCE(convert_expression,'x'),data_format,measure,0,request_code,request_size,request_type,slot_id,1,?,?,id,checkpoint_type_id,internal_checkpoint_type,aggregated FROM support_checkpoint WHERE id=? AND support_device_driver_id=? AND enable_to_use=1", p.Name, p.Name, p.Interval, p.Interval, p.Key, p.Key, device, iface, p.TemplateID, driverID)
			} else {
				point, e = insert(ctx, tx, "INSERT INTO checkpoint (name,display_name,builtin,check_interval,keep_interval,check_time_to_live,do_blink,driver_code,request_command,enable_monitor,enable_stats,enable_subscribe,convert_expression,data_format,measure,enable_data_validator,request_code,request_size,request_type,slot_id,save_value,device_id,device_interface_id,support_checkpoint_id) VALUES (?,?,?,?,?,0,0,?,?,1,1,0,'x',?,?,0,'',0,'',1,1,?,?,?)", p.Name, p.Name, builtin, p.Interval, p.Interval, p.Key, p.Key, p.Format, p.Measure, device, iface, templateID)
			}
			if e != nil {
				return nil, nil, e
			}
			ids = append(ids, point)
			refs = append(refs, audRef{"checkpoint", "Checkpoint", point})
			if p.TemplateID != 0 {
				if _, e = tx.ExecContext(ctx, "INSERT INTO checkpoint_label (label,raw_data,type,checkpoint_id,support_checkpoint_label_id) SELECT label,raw_data,type,?,id FROM support_checkpoint_label WHERE support_checkpoint_id=?", point, p.TemplateID); e != nil {
					return nil, nil, e
				}
				labels, e := options(ctx, tx, "SELECT id,COALESCE(label,'') FROM checkpoint_label WHERE checkpoint_id="+fmt.Sprint(point))
				if e != nil {
					return nil, nil, e
				}
				for _, label := range labels {
					refs = append(refs, audRef{"checkpoint_label", "CheckpointLabel", label.ID})
				}
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil, fmt.Errorf("등록할 체크포인트가 없습니다")
	}
	return ids, refs, nil
}

// One Envers revision per transaction: created rows (revtype 0) and related
// catalog rows touched by the relation (revtype 1).
func writeAudits(ctx context.Context, tx *sql.Tx, created, related []audRef) error {
	rev, e := insert(ctx, tx, "INSERT INTO revinfo (timestamp) VALUES (?)", time.Now().UnixMilli())
	if e != nil {
		return e
	}
	cache := map[string]auditShape{}
	for _, ref := range created {
		if e = audit(ctx, tx, cache, ref.table, ref.id, rev, 0, "com.onion.liz.common.database.entity."+ref.entity); e != nil {
			return e
		}
	}
	for _, ref := range related {
		if e = audit(ctx, tx, cache, ref.table, ref.id, rev, 1, "com.onion.liz.common.database.entity."+ref.entity); e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) progress(format string, a ...any) {
	if s.Progress != nil {
		s.Progress(fmt.Sprintf(format, a...))
	}
}

// proceed drives a receipt from stageDevices (or later) to the end. Every step
// is recorded first, so calling it again through Retry continues where it stopped.
func (s *Service) proceed(ctx context.Context, c Config, db *sql.DB, rec receipt) Result {
	if rec.Result.Stage == stageDevices {
		if !s.send(ctx, c, &rec, rec.DeviceMessages, &rec.DeviceSent, "장비 알림") {
			return rec.Result
		}
		if rec.DevicesNotifiedAt.IsZero() {
			rec.DevicesNotifiedAt = time.Now()
			_ = s.saveReceipt(rec)
		}
		if len(rec.DeviceMessages) > 0 && !s.settle(ctx, rec.DevicesNotifiedAt) {
			rec.Result.Message = "장비까지 등록됨. Collector 반영 대기 중 중단됐습니다 — 「이어서 진행」으로 체크포인트 등록을 계속하세요"
			return rec.Result
		}
		if e := s.commitCheckpoints(ctx, db, &rec); e != nil {
			rec.Result.Message = "장비까지 등록됨. 체크포인트 등록 실패 — 「이어서 진행」으로 다시 시도하세요: " + e.Error()
			return rec.Result
		}
	}
	// Start listening before the notification so the first values are not missed.
	watch := s.watchCollection(ctx, c, rec.Result.CheckpointIDs)
	defer watch.close()
	if !s.send(ctx, c, &rec, rec.PointMessages, &rec.PointSent, "체크포인트 알림") {
		return rec.Result
	}
	rec.Result.Notified = true
	window := verifyWindow(rec.Points, s.VerifyWindow)
	seen := watch.wait(ctx, window, func(done, total int, left time.Duration) {
		s.progress("4/4 수집 확인 중 — %d/%d개 값 수신 (최대 %d초 남음)", done, total, int(left.Seconds()+0.99))
	})
	summarizeCollection(&rec.Result, seen, watch.err)
	_ = s.saveReceipt(rec)
	return rec.Result
}

func (s *Service) send(ctx context.Context, c Config, rec *receipt, msgs []json.RawMessage, sent *int, what string) bool {
	if *sent >= len(msgs) {
		return true
	}
	transport, e := kafkaTransport(c)
	if e != nil {
		rec.Result.Message = fmt.Sprintf("%s 전송 준비 실패 — 「이어서 진행」으로 재시도하세요: %v", what, e)
		return false
	}
	defer transport.CloseIdleConnections()
	w := &kafka.Writer{Addr: kafka.TCP(brokers(c)...), Topic: "liz.message.pipeline", Balancer: &kafka.Hash{}, Transport: transport, RequiredAcks: kafka.RequireAll, WriteTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, MaxAttempts: 2}
	defer w.Close()
	for *sent < len(msgs) {
		if e = w.WriteMessages(ctx, kafka.Message{Key: []byte("rtaskmgr-stats"), Value: msgs[*sent]}); e != nil {
			rec.Result.Message = fmt.Sprintf("%s 전송 실패 — 「이어서 진행」으로 재시도하세요(DB 재등록 없음): %v", what, e)
			return false
		}
		*sent++
		if e = s.saveReceipt(*rec); e != nil {
			rec.Result.Message = fmt.Sprintf("%s 복구 기록 저장 실패: %v", what, e)
			return false
		}
	}
	return true
}

const defaultSettle = 30 * time.Second

// settle waits until the collector has had time to apply DEVICE_ADDED for the
// still-empty devices. Measured from the notification, so a resumed run does
// not wait again for time that already passed.
func (s *Service) settle(ctx context.Context, since time.Time) bool {
	wait := s.SettleWait
	if wait <= 0 {
		wait = defaultSettle
	}
	deadline := since.Add(wait)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return true
		}
		s.progress("2/4 Collector 반영 대기 %d초 — 빈 장비의 DEVICE_ADDED를 먼저 처리하게 합니다", int(left.Seconds()+0.99))
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
	}
}

// checkpointsOn finds checkpoints on these interfaces that use one of the
// run's keys (an update targets devices that already have other checkpoints).
func checkpointsOn(ctx context.Context, q queryer, ifaces []int64, points []Point) ([]int64, error) {
	if len(ifaces) == 0 || len(points) == 0 {
		return nil, fmt.Errorf("인터페이스 또는 체크포인트 기록이 없습니다")
	}
	args := make([]any, 0, len(ifaces)+len(points))
	for _, v := range ifaces {
		args = append(args, v)
	}
	for _, p := range points {
		args = append(args, p.Key)
	}
	rows, e := q.QueryContext(ctx, "SELECT id FROM checkpoint WHERE device_interface_id IN ("+inList(len(ifaces))+") AND request_command IN ("+inList(len(points))+") AND flag=1 ORDER BY id", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Service) commitCheckpoints(ctx context.Context, db *sql.DB, rec *receipt) error {
	existing, e := checkpointsOn(ctx, db, rec.Result.InterfaceIDs, rec.Points)
	if e != nil {
		return e
	}
	if len(existing) > 0 {
		// Only an earlier attempt of this receipt that lost the COMMIT answer may
		// have put them there (PointMessages are recorded just before COMMIT, and
		// the phase is one transaction, so it is all or nothing).
		if len(rec.PointMessages) == 0 || len(existing) != len(rec.Points) {
			return fmt.Errorf("대상 장비에 같은 키의 체크포인트 %d개가 이미 있습니다(이 등록이 만든 것이 아님). 웹에서 확인하세요", len(existing))
		}
		rec.Result.CheckpointIDs = existing
	} else {
		var driverID int64
		if e = db.QueryRowContext(ctx, "SELECT device_driver_id FROM device_interface WHERE id=?", rec.Result.InterfaceIDs[0]).Scan(&driverID); e != nil {
			return fmt.Errorf("인터페이스 확인 실패: %w", e)
		}
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		// The first phase checked for duplicates, but the settle wait leaves ~30s
		// in which the web console could register one of these keys: check again
		// inside this transaction, right before inserting.
		keys := make([]string, len(rec.Points))
		for i, p := range rec.Points {
			keys[i] = p.Key
		}
		if n, e := registeredCount(ctx, tx, driverID, keys); e != nil {
			return e
		} else if n > 0 {
			return fmt.Errorf("대기 중에 선택한 키 %d개가 다른 곳에서 등록됐습니다 — 체크포인트는 등록하지 않았습니다(장비만 등록된 상태). 다시 조회해서 업데이트로 진행하세요", n)
		}
		s.progress("3/4 체크포인트 %d개 저장 중…", len(rec.Points))
		ids, refs, e := insertCheckpoints(ctx, tx, rec.Points, rec.Hosts, rec.Result.DeviceIDs, rec.Result.InterfaceIDs, driverID)
		if e != nil {
			return e
		}
		if e = writeAudits(ctx, tx, refs, nil); e != nil {
			return e
		}
		rec.PointMessages = pointNotifications(rec.Result)
		rec.PointSent = 0
		if e = s.saveReceipt(*rec); e != nil {
			return fmt.Errorf("복구 기록 저장 실패 — 체크포인트 등록 취소: %w", e)
		}
		if e = tx.Commit(); e != nil {
			return fmt.Errorf("커밋 결과 불확실 — 「이어서 진행」이 저장 여부를 먼저 확인합니다: %w", e)
		}
		rec.Result.CheckpointIDs = ids
	}
	rec.Result.Stage = stageCheckpoints
	rec.Result.Committed = true
	// If this write fails the receipt still says "devices"; a resume finds the
	// rows through checkpointsOn and continues.
	_ = s.saveReceipt(*rec)
	return nil
}

func notification(code string, body map[string]any, recipients []string) json.RawMessage {
	body["header"] = map[string]any{"version": 1, "time": time.Now().UnixMilli(), "protocolCode": code, "lizMessageType": "NOTIFICATION", "recipientTypes": recipients, "recipientIds": []int{}, "senderType": "ADMIN_CONSOLE", "senderId": nil, "lizUserId": nil, "uuid": uuid.NewString()}
	data, _ := json.Marshal(body)
	return data
}

var specifyRecipients = []string{"ADMIN_CONSOLE", "API_SERVER", "ADMIN_MOBILE"}

// Web console order: SPECIFY_RULE_UPDATED, DEVICE_ADDED ... CHECKPOINT_UPDATED, SPECIFY_RULE_UPDATED.
func deviceNotifications(r Result) []json.RawMessage {
	return []json.RawMessage{
		notification("SPECIFY_RULE_UPDATED_NOTIFICATION", map[string]any{"specifyRuleId": nil}, specifyRecipients),
		notification("DEVICE_ADDED_NOTIFICATION", map[string]any{"deviceIds": r.NewDeviceIDs}, []string{}),
	}
}
func pointNotifications(r Result) []json.RawMessage {
	return []json.RawMessage{
		notification("CHECKPOINT_UPDATED_NOTIFICATION", map[string]any{"deviceInterfaceIds": r.InterfaceIDs}, []string{}),
		notification("SPECIFY_RULE_UPDATED_NOTIFICATION", map[string]any{"specifyRuleId": nil}, specifyRecipients),
	}
}

func (s *Service) receiptPath(id string) (string, error) {
	if _, e := uuid.Parse(id); e != nil {
		return "", fmt.Errorf("잘못된 복구 ID")
	}
	dir := s.Directory
	if dir == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		dir = filepath.Join(home, ".rtaskmgr", "stats-registration")
	}
	return filepath.Join(dir, id+".json"), nil
}
func (s *Service) saveReceipt(rec receipt) error {
	path, e := s.receiptPath(rec.Result.ID)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	data, e := json.MarshalIndent(rec, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}

// deliverLegacy finishes receipts written by the single-transaction version.
func (s *Service) deliverLegacy(ctx context.Context, c Config, rec receipt) Result {
	if !s.send(ctx, c, &rec, rec.Messages, &rec.Sent, "Kafka 알림") {
		return rec.Result
	}
	rec.Result.Notified = true
	rec.Result.Message = fmt.Sprintf("등록 완료: 장비 %d개, 체크포인트 %d개, Kafka 알림 %d건", len(rec.Result.DeviceIDs), len(rec.Result.CheckpointIDs), len(rec.Messages))
	if e := s.saveReceipt(rec); e != nil {
		rec.Result.Message += " (결과 파일 저장 실패: " + e.Error() + ")"
	}
	return rec.Result
}

func countIDs(ctx context.Context, db *sql.DB, table string, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, v := range ids {
		args[i] = v
	}
	var n int
	e := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+table+"` WHERE id IN ("+marks+")", args...).Scan(&n)
	return n, e
}

// Retry resumes a receipt: it never re-inserts what is already saved. For a
// finished registration it only repeats the collection check.
func (s *Service) Retry(ctx context.Context, c Config, id string) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, e := s.receiptPath(id)
	if e != nil {
		return Result{}, e
	}
	data, e := os.ReadFile(path)
	if e != nil {
		return Result{}, e
	}
	var rec receipt
	if e = json.Unmarshal(data, &rec); e != nil {
		return Result{}, e
	}
	if c.DBHost != rec.DBHost || c.DBPort != rec.DBPort || c.Brokers != rec.Brokers {
		return rec.Result, fmt.Errorf("최초 등록과 동일한 MariaDB/Kafka 주소를 사용하세요")
	}
	db, e := openDB(ctx, c)
	if e != nil {
		return rec.Result, e
	}
	defer db.Close()
	if rec.Result.Update && rec.Result.Stage == stagePrepared && len(rec.Result.NewDeviceIDs) > 0 {
		// The group existed before; whether the first phase committed shows in the new devices.
		if n, e := countIDs(ctx, db, "device", rec.Result.NewDeviceIDs); e != nil || n == 0 {
			return rec.Result, fmt.Errorf("업데이트의 새 장비가 저장되지 않았습니다(커밋 안 됨). 다시 조회해서 진행하세요")
		}
	}
	var name string
	e = db.QueryRowContext(ctx, "SELECT name FROM device_group WHERE id=?", rec.Result.GroupID).Scan(&name)
	if rec.TwoPhase && rec.Result.Stage == stagePrepared && errors.Is(e, sql.ErrNoRows) {
		return rec.Result, fmt.Errorf("장비 등록이 저장되지 않았습니다(커밋 안 됨). 처음부터 다시 등록하세요")
	}
	if e != nil || name != rec.GroupName {
		return rec.Result, fmt.Errorf("등록된 그룹을 확인할 수 없습니다. DB 상태를 확인하세요")
	}
	if !rec.TwoPhase {
		if n, e := countIDs(ctx, db, "checkpoint", rec.Result.CheckpointIDs); e != nil || n != len(rec.Result.CheckpointIDs) {
			return rec.Result, fmt.Errorf("등록 결과 확인 실패: 체크포인트 %d/%d개 확인", n, len(rec.Result.CheckpointIDs))
		}
		rec.Result.Committed = true
		if rec.Result.Notified {
			return rec.Result, nil
		}
		return s.deliverLegacy(ctx, c, rec), nil
	}
	if n, e := countIDs(ctx, db, "device", rec.Result.DeviceIDs); e != nil || n != len(rec.Result.DeviceIDs) {
		return rec.Result, fmt.Errorf("등록 결과 확인 실패: 장비 %d/%d개 확인", n, len(rec.Result.DeviceIDs))
	}
	if rec.Result.Stage == stagePrepared {
		rec.Result.Stage = stageDevices
		_ = s.saveReceipt(rec)
	}
	if rec.Result.Stage == stageCheckpoints {
		if n, e := countIDs(ctx, db, "checkpoint", rec.Result.CheckpointIDs); e != nil || n != len(rec.Result.CheckpointIDs) {
			return rec.Result, fmt.Errorf("등록 결과 확인 실패: 체크포인트 %d/%d개 확인", n, len(rec.Result.CheckpointIDs))
		}
	}
	return s.proceed(ctx, c, db, rec), nil
}

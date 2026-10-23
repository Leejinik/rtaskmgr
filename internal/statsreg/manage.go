package statsreg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
)

// 「기존 등록 관리」: rename the checkpoints of a registered group (every server
// of a key shape at once), change their interval, rename its devices, delete a
// device or the whole group (manage_delete.go).
//
// Admin has no bulk rename. What this does is the shape of admin's batch update
// for the notices and of admin's single edit for the rows, both observed on
// trunk #987 (2026-10-22, observe-trunk.md):
//   - one transaction: UPDATE checkpoint name+display_name (and/or check+keep
//     interval) per row, guarded by the values read when the group was loaded, so
//     a row someone changed meanwhile is skipped and reported, not overwritten;
//   - audit like admin: revinfo + <table>_aud revtype 1 with _mod=1 only for the
//     changed columns + revchanges, and one liz_log row per object. Labels are not
//     touched (admin deletes and re-inserts them on every edit; that is not wanted);
//   - after COMMIT: CHECKPOINT_UPDATED{every changed interface once} +
//     SPECIFY_RULE_UPDATED, and for renamed devices SPECIFY_RULE_UPDATED then
//     DEVICE_UPDATED{live ids, restartService:true} (admin's order and flag);
//   - then the Redis timestamp check of verify.go on the touched checkpoints, and
//     the new names are remembered as registration patterns.

const manageTTL = 30 * time.Minute

// One wording for the tester build everywhere (2nd round met 「claude 접두어」
// and 「claude_ 로 시작」 for the same rule).
func testerGroupRule() string {
	return fmt.Sprintf("테스트 빌드는 이름 앞에 「%s」가 붙은 그룹(%s…)만 다룹니다", TesterPrefix, testerGroupPrefix())
}

func testerDeviceRule() string {
	return fmt.Sprintf("테스트 빌드는 이름 앞에 「%s」가 붙은 장비(%s…)만 다룹니다", TesterPrefix, testerDevicePrefix())
}

type manageState struct {
	sessions map[string]*manageSession
	// run serializes applies, deletes and their resends. s.mu is held only while
	// sessions or the DB transaction are touched, not through the notice /
	// collector / Redis waits, so Inspect and the rest stay usable meanwhile.
	run sync.Mutex
}

type manageSession struct {
	Config   Config
	DriverID int64
	Group    ManagedGroup
	rule     sql.NullInt64
	points   map[int64]ManagedPoint
	devices  map[int64]ManagedDevice
	previews map[string]managePlan
	removals map[string]deletePlan
	Expires  time.Time
}

type ManagedDevice struct {
	DeviceID int64  `json:"deviceId"`
	Name     string `json:"name"`
	// Host: the server this device stands for ("common" for keys without a server part, "" unknown).
	Host string `json:"host"`
	// Checkpoints: all live checkpoints of the device (what a delete removes).
	Checkpoints int `json:"checkpoints"`
	// OtherInterfaces: live interfaces of another driver (a delete would stop them too).
	OtherInterfaces int `json:"otherInterfaces"`
	// Locked: why this device must not be renamed, deleted or have its checkpoints
	// changed here ("" = free): it is also in another group (a change would show
	// there too), or a tester build meets a device without the tester's prefix.
	Locked string `json:"locked"`
	dc     sql.NullInt64
	ifaces []int64
}

type ManagedPoint struct {
	CheckpointID int64  `json:"checkpointId"`
	DeviceID     int64  `json:"deviceId"`
	InterfaceID  int64  `json:"interfaceId"`
	Key          string `json:"key"`
	Name         string `json:"name"`
	Interval     int    `json:"interval"` // check_interval, ms
	TemplateID   int64  `json:"templateId"`
	Format       int    `json:"format"`
	Measure      string `json:"measure"`
	Host         string `json:"host"`
	// RawName: the name is still the Redis key / driver code (registered without a name).
	RawName bool `json:"rawName"`
	// ServerPart: the value of the key's server part (hostname, IP or module id).
	ServerPart string `json:"serverPart"`
}

// ManagedRow is one key shape: the key with its server part replaced by {서버별},
// e.g. liz.stats.server.{서버별}.free.memory, with its checkpoint on every device.
type ManagedRow struct {
	Shape string `json:"shape"`
	// Template: the current name as a name pattern in screen words ({서버} …),
	// majority over the devices, with every server's own part as a placeholder.
	Template string `json:"template"`
	// TemplateWarning: the pattern does not fit every server (a server's own part
	// is left as text, or a placeholder cannot be filled) — said next to the button.
	TemplateWarning string         `json:"templateWarning"`
	Points          []ManagedPoint `json:"points"`
	Devices         int            `json:"devices"`
	RawName         bool           `json:"rawName"`
}

type ManagedGroup struct {
	ID          string          `json:"id"`
	GroupID     int64           `json:"groupId"`
	Name        string          `json:"name"`
	Devices     []ManagedDevice `json:"devices"`
	Rows        []ManagedRow    `json:"rows"`
	Checkpoints int             `json:"checkpoints"`
	RawNames    int             `json:"rawNames"`
	// OtherDevices: live devices of the group without a resource-stats interface.
	OtherDevices []string `json:"otherDevices"`
	Warnings     []string `json:"warnings"`
}

type PointOverride struct {
	CheckpointID int64  `json:"checkpointId"`
	Name         string `json:"name"`
}
type RowEdit struct {
	Shape string `json:"shape"`
	// Name: new name for every server of the shape ("" = keep). {Host} {host} {n} {t1}…
	// are filled per device like registration patterns.
	Name      string          `json:"name"`
	Interval  int             `json:"interval"` // ms, 0 = keep
	Overrides []PointOverride `json:"overrides"`
}
type DeviceEdit struct {
	DeviceID int64  `json:"deviceId"`
	Name     string `json:"name"`
}
type ManageEdit struct {
	ManageID string       `json:"manageId"`
	Rows     []RowEdit    `json:"rows"`
	Devices  []DeviceEdit `json:"devices"`
}

type PointChange struct {
	CheckpointID int64  `json:"checkpointId"`
	DeviceID     int64  `json:"deviceId"`
	InterfaceID  int64  `json:"interfaceId"`
	DeviceName   string `json:"deviceName"`
	Key          string `json:"key"`
	Shape        string `json:"shape"`
	OldName      string `json:"oldName"`
	NewName      string `json:"newName"`
	OldInterval  int    `json:"oldInterval"`
	NewInterval  int    `json:"newInterval"`
	// Suspicious: the new name holds another server's own value (its hostname or
	// the server part of its key) — 2nd round: 「지금 이름에서 시작」 copied {11} to every server.
	Suspicious bool `json:"suspicious"`
}
type DeviceChange struct {
	DeviceID int64  `json:"deviceId"`
	OldName  string `json:"oldName"`
	NewName  string `json:"newName"`
}

// ManagePreview shows exactly what an apply would change. Apply takes its ID
// (empty when there are errors or nothing to change) and applies the stored
// changes, never a new request.
type ManagePreview struct {
	ID         string         `json:"id"`
	Points     []PointChange  `json:"points"`
	Devices    []DeviceChange `json:"devices"`
	Renamed    int            `json:"renamed"`
	Intervals  int            `json:"intervals"`
	Touched    int            `json:"touched"` // devices whose checkpoints or name change
	Interfaces int            `json:"interfaces"`
	Warnings   []string       `json:"warnings"`
	Errors     []string       `json:"errors"`
}

type managePlan struct {
	points  []PointChange
	devices []DeviceChange
}

type ManageResult struct {
	ID        string   `json:"id"`
	Committed bool     `json:"committed"`
	Notified  bool     `json:"notified"`
	Pending   bool     `json:"pending"` // notices not all sent: StatsManageResend continues
	Updated   []int64  `json:"updated"`
	Renamed   []int64  `json:"renamed"`
	Conflicts []string `json:"conflicts"`
	Watched   int      `json:"watched"`
	Collected int      `json:"collected"`
	Missing   []int64  `json:"missing"`
	Verified  bool     `json:"verified"`
	Message   string   `json:"message"`
}

func (s *Service) pruneManage() {
	for id, m := range s.manage.sessions {
		if time.Now().After(m.Expires) {
			delete(s.manage.sessions, id)
		}
	}
}

func (s *Service) manageSession(id string) (*manageSession, error) {
	m := s.manage.sessions[id]
	if m == nil || time.Now().After(m.Expires) {
		return nil, fmt.Errorf("관리 화면이 만료됐거나 이미 적용했습니다. 그룹을 다시 불러오세요")
	}
	return m, nil
}

// ManageLoad reads one registered group of the analysis for management.
func (s *Service) ManageLoad(ctx context.Context, planID string, groupID int64) (ManagedGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.session(planID)
	if e != nil {
		return ManagedGroup{}, e
	}
	found := false
	for _, g := range v.Plan.Existing {
		found = found || g.GroupID == groupID
	}
	if !found {
		return ManagedGroup{}, fmt.Errorf("관리할 그룹을 조회 결과에서 찾을 수 없습니다(ID %d) — 다시 조회하세요", groupID)
	}
	db, e := openDB(ctx, v.Config)
	if e != nil {
		return ManagedGroup{}, e
	}
	defer db.Close()
	mods, e := loadModules(ctx, db)
	if e != nil {
		return ManagedGroup{}, e
	}
	m, e := loadManaged(ctx, db, groupID, v.Catalog.DriverID, v.Plan, mods)
	if e != nil {
		return ManagedGroup{}, e
	}
	m.Config, m.DriverID = v.Config, v.Catalog.DriverID
	m.Group.ID = uuid.NewString()
	m.Expires = time.Now().Add(manageTTL)
	if s.manage.sessions == nil {
		s.manage.sessions = map[string]*manageSession{}
	}
	s.pruneManage()
	s.manage.sessions[m.Group.ID] = m
	return m.Group, nil
}

func loadModules(ctx context.Context, q queryer) ([]module, error) {
	rows, e := q.QueryContext(ctx, "SELECT id,COALESCE(hostname,'') FROM liz_module")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []module{}
	for rows.Next() {
		var m module
		if e = rows.Scan(&m.ID, &m.Host); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// forUpdate is appended to the reads a write transaction re-checks, so rows
// committed concurrently cannot slip in between the check and the write.
func forUpdate(lock bool) string {
	if lock {
		return " FOR UPDATE"
	}
	return ""
}

// groupDevices returns the live devices of a group's rule that have an
// interface of driverID (managed, with Locked filled) and the names of the
// other live devices.
func groupDevices(ctx context.Context, q queryer, rule, driverID int64, lock bool) ([]ManagedDevice, []string, error) {
	rows, e := q.QueryContext(ctx, `SELECT d.id, d.name, d.data_center_id, i.id, COALESCE(i.device_driver_id,0)
FROM device_specify_rule_mapping_device m
JOIN device d ON d.id=m.device_id
LEFT JOIN device_interface i ON i.device_id=d.id AND i.flag=1
WHERE m.device_specify_rule_id=? AND d.flag=1
ORDER BY d.id, i.id`+forUpdate(lock), rule)
	if e != nil {
		return nil, nil, e
	}
	defer rows.Close()
	order := []int64{}
	byID := map[int64]*ManagedDevice{}
	seenIface := map[int64]bool{}
	for rows.Next() {
		var id int64
		var name string
		var dc, iface sql.NullInt64
		var driver int64
		if e = rows.Scan(&id, &name, &dc, &iface, &driver); e != nil {
			return nil, nil, e
		}
		d := byID[id]
		if d == nil {
			d = &ManagedDevice{DeviceID: id, Name: name, dc: dc, ifaces: []int64{}}
			byID[id] = d
			order = append(order, id)
		}
		// A device mapped twice to the rule repeats its interfaces.
		if !iface.Valid || seenIface[iface.Int64] {
			continue
		}
		seenIface[iface.Int64] = true
		if driver == driverID {
			d.ifaces = append(d.ifaces, iface.Int64)
		} else {
			d.OtherInterfaces++
		}
	}
	if e = rows.Err(); e != nil {
		return nil, nil, e
	}
	managed, others := []ManagedDevice{}, []string{}
	for _, id := range order {
		if d := byID[id]; len(d.ifaces) > 0 {
			managed = append(managed, *d)
		} else {
			others = append(others, fmt.Sprintf("%s (ID %d)", d.Name, d.DeviceID))
		}
	}
	if e = deviceLocks(ctx, q, rule, managed, lock); e != nil {
		return nil, nil, e
	}
	return managed, others, nil
}

// deviceLocks fills Locked: a device is one row however many groups map it,
// so renaming or deleting it here would change the other groups too.
func deviceLocks(ctx context.Context, q queryer, rule int64, devices []ManagedDevice, lock bool) error {
	if len(devices) == 0 {
		return nil
	}
	ids := deviceIDs(devices)
	rows, e := q.QueryContext(ctx, "SELECT m.device_id, g.id, COALESCE(g.name,'') FROM device_specify_rule_mapping_device m JOIN device_group g ON g.device_specify_rule_id=m.device_specify_rule_id WHERE m.device_id IN ("+inList(len(ids))+") AND m.device_specify_rule_id<>? ORDER BY g.id"+forUpdate(lock), append(idArgs(ids), rule)...)
	if e != nil {
		return e
	}
	defer rows.Close()
	groups := map[int64][]string{}
	for rows.Next() {
		var device, group int64
		var name string
		if e = rows.Scan(&device, &group, &name); e != nil {
			return e
		}
		groups[device] = append(groups[device], fmt.Sprintf("%s (ID %d)", name, group))
	}
	if e = rows.Err(); e != nil {
		return e
	}
	for i := range devices {
		d := &devices[i]
		switch {
		case len(groups[d.DeviceID]) > 0:
			d.Locked = "다른 그룹에도 들어 있는 장비라(" + strings.Join(groups[d.DeviceID], ", ") + ") 여기서 바꾸거나 지우면 그 그룹에도 반영됩니다 — admin 에서 정리하세요"
		case TesterPrefix != "" && !strings.HasPrefix(d.Name, testerDevicePrefix()):
			d.Locked = testerDeviceRule()
		}
	}
	return nil
}

func idArgs(ids []int64) []any {
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out
}

// isRawName: the checkpoint still carries its key as name (a key registered
// without a driver template gets "liz.stats." trimmed off as its name).
func isRawName(name, key, code string) bool {
	for _, k := range []string{key, code} {
		if k != "" && (name == k || name == strings.TrimPrefix(k, "liz.stats.")) {
			return true
		}
	}
	return false
}

// managedShape collapses the placeholder naming host (hostname, IP or module id)
// into {서버}; the first one when host is unknown. Other placeholders stay.
func managedShape(key, host string, hosts, mods map[string]string) string {
	locs := placeholder.FindAllStringSubmatchIndex(key, -1)
	if len(locs) == 0 {
		return key
	}
	l := locs[serverPartIndex(key, host, hosts, mods)]
	return key[:l[0]] + "{서버별}" + key[l[1]:]
}

// serverPartIndex is the index of the placeholder naming host (0 when unknown).
func serverPartIndex(key, host string, hosts, mods map[string]string) int {
	locs := placeholder.FindAllStringSubmatchIndex(key, -1)
	pick := 0
	if host != "" && host != "common" {
		for i, l := range locs {
			t := key[l[2]:l[3]]
			if hosts[strings.ToLower(t)] == host || mods[t] == host {
				pick = i
				break
			}
		}
	}
	return pick
}

func loadManaged(ctx context.Context, q queryer, groupID, driverID int64, plan Plan, mods []module) (*manageSession, error) {
	var name string
	var explorer, rule sql.NullInt64
	e := q.QueryRowContext(ctx, "SELECT name, explorer_id, device_specify_rule_id FROM device_group WHERE id=?", groupID).Scan(&name, &explorer, &rule)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, fmt.Errorf("그룹(ID %d)이 없습니다 — 이미 삭제됐을 수 있습니다. 다시 조회하세요", groupID)
	}
	if e != nil {
		return nil, e
	}
	if !testerOwnsGroup(name) {
		return nil, fmt.Errorf("%s: %s", testerGroupRule(), name)
	}
	m := &manageSession{rule: rule, points: map[int64]ManagedPoint{}, devices: map[int64]ManagedDevice{}, previews: map[string]managePlan{}, removals: map[string]deletePlan{},
		Group: ManagedGroup{GroupID: groupID, Name: name, Devices: []ManagedDevice{}, Rows: []ManagedRow{}, OtherDevices: []string{}, Warnings: []string{}}}
	if !rule.Valid {
		m.Group.Warnings = append(m.Group.Warnings, "이 그룹에는 장비 규칙이 없어 장비가 없습니다")
		return m, nil
	}
	devs, others, e := groupDevices(ctx, q, rule.Int64, driverID, false)
	if e != nil {
		return nil, e
	}
	m.Group.OtherDevices = others
	for _, d := range devs {
		if d.Locked != "" {
			m.Group.Warnings = append(m.Group.Warnings, fmt.Sprintf("장비 %s: %s. 이 장비와 그 체크포인트는 바꾸지 않습니다.", d.Name, d.Locked))
		}
	}
	if len(others) > 0 {
		m.Group.Warnings = append(m.Group.Warnings, fmt.Sprintf("이 그룹에는 리소스 수집 장비가 아닌 장비 %d대가 있습니다(%s). 이 화면은 그 장비를 바꾸지 않고, 그룹 삭제도 막습니다.", len(others), strings.Join(others, ", ")))
	}
	hosts := map[string]string{}
	for _, v := range plan.Servers {
		if v.Hostname != "common" {
			hosts[strings.ToLower(v.Hostname)] = v.Hostname
			if v.IP != "" {
				hosts[strings.ToLower(v.IP)] = v.Hostname
			}
		}
	}
	modHost := map[string]string{}
	for _, v := range mods {
		modHost[strconv.FormatInt(v.ID, 10)] = v.Host
	}
	known := map[int64]string{}
	for _, g := range plan.Existing {
		if g.GroupID == groupID {
			for _, d := range g.Devices {
				known[d.DeviceID] = d.Host
			}
		}
	}
	ifaceOf := map[int64]bool{}
	ids := []int64{}
	for i := range devs {
		d := &devs[i]
		d.Host = known[d.DeviceID]
		if d.Host == "" {
			bare := d.Name
			if TesterPrefix != "" {
				bare = strings.TrimPrefix(bare, testerDevicePrefix())
			}
			d.Host = hosts[strings.ToLower(bare)]
		}
		for _, f := range d.ifaces {
			ifaceOf[f] = true
		}
		ids = append(ids, d.DeviceID)
	}
	byDevice := map[int64]*ManagedDevice{}
	for i := range devs {
		byDevice[devs[i].DeviceID] = &devs[i]
	}
	points := []ManagedPoint{}
	if len(ids) > 0 {
		rows, e := q.QueryContext(ctx, "SELECT id, device_id, COALESCE(device_interface_id,0), COALESCE(request_command,''), COALESCE(driver_code,''), COALESCE(name,''), COALESCE(check_interval,0), COALESCE(support_checkpoint_id,0), COALESCE(data_format,4), COALESCE(measure,'') FROM checkpoint WHERE device_id IN ("+inList(len(ids))+") AND flag=1 ORDER BY id", idArgs(ids)...)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var p ManagedPoint
			var code string
			if e = rows.Scan(&p.CheckpointID, &p.DeviceID, &p.InterfaceID, &p.Key, &code, &p.Name, &p.Interval, &p.TemplateID, &p.Format, &p.Measure); e != nil {
				rows.Close()
				return nil, e
			}
			d := byDevice[p.DeviceID]
			if d == nil {
				continue
			}
			d.Checkpoints++
			if !ifaceOf[p.InterfaceID] {
				continue // a checkpoint of another driver on the same device
			}
			if p.Key == "" {
				p.Key = code
			}
			p.RawName = isRawName(p.Name, p.Key, code)
			points = append(points, p)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	// A device holding only keys without a server part is the "common" device.
	for i := range devs {
		d := &devs[i]
		if d.Host != "" {
			continue
		}
		common, any := true, false
		for _, p := range points {
			if p.DeviceID == d.DeviceID {
				any = true
				common = common && !strings.Contains(p.Key, "{")
			}
		}
		if any && common {
			d.Host = "common"
		}
	}
	rowOf := map[string]*ManagedRow{}
	order := []string{}
	for _, p := range points {
		p.Host = byDevice[p.DeviceID].Host
		shape := managedShape(p.Key, p.Host, hosts, modHost)
		if toks := keyTokens(p.Key); len(toks) > 0 {
			p.ServerPart = toks[serverPartIndex(p.Key, p.Host, hosts, modHost)]
		}
		if p.Key == "" {
			shape = fmt.Sprintf("(키 없음 · 체크포인트 %d)", p.CheckpointID)
		}
		r := rowOf[shape]
		if r == nil {
			r = &ManagedRow{Shape: shape, Points: []ManagedPoint{}}
			rowOf[shape] = r
			order = append(order, shape)
		}
		r.Points = append(r.Points, p)
		r.RawName = r.RawName || p.RawName
		m.points[p.CheckpointID] = p
		if p.RawName {
			m.Group.RawNames++
		}
	}
	sort.Strings(order)
	for _, shape := range order {
		r := rowOf[shape]
		sort.SliceStable(r.Points, func(i, j int) bool {
			return byDevice[r.Points[i].DeviceID].Name < byDevice[r.Points[j].DeviceID].Name
		})
		devices := map[int64]bool{}
		for _, p := range r.Points {
			devices[p.DeviceID] = true
		}
		r.Devices = len(devices)
		r.Template, r.TemplateWarning = rowTemplate(r.Points, hosts, modHost)
		m.Group.Rows = append(m.Group.Rows, *r)
	}
	sort.SliceStable(devs, func(i, j int) bool { return devs[i].Name < devs[j].Name })
	for _, d := range devs {
		m.devices[d.DeviceID] = d
	}
	m.Group.Devices = devs
	m.Group.Checkpoints = len(points)
	return m, nil
}

var namePlaceholder = regexp.MustCompile(`\{(Host|host|n|t\d+)\}`)

// resolveName fills a name pattern for one checkpoint; a name without
// placeholders is taken as it is.
func resolveName(tmpl, key, host string) (string, bool) {
	tmpl = screenToPattern(tmpl)
	if !namePlaceholder.MatchString(tmpl) {
		return tmpl, true
	}
	return render(tmpl, key, host)
}

// Name placeholders in screen words ↔ the registration patterns' own
// ({Host} {host} {n} {t1}…, kept in the pattern file and docs/stats-name-patterns.md).
var (
	screenKeyPart  = regexp.MustCompile(`\{키(\d+)\}`)
	patternKeyPart = regexp.MustCompile(`\{t(\d+)\}`)
)

func screenToPattern(s string) string {
	s = strings.NewReplacer("{서버대문자}", "{Host}", "{서버번호}", "{n}", "{서버}", "{host}").Replace(s)
	return screenKeyPart.ReplaceAllString(s, "{t$1}")
}

func patternToScreen(s string) string {
	s = strings.NewReplacer("{Host}", "{서버대문자}", "{n}", "{서버번호}", "{host}", "{서버}").Replace(s)
	return patternKeyPart.ReplaceAllString(s, "{키$1}")
}

// bounded: v occurs in s as a whole value (not inside a longer word or number).
func bounded(s, v string) bool {
	if v == "" {
		return false
	}
	return regexp.MustCompile(`(^|[^0-9A-Za-z])` + regexp.QuoteMeta(v) + `($|[^0-9A-Za-z])`).MatchString(s)
}

// ownValues: what is particular to the server of p (hostname, the key's server part).
func ownValues(p ManagedPoint) []string {
	out := []string{}
	for _, v := range []string{p.Host, p.ServerPart} {
		if v != "" && v != "common" {
			out = append(out, v)
		}
	}
	return out
}

// foreignValue returns another server's own value found in name ("" if none).
func foreignValue(name string, p ManagedPoint, row []ManagedPoint) string {
	own := map[string]bool{}
	for _, v := range ownValues(p) {
		own[v] = true
	}
	for _, q := range row {
		if q.DeviceID == p.DeviceID {
			continue
		}
		for _, v := range ownValues(q) {
			if !own[v] && bounded(name, v) {
				return v
			}
		}
	}
	return ""
}

// rowTemplate turns the row's current names into one pattern: generalize() puts
// the hostname, its number and the key's other values in placeholders; the key's
// server part (e.g. a module id like {11}) is put in its {키N} too, which
// generalize leaves as text when it is a number. Majority over the devices; the
// warning says when the pattern still copies one server's value to the others.
func rowTemplate(points []ManagedPoint, hosts, mods map[string]string) (string, string) {
	votes := map[string]int{}
	for _, p := range points {
		t := generalize(p.Name, p.Key, p.Host)
		if toks := keyTokens(p.Key); len(toks) > 0 && p.ServerPart != "" {
			i := serverPartIndex(p.Key, p.Host, hosts, mods)
			re := regexp.MustCompile(`(^|[^0-9A-Za-z])` + regexp.QuoteMeta(p.ServerPart) + `($|[^0-9A-Za-z])`)
			t = re.ReplaceAllString(t, "${1}"+fmt.Sprintf("{t%d}", i+1)+"${2}")
		}
		votes[t]++
	}
	best := ""
	for t, n := range votes {
		if n > votes[best] || (n == votes[best] && t < best) {
			best = t
		}
	}
	if best == "" {
		return "", ""
	}
	unfilled, copied := 0, []string{}
	for _, p := range points {
		name, ok := render(best, p.Key, p.Host)
		if !ok {
			unfilled++
			continue
		}
		if v := foreignValue(name, p, points); v != "" {
			copied = append(copied, v)
		}
	}
	warn := ""
	switch {
	case len(copied) > 0:
		warn = fmt.Sprintf("이 틀에는 서버마다 달라야 할 값(%s)이 글자 그대로 들어 있어 다른 서버에도 같은 값이 들어갑니다 — 그 부분을 {서버}·{서버번호}·{키1} 같은 자리로 바꾸세요", strings.Join(uniq(copied), ", "))
	case unfilled > 0:
		warn = fmt.Sprintf("서버 %d대는 이 틀의 자리를 채울 수 없습니다(서버를 모름) — 펼쳐서 따로 입력하세요", unfilled)
	}
	return patternToScreen(best), warn
}

func validName(n string) bool {
	return strings.TrimSpace(n) != "" && n == strings.TrimSpace(n) && len([]rune(n)) <= 100
}

// resolveEdit turns the screen's edits into per-checkpoint and per-device changes.
func resolveEdit(m *manageSession, edit ManageEdit) (managePlan, []string, []string) {
	plan := managePlan{points: []PointChange{}, devices: []DeviceChange{}}
	errs, warns := []string{}, []string{}
	rows := map[string]ManagedRow{}
	for _, r := range m.Group.Rows {
		rows[r.Shape] = r
	}
	devName := func(id int64) string { return m.devices[id].Name }
	seenShape, skipped := map[string]bool{}, map[int64]bool{}
	suspicious := []string{}
	for _, re := range edit.Rows {
		row, ok := rows[re.Shape]
		if !ok || seenShape[re.Shape] {
			errs = append(errs, fmt.Sprintf("목록에 없거나 두 번 들어온 키 묶음입니다: %s — 다시 불러오세요", re.Shape))
			continue
		}
		seenShape[re.Shape] = true
		if re.Interval != 0 && (re.Interval < 1000 || re.Interval > 86400000) {
			errs = append(errs, fmt.Sprintf("%s: 주기는 1000~86400000 ms(1초~1일)로 입력하세요 (%d)", re.Shape, re.Interval))
			continue
		}
		if re.Name != "" && !validName(re.Name) {
			errs = append(errs, fmt.Sprintf("%s: 새 이름은 앞뒤 공백 없이 1~100자로 입력하세요 (%q)", re.Shape, re.Name))
			continue
		}
		inRow := map[int64]bool{}
		for _, p := range row.Points {
			inRow[p.CheckpointID] = true
		}
		over := map[int64]string{}
		for _, o := range re.Overrides {
			if !inRow[o.CheckpointID] {
				errs = append(errs, fmt.Sprintf("%s: 이 줄에 없는 체크포인트 %d", re.Shape, o.CheckpointID))
				continue
			}
			if o.Name != "" {
				over[o.CheckpointID] = o.Name
			}
		}
		for _, p := range row.Points {
			name := p.Name
			tmpl, ok := over[p.CheckpointID]
			if lock := m.devices[p.DeviceID].Locked; lock != "" {
				if ok {
					errs = append(errs, fmt.Sprintf("%s · %s: %s", p.Key, devName(p.DeviceID), lock))
				} else if (re.Name != "" || re.Interval > 0) && !skipped[p.DeviceID] {
					skipped[p.DeviceID] = true
					warns = append(warns, fmt.Sprintf("장비 %s 의 체크포인트는 건너뜁니다: %s", devName(p.DeviceID), lock))
				}
				continue
			}
			if !ok {
				tmpl = re.Name
			}
			if tmpl != "" {
				n, ok := resolveName(tmpl, p.Key, p.Host)
				if !ok {
					errs = append(errs, fmt.Sprintf("%s · %s: 이름 틀 %q 을(를) 채울 수 없습니다 — {Host}·{host}·{n}은 장비의 서버를 알 때만, {t1}… 은 키에 그만큼 {} 가 있을 때만 쓸 수 있습니다", p.Key, devName(p.DeviceID), tmpl))
					continue
				}
				if !validName(n) {
					errs = append(errs, fmt.Sprintf("%s · %s: 새 이름은 앞뒤 공백 없이 1~100자로 입력하세요 (%q)", p.Key, devName(p.DeviceID), n))
					continue
				}
				name = n
			}
			interval := p.Interval
			if re.Interval > 0 {
				interval = re.Interval
			}
			if name == p.Name && interval == p.Interval {
				continue
			}
			c := PointChange{CheckpointID: p.CheckpointID, DeviceID: p.DeviceID, InterfaceID: p.InterfaceID, DeviceName: devName(p.DeviceID), Key: p.Key, Shape: row.Shape, OldName: p.Name, NewName: name, OldInterval: p.Interval, NewInterval: interval}
			if name != p.Name {
				if v := foreignValue(name, p, row.Points); v != "" {
					c.Suspicious = true
					suspicious = append(suspicious, fmt.Sprintf("%s(%s)", devName(p.DeviceID), v))
				}
			}
			plan.points = append(plan.points, c)
		}
	}
	if len(suspicious) > 0 {
		warns = append(warns, fmt.Sprintf("새 이름에 다른 서버의 값이 들어간 체크포인트 %d개: %s — 미리보기에서 ⚠ 표시된 줄을 확인하세요. 서버마다 다른 부분은 {서버}·{서버번호}·{키1} 자리로 쓰면 서버별로 채워집니다", len(suspicious), strings.Join(firstN(suspicious, 8), ", ")))
	}
	// Same name twice on one device after the change (admin allows it; easy to mix up).
	final := map[int64]map[string]int{}
	changed := map[int64]bool{}
	for _, c := range plan.points {
		changed[c.CheckpointID] = true
	}
	newName := map[int64]string{}
	for _, c := range plan.points {
		newName[c.CheckpointID] = c.NewName
	}
	for id, p := range m.points {
		n := p.Name
		if v, ok := newName[id]; ok {
			n = v
		}
		if final[p.DeviceID] == nil {
			final[p.DeviceID] = map[string]int{}
		}
		final[p.DeviceID][n]++
	}
	dup := map[string]bool{}
	for _, c := range plan.points {
		if c.NewName != c.OldName && final[c.DeviceID][c.NewName] > 1 && !dup[fmt.Sprint(c.DeviceID, c.NewName)] {
			dup[fmt.Sprint(c.DeviceID, c.NewName)] = true
			warns = append(warns, fmt.Sprintf("장비 %s 에 이름 %q 인 체크포인트가 %d개가 됩니다(admin 도 허용하지만 화면에서 구분하기 어렵습니다) — 이름에 {t1} 처럼 키의 값을 넣거나 줄을 펼쳐 따로 입력하세요", c.DeviceName, c.NewName, final[c.DeviceID][c.NewName]))
		}
	}
	seenDev := map[int64]bool{}
	names := map[string]int64{}
	for _, de := range edit.Devices {
		d, ok := m.devices[de.DeviceID]
		if !ok || seenDev[de.DeviceID] {
			errs = append(errs, fmt.Sprintf("이 그룹에 없거나 두 번 들어온 장비입니다(ID %d) — 다시 불러오세요", de.DeviceID))
			continue
		}
		seenDev[de.DeviceID] = true
		name := withPrefix(de.Name, testerDevicePrefix())
		if name == d.Name {
			continue
		}
		if d.Locked != "" {
			errs = append(errs, fmt.Sprintf("장비 %s: %s", d.Name, d.Locked))
			continue
		}
		if !validName(name) {
			errs = append(errs, fmt.Sprintf("장비 %s: 새 이름은 앞뒤 공백 없이 1~100자로 입력하세요 (%q)", d.Name, name))
			continue
		}
		if other, ok := names[strings.ToLower(name)]; ok {
			errs = append(errs, fmt.Sprintf("장비 이름 %q 을(를) 장비 %d 와 %d 에 같이 줄 수 없습니다", name, other, d.DeviceID))
			continue
		}
		names[strings.ToLower(name)] = d.DeviceID
		bare := func(n string) string {
			if TesterPrefix != "" {
				return strings.TrimPrefix(n, testerDevicePrefix())
			}
			return n
		}
		if d.Host != "" && d.Host != "common" && strings.EqualFold(bare(d.Name), d.Host) && !strings.EqualFold(bare(name), d.Host) {
			warns = append(warns, fmt.Sprintf("장비 %s: 이름이 서버 이름(%s)과 달라지면, 다음 「새 키 추가(업데이트)」 때 이 장비가 어느 서버 것인지 이름 대신 키로 추정합니다", d.Name, d.Host))
		}
		plan.devices = append(plan.devices, DeviceChange{DeviceID: d.DeviceID, OldName: d.Name, NewName: name})
	}
	sort.SliceStable(plan.points, func(i, j int) bool {
		if plan.points[i].Shape != plan.points[j].Shape {
			return plan.points[i].Shape < plan.points[j].Shape
		}
		return plan.points[i].DeviceName < plan.points[j].DeviceName
	})
	return plan, errs, warns
}

// deviceNameConflicts: new device names already used by another live device
// (admin refuses to save a device whose name is used twice).
func deviceNameConflicts(ctx context.Context, q queryer, devices []DeviceChange) ([]string, error) {
	if len(devices) == 0 {
		return nil, nil
	}
	renamed := map[int64]bool{}
	args := []any{}
	for _, d := range devices {
		renamed[d.DeviceID] = true
		args = append(args, d.NewName)
	}
	rows, e := q.QueryContext(ctx, "SELECT id, name FROM device WHERE flag=1 AND name IN ("+inList(len(args))+")", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id int64
		var name string
		if e = rows.Scan(&id, &name); e != nil {
			return nil, e
		}
		if renamed[id] {
			continue // renamed away in the same apply (or the device itself)
		}
		out = append(out, fmt.Sprintf("장비 이름 %q 은(는) 다른 장비(ID %d)가 쓰고 있습니다 — 이름이 같은 장비는 admin 에서 편집할 수 없게 되므로 막습니다", name, id))
	}
	return out, rows.Err()
}

// nameWarnings: read-only check of what may select checkpoints by name.
// Checkpoint specify rules (dynamic checkpoint groups / event policy targets)
// match display_name with EQUALS/CONTAINS, so a condition value found in an
// old or new name can move a checkpoint in or out of that rule.
func nameWarnings(ctx context.Context, q queryer, points []PointChange) []string {
	renamed := []PointChange{}
	for _, c := range points {
		if c.NewName != c.OldName {
			renamed = append(renamed, c)
		}
	}
	if len(renamed) == 0 {
		return nil
	}
	out := []string{}
	type hit struct {
		name  string
		value string
		n     int
	}
	hits := map[int64]*hit{}
	ruleIDs := []int64{}
	rows, e := q.QueryContext(ctx, `SELECT r.id, COALESCE(r.name,''), v.value FROM checkpoint_specify_rule_condition_value v
JOIN checkpoint_specify_rule_condition c ON c.id=v.checkpoint_specify_rule_condition_id
JOIN checkpoint_specify_rule r ON r.id=c.checkpoint_specify_rule_id
WHERE v.value IS NOT NULL AND v.value<>''`)
	if e != nil {
		out = append(out, "체크포인트 지정 규칙(이름 조건)을 확인하지 못했습니다: "+e.Error())
	} else {
		for rows.Next() {
			var id int64
			var name, value string
			if e = rows.Scan(&id, &name, &value); e != nil {
				break
			}
			v := strings.ToLower(strings.TrimSpace(value))
			if len([]rune(v)) < 2 || isDigits(v) {
				continue // ids and one-letter values are not name conditions worth a warning
			}
			n := 0
			for _, c := range renamed {
				if strings.Contains(strings.ToLower(c.OldName), v) || strings.Contains(strings.ToLower(c.NewName), v) {
					n++
				}
			}
			if n == 0 {
				continue
			}
			if h := hits[id]; h != nil {
				h.n += n
			} else {
				hits[id] = &hit{name, value, n}
				ruleIDs = append(ruleIDs, id)
			}
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			out = append(out, "체크포인트 지정 규칙(이름 조건)을 확인하지 못했습니다: "+e.Error())
		}
	}
	policies := map[int64][]string{}
	if len(ruleIDs) > 0 {
		rows, e := q.QueryContext(ctx, "SELECT COALESCE(name,''), checkpoint_specify_rule_id FROM alarm_policy WHERE flag=1 AND checkpoint_specify_rule_id IN ("+inList(len(ruleIDs))+")", idArgs(ruleIDs)...)
		if e == nil {
			for rows.Next() {
				var name string
				var rule int64
				if rows.Scan(&name, &rule) == nil {
					policies[rule] = append(policies[rule], name)
				}
			}
			rows.Close()
		}
	}
	sort.Slice(ruleIDs, func(i, j int) bool { return ruleIDs[i] < ruleIDs[j] })
	for _, id := range ruleIDs {
		h := hits[id]
		w := fmt.Sprintf("체크포인트 지정 규칙 「%s」(ID %d)의 조건 값 %q 이(가) 바꾸는 이름 %d개(옛 이름 또는 새 이름)에 들어 있습니다 — 이 규칙을 쓰는 동적 체크포인트 그룹·이벤트 정책의 대상이 달라질 수 있습니다", h.name, id, h.value, h.n)
		if p := policies[id]; len(p) > 0 {
			w += " · 이 규칙을 쓰는 이벤트 정책: " + strings.Join(p, ", ")
		}
		out = append(out, w)
	}
	ids := []int64{}
	for _, c := range renamed {
		ids = append(ids, c.CheckpointID)
	}
	rows, e = q.QueryContext(ctx, "SELECT COALESCE(name,'') FROM alarm_policy WHERE flag=1 AND checkpoint_id IN ("+inList(len(ids))+")", idArgs(ids)...)
	if e != nil {
		out = append(out, "이벤트 정책을 확인하지 못했습니다: "+e.Error())
		return out
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		out = append(out, fmt.Sprintf("이름을 바꾸는 체크포인트를 직접 대상으로 한 이벤트 정책 %d개(%s) — 정책은 체크포인트 ID로 이어져 그대로 동작하지만, 이미 열린 알람에는 옛 이름이 남습니다", len(names), strings.Join(names, ", ")))
	}
	return out
}

// ManagePreview resolves the edits and checks them without writing anything.
func (s *Service) ManagePreview(ctx context.Context, edit ManageEdit) (ManagePreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, e := s.manageSession(edit.ManageID)
	if e != nil {
		return ManagePreview{}, e
	}
	m.previews = map[string]managePlan{} // only the latest preview can be applied
	plan, errs, warns := resolveEdit(m, edit)
	out := ManagePreview{Points: plan.points, Devices: plan.devices, Warnings: warns, Errors: errs}
	if len(plan.points)+len(plan.devices) == 0 && len(errs) == 0 {
		out.Errors = append(out.Errors, "바뀌는 것이 없습니다 — 새 이름이나 주기를 입력하세요")
	}
	if len(plan.points)+len(plan.devices) > 0 {
		db, e := openDB(ctx, m.Config)
		if e != nil {
			return out, e
		}
		defer db.Close()
		conflicts, e := deviceNameConflicts(ctx, db, plan.devices)
		if e != nil {
			return out, fmt.Errorf("장비 이름 중복 확인 실패: %w", e)
		}
		out.Errors = append(out.Errors, conflicts...)
		out.Warnings = append(out.Warnings, nameWarnings(ctx, db, plan.points)...)
		out.Warnings = append(out.Warnings, deviceNameWarnings(ctx, db, plan.devices)...)
	}
	devices, ifaces := map[int64]bool{}, map[int64]bool{}
	for _, c := range plan.points {
		devices[c.DeviceID], ifaces[c.InterfaceID] = true, true
		if c.NewName != c.OldName {
			out.Renamed++
		}
		if c.NewInterval != c.OldInterval {
			out.Intervals++
		}
	}
	for _, d := range plan.devices {
		devices[d.DeviceID] = true
	}
	out.Touched, out.Interfaces = len(devices), len(ifaces)
	if len(out.Errors) == 0 {
		out.ID = uuid.NewString()
		m.previews[out.ID] = plan
	}
	return out, nil
}

func (s *Service) findPreview(id string) (*manageSession, managePlan, error) {
	for _, m := range s.manage.sessions {
		if p, ok := m.previews[id]; ok && time.Now().Before(m.Expires) {
			return m, p, nil
		}
	}
	return nil, managePlan{}, fmt.Errorf("미리보기가 만료됐거나 이미 적용했습니다 — 다시 미리보기 하세요")
}

// localIP is this PC's address toward the DB, for liz_log.ip_address.
func localIP(c Config) string {
	conn, e := net.DialTimeout("udp", net.JoinHostPort(c.DBHost, strconv.Itoa(c.DBPort)), 2*time.Second)
	if e != nil {
		return ""
	}
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

const logSource = "rtaskmgr"

// liz_log values seen from admin on trunk: log_type 113 update / 114 delete,
// module_type 1 (admin console), object_type 1 device / 2 checkpoint /
// 3 device group / 5 device explorer, result 1. There is no user: user_id NULL,
// user_name "rtaskmgr" (shown in the device history screen).
const (
	logUpdate      = 113
	logDelete      = 114
	objectDevice   = 1
	objectPoint    = 2
	objectGroup    = 3
	objectExplorer = 5
)

func lizLog(ctx context.Context, tx *sql.Tx, dc sql.NullInt64, ip, msg string, logType int, objID int64, objName string, objType int) error {
	var addr any
	if ip != "" {
		addr = ip
	}
	_, e := tx.ExecContext(ctx, "INSERT INTO liz_log (log_date,data_center_id,ip_address,liz_module_id,liz_module_name,log,log_type,module_type,object_id,object_name,object_type,result,revision_id,user_id,user_name,user_session_id) VALUES (NOW(3),?,?,NULL,?,?,?,1,?,?,?,1,NULL,NULL,?,NULL)",
		dc, addr, logSource, msg, logType, objID, objName, objType, logSource)
	return e
}

// auditMods writes one Envers row like admin's entity save: every column of the
// row, _mod=1 only for the listed (changed) columns.
func auditMods(ctx context.Context, tx *sql.Tx, cache map[string]auditShape, table string, id, rev int64, revtype int, entity string, mods ...string) error {
	shape, e := auditColumns(ctx, tx, cache, table)
	if e != nil {
		return e
	}
	changed := map[string]bool{}
	for _, c := range mods {
		changed[c] = true
	}
	quoted, expr, args := []string{}, []string{}, []any{}
	for _, c := range shape.columns {
		quoted = append(quoted, "`"+c+"`")
		switch {
		case c == "rev":
			expr = append(expr, "?")
			args = append(args, rev)
		case c == "revtype":
			expr = append(expr, "?")
			args = append(args, revtype)
		case shape.base[c]:
			expr = append(expr, "`"+c+"`")
		case strings.HasSuffix(c, "_mod"):
			if changed[strings.TrimSuffix(c, "_mod")] {
				expr = append(expr, "1")
			} else {
				expr = append(expr, "0")
			}
		default:
			expr = append(expr, "NULL")
		}
	}
	args = append(args, id)
	if _, e = tx.ExecContext(ctx, "INSERT INTO `"+table+"_aud` ("+strings.Join(quoted, ",")+") SELECT "+strings.Join(expr, ",")+" FROM `"+table+"` WHERE id=?", args...); e != nil {
		return e
	}
	full := "com.onion.liz.common.database.entity." + entity
	_, e = tx.ExecContext(ctx, "INSERT INTO revchanges (rev,entityname) SELECT ?,? WHERE NOT EXISTS (SELECT 1 FROM revchanges WHERE rev=? AND entityname=?)", rev, full, rev, full)
	return e
}

// checkGroup re-reads the group inside the transaction.
func checkGroup(ctx context.Context, tx *sql.Tx, groupID int64, name string) error {
	var now string
	e := tx.QueryRowContext(ctx, "SELECT name FROM device_group WHERE id=? FOR UPDATE", groupID).Scan(&now)
	if errors.Is(e, sql.ErrNoRows) {
		return fmt.Errorf("그룹(ID %d)이 없습니다 — 다른 곳에서 삭제됐습니다. 아무것도 바꾸지 않았습니다", groupID)
	}
	if e != nil {
		return e
	}
	if !testerOwnsGroup(now) {
		return fmt.Errorf("%s: %s", testerGroupRule(), now)
	}
	if now != name {
		return fmt.Errorf("그룹 이름이 불러온 뒤 바뀌었습니다(%s → %s) — 다시 불러오세요. 아무것도 바꾸지 않았습니다", name, now)
	}
	return nil
}

// applyPlan writes the changes, the audit rows and liz_log in tx. A row that
// changed since it was loaded is skipped and reported; a device name that ends
// up used twice fails the whole apply.
func applyPlan(ctx context.Context, tx *sql.Tx, m *manageSession, plan managePlan, ip string) ([]PointChange, []DeviceChange, []string, error) {
	if e := checkGroup(ctx, tx, m.Group.GroupID, m.Group.Name); e != nil {
		return nil, nil, nil, e
	}
	if len(plan.points)+len(plan.devices) > 0 {
		// Every device touched must still be a free device of this group.
		if !m.rule.Valid {
			return nil, nil, nil, fmt.Errorf("그룹에 장비 규칙이 없습니다 — 아무것도 바꾸지 않았습니다")
		}
		now, _, e := groupDevices(ctx, tx, m.rule.Int64, m.DriverID, true)
		if e != nil {
			return nil, nil, nil, e
		}
		free := map[int64]string{}
		for _, d := range now {
			free[d.DeviceID] = d.Locked
		}
		check := func(id int64, name string) error {
			lock, ok := free[id]
			if !ok {
				return fmt.Errorf("장비 %s(ID %d)가 불러온 뒤 이 그룹에서 빠졌거나 삭제됐습니다 — 아무것도 바꾸지 않았습니다. 다시 불러오세요", name, id)
			}
			if lock != "" {
				return fmt.Errorf("장비 %s: %s — 아무것도 바꾸지 않았습니다", name, lock)
			}
			return nil
		}
		for _, c := range plan.points {
			if e = check(c.DeviceID, c.DeviceName); e != nil {
				return nil, nil, nil, e
			}
		}
		for _, d := range plan.devices {
			if e = check(d.DeviceID, d.OldName); e != nil {
				return nil, nil, nil, e
			}
		}
	}
	points, devices, conflicts := []PointChange{}, []DeviceChange{}, []string{}
	for _, c := range plan.points {
		sets, args := []string{}, []any{}
		if c.NewName != c.OldName {
			sets = append(sets, "name=?", "display_name=?")
			args = append(args, c.NewName, c.NewName)
		}
		if c.NewInterval != c.OldInterval {
			sets = append(sets, "check_interval=?", "keep_interval=?")
			args = append(args, c.NewInterval, c.NewInterval)
		}
		sets = append(sets, "update_date=NOW(3)")
		args = append(args, c.CheckpointID, c.InterfaceID, c.OldName, c.OldInterval)
		r, e := tx.ExecContext(ctx, "UPDATE checkpoint SET "+strings.Join(sets, ", ")+" WHERE id=? AND device_interface_id=? AND flag=1 AND BINARY COALESCE(name,'')=BINARY ? AND COALESCE(check_interval,0)=?", args...)
		if e != nil {
			return nil, nil, nil, e
		}
		if n, err := r.RowsAffected(); err != nil || n != 1 {
			conflicts = append(conflicts, pointConflict(ctx, tx, c))
			continue
		}
		points = append(points, c)
	}
	for _, d := range plan.devices {
		r, e := tx.ExecContext(ctx, "UPDATE device SET name=?, update_date=NOW(3) WHERE id=? AND flag=1 AND BINARY COALESCE(name,'')=BINARY ? AND EXISTS (SELECT 1 FROM device_specify_rule_mapping_device m WHERE m.device_id=device.id AND m.device_specify_rule_id=?)", d.NewName, d.DeviceID, d.OldName, m.rule.Int64)
		if e != nil {
			return nil, nil, nil, e
		}
		if n, err := r.RowsAffected(); err != nil || n != 1 {
			conflicts = append(conflicts, fmt.Sprintf("장비 %d (%s): 불러온 뒤 이름이 바뀌었거나, 삭제됐거나, 그룹에서 빠져 건너뜀", d.DeviceID, d.OldName))
			continue
		}
		devices = append(devices, d)
	}
	for _, d := range devices {
		var n int
		if e := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM device WHERE flag=1 AND name=? AND id<>?", d.NewName, d.DeviceID).Scan(&n); e != nil {
			return nil, nil, nil, e
		}
		if n > 0 {
			return nil, nil, nil, fmt.Errorf("장비 이름 %q 을(를) 다른 장비가 쓰고 있습니다(미리보기 뒤에 생김) — 아무것도 바꾸지 않았습니다", d.NewName)
		}
	}
	if len(points)+len(devices) == 0 {
		return points, devices, conflicts, nil
	}
	rev, e := insert(ctx, tx, "INSERT INTO revinfo (timestamp) VALUES (?)", time.Now().UnixMilli())
	if e != nil {
		return nil, nil, nil, e
	}
	cache := map[string]auditShape{}
	for _, c := range points {
		mods := []string{"update_date"}
		if c.NewName != c.OldName {
			mods = append(mods, "name", "display_name")
		}
		if c.NewInterval != c.OldInterval {
			mods = append(mods, "check_interval", "keep_interval")
		}
		if e = auditMods(ctx, tx, cache, "checkpoint", c.CheckpointID, rev, 1, "Checkpoint", mods...); e != nil {
			return nil, nil, nil, e
		}
		if e = lizLog(ctx, tx, m.devices[c.DeviceID].dc, ip, "Point Updated.", logUpdate, c.CheckpointID, c.NewName, objectPoint); e != nil {
			return nil, nil, nil, e
		}
	}
	for _, d := range devices {
		if e = auditMods(ctx, tx, cache, "device", d.DeviceID, rev, 1, "Device", "name", "update_date"); e != nil {
			return nil, nil, nil, e
		}
		if e = lizLog(ctx, tx, m.devices[d.DeviceID].dc, ip, "Device properties information has been modified.", logUpdate, d.DeviceID, d.NewName, objectDevice); e != nil {
			return nil, nil, nil, e
		}
	}
	return points, devices, conflicts, nil
}

func pointConflict(ctx context.Context, tx *sql.Tx, c PointChange) string {
	var name string
	var flag, interval, iface int64
	e := tx.QueryRowContext(ctx, "SELECT COALESCE(name,''), flag, COALESCE(check_interval,0), COALESCE(device_interface_id,0) FROM checkpoint WHERE id=?", c.CheckpointID).Scan(&name, &flag, &interval, &iface)
	switch {
	case e != nil || flag != 1:
		return fmt.Sprintf("체크포인트 %d (%s · %s): 불러온 뒤 삭제돼 건너뜀", c.CheckpointID, c.Key, c.DeviceName)
	case iface != c.InterfaceID:
		return fmt.Sprintf("체크포인트 %d (%s · %s): 불러온 뒤 다른 장비로 옮겨져 건너뜀", c.CheckpointID, c.Key, c.DeviceName)
	default:
		return fmt.Sprintf("체크포인트 %d (%s · %s): 불러온 뒤 다른 곳에서 바뀌어 건너뜀 — 지금 이름 %q, 주기 %d ms", c.CheckpointID, c.Key, c.DeviceName, name, interval)
	}
}

func uniqueIDs(ids []int64) []int64 {
	out, seen := []int64{}, map[int64]bool{}
	for _, v := range ids {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// manageNotices: CHECKPOINT_UPDATED{interfaces} → SPECIFY_RULE_UPDATED, and for
// renamed devices SPECIFY_RULE_UPDATED → DEVICE_UPDATED (one SPECIFY serves both).
func manageNotices(ifaces, devices []int64) []json.RawMessage {
	out := []json.RawMessage{}
	if len(ifaces) > 0 {
		out = append(out, notification("CHECKPOINT_UPDATED_NOTIFICATION", map[string]any{"deviceInterfaceIds": uniqueIDs(ifaces)}, []string{}))
	}
	if len(ifaces)+len(devices) > 0 {
		out = append(out, notification("SPECIFY_RULE_UPDATED_NOTIFICATION", map[string]any{"specifyRuleId": nil}, specifyRecipients))
	}
	if len(devices) > 0 {
		out = append(out, notification("DEVICE_UPDATED_NOTIFICATION", map[string]any{"deviceIds": uniqueIDs(devices), "restartService": true}, []string{}))
	}
	return out
}

// liveDevices keeps ids of devices that exist with flag=1: the collector fails
// the whole DEVICE_UPDATED on an id it cannot load.
func liveDevices(ctx context.Context, q queryer, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return []int64{}, nil
	}
	rows, e := q.QueryContext(ctx, "SELECT id FROM device WHERE flag=1 AND id IN ("+inList(len(ids))+") ORDER BY id", idArgs(ids)...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// publish sends msgs[*sent:] in order; *sent counts what the broker took.
func publish(ctx context.Context, c Config, msgs []json.RawMessage, sent *int) error {
	if *sent >= len(msgs) {
		return nil
	}
	w, done, e := pipelineWriter(c)
	if e != nil {
		return e
	}
	defer done()
	for *sent < len(msgs) {
		if e = w.WriteMessages(ctx, kafka.Message{Key: []byte(pipelineKey), Value: msgs[*sent]}); e != nil {
			return e
		}
		*sent++
	}
	return nil
}

// ManageApply applies a preview: one transaction, then the notices and the
// collection check. The record of the run is written before COMMIT, so notices
// that could not be sent can be sent later, after a restart too.
func (s *Service) ManageApply(ctx context.Context, previewID string) (ManageResult, error) {
	s.manage.run.Lock()
	defer s.manage.run.Unlock()
	rec, cfg, res, e := s.applyLocked(ctx, previewID)
	if e != nil || rec == nil {
		return res, e
	}
	out := s.finishRename(ctx, cfg, rec)
	return out, nil
}

// applyLocked is the part of an apply that needs s.mu: the session and the DB transaction.
func (s *Service) applyLocked(ctx context.Context, previewID string) (*manageRecord, Config, ManageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, plan, e := s.findPreview(previewID)
	if e != nil {
		return nil, Config{}, ManageResult{}, e
	}
	db, e := openDB(ctx, m.Config)
	if e != nil {
		return nil, m.Config, ManageResult{}, e
	}
	defer db.Close()
	if e = checkKafka(ctx, m.Config); e != nil {
		return nil, m.Config, ManageResult{}, noteErr(e, " — 수집 서버에 알릴 수 없어 아무것도 바꾸지 않았습니다")
	}
	ip := localIP(m.Config)
	s.progress("1/3 DB에 저장 중 — 체크포인트 %d개 · 장비 %d개", len(plan.points), len(plan.devices))
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return nil, m.Config, ManageResult{}, e
	}
	defer tx.Rollback()
	points, devices, conflicts, e := applyPlan(ctx, tx, m, plan, ip)
	if e != nil {
		return nil, m.Config, ManageResult{}, e
	}
	res := ManageResult{ID: uuid.NewString(), Updated: []int64{}, Renamed: []int64{}, Conflicts: conflicts, Missing: []int64{}}
	delete(m.previews, previewID)
	if len(points)+len(devices) == 0 {
		res.Message = fmt.Sprintf("바뀐 것이 없습니다 — 불러온 뒤 다른 곳에서 바뀐 항목 %d개를 건너뛰었습니다. 다시 불러와 확인하세요.", len(conflicts))
		return nil, m.Config, res, nil
	}
	ifaces, watch, longest := []int64{}, []int64{}, 0
	for _, c := range points {
		res.Updated = append(res.Updated, c.CheckpointID)
		ifaces = append(ifaces, c.InterfaceID)
		watch = append(watch, c.CheckpointID)
		if c.NewInterval > longest {
			longest = c.NewInterval
		}
	}
	renamed := []int64{}
	for _, d := range devices {
		res.Renamed = append(res.Renamed, d.DeviceID)
		renamed = append(renamed, d.DeviceID)
		// DEVICE_UPDATED restarts the device's monitors: check its checkpoints too.
		for _, p := range m.points {
			if p.DeviceID == d.DeviceID {
				watch = append(watch, p.CheckpointID)
				if p.Interval > longest {
					longest = p.Interval
				}
			}
		}
	}
	// The renamed devices were just updated with flag=1 in this transaction.
	rec := &manageRecord{ID: res.ID, Kind: kindRename, At: time.Now().Format(time.RFC3339), GroupName: m.Group.Name, DBHost: m.Config.DBHost, DBPort: m.Config.DBPort, Brokers: m.Config.Brokers,
		Msgs: manageNotices(ifaces, renamed), Points: points, Devices: devices, Watch: uniqueIDs(watch), Interval: longest, Rename: res}
	if e = s.saveRecord(rec); e != nil {
		return nil, m.Config, ManageResult{}, fmt.Errorf("작업 기록 저장 실패 — 아무것도 바꾸지 않았습니다: %w", e)
	}
	if e = tx.Commit(); e != nil {
		delete(s.manage.sessions, m.Group.ID)
		return nil, m.Config, res, fmt.Errorf("저장(커밋) 결과를 알 수 없습니다. 알림은 보내지 않았습니다 — 「마무리 안 된 작업」의 「수집 서버에 다시 알리기」가 저장 여부부터 확인합니다: %w", e)
	}
	// Names changed: the loaded snapshot is stale, the screen loads the group again.
	delete(s.manage.sessions, m.Group.ID)
	rec.Committed, rec.Rename.Committed = true, true
	_ = s.saveRecord(rec) // if this fails the record says "not sure"; a resend checks the DB
	rec.Learned = s.learnManaged(m, points, res.ID)
	return rec, m.Config, rec.Rename, nil
}

// learnManaged remembers the new names (and intervals) as registration
// patterns, so deleting and registering again brings them back. Every live
// checkpoint of a touched key shape is given, so the majority stays right.
func (s *Service) learnManaged(m *manageSession, points []PointChange, id string) string {
	if len(points) == 0 {
		return ""
	}
	changed := map[int64]PointChange{}
	shapes := map[string]bool{}
	for _, c := range points {
		changed[c.CheckpointID] = c
		shapes[keyShape(c.Key)] = true
	}
	learn := []Point{}
	for _, p := range m.points {
		if !shapes[keyShape(p.Key)] || p.Host == "" {
			continue
		}
		pt := Point{Key: p.Key, Name: p.Name, Host: p.Host, TemplateID: p.TemplateID, Format: p.Format, Interval: p.Interval, Selected: true}
		if c, ok := changed[p.CheckpointID]; ok {
			pt.Name, pt.Interval = c.NewName, c.NewInterval
		}
		learn = append(learn, pt)
	}
	sort.Slice(learn, func(i, j int) bool { return learn[i].Key < learn[j].Key })
	ev, e := s.learnPatterns(learn, "manage "+id)
	if e != nil {
		return "(이름 기억 실패: " + e.Error() + ")"
	}
	if n := len(ev.Changes); n > 0 {
		return fmt.Sprintf("바꾼 포인트 이름·주기 %d건을 기억해 다음 등록에도 씁니다(장비 이름은 기억하지 않음).", n)
	}
	return ""
}

// finishRename sends the remaining notices and checks collection, without
// s.mu. If a notice cannot be sent the record stays pending for a resend.
func (s *Service) finishRename(ctx context.Context, cfg Config, rec *manageRecord) ManageResult {
	watch := s.watchCollection(ctx, cfg, rec.Watch)
	defer watch.close()
	s.progress("2/3 수집 서버에 변경 알리는 중(%d건)…", len(rec.Msgs)-rec.Sent)
	e := publish(ctx, cfg, rec.Msgs, &rec.Sent)
	if e != nil {
		rec.Rename.Pending = true
		rec.Rename.Message = fmt.Sprintf("DB에는 저장됐지만 알림 %d/%d건만 보냈습니다 — 「수집 서버에 다시 알리기」를 누르세요(DB는 다시 바꾸지 않음, 앱을 다시 켜도 「마무리 안 된 작업」에 남음): %v", rec.Sent, len(rec.Msgs), e)
		s.recordNote(rec)
		return rec.Rename
	}
	rec.Rename.Pending, rec.Rename.Notified = false, true
	rec.Done = true
	_ = s.saveRecord(rec)
	window := verifyWindow([]Point{{Interval: rec.Interval}}, s.VerifyWindow)
	seen := watch.wait(ctx, window, func(done, total int, left time.Duration) {
		s.progress("3/3 수집 확인 중 — %d/%d개 값 갱신 (최대 %d초 남음)", done, total, int(left.Seconds()+0.99))
	})
	summarizeManaged(&rec.Rename, rec.Watch, seen, watch.err)
	for _, n := range []string{rec.Note, rec.Learned} {
		if n != "" {
			rec.Rename.Message += " " + n
		}
	}
	s.recordNote(rec)
	return rec.Rename
}

func summarizeManaged(r *ManageResult, ids []int64, seen map[int64]int, watchErr error) {
	saved := fmt.Sprintf("체크포인트 %d개 · 장비 %d개를 바꿨습니다", len(r.Updated), len(r.Renamed))
	if len(r.Conflicts) > 0 {
		saved += fmt.Sprintf(" (다른 곳에서 바뀐 %d개는 건너뜀)", len(r.Conflicts))
	}
	r.Watched, r.Collected, r.Missing = len(ids), 0, []int64{}
	if watchErr != nil && seen == nil {
		r.Message = saved + ". 수집 확인은 못 했습니다(" + watchErr.Error() + ") — admin 에서 값이 갱신되는지 확인하세요."
		return
	}
	partial := 0
	for _, id := range ids {
		switch n := seen[id]; {
		case n == 0:
			r.Missing = append(r.Missing, id)
		case n < collectNeed:
			partial++
			r.Collected++
		default:
			r.Collected++
		}
	}
	sort.Slice(r.Missing, func(i, j int) bool { return r.Missing[i] < r.Missing[j] })
	r.Verified = len(r.Missing) == 0 && partial == 0
	switch {
	case r.Verified:
		r.Message = fmt.Sprintf("%s. 바꾼 뒤에도 %d개 모두 값이 계속 갱신되고 있습니다.", saved, len(ids))
	case len(r.Missing) == 0:
		r.Message = fmt.Sprintf("%s. 모두 값은 있지만 %d개는 확인 시간 안에 새 값이 안 들어왔습니다(주기가 길거나 원본 키가 멈춤).", saved, partial)
	default:
		r.Message = fmt.Sprintf("%s. 하지만 %d/%d개만 값이 갱신됩니다. 값이 없는 체크포인트: %s — 수집 서버 로그를 확인하세요.\n자세히: Active Collector 의 collector.log 에서 Received/Done 과 「Cannot find CheckpointCdo」 를 확인하세요.", saved, r.Collected, len(ids), idList(r.Missing, 20))
	}
	if watchErr != nil {
		r.Message += " (확인 도중 Redis 오류: " + watchErr.Error() + ")"
	}
}

// ManageResend continues an apply or a delete whose notices were not all
// sent, also after a restart; planID gives the current connection.
func (s *Service) ManageResend(ctx context.Context, planID, id string) (ManageResult, DeleteResult, error) {
	s.manage.run.Lock()
	defer s.manage.run.Unlock()
	rec, cfg, e := s.resumeRecord(ctx, planID, id)
	if e != nil || rec.Done {
		if rec != nil {
			return rec.Rename, rec.Delete, e
		}
		return ManageResult{}, DeleteResult{}, e
	}
	if rec.Kind == kindDelete {
		return ManageResult{}, s.finishDelete(ctx, cfg, rec), nil
	}
	return s.finishRename(ctx, cfg, rec), DeleteResult{}, nil
}

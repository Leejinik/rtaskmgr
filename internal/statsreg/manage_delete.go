package statsreg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Deleting a device or a whole group of a registration, the way admin deletes
// a device (trunk #987, observed 2026-10-22):
//   - one transaction: device / device_interface / checkpoint / device_control
//     flag=-1 (bulk updates, no _aud rows, like admin) + liz_log "Device Deleted."
//     per device; for a whole group then the device_group row itself, like admin's
//     tree save: _aud revtype 2 + hard DELETE + liz_log. Its device_specify_rule
//     and mapping rows are left behind exactly as admin leaves them (nothing reads
//     a rule without its group; deleted devices' mappings are filtered by flag);
//   - after COMMIT: DEVICE_DELETED{ids} → SPECIFY_RULE_UPDATED (no CHECKPOINT_UPDATED:
//     a checkpoint deleted on its own keeps being polled by the collector, B-25);
//   - Redis checkpoint:<id> of the deleted checkpoints: this build of the collector
//     leaves them forever (TTL -1, 427/434 left on trunk 2026-10-23), so the tool
//     deletes them itself after the collector had time to stop the devices.
// Not done (refused instead): event policies that target the checkpoints. Admin
// deletes them and closes their open alarms with ALARM_* notices; a device with
// such a policy has to be deleted in admin.
// Only whole devices: a checkpoint deleted alone keeps the collector warning
// ~10 times a second until restart (B-25), so per-checkpoint delete is not offered.

type DeleteRequest struct {
	ManageID   string `json:"manageId"`
	DeviceID   int64  `json:"deviceId"`   // one device of the group
	WholeGroup bool   `json:"wholeGroup"` // every device, then the group
}

type DeletePreview struct {
	ID          string          `json:"id"` // empty when blocked
	WholeGroup  bool            `json:"wholeGroup"`
	GroupName   string          `json:"groupName"`
	Devices     []ManagedDevice `json:"devices"`
	Checkpoints int             `json:"checkpoints"`
	// Confirm: what has to be typed to enable the delete button.
	Confirm  string   `json:"confirm"`
	Blockers []string `json:"blockers"`
	Warnings []string `json:"warnings"`
}

type deletePlan struct {
	whole bool
	// orphan: one device that is in no group (manage_orphan.go); typeID re-checks it.
	orphan      bool
	typeID      int64
	groupID     int64
	groupName   string
	rule        int64
	driverID    int64
	devices     []ManagedDevice
	checkpoints []int64
	longest     int
	fingerprint string
	confirm     string
}

type DeleteResult struct {
	ID           string   `json:"id"`
	Committed    bool     `json:"committed"`
	Notified     bool     `json:"notified"`
	Pending      bool     `json:"pending"`
	DeviceIDs    []int64  `json:"deviceIds"`
	Checkpoints  int      `json:"checkpoints"`
	GroupDeleted bool     `json:"groupDeleted"`
	KeysDeleted  int      `json:"keysDeleted"`
	KeysLeft     []string `json:"keysLeft"`
	DBLeft       int      `json:"dbLeft"` // rows not deleted after the commit (should be 0)
	Verified     bool     `json:"verified"`
	Message      string   `json:"message"`
}

func deleteFingerprint(devices []ManagedDevice, checkpoints []int64) string {
	devs := deviceIDs(devices)
	sort.Slice(devs, func(i, j int) bool { return devs[i] < devs[j] })
	cps := append([]int64{}, checkpoints...)
	sort.Slice(cps, func(i, j int) bool { return cps[i] < cps[j] })
	ids := []string{}
	for _, d := range devs {
		ids = append(ids, "d"+strconv.FormatInt(d, 10))
	}
	for _, c := range cps {
		ids = append(ids, "c"+strconv.FormatInt(c, 10))
	}
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return hex.EncodeToString(sum[:])
}

func liveCheckpoints(ctx context.Context, q queryer, devices []int64, lock bool) ([]int64, int, error) {
	if len(devices) == 0 {
		return []int64{}, 0, nil
	}
	rows, e := q.QueryContext(ctx, "SELECT id, COALESCE(check_interval,0) FROM checkpoint WHERE device_id IN ("+inList(len(devices))+") AND flag=1 ORDER BY id"+forUpdate(lock), idArgs(devices)...)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	ids, longest := []int64{}, 0
	for rows.Next() {
		var id int64
		var interval int
		if e = rows.Scan(&id, &interval); e != nil {
			return nil, 0, e
		}
		ids = append(ids, id)
		if interval > longest {
			longest = interval
		}
	}
	return ids, longest, rows.Err()
}

// checkpointPolicies: live event policies targeting one of the checkpoints
// (admin's own lookup: flag<>-1 and checkpoint_id IN …).
func checkpointPolicies(ctx context.Context, q queryer, ids []int64, lock bool) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, e := q.QueryContext(ctx, "SELECT id, COALESCE(name,''), checkpoint_id FROM alarm_policy WHERE flag<>-1 AND checkpoint_id IN ("+inList(len(ids))+") ORDER BY id"+forUpdate(lock), idArgs(ids)...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id, cp int64
		var name string
		if e = rows.Scan(&id, &name, &cp); e != nil {
			return nil, e
		}
		out = append(out, fmt.Sprintf("「%s」(정책 ID %d → 체크포인트 %d)", name, id, cp))
	}
	return out, rows.Err()
}

// conditionRefs: checkpoint specify-rule conditions that name one of the
// checkpoints by id. The collector (SpecifyRuleHandlerUsingCdo) fails on such a
// condition once the checkpoint is gone, which disables every policy of the rule.
func conditionRefs(ctx context.Context, q queryer, ids []int64) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, e := q.QueryContext(ctx, `SELECT DISTINCT r.id, COALESCE(r.name,''), v.checkpoint_id FROM checkpoint_specify_rule_condition_value v
JOIN checkpoint_specify_rule_condition c ON c.id=v.checkpoint_specify_rule_condition_id
JOIN checkpoint_specify_rule r ON r.id=c.checkpoint_specify_rule_id
WHERE v.checkpoint_id IN (`+inList(len(ids))+`) ORDER BY r.id`, idArgs(ids)...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id, cp int64
		var name string
		if e = rows.Scan(&id, &name, &cp); e != nil {
			return nil, e
		}
		out = append(out, fmt.Sprintf("「%s」(규칙 ID %d → 체크포인트 %d)", name, id, cp))
	}
	return out, rows.Err()
}

func deviceIDs(devices []ManagedDevice) []int64 {
	out := []int64{}
	for _, d := range devices {
		out = append(out, d.DeviceID)
	}
	return out
}

// buildDelete reads what a delete would remove and what blocks it. lock: the
// reads run inside the delete transaction and lock what they read.
func buildDelete(ctx context.Context, q queryer, groupID, driverID int64, req DeleteRequest, lock bool) (deletePlan, DeletePreview, error) {
	var name string
	var explorer, rule sql.NullInt64
	if e := q.QueryRowContext(ctx, "SELECT name, explorer_id, device_specify_rule_id FROM device_group WHERE id=?", groupID).Scan(&name, &explorer, &rule); e != nil {
		return deletePlan{}, DeletePreview{}, fmt.Errorf("그룹(ID %d)을 확인할 수 없습니다 — 다시 조회하세요: %w", groupID, e)
	}
	if !testerOwnsGroup(name) {
		return deletePlan{}, DeletePreview{}, fmt.Errorf("테스트 빌드(%s)는 %s 로 시작하는 그룹만 지웁니다: %s", TesterPrefix, testerGroupPrefix(), name)
	}
	plan := deletePlan{whole: req.WholeGroup, groupID: groupID, groupName: name, driverID: driverID, devices: []ManagedDevice{}}
	prev := DeletePreview{WholeGroup: req.WholeGroup, GroupName: name, Devices: []ManagedDevice{}, Blockers: []string{}, Warnings: []string{}}
	devs, others := []ManagedDevice{}, []string{}
	if rule.Valid {
		var e error
		if devs, others, e = groupDevices(ctx, q, rule.Int64, driverID, lock); e != nil {
			return plan, prev, e
		}
		plan.rule = rule.Int64
	}
	if req.WholeGroup {
		plan.devices = devs
		if len(others) > 0 {
			prev.Blockers = append(prev.Blockers, fmt.Sprintf("이 그룹에 리소스 수집 장비가 아닌 장비 %d대가 있습니다(%s) — 그룹을 지우면 그 장비는 어느 그룹에도 안 보이게 되므로 막습니다. admin 에서 먼저 옮기거나 지우세요.", len(others), strings.Join(others, ", ")))
		}
		var children int
		if e := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM device_group WHERE parent_id=?", groupID).Scan(&children); e != nil {
			return plan, prev, e
		}
		if children > 0 {
			prev.Blockers = append(prev.Blockers, fmt.Sprintf("하위 그룹 %d개가 있습니다 — admin 처럼 지우면 하위 그룹이 맨 위로 올라가므로 막습니다. admin 에서 정리하세요.", children))
		}
		prev.Confirm = name
	} else {
		for _, d := range devs {
			if d.DeviceID == req.DeviceID {
				plan.devices = append(plan.devices, d)
			}
		}
		if len(plan.devices) == 0 {
			return plan, prev, fmt.Errorf("장비(ID %d)가 이 그룹의 리소스 수집 장비가 아니거나 이미 삭제됐습니다 — 다시 불러오세요", req.DeviceID)
		}
		prev.Confirm = plan.devices[0].Name
	}
	for _, d := range plan.devices {
		if d.Locked != "" {
			prev.Blockers = append(prev.Blockers, fmt.Sprintf("장비 %s: %s.", d.Name, d.Locked))
		}
		if d.OtherInterfaces > 0 {
			prev.Blockers = append(prev.Blockers, fmt.Sprintf("장비 %s 에 다른 드라이버 인터페이스 %d개가 있습니다 — 지우면 그 수집도 멈추므로 막습니다. admin 에서 정리하세요.", d.Name, d.OtherInterfaces))
		}
	}
	ids, longest, e := liveCheckpoints(ctx, q, deviceIDs(plan.devices), lock)
	if e != nil {
		return plan, prev, e
	}
	plan.checkpoints, plan.longest = ids, longest
	counts := map[int64]int{}
	if len(ids) > 0 {
		rows, e := q.QueryContext(ctx, "SELECT device_id, COUNT(*) FROM checkpoint WHERE device_id IN ("+inList(len(plan.devices))+") AND flag=1 GROUP BY device_id", idArgs(deviceIDs(plan.devices))...)
		if e != nil {
			return plan, prev, e
		}
		for rows.Next() {
			var id int64
			var n int
			if e = rows.Scan(&id, &n); e != nil {
				rows.Close()
				return plan, prev, e
			}
			counts[id] = n
		}
		rows.Close()
	}
	for i := range plan.devices {
		plan.devices[i].Checkpoints = counts[plan.devices[i].DeviceID]
	}
	if e = deleteBlockers(ctx, q, &plan, &prev, lock); e != nil {
		return plan, prev, e
	}
	if req.WholeGroup && len(plan.devices) == 0 {
		prev.Warnings = append(prev.Warnings, "리소스 수집 장비가 없는 빈 그룹입니다 — 그룹만 지웁니다.")
	}
	plan.fingerprint = deleteFingerprint(plan.devices, ids)
	plan.confirm = prev.Confirm
	prev.Devices, prev.Checkpoints = plan.devices, len(ids)
	return plan, prev, nil
}

// deleteBlockers adds what refuses a delete (event policies or specify-rule
// conditions on its checkpoints) and, outside the transaction, open alarms.
func deleteBlockers(ctx context.Context, q queryer, plan *deletePlan, prev *DeletePreview, lock bool) error {
	policies, e := checkpointPolicies(ctx, q, plan.checkpoints, lock)
	if e != nil {
		return fmt.Errorf("이벤트 정책 확인 실패 — 지우지 않습니다: %w", e)
	}
	if len(policies) > 0 {
		prev.Blockers = append(prev.Blockers, fmt.Sprintf("지울 체크포인트를 대상으로 한 이벤트 정책 %d개가 있습니다: %s. 정책 삭제와 열린 알람 종료는 admin 이 하는 일이라 이 앱은 지우지 않습니다 — admin 에서 이 장비를 삭제하거나 정책을 먼저 지우세요.", len(policies), strings.Join(policies, ", ")))
	}
	if refs, e := conditionRefs(ctx, q, plan.checkpoints); e != nil {
		prev.Warnings = append(prev.Warnings, "체크포인트를 번호로 가리키는 지정 규칙 조건을 확인하지 못했습니다: "+e.Error())
	} else if len(refs) > 0 {
		prev.Blockers = append(prev.Blockers, fmt.Sprintf("지울 체크포인트를 번호로 가리키는 체크포인트 지정 규칙 조건이 있습니다: %s. 체크포인트가 지워지면 Collector가 이 조건에서 오류를 내 그 규칙을 쓰는 이벤트 정책 전체가 멈출 수 있으므로 막습니다 — admin 에서 조건을 먼저 지우세요.", strings.Join(refs, ", ")))
	}
	if len(plan.devices) > 0 && !lock {
		var open int
		if e := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1 FROM alarm WHERE device_id IN ("+inList(len(plan.devices))+") AND confirm_state<>2 LIMIT 1000) x", idArgs(deviceIDs(plan.devices))...).Scan(&open); e != nil {
			prev.Warnings = append(prev.Warnings, "열린 알람을 확인하지 못했습니다: "+e.Error())
		} else if open > 0 {
			prev.Warnings = append(prev.Warnings, fmt.Sprintf("이 장비에 아직 닫히지 않은 알람이 %d건 있습니다. 장비를 지워도 이 알람은 자동으로 닫히지 않을 수 있으니 필요하면 알람 화면에서 정리하세요.", open))
		}
	}
	return nil
}

// ManageDeletePreview shows what a delete removes; nothing is written.
func (s *Service) ManageDeletePreview(ctx context.Context, req DeleteRequest) (DeletePreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, e := s.manageSession(req.ManageID)
	if e != nil {
		return DeletePreview{}, e
	}
	m.removals = map[string]deletePlan{}
	db, e := openDB(ctx, m.Config)
	if e != nil {
		return DeletePreview{}, e
	}
	defer db.Close()
	plan, prev, e := buildDelete(ctx, db, m.Group.GroupID, m.DriverID, req, false)
	if e != nil {
		return prev, e
	}
	if len(prev.Blockers) == 0 {
		prev.ID = uuid.NewString()
		m.removals[prev.ID] = plan
	}
	return prev, nil
}

// deleteTx deletes the plan's devices (and the group) in tx after checking that
// nothing changed since the preview.
func deleteTx(ctx context.Context, tx *sql.Tx, plan deletePlan, ip string) error {
	if plan.orphan {
		// Still a device of this tool, in no group, unchanged since the preview.
		again, check, err := buildOrphanDelete(ctx, tx, plan.typeID, plan.driverID, firstDevice(plan.devices), true)
		if err != nil {
			return noteErr(err, " — 아무것도 지우지 않았습니다")
		}
		if again.fingerprint != plan.fingerprint {
			return fmt.Errorf("미리보기 뒤에 장비나 체크포인트가 바뀌었습니다 — 아무것도 지우지 않았습니다. 다시 미리보기 하세요")
		}
		if len(check.Blockers) > 0 {
			return fmt.Errorf("미리보기 뒤에 삭제를 막는 것이 생겼습니다 — 아무것도 지우지 않았습니다: %s", strings.Join(check.Blockers, " / "))
		}
		return deleteDevicesTx(ctx, tx, plan, ip)
	}
	var name string
	var explorer sql.NullInt64
	if e := tx.QueryRowContext(ctx, "SELECT name, explorer_id FROM device_group WHERE id=? FOR UPDATE", plan.groupID).Scan(&name, &explorer); e != nil {
		return fmt.Errorf("그룹(ID %d)을 확인할 수 없습니다 — 아무것도 지우지 않았습니다: %w", plan.groupID, e)
	}
	if name != plan.groupName || !testerOwnsGroup(name) {
		return fmt.Errorf("그룹 이름이 미리보기 뒤에 바뀌었습니다(%s → %s) — 아무것도 지우지 않았습니다", plan.groupName, name)
	}
	// Everything the preview checked, again inside the transaction.
	again, check, err := buildDelete(ctx, tx, plan.groupID, plan.driverID, DeleteRequest{WholeGroup: plan.whole, DeviceID: firstDevice(plan.devices)}, true)
	if err != nil {
		return fmt.Errorf("%w — 아무것도 지우지 않았습니다", err)
	}
	if again.fingerprint != plan.fingerprint {
		return fmt.Errorf("미리보기 뒤에 장비나 체크포인트가 바뀌었습니다 — 아무것도 지우지 않았습니다. 다시 미리보기 하세요")
	}
	if len(check.Blockers) > 0 {
		return fmt.Errorf("미리보기 뒤에 삭제를 막는 것이 생겼습니다 — 아무것도 지우지 않았습니다: %s", strings.Join(check.Blockers, " / "))
	}
	if e := deleteDevicesTx(ctx, tx, plan, ip); e != nil {
		return e
	}
	if !plan.whole {
		return nil
	}
	return deleteGroupTx(ctx, tx, plan, explorer, ip)
}

// deleteDevicesTx deletes the plan's devices like admin: bulk flag=-1 on the
// device, its interfaces, checkpoints and controls (no _aud), liz_log per device.
func deleteDevicesTx(ctx context.Context, tx *sql.Tx, plan deletePlan, ip string) error {
	ids := deviceIDs(plan.devices)
	if len(ids) > 0 {
		args := idArgs(ids)
		marks := inList(len(ids))
		r, e := tx.ExecContext(ctx, "UPDATE device SET flag=-1, update_date=NOW(3) WHERE id IN ("+marks+") AND flag=1", args...)
		if e != nil {
			return e
		}
		if n, err := r.RowsAffected(); err != nil || n != int64(len(ids)) {
			return fmt.Errorf("장비 %d대 중 %d대만 지울 수 있습니다 — 아무것도 지우지 않았습니다", len(ids), n)
		}
		for _, q := range []string{
			"UPDATE device_interface SET flag=-1 WHERE device_id IN (" + marks + ")",
			"UPDATE checkpoint SET flag=-1 WHERE device_id IN (" + marks + ")",
			"UPDATE device_control SET flag=-1 WHERE device_interface_id IN (SELECT id FROM device_interface WHERE device_id IN (" + marks + "))",
		} {
			if _, e = tx.ExecContext(ctx, q, args...); e != nil {
				return e
			}
		}
		for _, d := range plan.devices {
			if e = lizLog(ctx, tx, d.dc, ip, "Device Deleted.", logDelete, d.DeviceID, d.Name, objectDevice); e != nil {
				return e
			}
		}
	}
	return nil
}

// deleteGroupTx deletes the group row like admin's tree save.
func deleteGroupTx(ctx context.Context, tx *sql.Tx, plan deletePlan, explorer sql.NullInt64, ip string) error {
	rev, e := insert(ctx, tx, "INSERT INTO revinfo (timestamp) VALUES (?)", time.Now().UnixMilli())
	if e != nil {
		return e
	}
	// Envers keeps the deleted row's values; copy them before the DELETE.
	if e = auditMods(ctx, tx, map[string]auditShape{}, "device_group", plan.groupID, rev, 2, "DeviceGroup"); e != nil {
		return e
	}
	r, e := tx.ExecContext(ctx, "DELETE FROM device_group WHERE id=?", plan.groupID)
	if e != nil {
		return fmt.Errorf("그룹 행 삭제 실패 — 아무것도 지우지 않았습니다: %w", e)
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("그룹 행을 지우지 못했습니다 — 아무것도 지우지 않았습니다")
	}
	var dc sql.NullInt64
	var explorerName string
	if explorer.Valid {
		if e = tx.QueryRowContext(ctx, "SELECT COALESCE(name,''), data_center_id FROM device_explorer WHERE id=?", explorer.Int64).Scan(&explorerName, &dc); e != nil && e != sql.ErrNoRows {
			return e
		}
	}
	if e = lizLog(ctx, tx, dc, ip, "Device Group Deleted.", logDelete, plan.groupID, plan.groupName, objectGroup); e != nil {
		return e
	}
	if explorer.Valid {
		if e = lizLog(ctx, tx, dc, ip, "Device Explorer Updated.", logUpdate, explorer.Int64, explorerName, objectExplorer); e != nil {
			return e
		}
	}
	return nil
}

func firstDevice(d []ManagedDevice) int64 {
	if len(d) == 0 {
		return 0
	}
	return d[0].DeviceID
}

// deleteNotices: DEVICE_DELETED → SPECIFY_RULE_UPDATED (admin's order).
func deleteNotices(devices []int64) []json.RawMessage {
	out := []json.RawMessage{}
	if len(devices) > 0 {
		out = append(out, notification("DEVICE_DELETED_NOTIFICATION", map[string]any{"deviceIds": uniqueIDs(devices)}, []string{}))
	}
	return append(out, notification("SPECIFY_RULE_UPDATED_NOTIFICATION", map[string]any{"specifyRuleId": nil}, specifyRecipients))
}

// ManageDelete runs a delete preview. confirm must be the name the preview asked for.
func (s *Service) ManageDelete(ctx context.Context, previewID, confirm string) (DeleteResult, error) {
	s.manage.run.Lock()
	defer s.manage.run.Unlock()
	rec, cfg, res, e := s.deleteLocked(ctx, previewID, confirm)
	if e != nil || rec == nil {
		return res, e
	}
	return s.finishDelete(ctx, cfg, rec), nil
}

// deleteLocked is the part of a delete that needs s.mu: the session and the DB transaction.
func (s *Service) deleteLocked(ctx context.Context, previewID, confirm string) (*manageRecord, Config, DeleteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var m *manageSession
	var plan deletePlan
	for _, v := range s.manage.sessions {
		if p, ok := v.removals[previewID]; ok && time.Now().Before(v.Expires) {
			m, plan = v, p
		}
	}
	if m == nil {
		return nil, Config{}, DeleteResult{}, fmt.Errorf("삭제 미리보기가 만료됐거나 이미 실행했습니다 — 다시 미리보기 하세요")
	}
	if confirm != plan.confirm {
		return nil, m.Config, DeleteResult{}, fmt.Errorf("확인 칸에 %q 을(를) 그대로 입력하세요", plan.confirm)
	}
	db, e := openDB(ctx, m.Config)
	if e != nil {
		return nil, m.Config, DeleteResult{}, e
	}
	defer db.Close()
	if e = checkKafka(ctx, m.Config); e != nil {
		return nil, m.Config, DeleteResult{}, noteErr(e, " — 수집 서버에 알릴 수 없어 아무것도 지우지 않았습니다")
	}
	// Checked before writing: the keys are deleted by this tool after the commit,
	// and only a master takes a DEL.
	r, e := openRedisMaster(ctx, m.Config)
	if e != nil {
		return nil, m.Config, DeleteResult{}, noteErr(e, " — Redis 에 남은 최근값을 지울 수 없어 아무것도 지우지 않았습니다")
	}
	r.Close()
	s.progress("1/4 DB에서 삭제 중 — 장비 %d대 · 체크포인트 %d개", len(plan.devices), len(plan.checkpoints))
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return nil, m.Config, DeleteResult{}, e
	}
	defer tx.Rollback()
	if e = deleteTx(ctx, tx, plan, localIP(m.Config)); e != nil {
		return nil, m.Config, DeleteResult{}, e
	}
	delete(m.removals, previewID)
	res := DeleteResult{ID: uuid.NewString(), DeviceIDs: deviceIDs(plan.devices), Checkpoints: len(plan.checkpoints), KeysLeft: []string{}, GroupDeleted: plan.whole}
	keys := []string{}
	for _, id := range plan.checkpoints {
		keys = append(keys, checkpointValueKey+strconv.FormatInt(id, 10))
	}
	rec := &manageRecord{ID: res.ID, Kind: kindDelete, At: time.Now().Format(time.RFC3339), GroupName: plan.groupName, DBHost: m.Config.DBHost, DBPort: m.Config.DBPort, Brokers: m.Config.Brokers,
		Msgs: deleteNotices(res.DeviceIDs), DeviceIDs: res.DeviceIDs, GroupID: plan.groupID, Whole: plan.whole, Keys: keys, Longest: plan.longest, Delete: res}
	if e = s.saveRecord(rec); e != nil {
		return nil, m.Config, DeleteResult{}, fmt.Errorf("작업 기록 저장 실패 — 아무것도 지우지 않았습니다: %w", e)
	}
	if e = tx.Commit(); e != nil {
		delete(s.manage.sessions, m.Group.ID)
		return nil, m.Config, res, fmt.Errorf("삭제(커밋) 결과를 알 수 없습니다. 알림은 보내지 않았습니다 — 「끝나지 않은 작업」의 「알림 다시 보내기」가 저장 여부부터 확인합니다: %w", e)
	}
	delete(s.manage.sessions, m.Group.ID)
	rec.Committed, rec.Delete.Committed = true, true
	_ = s.saveRecord(rec)
	return rec, m.Config, rec.Delete, nil
}

const deleteSettle = 10 * time.Second

// finishDelete sends the remaining notices, lets the collector stop the
// devices, deletes their checkpoint:<id> keys on the Redis master and checks
// the result — without s.mu.
func (s *Service) finishDelete(ctx context.Context, cfg Config, rec *manageRecord) DeleteResult {
	s.progress("2/4 알림 %d건 보내는 중…", len(rec.Msgs)-rec.Sent)
	if e := publish(ctx, cfg, rec.Msgs, &rec.Sent); e != nil {
		rec.Delete.Pending = true
		rec.Delete.Message = fmt.Sprintf("DB에서는 지웠지만 알림 %d/%d건만 보냈습니다 — 「알림 다시 보내기」를 누르세요(DB는 다시 바꾸지 않음, 앱을 다시 켜도 「끝나지 않은 작업」에 남음): %v", rec.Sent, len(rec.Msgs), e)
		s.recordNote(rec)
		return rec.Delete
	}
	rec.Delete.Pending, rec.Delete.Notified = false, true
	s.recordNote(rec)
	// Let the collector stop the devices first, or it may write a key once more.
	wait := s.SettleWait
	if wait <= 0 {
		wait = deleteSettle
	}
	for left := wait; left > 0; left -= time.Second {
		s.progress("3/4 Collector가 장비를 멈출 시간을 줍니다 — %d초", int(left.Seconds()))
		select {
		case <-ctx.Done():
			rec.Delete.Message = "DB 삭제·알림까지 했고, 수집값 키를 지우기 전에 중단됐습니다 — 「Redis 정리 다시」를 누르세요."
			s.recordNote(rec)
			return rec.Delete
		case <-time.After(time.Second):
		}
	}
	s.cleanRecordKeys(ctx, cfg, rec, rec.Keys)
	return rec.Delete
}

// cleanRecordKeys deletes keys on the Redis master, checks they stay gone and
// what is left in the DB, and records the outcome. The delete is done when no key is left.
func (s *Service) cleanRecordKeys(ctx context.Context, cfg Config, rec *manageRecord, keys []string) {
	window := s.VerifyWindow
	if window <= 0 {
		window = time.Duration(rec.Longest)*time.Millisecond + 5*time.Second
		if window < 15*time.Second {
			window = 15 * time.Second
		}
		if window > 70*time.Second {
			window = 70 * time.Second
		}
	}
	var clean cleanup
	r, e := openRedisMaster(ctx, cfg)
	if e == nil {
		clean = cleanKeys(ctx, redisKeys{r}, keys, window, pollEvery, func(left time.Duration) {
			s.progress("4/4 수집값 키 %d개를 지우고 다시 생기는지 확인 중 (%d초 남음)", len(keys), int(left.Seconds()+0.99))
		})
		r.Close()
	} else {
		clean.err, clean.left = e, keys
	}
	rec.Delete.KeysDeleted, rec.Delete.KeysLeft = len(keys)-len(clean.left), clean.left
	db, e := openDB(ctx, cfg)
	dbErr := e
	if e == nil {
		rec.Delete.DBLeft, dbErr = deletedLeft(ctx, db, rec.DeviceIDs, rec.GroupID, rec.Whole)
		db.Close()
	}
	summarizeDelete(&rec.Delete, clean, dbErr)
	if clean.err != nil || len(clean.left) > 0 {
		rec.Delete.Message += " 「Redis 정리 다시」로 다시 지울 수 있습니다."
	}
	rec.Done = clean.err == nil && len(clean.left) == 0
	s.recordNote(rec)
}

// deletedLeft counts what should be gone after the commit but is not.
func deletedLeft(ctx context.Context, q queryer, ids []int64, groupID int64, whole bool) (int, error) {
	left := 0
	if len(ids) > 0 {
		for _, query := range []string{
			"SELECT COUNT(*) FROM device WHERE id IN (" + inList(len(ids)) + ") AND flag<>-1",
			"SELECT COUNT(*) FROM checkpoint WHERE device_id IN (" + inList(len(ids)) + ") AND flag<>-1",
		} {
			var n int
			if e := q.QueryRowContext(ctx, query, idArgs(ids)...).Scan(&n); e != nil {
				return left, e
			}
			left += n
		}
	}
	if whole {
		var n int
		if e := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM device_group WHERE id=?", groupID).Scan(&n); e != nil {
			return left, e
		}
		left += n
	}
	return left, nil
}

func summarizeDelete(r *DeleteResult, c cleanup, dbErr error) {
	what := fmt.Sprintf("장비 %d대 · 체크포인트 %d개를 지웠습니다", len(r.DeviceIDs), r.Checkpoints)
	if r.GroupDeleted {
		what += " (그룹 포함)"
	}
	parts := []string{what}
	switch {
	case dbErr != nil:
		parts = append(parts, "DB 확인 실패: "+dbErr.Error())
	case r.DBLeft > 0:
		parts = append(parts, fmt.Sprintf("DB에 아직 안 지워진 행이 %d개 있습니다 — admin 에서 확인하세요", r.DBLeft))
	default:
		parts = append(parts, "DB 확인: 남은 행 0")
	}
	switch {
	case c.err != nil:
		parts = append(parts, fmt.Sprintf("수집값 키(checkpoint:<id>) %d개를 다 지우지 못했습니다: %v", len(c.left), c.err))
	case len(c.left) > 0:
		parts = append(parts, fmt.Sprintf("지운 수집값 키 %d개가 다시 생겼습니다(%s) — Collector가 아직 이 장비를 수집하는지 확인하세요", len(c.left), strings.Join(firstN(c.left, 10), ", ")))
	default:
		parts = append(parts, fmt.Sprintf("수집값 키 %d개 삭제, 다시 생기지 않음", r.KeysDeleted))
		if c.again {
			parts[len(parts)-1] += "(한 번 다시 생긴 키를 한 번 더 지웠음)"
		}
	}
	r.Verified = dbErr == nil && r.DBLeft == 0 && c.err == nil && len(c.left) == 0
	r.Message = strings.Join(parts, ". ") + "."
}

func firstN(v []string, n int) []string {
	if len(v) > n {
		return append(append([]string{}, v[:n]...), fmt.Sprintf("외 %d개", len(v)-n))
	}
	return v
}

// keyStore is the part of Redis a delete needs (a fake in tests).
type keyStore interface {
	del(ctx context.Context, keys []string) error
	exist(ctx context.Context, keys []string) ([]string, error)
}

// redisKeys works on a single node and on a Redis Cluster alike: a pipeline of
// one-key commands, which the cluster client splits by slot.
type redisKeys struct{ r *redisConn }

func (k redisKeys) del(ctx context.Context, keys []string) error {
	for start := 0; start < len(keys); start += 1000 {
		pipe := k.r.Pipeline()
		for _, key := range keys[start:min(start+1000, len(keys))] {
			pipe.Del(ctx, key)
		}
		if _, e := pipe.Exec(ctx); e != nil {
			return e
		}
	}
	return nil
}

func (k redisKeys) exist(ctx context.Context, keys []string) ([]string, error) {
	out := []string{}
	for start := 0; start < len(keys); start += 1000 {
		chunk := keys[start:min(start+1000, len(keys))]
		pipe := k.r.Pipeline()
		cmds := make([]*redis.IntCmd, len(chunk))
		for i, key := range chunk {
			cmds[i] = pipe.Exists(ctx, key)
		}
		if _, e := pipe.Exec(ctx); e != nil {
			return nil, e
		}
		for i, c := range cmds {
			if c.Val() > 0 {
				out = append(out, chunk[i])
			}
		}
	}
	return out, nil
}

type cleanup struct {
	left  []string // keys still there at the end
	again bool     // some came back once and were deleted again
	err   error
}

// cleanKeys deletes keys, then watches for the window that they stay gone. A
// key written again by a collector that had not stopped yet is deleted once more;
// if it comes back again, it is reported.
func cleanKeys(ctx context.Context, k keyStore, keys []string, window, poll time.Duration, tick func(left time.Duration)) cleanup {
	c := cleanup{left: []string{}}
	if len(keys) == 0 {
		return c
	}
	if e := k.del(ctx, keys); e != nil {
		c.err, c.left = e, keys
		return c
	}
	deadline := time.Now().Add(window)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		tick(left)
		select {
		case <-ctx.Done():
			c.err = ctx.Err()
			return c
		case <-time.After(min(poll, left)):
		}
		back, e := k.exist(ctx, keys)
		if e != nil {
			c.err = e
			return c
		}
		if len(back) == 0 {
			continue
		}
		if c.again {
			c.left = back
			return c
		}
		if e = k.del(ctx, back); e != nil {
			c.err, c.left = e, back
			return c
		}
		c.again = true
	}
	back, e := k.exist(ctx, keys)
	c.left, c.err = back, e
	if c.left == nil {
		c.left = []string{}
	}
	return c
}

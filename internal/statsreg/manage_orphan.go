package statsreg

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Devices in no group: admin's group delete leaves the group's devices behind,
// outside every group and still collected (2nd round: device 7517 had to be
// found and deleted in admin). They are all listed here, but this tool deletes
// only what it registered itself — an MK119 v10 System Stats device whose every
// live checkpoint is a liz.stats.* key on that driver. Anything else (no
// checkpoint at all, another driver, another kind of checkpoint, another tester's
// device) is locked with where to find it in admin. Whether a device still
// qualifies is checked again inside the delete transaction.

type OrphanDevice struct {
	DeviceID    int64  `json:"deviceId"`
	Name        string `json:"name"`
	Checkpoints int    `json:"checkpoints"`
	// Locked: why this tool does not delete it ("" = it can).
	Locked string `json:"locked"`
	dc     sql.NullInt64
}

const orphanLimit = 500

// orphanDevices lists live devices in no group that are resource-stats devices by
// type or by interface (deviceID > 0: just that one, locked FOR UPDATE).
func orphanDevices(ctx context.Context, q queryer, typeID, driverID, deviceID int64, lock bool) ([]OrphanDevice, error) {
	where, args := "", []any{driverID, driverID, driverID, typeID, driverID}
	if deviceID > 0 {
		where = " AND d.id=?"
		args = append(args, deviceID)
	}
	rows, e := q.QueryContext(ctx, `SELECT d.id, COALESCE(d.name,''), d.data_center_id,
 (SELECT COUNT(*) FROM device_interface i WHERE i.device_id=d.id AND i.flag=1 AND i.device_driver_id=?),
 (SELECT COUNT(*) FROM device_interface i WHERE i.device_id=d.id AND i.flag=1 AND COALESCE(i.device_driver_id,0)<>?),
 (SELECT COUNT(*) FROM checkpoint c WHERE c.device_id=d.id AND c.flag=1),
 (SELECT COUNT(*) FROM checkpoint c JOIN device_interface i ON i.id=c.device_interface_id WHERE c.device_id=d.id AND c.flag=1 AND i.device_driver_id=? AND c.request_command LIKE 'liz.stats.%')
FROM device d
WHERE d.flag=1 AND (d.device_type_id=? OR EXISTS (SELECT 1 FROM device_interface i WHERE i.device_id=d.id AND i.flag=1 AND i.device_driver_id=?))
 AND NOT EXISTS (SELECT 1 FROM device_specify_rule_mapping_device m JOIN device_group g ON g.device_specify_rule_id=m.device_specify_rule_id WHERE m.device_id=d.id)`+where+`
ORDER BY d.id LIMIT `+fmt.Sprint(orphanLimit)+forUpdate(lock), args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []OrphanDevice{}
	for rows.Next() {
		var d OrphanDevice
		var mine, other, stats int
		if e = rows.Scan(&d.DeviceID, &d.Name, &d.dc, &mine, &other, &d.Checkpoints, &stats); e != nil {
			return nil, e
		}
		if TesterPrefix != "" && !strings.HasPrefix(d.Name, testerDevicePrefix()) {
			continue // another tester's or a product device: not shown in a tester build
		}
		reason := ""
		switch {
		case d.Checkpoints == 0:
			reason = "체크포인트가 하나도 없어 이 앱이 등록한 장비인지 알 수 없습니다"
		case mine == 0 || other > 0:
			reason = "리소스 수집(MK119 v10 System Stats) 말고 다른 수집 방식이 붙어 있습니다"
		case stats != d.Checkpoints:
			reason = fmt.Sprintf("리소스 수집 키가 아닌 체크포인트가 %d개 있습니다 — 이 앱이 등록한 장비가 아닙니다", d.Checkpoints-stats)
		}
		if reason != "" {
			d.Locked = fmt.Sprintf("%s. 지우려면 admin 의 장비 트리 맨 위(그룹 밖 장비)에서 장비 ID %d 를 찾아 지우세요", reason, d.DeviceID)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ManageOrphans lists the devices in no group, for the manage screen.
func (s *Service) ManageOrphans(ctx context.Context, planID string) ([]OrphanDevice, error) {
	s.mu.Lock()
	v, e := s.session(planID)
	s.mu.Unlock()
	if e != nil {
		return nil, e
	}
	db, e := openDB(ctx, v.Config)
	if e != nil {
		return nil, e
	}
	defer db.Close()
	return orphanDevices(ctx, db, v.Catalog.TypeID, v.Catalog.DriverID, 0, false)
}

// buildOrphanDelete is buildDelete for one device in no group.
func buildOrphanDelete(ctx context.Context, q queryer, typeID, driverID, deviceID int64, lock bool) (deletePlan, DeletePreview, error) {
	plan := deletePlan{orphan: true, typeID: typeID, driverID: driverID, groupName: "(그룹 없음)", devices: []ManagedDevice{}}
	prev := DeletePreview{GroupName: plan.groupName, Devices: []ManagedDevice{}, Blockers: []string{}, Warnings: []string{}, Points: []DeletePoint{}}
	found, e := orphanDevices(ctx, q, typeID, driverID, deviceID, lock)
	if e != nil {
		return plan, prev, e
	}
	if len(found) != 1 {
		return plan, prev, fmt.Errorf("장비(ID %d)가 그룹 밖 리소스 수집 장비가 아니거나(그 사이 그룹에 들어감) 이미 삭제됐습니다 — 다시 불러오세요", deviceID)
	}
	o := found[0]
	if o.Locked != "" {
		prev.Blockers = append(prev.Blockers, fmt.Sprintf("장비 %s: %s.", o.Name, o.Locked))
	}
	plan.devices = []ManagedDevice{{DeviceID: o.DeviceID, Name: o.Name, Checkpoints: o.Checkpoints, dc: o.dc}}
	ids, longest, e := liveCheckpoints(ctx, q, []int64{o.DeviceID}, lock)
	if e != nil {
		return plan, prev, e
	}
	plan.checkpoints, plan.longest = ids, longest
	if e = deleteBlockers(ctx, q, &plan, &prev, lock); e != nil {
		return plan, prev, e
	}
	plan.fingerprint = deleteFingerprint(plan.devices, ids)
	plan.confirm, prev.Confirm = o.Name, o.Name
	prev.Devices, prev.Checkpoints = plan.devices, len(ids)
	return plan, prev, nil
}

// ManageOrphanDeletePreview shows what deleting a device in no group removes.
func (s *Service) ManageOrphanDeletePreview(ctx context.Context, planID string, deviceID int64) (DeletePreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.session(planID)
	if e != nil {
		return DeletePreview{}, e
	}
	db, e := openDB(ctx, v.Config)
	if e != nil {
		return DeletePreview{}, e
	}
	defer db.Close()
	plan, prev, e := buildOrphanDelete(ctx, db, v.Catalog.TypeID, v.Catalog.DriverID, deviceID, false)
	if e != nil {
		return prev, e
	}
	if len(prev.Blockers) > 0 {
		return prev, nil
	}
	// A session of its own, so ManageDelete finds and runs it like a group's delete.
	prev.ID = uuid.NewString()
	m := &manageSession{Config: v.Config, DriverID: v.Catalog.DriverID, Group: ManagedGroup{ID: uuid.NewString(), Name: plan.groupName},
		points: map[int64]ManagedPoint{}, devices: map[int64]ManagedDevice{}, previews: map[string]managePlan{}, removals: map[string]deletePlan{prev.ID: plan}, Expires: time.Now().Add(manageTTL)}
	if s.manage.sessions == nil {
		s.manage.sessions = map[string]*manageSession{}
	}
	s.pruneManage()
	s.manage.sessions[m.Group.ID] = m
	return prev, nil
}

package statsreg

import (
	"context"
	"strings"
)

// TesterPrefix turns a build into a tester-only build, so several testers can
// register the same keys of one environment side by side. It is set only with
//
//	-ldflags "-X rtaskmgr/internal/statsreg.TesterPrefix=agy"
//
// and every function here returns its input untouched while it is empty: a
// product build behaves exactly as before.
//
// With a prefix (agy):
//   - group names start with "agy_" and device names with "agy " (prepended when missing),
//   - only groups named "agy_..." count as existing registrations: keys registered
//     anywhere else look unregistered, so duplicates, update targets, moved keys,
//     deleted keys and the in-transaction re-checks all stay within the tester's groups.
var TesterPrefix string

func testerGroupPrefix() string  { return TesterPrefix + "_" }
func testerDevicePrefix() string { return TesterPrefix + " " }

// withPrefix leaves names validateSelection rejects (empty, surrounding spaces)
// as they are, so they are still rejected instead of becoming just the prefix.
func withPrefix(name, prefix string) string {
	if TesterPrefix == "" || name == "" || name != strings.TrimSpace(name) || strings.HasPrefix(name, prefix) {
		return name
	}
	return prefix + name
}

// testerSelection forces the tester's names on a selection before it is
// validated or registered. Servers is copied; the caller's slice is not changed.
func testerSelection(sel Selection) Selection {
	if TesterPrefix == "" {
		return sel
	}
	sel.GroupName = withPrefix(sel.GroupName, testerGroupPrefix())
	servers := make([]Server, len(sel.Servers))
	for i, v := range sel.Servers {
		v.Name = withPrefix(v.Name, testerDevicePrefix())
		servers[i] = v
	}
	sel.Servers = servers
	return sel
}

// testerPlanDefaults shows the names that will really be registered.
func testerPlanDefaults(p *Plan) {
	if TesterPrefix == "" {
		return
	}
	p.GroupName = withPrefix(p.GroupName, testerGroupPrefix())
	for i := range p.Servers {
		p.Servers[i].Name = withPrefix(p.Servers[i].Name, testerDevicePrefix())
	}
}

// testerOwnsGroup: the group belongs to this tester (always true in a product build).
func testerOwnsGroup(group string) bool {
	return TesterPrefix == "" || strings.HasPrefix(group, testerGroupPrefix())
}

// testerScope drops registrations outside the tester's groups, including keys
// on devices without a group.
func testerScope(regs []registeredKey) []registeredKey {
	if TesterPrefix == "" {
		return regs
	}
	out := []registeredKey{}
	for _, r := range regs {
		if r.groupID != 0 && testerOwnsGroup(r.group) {
			out = append(out, r)
		}
	}
	return out
}

// testerDeletedKeys is deletedKeys within the tester's groups. A deleted device
// may have lost its group mapping, so a device named with the tester's prefix
// counts too.
func testerDeletedKeys(ctx context.Context, q queryer, driverID int64) (map[string]bool, error) {
	rows, e := q.QueryContext(ctx, `SELECT DISTINCT c.request_command, COALESCE(g.name,''), COALESCE(d.name,'') FROM checkpoint c
JOIN device_interface i ON i.id=c.device_interface_id
LEFT JOIN device d ON d.id=i.device_id
LEFT JOIN device_specify_rule_mapping_device m ON m.device_id=d.id
LEFT JOIN device_group g ON g.device_specify_rule_id=m.device_specify_rule_id
WHERE i.device_driver_id=? AND c.flag<>1 AND c.request_command IS NOT NULL`, driverID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k, group, device string
		if e = rows.Scan(&k, &group, &device); e != nil {
			return nil, e
		}
		if (group != "" && testerOwnsGroup(group)) || strings.HasPrefix(device, testerDevicePrefix()) {
			out[k] = true
		}
	}
	return out, rows.Err()
}

// testerRegisteredCount is registeredCount within the tester's groups: live
// checkpoints of the driver using one of keys on a device of an "agy_..." group.
func testerRegisteredCount(ctx context.Context, q queryer, driverID int64, keys []string) (int, error) {
	args := []any{driverID}
	for _, k := range keys {
		args = append(args, k)
	}
	rows, e := q.QueryContext(ctx, "SELECT c.id, COALESCE(g.name,'') FROM checkpoint c JOIN device_interface i ON i.id=c.device_interface_id LEFT JOIN device_specify_rule_mapping_device m ON m.device_id=i.device_id LEFT JOIN device_group g ON g.device_specify_rule_id=m.device_specify_rule_id WHERE i.device_driver_id=? AND c.flag=1 AND c.request_command IN ("+inList(len(keys))+")", args...)
	if e != nil {
		return 0, e
	}
	defer rows.Close()
	ids := map[int64]bool{}
	for rows.Next() {
		var id int64
		var group string
		if e = rows.Scan(&id, &group); e != nil {
			return 0, e
		}
		if group != "" && testerOwnsGroup(group) {
			ids[id] = true
		}
	}
	return len(ids), rows.Err()
}

package statsreg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// An environment is registered again and again as modules stop, move to new
// servers or new servers are added. Those changes show up as new Redis keys
// (a moved module usually gets a new module id / hostname), while the keys of
// stopped modules expire. An update therefore:
//   - finds what this driver already has in the DB (authoritative, whoever registered it),
//   - offers only keys that are not registered yet,
//   - adds them to the host's existing device in the chosen group, or to a new
//     device in that group for a new server,
//   - never touches or deletes existing checkpoints, including ones whose key vanished.

type ExistingDevice struct {
	DeviceID    int64  `json:"deviceId"`
	InterfaceID int64  `json:"interfaceId"`
	Name        string `json:"name"`
	Host        string `json:"host"` // where most of its keys are placed now; "" if unknown
	Points      int    `json:"points"`
}
type ExistingGroup struct {
	GroupID int64            `json:"groupId"`
	Name    string           `json:"name"`
	Devices []ExistingDevice `json:"devices"`
	Points  int              `json:"points"`
	// Collector cluster most of its live devices use; new devices of an update go there.
	ClusterID int64 `json:"clusterId"`
	// Registered keys of this group that are not in Redis now (stopped or moved modules).
	Vanished int `json:"vanished"`
}

type registeredKey struct {
	key            string
	groupID        int64
	device, iface  int64
	devName, group string
}

// loadExisting reads the live (flag=1) checkpoints of this driver with their
// device, interface and group.
func loadExisting(ctx context.Context, q queryer, driverID int64) ([]registeredKey, error) {
	rows, e := q.QueryContext(ctx, `SELECT c.request_command, COALESCE(g.id,0), COALESCE(g.name,''), d.id, d.name, i.id
FROM checkpoint c
JOIN device_interface i ON i.id=c.device_interface_id
JOIN device d ON d.id=i.device_id
LEFT JOIN device_specify_rule_mapping_device m ON m.device_id=d.id
LEFT JOIN device_group g ON g.device_specify_rule_id=m.device_specify_rule_id
WHERE i.device_driver_id=? AND c.flag=1 AND d.flag=1 AND i.flag=1 AND c.request_command IS NOT NULL`, driverID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []registeredKey{}
	for rows.Next() {
		var r registeredKey
		if e = rows.Scan(&r.key, &r.groupID, &r.group, &r.device, &r.devName, &r.iface); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// deletedKeys returns keys of this driver whose checkpoints were deleted and
// that have no live checkpoint any more.
func deletedKeys(ctx context.Context, q queryer, driverID int64) (map[string]bool, error) {
	rows, e := q.QueryContext(ctx, `SELECT DISTINCT c.request_command FROM checkpoint c
JOIN device_interface i ON i.id=c.device_interface_id
WHERE i.device_driver_id=? AND c.flag<>1 AND c.request_command IS NOT NULL`, driverID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if e = rows.Scan(&k); e != nil {
			return nil, e
		}
		out[k] = true
	}
	return out, rows.Err()
}

// markDeleted tags keys whose checkpoints were deleted before. They are offered
// like any new key (user decision 2026-09-21: "flag가 있더라도 새로 생성하자");
// the tag is information only.
func markDeleted(p *Plan, deleted map[string]bool) {
	for i := range p.Points {
		if pt := &p.Points[i]; !pt.Registered && deleted[pt.Key] {
			pt.Deleted = true
		}
	}
}

// hostAnchors maps keys that name a server directly (a {hostname} or {ip}
// placeholder) to that server. Unlike module-id keys, these never move.
func hostAnchors(keys map[string]string, servers []Server) map[string]string {
	hosts := map[string]string{}
	for _, s := range servers {
		if s.Hostname != "common" {
			hosts[strings.ToLower(s.Hostname)] = s.Hostname
			if s.IP != "" {
				hosts[strings.ToLower(s.IP)] = s.Hostname
			}
		}
	}
	out := map[string]string{}
	for k := range keys {
		for _, t := range keyTokens(k) {
			if h := hosts[strings.ToLower(t)]; h != "" {
				out[k] = h
				break
			}
		}
	}
	return out
}

// markExisting fills p.Existing and flags points that are already registered.
// placeOf gives the current host of a key (same placement as new points);
// anchorOf the server a key names directly (see hostAnchors).
//
// Which server an existing device belongs to, in order:
//  1. its name is a hostname of the environment,
//  2. the keys naming a server directly,
//  3. the current host of all its keys.
//
// Module-id keys come last because modules move: on 2026-09-21 collectors
// 16/22/23 moved server07 -> server08, the device "server07" was voted to be
// server08 by its collector keys, and server08's new keys went into it.
func markExisting(p *Plan, regs []registeredKey, placeOf, anchorOf map[string]string) {
	hostnames := map[string]string{}
	for _, s := range p.Servers {
		hostnames[strings.ToLower(s.Hostname)] = s.Hostname
	}
	present := map[string]*Point{}
	for i := range p.Points {
		present[p.Points[i].Key] = &p.Points[i]
	}
	groups := map[int64]*ExistingGroup{}
	devs := map[int64]*ExistingDevice{}
	votes := map[int64]map[string]int{}
	anchors := map[int64]map[string]int{}
	for _, r := range regs {
		if pt := present[r.key]; pt != nil && r.groupID == 0 {
			// Registered on a device outside any group: hidden all the same (no duplicates),
			// but such a device cannot be an update target.
			pt.Registered, pt.RegisteredOn = true, "(그룹 없음) / "+r.devName
		}
		if r.groupID == 0 {
			continue
		}
		g := groups[r.groupID]
		if g == nil {
			g = &ExistingGroup{GroupID: r.groupID, Name: r.group}
			groups[r.groupID] = g
		}
		d := devs[r.device]
		if d == nil {
			d = &ExistingDevice{DeviceID: r.device, InterfaceID: r.iface, Name: r.devName}
			devs[r.device] = d
			votes[r.device] = map[string]int{}
			anchors[r.device] = map[string]int{}
			g.Devices = append(g.Devices, ExistingDevice{DeviceID: r.device})
		}
		d.Points++
		g.Points++
		if h := placeOf[r.key]; h != "" {
			votes[r.device][h]++
		}
		if h := anchorOf[r.key]; h != "" {
			anchors[r.device][h]++
		}
		if pt := present[r.key]; pt != nil {
			pt.Registered = true
			pt.RegisteredOn = fmt.Sprintf("%s / %s", r.group, r.devName)
		} else {
			g.Vanished++
		}
	}
	majority := func(v map[string]int) string {
		best, n := "", 0
		for h, c := range v {
			if c > n || (c == n && h < best) {
				best, n = h, c
			}
		}
		return best
	}
	for id, d := range devs {
		switch {
		case hostnames[strings.ToLower(d.Name)] != "":
			d.Host = hostnames[strings.ToLower(d.Name)]
		case len(anchors[id]) > 0:
			d.Host = majority(anchors[id])
		default:
			d.Host = majority(votes[id])
		}
	}
	// A registered key now placed on another host than its device: report only.
	for _, r := range regs {
		if pt := present[r.key]; pt != nil && r.groupID != 0 && pt.Host != "" && devs[r.device].Host != "" && pt.Host != devs[r.device].Host {
			pt.Moved = true
		}
	}
	p.Existing = []ExistingGroup{}
	for _, g := range groups {
		for i, d := range g.Devices {
			g.Devices[i] = *devs[d.DeviceID]
		}
		sort.Slice(g.Devices, func(i, j int) bool { return g.Devices[i].Name < g.Devices[j].Name })
		p.Existing = append(p.Existing, *g)
	}
	sort.Slice(p.Existing, func(i, j int) bool { return p.Existing[i].GroupID < p.Existing[j].GroupID })
}

// updateTarget resolves the selection's update group against the analysis.
func updateTarget(plan Plan, sel Selection) (*ExistingGroup, error) {
	if sel.UpdateGroupID == 0 {
		return nil, nil
	}
	for i := range plan.Existing {
		if plan.Existing[i].GroupID == sel.UpdateGroupID {
			return &plan.Existing[i], nil
		}
	}
	return nil, fmt.Errorf("업데이트할 그룹을 분석 결과에서 찾을 수 없습니다(ID %d)", sel.UpdateGroupID)
}

// groupCluster is the collector cluster most live devices of the group use (0 if none).
// An update puts new devices there: on 2026-09-22 new devices went to the first
// cluster in the list (an idle default cluster) and nothing was collected.
func groupCluster(ctx context.Context, q queryer, groupID int64) (int64, error) {
	var id int64
	e := q.QueryRowContext(ctx, `SELECT d.cluster_id FROM device_group g
JOIN device_specify_rule_mapping_device m ON m.device_specify_rule_id=g.device_specify_rule_id
JOIN device d ON d.id=m.device_id
WHERE g.id=? AND d.flag=1 AND d.cluster_id IS NOT NULL
GROUP BY d.cluster_id ORDER BY COUNT(*) DESC, d.cluster_id LIMIT 1`, groupID).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, nil
	}
	return id, e
}

// deviceFor returns the group's device for host (the one with most checkpoints).
func (g *ExistingGroup) deviceFor(host string) *ExistingDevice {
	var best *ExistingDevice
	for i := range g.Devices {
		if d := &g.Devices[i]; d.Host == host && (best == nil || d.Points > best.Points) {
			best = d
		}
	}
	return best
}

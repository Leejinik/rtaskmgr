package statsreg

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Renaming a device can move it in or out of a device specify rule that selects
// devices by name — admin's 「Apply Dynamically」 event policies and dynamic
// groups (condition Device Name, operator Equals or Contains). 4th round: a
// policy 「Device Name Contains codex server_2」 lost its device to a rename and
// the preview said nothing. Read-only, like nameWarnings for checkpoint names.

// Codes of admin's SpecifyConditionTypeEnum / SpecifyOperatorTypeEnum.
const (
	conditionDeviceName = 102
	operatorEquals      = 1
	operatorContains    = 2
)

type deviceNameCond struct {
	rule     int64
	ruleName string
	operator int
	value    string
}

// nameMatches is how the DB matches (= / like, case-insensitive collation).
func nameMatches(operator int, value, name string) bool {
	v, n := strings.ToLower(strings.TrimSpace(value)), strings.ToLower(name)
	if operator == operatorEquals {
		return n == v
	}
	return strings.Contains(n, v)
}

// deviceRuleHits: for each rule, the renamed devices whose match changes
// ("in" — the new name matches, "out" — only the old one did).
func deviceRuleHits(conds []deviceNameCond, devices []DeviceChange) map[int64][]string {
	out := map[int64][]string{}
	for _, c := range conds {
		for _, d := range devices {
			was, now := nameMatches(c.operator, c.value, d.OldName), nameMatches(c.operator, c.value, d.NewName)
			switch {
			case was && !now:
				out[c.rule] = append(out[c.rule], fmt.Sprintf("%s → %s: 대상에서 빠짐", d.OldName, d.NewName))
			case !was && now:
				out[c.rule] = append(out[c.rule], fmt.Sprintf("%s → %s: 대상에 새로 들어감", d.OldName, d.NewName))
			}
		}
	}
	return out
}

func deviceNameWarnings(ctx context.Context, q queryer, devices []DeviceChange) []string {
	if len(devices) == 0 {
		return nil
	}
	rows, e := q.QueryContext(ctx, `SELECT r.id, COALESCE(r.name,''), COALESCE(c.operator_type,2), v.value FROM device_specify_rule_condition_value v
JOIN device_specify_rule_condition c ON c.id=v.device_specify_rule_condition_id
JOIN device_specify_rule r ON r.id=c.device_specify_rule_id
WHERE c.condition_type=? AND v.value IS NOT NULL AND v.value<>''`, conditionDeviceName)
	if e != nil {
		return []string{"장비 이름 조건(이벤트 정책·동적 그룹)을 확인하지 못했습니다: " + e.Error()}
	}
	conds := []deviceNameCond{}
	for rows.Next() {
		var c deviceNameCond
		if rows.Scan(&c.rule, &c.ruleName, &c.operator, &c.value) == nil {
			conds = append(conds, c)
		}
	}
	rows.Close()
	hits := deviceRuleHits(conds, devices)
	if len(hits) == 0 {
		return nil
	}
	ids := []int64{}
	for id := range hits {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	users := map[int64][]string{}
	if rs, e := q.QueryContext(ctx, "SELECT COALESCE(name,''), device_specify_rule_id FROM alarm_policy WHERE flag=1 AND device_specify_rule_id IN ("+inList(len(ids))+")", idArgs(ids)...); e == nil {
		for rs.Next() {
			var n string
			var id int64
			if rs.Scan(&n, &id) == nil {
				users[id] = append(users[id], "이벤트 정책 「"+n+"」")
			}
		}
		rs.Close()
	}
	if rs, e := q.QueryContext(ctx, "SELECT COALESCE(name,''), device_specify_rule_id FROM device_group WHERE device_specify_rule_id IN ("+inList(len(ids))+")", idArgs(ids)...); e == nil {
		for rs.Next() {
			var n string
			var id int64
			if rs.Scan(&n, &id) == nil {
				users[id] = append(users[id], "장비 그룹 「"+n+"」")
			}
		}
		rs.Close()
	}
	out := []string{}
	for _, id := range ids {
		if len(users[id]) == 0 {
			continue // a rule nothing uses
		}
		out = append(out, fmt.Sprintf("장비 이름 조건으로 장비를 고르는 %s 의 대상이 바뀝니다 — %s. 의도한 것이 아니면 admin 에서 조건을 고치거나 이름을 다시 정하세요", strings.Join(users[id], "·"), strings.Join(hits[id], ", ")))
	}
	return out
}

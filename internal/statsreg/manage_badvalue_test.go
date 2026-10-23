package statsreg

import (
	"strings"
	"testing"
)

// 3rd round (s5-04): liz.stats.api.{3}.snmp.{3}.epm with value "" was ticked,
// its data format locked to Double, and the check passed.
func TestSelectionRefusesANonNumberValueAsANumber(t *testing.T) {
	bad := Point{Key: "liz.stats.api.{3}.snmp.{3}.epm", Name: "EPM", Host: "server_1", TemplateID: 7, Format: 3, Interval: 10000, BadValue: true}
	plan := Plan{Points: []Point{bad}}
	sel := Selection{GroupName: "System", Servers: []Server{{Hostname: "server_1", Name: "server_1"}}, Points: []Point{bad}}
	plan.Servers = sel.Servers
	sel.Points[0].Selected = true
	if e := validateSelection(plan, sel); e == nil || !strings.Contains(e.Error(), "다음 업데이트에서 추가") {
		t.Fatalf("accepted: %v", e)
	}
	// A key without a product template may be registered as text instead.
	bad.TemplateID, bad.Format = 0, 4
	plan.Points[0] = bad
	sel.Points[0] = bad
	sel.Points[0].Selected = true
	if e := validateSelection(plan, sel); e != nil {
		t.Fatalf("as text: %v", e)
	}
}

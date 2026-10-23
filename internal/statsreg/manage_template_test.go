package statsreg

import (
	"strings"
	"testing"
)

// 2nd round: on the lizstats row 「지금 이름에서 시작」 gave lizstats.{11}.cpu,
// and the module id 11 of server_1 went to server_2 and server_3 as well.
func TestRowTemplatePutsTheServerPartInAPlaceholder(t *testing.T) {
	hosts := map[string]string{"server_1": "server_1", "server_2": "server_2"}
	mods := map[string]string{"11": "server_1", "12": "server_2"}
	row := []ManagedPoint{
		{DeviceID: 1, Key: "liz.stats.lizstats.{11}.cpu", Name: "lizstats.{11}.cpu", Host: "server_1", ServerPart: "11"},
		{DeviceID: 2, Key: "liz.stats.lizstats.{12}.cpu", Name: "lizstats.{12}.cpu", Host: "server_2", ServerPart: "12"},
	}
	tmpl, warn := rowTemplate(row, hosts, mods)
	if tmpl != "lizstats.{{키1}}.cpu" || warn != "" {
		t.Fatalf("%q %q", tmpl, warn)
	}
	for _, p := range row {
		if got, ok := resolveName(tmpl, p.Key, p.Host); !ok || got != p.Name {
			t.Fatalf("%s → %q %v", p.Key, got, ok)
		}
	}
	// Hostnames go to {서버}, the number at their end to {서버번호}.
	row = []ManagedPoint{
		{DeviceID: 1, Key: "liz.stats.server.{server_1}.cpu", Name: "server_1 CPU", Host: "server_1", ServerPart: "server_1"},
		{DeviceID: 2, Key: "liz.stats.server.{server_2}.cpu", Name: "server_2 CPU", Host: "server_2", ServerPart: "server_2"},
	}
	if tmpl, warn = rowTemplate(row, hosts, mods); tmpl != "{서버} CPU" || warn != "" {
		t.Fatalf("%q %q", tmpl, warn)
	}
	// A name the placeholders cannot reproduce (another server's value as text) is said.
	row[1].Name, row[0].Name = "server_1 CPU", "server_1 CPU"
	row = append(row, ManagedPoint{DeviceID: 3, Key: "liz.stats.server.{server_3}.cpu", Name: "server_1 CPU", Host: "server_3", ServerPart: "server_3"})
	if _, warn = rowTemplate(row, map[string]string{"server_1": "server_1", "server_2": "server_2", "server_3": "server_3"}, mods); !strings.Contains(warn, "server_1") {
		t.Fatalf("no warning: %q", warn)
	}
}

func TestScreenPlaceholders(t *testing.T) {
	if got, ok := resolveName("{서버대문자} {서버} #{서버번호} {키2}", "liz.stats.server.{server_3}.{eth0}.x", "server_3"); !ok || got != "Server_3 server_3 #3 eth0" {
		t.Fatalf("%q %v", got, ok)
	}
	if got := patternToScreen("{Host} {host} {n} {t1}"); got != "{서버대문자} {서버} {서버번호} {키1}" {
		t.Fatal(got)
	}
	if !bounded("server_1 CPU", "server_1") || bounded("server_10 CPU", "server_1") || bounded("cpu1", "1") {
		t.Fatal("bounded")
	}
}

func TestResolveEditMarksAnotherServersValue(t *testing.T) {
	setTester(t, "")
	s := loadTestSession(t)
	plan, errs, warns := resolveEdit(s, ManageEdit{Rows: []RowEdit{{Shape: "liz.stats.server.{서버별}.free.memory", Name: "server_1 메모리"}}})
	if len(errs) != 0 || len(plan.points) != 2 {
		t.Fatalf("%v %+v", errs, plan.points)
	}
	marked := 0
	for _, c := range plan.points {
		if c.Suspicious {
			marked++
			if c.DeviceID != 200 {
				t.Fatalf("own value marked: %+v", c)
			}
		}
	}
	if marked != 1 || len(warns) != 1 || !strings.Contains(warns[0], "server_2(server_1)") {
		t.Fatalf("%d %v", marked, warns)
	}
}

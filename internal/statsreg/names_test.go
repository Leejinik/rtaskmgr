package statsreg

import (
	"strings"
	"testing"
)

// trunk: 「Zookeeper CPU (%) - {서버}」 was registered as typed on three checkpoints.
func TestRegistrationFillsNamePlaceholdersPerRow(t *testing.T) {
	sel := Selection{Points: []Point{
		{Key: "liz.stats.zookeeper.{server_1}.cpu", Host: "server_1", Name: "Zookeeper CPU (%) - {서버}", Selected: true},
		{Key: "liz.stats.zookeeper.{server_3}.cpu", Host: "server_3", Name: "ZK{서버번호} {서버대문자}", Selected: true},
		{Key: "liz.stats.server.{server_2}.{eth0}.network.traffic", Host: "server_2", Name: "{host} {t2} 트래픽", Selected: true},
		{Key: "liz.stats.vip.location", Host: "common", Name: "VIP", Selected: true},
		{Key: "liz.stats.x.{server_1}.y", Host: "server_1", Name: "{서버} 안 고름", Selected: false},
	}}
	got, names, e := resolveSelectionNames(sel)
	if e != nil {
		t.Fatal(e)
	}
	want := []string{"Zookeeper CPU (%) - server_1", "ZK3 Server_3", "server_2 eth0 트래픽", "VIP", "{서버} 안 고름"}
	for i, w := range want {
		if got.Points[i].Name != w {
			t.Errorf("%s: %q, want %q", got.Points[i].Key, got.Points[i].Name, w)
		}
	}
	if len(names) != 3 || names[0].From != "Zookeeper CPU (%) - {서버}" || names[0].To != "Zookeeper CPU (%) - server_1" {
		t.Fatalf("%+v", names)
	}
	if sel.Points[0].Name != "Zookeeper CPU (%) - {서버}" {
		t.Fatal("caller's points changed")
	}
}

func TestRegistrationRefusesAPlaceholderItsRowCannotFill(t *testing.T) {
	for _, p := range []Point{
		{Key: "liz.stats.vip.location", Host: "common", Name: "VIP {서버}", Selected: true},
		{Key: "liz.stats.a.{lizlogdb}.cpu", Host: "lizlogdb", Name: "DB {서버번호}", Selected: true},
		{Key: "liz.stats.a.{server_1}.cpu", Host: "server_1", Name: "{키2} CPU", Selected: true},
	} {
		if _, _, e := resolveSelectionNames(Selection{Points: []Point{p}}); e == nil || !strings.Contains(e.Error(), "채울 수 없습니다") {
			t.Errorf("%s %q accepted: %v", p.Host, p.Name, e)
		}
	}
}

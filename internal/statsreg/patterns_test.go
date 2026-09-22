package statsreg

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// The naming the user chose on 2026-09-21 must round-trip to other hosts.
func TestPatternGeneralizeAndRender(t *testing.T) {
	cases := []struct{ name, key, host, tmpl string }{
		{"Cache DB_1 CPU load", "liz.stats.cachedb.{server01}.cpu", "server01", "Cache DB_{n} CPU load"},
		{"Server01 Free Disk", "liz.stats.server.{server01}.free.disk", "server01", "{Host} Free Disk"},
		{"Server01 eth0 NIC Network Traffic", "liz.stats.server.{server01}.{eth0}.network.traffic", "server01", "{Host} {t2} NIC Network Traffic"},
		{"Data-pipeline_1 status", "liz.stats.pipeline.{lizserver01}.status", "server01", "Data-pipeline_{n} status"},
		{"API process CPU load", "liz.stats.api.{2}.cpu", "server01", "API process CPU load"},
		{"VIP location", "liz.stats.vip.location", "common", "VIP location"},
	}
	for _, c := range cases {
		if got := generalize(c.name, c.key, c.host); got != c.tmpl {
			t.Errorf("generalize(%q) = %q, want %q", c.name, got, c.tmpl)
		}
		if got, ok := render(c.tmpl, c.key, c.host); !ok || got != c.name {
			t.Errorf("render(%q) = %q %v, want %q", c.tmpl, got, ok, c.name)
		}
	}
	if got, ok := render("Cache DB_{n} CPU load", "liz.stats.cachedb.{server_3}.cpu", "server_3"); !ok || got != "Cache DB_3 CPU load" {
		t.Errorf("other naming scheme: %q %v", got, ok)
	}
	if got, ok := render("{Host} {t2} NIC", "liz.stats.server.{server05}.{ens192}.network.traffic", "server05"); !ok || got != "Server05 ens192 NIC" {
		t.Errorf("second token: %q %v", got, ok)
	}
	if _, ok := render("Cache DB_{n}", "liz.stats.cachedb.{ELON}.cpu", "ELON"); ok {
		t.Error("{n} without a host number must not render")
	}
}

func TestPatternLearnApplyAndHistory(t *testing.T) {
	s := Service{PatternFile: filepath.Join(t.TempDir(), "p.json")}
	pts := []Point{
		{Key: "liz.stats.server.{server01}.total.disk", Host: "server01", Name: "Server01 Total Disk", Format: 3, Interval: 10000, Selected: true},
		{Key: "liz.stats.server.{server02}.total.disk", Host: "server02", Name: "Server02 System Total Disk", Format: 3, Interval: 10000, Selected: true},
		{Key: "liz.stats.server.{server03}.total.disk", Host: "server03", Name: "Server03 Total Disk", Format: 3, Interval: 10000, Selected: true},
		{Key: "liz.stats.collector.{8}.cpu", Host: "server01", Name: "Collector CPU", TemplateID: 7, Format: 3, Interval: 10000, Selected: false},
		{Key: "liz.stats.logdb.{liz_log_db_1}.cpu", Host: "", Name: "x", Selected: false},
	}
	ev, e := s.learnPatterns(pts, "test 1")
	if e != nil {
		t.Fatal(e)
	}
	if len(ev.Changes) != 2 || len(ev.Notes) != 1 || !strings.Contains(ev.Notes[0], "server02") {
		t.Fatalf("first learning: %+v", ev)
	}
	p := Plan{Points: []Point{
		{Key: "liz.stats.server.{server_7}.total.disk", Host: "server_7", Name: "server.total.disk", Format: 4, Interval: 10000, Selected: true},
		{Key: "liz.stats.collector.{3}.cpu", Host: "server_7", Name: "Collector CPU", TemplateID: 7, Format: 2, Interval: 10000, Selected: true},
		{Key: "liz.stats.unknown.{a}.x", Host: "server_7", Name: "keep", Selected: true},
	}}
	s.applyPatterns(&p)
	if p.Points[0].Name != "Server_7 Total Disk" || p.Points[0].Format != 3 || !p.Points[0].Remembered {
		t.Fatalf("applied: %+v", p.Points[0])
	}
	if p.Points[1].Selected || p.Points[1].Format != 2 {
		t.Fatalf("unselected shape must stay unselected, template format untouched: %+v", p.Points[1])
	}
	if p.Points[2].Name != "keep" || p.Points[2].Remembered {
		t.Fatal("unknown shape changed")
	}
	pts[0].Name, pts[1].Name, pts[2].Name = "Server01 Disk Total", "Server02 Disk Total", "Server03 Disk Total"
	ev, _ = s.learnPatterns(pts, "test 2")
	if len(ev.Changes) != 1 || ev.Changes[0].Field != "name" || ev.Changes[0].From != "{Host} Total Disk" || ev.Changes[0].To != "{Host} Disk Total" {
		t.Fatalf("rename history: %+v", ev)
	}
	if ev, _ = s.learnPatterns(pts, "test 3"); len(ev.Changes) != 0 {
		t.Fatal("unchanged learning must not add history")
	}
	f, _ := s.loadPatterns()
	if len(f.History) != 2 {
		t.Fatalf("history entries: %d", len(f.History))
	}
}

// A host with nothing selected creates no device, so an emptied name must not block.
func TestValidationIgnoresUnusedHostNames(t *testing.T) {
	plan := Plan{Servers: []Server{{Hostname: "server01"}, {Hostname: "ghost"}}, Points: []Point{{Key: "liz.stats.server.{server01}.cpu", Host: "server01", Name: "cpu", Interval: 10000, Format: 3}}}
	sel := Selection{GroupName: "G", Servers: []Server{{Hostname: "server01", Name: "server01"}, {Hostname: "ghost", Name: ""}}, Points: []Point{{Key: "liz.stats.server.{server01}.cpu", Host: "server01", Name: "cpu", Interval: 10000, Format: 3, Selected: true}}}
	if e := validateSelection(plan, sel); e != nil {
		t.Fatal(e)
	}
	sel.Points[0].Name = " cpu"
	if e := validateSelection(plan, sel); e == nil || !strings.Contains(e.Error(), "liz.stats.server.{server01}.cpu") {
		t.Fatalf("error must name the key: %v", e)
	}
}

func TestPatternRemembersManualPlacement(t *testing.T) {
	s := Service{PatternFile: filepath.Join(t.TempDir(), "p.json")}
	ev, e := s.learnPatterns([]Point{
		{Key: "liz.stats.logdb.{lizlogdb01}.status", Host: "server01", Name: "Log DB status", TemplateID: 5, Format: 2, Interval: 10000, Selected: true},
		{Key: "liz.stats.api.{2}.cpu", Host: "server01", Name: "API", TemplateID: 6, Interval: 10000, Selected: true},
	}, "t")
	if e != nil || len(ev.Changes) != 3 {
		t.Fatalf("%v %+v", e, ev)
	}
	p := Plan{Servers: []Server{{Hostname: "server01"}}, Points: []Point{
		{Key: "liz.stats.logdb.{lizlogdb01}.status", Name: "x", TemplateID: 5, Format: 2, Interval: 10000, Warning: "· 서버를 찾을 수 없음: 배치 선택 또는 제외"},
		{Key: "liz.stats.logdb.{other}.status", Name: "y"},
	}}
	s.applyPatterns(&p)
	if p.Points[0].Host != "server01" || p.Points[0].Name != "Log DB status" || !p.Points[0].Selected {
		t.Fatalf("placement not applied: %+v", p.Points[0])
	}
	if p.Points[1].Host != "" {
		t.Fatal("unrelated token placed")
	}
	p = Plan{Servers: []Server{{Hostname: "server_1"}}, Points: []Point{{Key: "liz.stats.logdb.{lizlogdb01}.status", Name: "x"}}}
	s.applyPatterns(&p)
	if p.Points[0].Host != "" {
		t.Fatal("placed on a host that does not exist here")
	}
}

// Keys that are already live checkpoints are hidden and can never be sent again;
// an update reuses the host's device and only names new servers.
func TestExistingKeysAndUpdateTarget(t *testing.T) {
	p := Plan{Servers: []Server{{Hostname: "server01"}, {Hostname: "server08"}}, Points: []Point{
		{Key: "liz.stats.server.{server01}.cpu", Host: "server01", Name: "a", Interval: 10000, Format: 3, Selected: true},
		{Key: "liz.stats.server.{server08}.cpu", Host: "server08", Name: "b", Interval: 10000, Format: 3, Selected: true},
		{Key: "liz.stats.collector.{16}.cpu", Host: "server08", Name: "c", Interval: 10000, Format: 3, Selected: true},
	}}
	regs := []registeredKey{
		{key: "liz.stats.server.{server01}.cpu", groupID: 40, group: "System", device: 1996, devName: "server01", iface: 1996},
		{key: "liz.stats.collector.{16}.cpu", groupID: 40, group: "System", device: 2001, devName: "server07", iface: 2001},
		{key: "liz.stats.server.{server07}.cpu", groupID: 40, group: "System", device: 2001, devName: "server07", iface: 2001},
	}
	markExisting(&p, regs, map[string]string{"liz.stats.server.{server01}.cpu": "server01", "liz.stats.collector.{16}.cpu": "server07", "liz.stats.server.{server07}.cpu": "server07"}, nil)
	if !p.Points[0].Registered || p.Points[1].Registered || !p.Points[2].Registered || !p.Points[2].Moved {
		t.Fatalf("registered/moved flags: %+v", p.Points)
	}
	if len(p.Existing) != 1 || p.Existing[0].Vanished != 1 || p.Existing[0].Points != 3 {
		t.Fatalf("existing: %+v", p.Existing)
	}
	sel := Selection{GroupName: "ignored", UpdateGroupID: 40, Servers: []Server{{Hostname: "server01", Name: "server01"}, {Hostname: "server08", Name: "server08"}}, Points: []Point{p.Points[0], p.Points[1]}}
	sel.Points[0].Selected = false
	if e := validateSelection(p, sel); e != nil {
		t.Fatal(e)
	}
	sel.Points[0].Selected = true
	if e := validateSelection(p, sel); e == nil || !strings.Contains(e.Error(), "중복 등록 불가") {
		t.Fatalf("registered key accepted: %v", e)
	}
	sel.UpdateGroupID = 0
	sel.GroupName = "New"
	if e := validateSelection(p, sel); e == nil {
		t.Fatal("registered key accepted in a new registration")
	}
	g, _ := updateTarget(p, Selection{UpdateGroupID: 40})
	if d := g.deviceFor("server01"); d == nil || d.DeviceID != 1996 {
		t.Fatalf("existing device for server01: %+v", d)
	}
	if g.deviceFor("server08") != nil {
		t.Fatal("server08 must get a new device")
	}
}

func TestDeletedKeysOfferedAsNew(t *testing.T) {
	p := Plan{Points: []Point{{Key: "a", Selected: true}, {Key: "b", Selected: true, Registered: true}, {Key: "c", Selected: true}}}
	markDeleted(&p, map[string]bool{"a": true, "b": true})
	if !p.Points[0].Deleted || !p.Points[0].Selected || p.Points[1].Deleted || !p.Points[2].Selected {
		t.Fatalf("%+v", p.Points)
	}
}

// 2026-09-21: collectors 16/22/23 moved server07 -> server08. The device named
// server07 must stay server07, so server08's new keys get a new device.
func TestExistingDeviceNotStolenByMovedModules(t *testing.T) {
	p := Plan{Servers: []Server{{Hostname: "server07"}, {Hostname: "server08"}, {Hostname: "common"}}, Points: []Point{
		{Key: "liz.stats.server.{server08}.cpu", Host: "server08", Name: "x", Interval: 10000, Format: 3, Selected: true},
	}}
	regs := []registeredKey{}
	placeOf := map[string]string{}
	keys := map[string]string{}
	add := func(dev int64, name, key, now string) {
		regs = append(regs, registeredKey{key: key, groupID: 40, group: "System", device: dev, devName: name, iface: dev})
		placeOf[key], keys[key] = now, ""
	}
	for _, m := range []string{"16", "22", "23"} {
		for i := 0; i < 16; i++ {
			add(2001, "server07", "liz.stats.collector.{"+m+"}.k"+strconvI(i), "server08")
		}
	}
	for i := 0; i < 10; i++ {
		add(2001, "server07", "liz.stats.server.{server07}.k"+strconvI(i), "server07")
	}
	for i := 0; i < 5; i++ {
		add(3000, "custom name", "liz.stats.server.{server07}.m"+strconvI(i), "server07") // no hostname in the name: anchored keys decide
	}
	markExisting(&p, regs, placeOf, hostAnchors(keys, p.Servers))
	g := p.Existing[0]
	for _, d := range g.Devices {
		if d.Host != "server07" {
			t.Fatalf("device %d %q judged as %q", d.DeviceID, d.Name, d.Host)
		}
	}
	if g.deviceFor("server08") != nil {
		t.Fatal("server08 must get a new device")
	}
}

func strconvI(i int) string { return fmt.Sprint(i) }

func TestSavedConnectionsMostRecentFirst(t *testing.T) {
	s := Service{ConnectionFile: filepath.Join(t.TempDir(), "c.json")}
	for _, h := range []string{"a", "b", "a"} {
		if e := s.SaveConnection(SavedConnection{Config: Config{DBHost: h, DBPort: 3306, DBPassword: "pw-" + h}}); e != nil {
			t.Fatal(e)
		}
	}
	list, e := s.Connections()
	if e != nil || len(list) != 2 || list[0].Config.DBHost != "a" || list[1].Config.DBHost != "b" || list[0].Config.DBPassword != "pw-a" {
		t.Fatalf("%v %+v", e, list)
	}
}

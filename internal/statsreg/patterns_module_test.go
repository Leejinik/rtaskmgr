package statsreg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 4th round: keys whose server part is a module id. server_1's 「lizstats.{11}.cpu」
// was remembered as text and filled into the {12} and {13} keys.
func TestModuleIDKeysAreRememberedAsPlaceholders(t *testing.T) {
	s := &Service{PatternFile: filepath.Join(t.TempDir(), "p.json")}
	learn := []Point{}
	for i, h := range []string{"server_1", "server_2", "server_3"} {
		id := []string{"11", "12", "13"}[i]
		learn = append(learn, Point{Key: "liz.stats.lizstats.{" + id + "}.cpu", Host: h, Name: "LizStats {" + id + "}.cpu", Format: 3, Interval: 10000, Selected: true})
	}
	if _, e := s.learnPatterns(learn, "test"); e != nil {
		t.Fatal(e)
	}
	servers := []Server{{Hostname: "server_1"}, {Hostname: "server_2"}, {Hostname: "server_3"}, {Hostname: "common"}}
	p := Plan{Servers: servers, Points: []Point{
		{Key: "liz.stats.lizstats.{11}.cpu", Host: "server_1", Name: "lizstats.{11}.cpu"},
		{Key: "liz.stats.lizstats.{13}.cpu", Host: "server_3", Name: "lizstats.{13}.cpu"},
	}}
	s.applyPatterns(&p)
	if p.Points[0].Name != "LizStats {11}.cpu" || p.Points[1].Name != "LizStats {13}.cpu" {
		t.Fatalf("%q %q", p.Points[0].Name, p.Points[1].Name)
	}
}

func TestAWronglyRememberedNameIsNotFilledIntoAnotherKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.json")
	f := patternFile{Version: 1, Patterns: map[string]NamePattern{
		"liz.stats.lizstats.{*}.cpu": {Name: "lizstats.{11}.cpu", Samples: 3},
		"liz.stats.server.{*}.cpu":   {Name: "server_1 CPU", Samples: 3},
	}}
	data, _ := json.Marshal(f)
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	s := &Service{PatternFile: path}
	servers := []Server{{Hostname: "server_1"}, {Hostname: "server_3"}, {Hostname: "common"}}
	p := Plan{Servers: servers, Points: []Point{
		{Key: "liz.stats.lizstats.{11}.cpu", Host: "server_1", Name: "a"},
		{Key: "liz.stats.lizstats.{13}.cpu", Host: "server_3", Name: "b"},
		{Key: "liz.stats.server.{server_3}.cpu", Host: "server_3", Name: "c"},
	}}
	s.applyPatterns(&p)
	if p.Points[0].Name != "lizstats.{11}.cpu" || p.Points[1].Name != "b" || p.Points[2].Name != "c" {
		t.Fatalf("%q %q %q", p.Points[0].Name, p.Points[1].Name, p.Points[2].Name)
	}
}

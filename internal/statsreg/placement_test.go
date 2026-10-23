package statsreg

import (
	"strings"
	"testing"
)

func TestPlacementHintsFromEtcHosts(t *testing.T) {
	points := []Point{
		{Key: "liz.stats.logdb.{liz_log_db_1}.cpu"},
		{Key: "liz.stats.logdb.{liz_log_db_1}.mem"},
		{Key: "liz.stats.logdb.{liz_log_db_2}.cpu"},
		{Key: "liz.stats.logdb.{liz_log_db_3}.cpu"},
		{Key: "liz.stats.logdb.{other_db}.cpu"},
		{Key: "liz.stats.server.{server_1}.cpu", Host: "server_1"},
	}
	servers := []Server{{Hostname: "server_1", IP: "192.0.2.11"}, {Hostname: "server_2"}, {Hostname: "common"}}
	res := DiscoverResult{
		Servers: []DiscoverServer{{Name: "a", Addr: "192.0.2.12:22", Hostname: "server_2"}},
		Hosts: []DiscoverHostsEntry{
			{Server: "server_1", Hostname: "server_1", Name: "liz_log_db_1", IP: "192.0.2.11"}, // by liz_server IP
			{Server: "server_2", Hostname: "server_2", Name: "liz_log_db_2", IP: "127.0.0.1"},  // the server names itself
			{Server: "server_1", Hostname: "server_1", Name: "liz_log_db_3", IP: "192.0.2.12"}, // by the SSH address
			{Server: "server_1", Hostname: "server_1", Name: "other_db", IP: "198.51.100.9"},   // outside the environment
		},
	}
	got := map[string]PlacementHint{}
	for _, h := range placementHints(points, servers, res) {
		got[h.Token] = h
	}
	if len(got) != 4 {
		t.Fatalf("%+v", got)
	}
	if h := got["liz_log_db_1"]; h.Host != "server_1" || !strings.Contains(h.Source, "server_1:/etc/hosts 192.0.2.11") {
		t.Fatalf("%+v", h)
	}
	if h := got["liz_log_db_2"]; h.Host != "server_2" {
		t.Fatalf("%+v", h)
	}
	if h := got["liz_log_db_3"]; h.Host != "server_2" {
		t.Fatalf("%+v", h)
	}
	if h := got["other_db"]; h.Host != "" || !strings.Contains(h.Note, "찾지 못했습니다") {
		t.Fatalf("%+v", h)
	}
	// Two servers' files disagree: no recommendation.
	res.Hosts = append(res.Hosts, DiscoverHostsEntry{Server: "server_2", Hostname: "server_2", Name: "liz_log_db_1", IP: "127.0.0.1"})
	for _, h := range placementHints(points, servers, res) {
		if h.Token == "liz_log_db_1" && (h.Host != "" || !strings.Contains(h.Note, "직접 고르세요")) {
			t.Fatalf("%+v", h)
		}
	}
}

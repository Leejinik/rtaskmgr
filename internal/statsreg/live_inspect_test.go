package statsreg

import (
	"context"
	"os"
	"testing"
	"time"
)

// Read-only: inspects an environment and prints what an update would offer.
// RTM_STATS_REG_HOST RTM_STATS_REG_REDIS RTM_STATS_REG_PASSWORD RTM_STATS_REG_BROKERS
func TestLiveInspectExisting(t *testing.T) {
	host := os.Getenv("RTM_STATS_REG_HOST")
	if host == "" || os.Getenv("RTM_STATS_INSPECT") != "yes" {
		t.Skip("set RTM_STATS_INSPECT=yes and RTM_STATS_REG_* for a read-only inspection")
	}
	pw := os.Getenv("RTM_STATS_REG_PASSWORD")
	redisHost := os.Getenv("RTM_STATS_REG_REDIS")
	if redisHost == "" {
		redisHost = host
	}
	c := Config{DBHost: host, DBPort: 3306, DBUser: "root", DBPassword: pw, RedisHost: redisHost, RedisPort: 5000, RedisPassword: pw, Brokers: os.Getenv("RTM_STATS_REG_BROKERS"), KafkaSecurity: "PLAINTEXT", KafkaHosts: os.Getenv("RTM_STATS_REG_KAFKA_HOSTS")}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var s Service
	p, e := s.Inspect(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	reg, moved, fresh, placed := 0, 0, 0, 0
	for _, pt := range p.Points {
		switch {
		case pt.Registered:
			reg++
			if pt.Moved {
				moved++
			}
		default:
			fresh++
			if pt.Host != "" {
				placed++
			}
		}
	}
	for _, g := range p.Existing {
		t.Logf("group %d %q: points %d, vanished %d", g.GroupID, g.Name, g.Points, g.Vanished)
		for _, d := range g.Devices {
			t.Logf("   device %d %q host=%q points=%d", d.DeviceID, d.Name, d.Host, d.Points)
		}
	}
	t.Logf("keys in Redis %d: registered (hidden) %d, of which moved %d; new %d (placed %d)", len(p.Points), reg, moved, fresh, placed)
	type tally struct{ selected, deleted, off int }
	byHost := map[string]*tally{}
	for _, pt := range p.Points {
		if pt.Registered {
			continue
		}
		tl := byHost[pt.Host]
		if tl == nil {
			tl = &tally{}
			byHost[pt.Host] = tl
		}
		switch {
		case pt.Deleted:
			tl.deleted++
		case pt.Selected:
			tl.selected++
		default:
			tl.off++
		}
		if os.Getenv("RTM_STATS_INSPECT_KEYS") != "" {
			t.Logf("   new: %s -> %q deleted=%v selected=%v name=%q", pt.Key, pt.Host, pt.Deleted, pt.Selected, pt.Name)
		}
	}
	for h, tl := range byHost {
		t.Logf("   host %q: preselected %d, previously deleted (unselected) %d, other unselected %d", h, tl.selected, tl.deleted, tl.off)
	}
}

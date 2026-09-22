package statsreg

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Writes ONLY the local pattern file (~/.rtaskmgr/stats-patterns.json), then
// re-inspects the source environment read-only and compares the pre-filled
// names with what was actually registered.
// RTM_STATS_IMPORT_ID=<receipt> RTM_STATS_REG_HOST RTM_STATS_REG_PASSWORD RTM_STATS_REG_BROKERS RTM_STATS_REG_REDIS
func TestLiveImportAndReinspect(t *testing.T) {
	id := os.Getenv("RTM_STATS_IMPORT_ID")
	if id == "" {
		t.Skip("set RTM_STATS_IMPORT_ID to remember an earlier registration")
	}
	var s Service
	ev, e := s.ImportPatterns(id)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("learned: %d changes, %d notes", len(ev.Changes), len(ev.Notes))
	for _, n := range ev.Notes {
		t.Log("note:", n)
	}
	path, _ := s.receiptPath(id)
	data, _ := os.ReadFile(path)
	var rec receipt
	json.Unmarshal(data, &rec)
	pw := os.Getenv("RTM_STATS_REG_PASSWORD")
	c := Config{DBHost: os.Getenv("RTM_STATS_REG_HOST"), DBPort: 3306, DBUser: "root", DBPassword: pw, RedisHost: os.Getenv("RTM_STATS_REG_REDIS"), RedisPort: 5000, RedisPassword: pw, Brokers: os.Getenv("RTM_STATS_REG_BROKERS"), KafkaSecurity: "PLAINTEXT"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	p, e := s.Inspect(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	got := map[string]Point{}
	for _, pt := range p.Points {
		got[pt.Key] = pt
	}
	same, differ, gone := 0, 0, 0
	for _, want := range rec.Points {
		pt, ok := got[want.Key]
		switch {
		case !ok:
			gone++
		case pt.Name == want.Name && pt.Selected && pt.Interval == want.Interval && pt.Format == want.Format:
			same++
		default:
			differ++
			t.Logf("differs %s: registered %q/%d/%d, now %q/%d/%d selected=%v host=%q", want.Key, want.Name, want.Format, want.Interval, pt.Name, pt.Format, pt.Interval, pt.Selected, pt.Host)
		}
	}
	t.Logf("%s\nregistered %d: same %d, differ %d, key gone %d", p.PatternInfo, len(rec.Points), same, differ, gone)
}

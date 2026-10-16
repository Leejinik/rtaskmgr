package gclog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// RTM_GC_DUMPDIR=<dir of reduced logs from TestLiveGcFetch> go test -run TestLiveDump -v ./internal/gclog/
func TestLiveDump(t *testing.T) {
	dir := os.Getenv("RTM_GC_DUMPDIR")
	if dir == "" {
		t.Skip("set RTM_GC_DUMPDIR")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		p := Parse(string(b), 0)
		runs := SplitRuns(p.Events)
		t.Logf("== %s events=%d runs=%d maxHeap=%dMB", filepath.Base(f), len(p.Events), len(runs), p.MaxHeap/mb)
		kinds := map[string]int{}
		for _, e := range p.Events {
			kinds[e.Kind]++
		}
		t.Logf("   kinds=%v", kinds)
		if len(runs) == 0 {
			continue
		}
		r := runs[len(runs)-1]
		a := Analyze(r, p.MaxHeap)
		t.Logf("   run %s ~ %s  verdict=%s method=%s pts=%d reclaims=%d full=%d slope=%.2fMB/h rise=%.1f rho=%.2f floor %.0f→%.0f cap=%.0f gc%%=%.3f maxPause=%.0fms",
			time.UnixMilli(r[0].T).Format("01-02 15:04"), time.UnixMilli(r[len(r)-1].T).Format("01-02 15:04"),
			a.Verdict, a.Method, a.Points, a.Reclaims, a.FullGCs, a.SlopeMBh, a.RiseMB, a.Rho, a.FloorFromMB, a.FloorToMB, a.CapMB, a.GCTimePct, a.MaxPauseMs)
		t.Logf("   %s", a.Headline)
		for _, n := range a.Notes {
			t.Logf("   - %s", n)
		}
	}
}

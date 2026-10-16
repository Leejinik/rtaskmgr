package gclog

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Lines as daniell's lizcollector (Zulu 8u482, G1, PrintGCDetails) writes them,
// already reduced by the host-side awk filter.
const g1Detailed = `CommandLine flags: -XX:GCLogFileSize=104857600 -XX:InitialHeapSize=652911552 -XX:MaxHeapSize=10446584832 -XX:+UseG1GC
2026-09-29T10:13:26.492+0900: 0.418: [GC pause (G1 Evacuation Pause) (young), 0.0135220 secs]
   [Eden: 30720.0K(30720.0K)->0.0B(30720.0K) Survivors: 0.0B->4096.0K Heap: 30720.0K(624.0M)->3796.6K(624.0M)]
2026-09-29T10:13:27.513+0900: 1.439: [GC cleanup 11286K->9151K(624M), 0.0017824 secs]
2026-09-29T10:13:31.238+0900: 5.164: [GC pause (G1 Evacuation Pause) (mixed), 0.2699443 secs]
   [Eden: 366.0M(366.0M)->0.0B(69632.0K) Survivors: 8192.0K->49152.0K Heap: 374.9M(624.0M)->164.5M(624.0M)]
2026-09-29T10:13:33.295+0900: 7.221: [GC pause (Metadata GC Threshold) (young) (initial-mark), 0.1195846 secs]
   [Eden: 40960.0K(59392.0K)->0.0B(116.0M) Survivors: 4096.0K->8192.0K Heap: 211.2M(1248.0M)->175.1M(2496.0M)]
2026-09-29T10:20:00.000+0900: 400.000: [Full GC (Allocation Failure)  9876M->5432M(10G), 12.3456789 secs]
   [Eden: 0.0B(512.0M)->0.0B(512.0M) Survivors: 0.0B->0.0B Heap: 9876.0M(10.0G)->5432.0M(10.0G)], [Metaspace: 100000K->100000K(1122304K)]
2026-09-29T10:21:00.000+0900: 460.000: [GC pause (G1 Evacuation Pause) (young) (to-space exhausted), 0.1377990 secs]
   [Eden: 1.0G(1.0G)->0.0B(1.0G) Survivors: 0.0B->0.0B Heap: 9.0G(10.0G)->9.5G(10.0G)]
`

func TestParseG1Detailed(t *testing.T) {
	p := Parse(g1Detailed, 0)
	if p.MaxHeap != 10446584832 {
		t.Fatalf("MaxHeap = %d", p.MaxHeap)
	}
	want := []struct {
		kind       string
		after, cap int64
		pause      float64
	}{
		{KindYoung, size("3796.6", "K"), 624 << 20, 13.522},
		{KindCleanup, 9151 << 10, 624 << 20, 0},
		{KindMixed, size("164.5", "M"), 624 << 20, 269.9443},
		{KindYoung, size("175.1", "M"), 2496 << 20, 119.5846},
		{KindFull, 5432 << 20, 10 << 30, 12345.6789},
		{KindYoung, size("9.5", "G"), 10 << 30, 137.799},
	}
	if len(p.Events) != len(want) {
		t.Fatalf("events = %d, want %d: %+v", len(p.Events), len(want), p.Events)
	}
	for i, w := range want {
		e := p.Events[i]
		if e.Kind != w.kind || e.After != w.after || e.Cap != w.cap || fmt.Sprintf("%.3f", e.PauseMs) != fmt.Sprintf("%.3f", w.pause) {
			t.Errorf("#%d = %+v, want %+v", i, e, w)
		}
	}
	ts, _ := time.Parse(time.RFC3339, "2026-09-29T10:13:26.492+09:00")
	if p.Events[0].T != ts.UnixMilli() || p.Events[0].Up != 418 {
		t.Errorf("stamp = %d/%d", p.Events[0].T, p.Events[0].Up)
	}
	if !p.Events[5].Exhaust {
		t.Error("to-space exhausted not flagged")
	}
}

func TestParseParallelAndCMS(t *testing.T) {
	text := `2026-01-01T00:00:00.000+0000: 10.000: [GC (Allocation Failure) [PSYoungGen: 65536K->10720K(76288K)] 65536K->10744K(251392K), 0.0100000 secs] [Times: user=0.01 sys=0.00, real=0.01 secs]
2026-01-01T00:01:00.000+0000: 70.000: [Full GC (Ergonomics) [PSYoungGen: 10720K->0K(76288K)] [ParOldGen: 24K->10500K(175104K)] 10744K->10500K(251392K), [Metaspace: 3000K->3000K(1056768K)], 0.0500000 secs]
2026-01-01T00:02:00.000+0000: 130.000: [GC (Allocation Failure) 2026-01-01T00:02:00.000+0000: 130.000: [ParNew: 1000K->100K(2000K), 0.0010000 secs] 5000K->4100K(9000K), 0.0020000 secs]`
	p := Parse(text, 0)
	if len(p.Events) != 3 {
		t.Fatalf("events = %+v", p.Events)
	}
	if e := p.Events[0]; e.Kind != KindYoung || e.After != 10744<<10 || e.Cap != 251392<<10 {
		t.Errorf("parallel young = %+v", e)
	}
	if e := p.Events[1]; e.Kind != KindFull || e.After != 10500<<10 {
		t.Errorf("parallel full took a generation or Metaspace, not the total: %+v", e)
	}
	if e := p.Events[2]; e.After != 4100<<10 || e.PauseMs != 2 {
		t.Errorf("cms young = %+v", e)
	}
}

func TestParseUptimeOnly(t *testing.T) {
	p := Parse("5.000: [GC pause (G1 Evacuation Pause) (young) 100M->50M(200M), 0.0100000 secs]", 1_000_000)
	if len(p.Events) != 1 || p.Events[0].T != 1_005_000 || !p.Events[0].Approx {
		t.Fatalf("%+v", p.Events)
	}
	if len(Parse("5.000: [GC pause (young) 100M->50M(200M), 0.01 secs]", 0).Events) != 0 {
		t.Error("uptime-only line without a process start must be dropped, not placed at 1970")
	}
}

func TestSplitRuns(t *testing.T) {
	ev := []Event{
		{T: 10_000, Up: 1_000}, {T: 20_000, Up: 11_000}, // run A, started at 9s
		{T: 30_000, Up: 500}, {T: 40_000, Up: 10_500}, // restart
	}
	r := SplitRuns(ev)
	if len(r) != 2 || len(r[0]) != 2 || len(r[1]) != 2 {
		t.Fatalf("%v", r)
	}
}

// synth builds a run of young collections every minute over hours, with a floor of
// base + slope*h MB, a sawtooth of garbage above it, and a mixed collection landing
// on the floor every reclaimEvery minutes (0 = never).
func synth(hours float64, base, slopeMBh float64, reclaimEvery int) []Event {
	var ev []Event
	start := int64(1_700_000_000_000)
	n := int(hours * 60)
	for i := 0; i <= n; i++ {
		h := float64(i) / 60
		floor := base + slopeMBh*h
		kind := KindYoung
		after := floor + float64(i%reclaimOr(reclaimEvery))*2 // unswept Old garbage piles up between reclaims
		if reclaimEvery > 0 && i > 0 && i%reclaimEvery == 0 {
			kind, after = KindMixed, floor
		}
		ev = append(ev, Event{T: start + int64(i)*60000, Up: int64(i) * 60000, Kind: kind,
			After: int64(after * mb), Before: int64((after + 300) * mb), Cap: 2048 * mb, PauseMs: 10})
	}
	return ev
}

func reclaimOr(n int) int {
	if n <= 0 {
		return 1 << 30
	}
	return n
}

func TestAnalyzeVerdicts(t *testing.T) {
	cases := []struct {
		name    string
		ev      []Event
		verdict string
	}{
		{"flat floor", synth(24, 500, 0, 30), VerdictStable},
		{"steady climb", synth(24, 500, 10, 30), VerdictSuspect},
		{"slow climb", synth(24, 500, 2, 30), VerdictWatch},
		{"small climb on a big floor", synth(24, 1000, 2, 30), VerdictStable}, // 44 MB on 1 GB is under 5%
		{"climb with no reclaim", synth(24, 500, 10, 0), VerdictHold},
		{"too short", synth(1.5, 500, 50, 10), VerdictInsufficient},
	}
	for _, c := range cases {
		a := Analyze(c.ev, 4096*mb)
		if a.Verdict != c.verdict {
			t.Errorf("%s: verdict %s (%s) slope=%.2f rise=%.1f rho=%.2f method=%s", c.name, a.Verdict, a.Headline, a.SlopeMBh, a.RiseMB, a.Rho, a.Method)
		}
		if a.Headline == "" {
			t.Errorf("%s: empty headline", c.name)
		}
	}
}

func TestAnalyzeUsesReclaimFloor(t *testing.T) {
	a := Analyze(synth(24, 500, 10, 30), 4096*mb)
	if a.Method != MethodReclaim {
		t.Fatalf("method = %s", a.Method)
	}
	if a.SlopeMBh < 9.5 || a.SlopeMBh > 10.5 {
		t.Errorf("slope = %.2f, want ~10", a.SlopeMBh)
	}
	if a.HoursToMax <= 0 {
		t.Error("no time-to-max projection for a climbing floor")
	}
}

// A single outlier must not flip a flat floor into a leak — the reason for Theil–Sen.
func TestAnalyzeOutlierResistant(t *testing.T) {
	ev := synth(24, 500, 0, 30)
	ev[len(ev)-1].Kind = KindMixed
	ev[len(ev)-1].After = 3000 * mb
	if a := Analyze(ev, 4096*mb); a.Verdict != VerdictStable {
		t.Errorf("verdict %s slope %.2f", a.Verdict, a.SlopeMBh)
	}
}

func TestMergeDedupesAndTrims(t *testing.T) {
	now := int64(100 * 24 * 3600 * 1000)
	a := &Archive{}
	old := Event{T: 1000, Up: 1, Kind: KindYoung}
	e1 := Event{T: now - 1000, Up: 5, Kind: KindYoung}
	e2 := Event{T: now - 500, Up: 6, Kind: KindMixed}
	if n := a.Merge([]Event{old, e2, e1}, now); n != 3 {
		t.Fatalf("added %d", n)
	}
	if n := a.Merge([]Event{e1, e2}, now); n != 0 {
		t.Fatalf("re-fetch added %d duplicates", n)
	}
	if len(a.Events) != 2 || a.Events[0] != e1 {
		t.Fatalf("not trimmed/sorted: %+v", a.Events)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s := Store{Root: t.TempDir()}
	k := ModuleKey("collector", "/usr/local/liz/lizcollector/logs/gc.log")
	if !strings.HasPrefix(k, "collector-") || k == ModuleKey("collector", "/other/gc.log") {
		t.Fatalf("key %q", k)
	}
	a := &Archive{Key: k, Name: "collector", Events: []Event{{T: 1, Kind: KindYoung}}}
	if err := s.Save("h/../1", a); err != nil {
		t.Fatal(err)
	}
	b, err := s.Load("h/../1", k)
	if err != nil || len(b.Events) != 1 || b.Name != "collector" {
		t.Fatalf("%+v %v", b, err)
	}
	if got := s.List("h/../1"); len(got) != 1 || got[0] != k {
		t.Fatalf("list %v", got)
	}
}

func TestBuildChartThins(t *testing.T) {
	ev := synth(72, 500, 1, 30)
	a := Analyze(ev, 4096*mb)
	c := BuildChart(ev, a, 100)
	if len(c.After) > 2*100+2 || len(c.After) < 100 {
		t.Errorf("after points = %d", len(c.After))
	}
	if c.FitTo.MB <= c.FitFrom.MB {
		t.Error("fit line not rising")
	}
}

// sawtooth: garbage piles up at gMBh between weekly sweeps; each sweep lands on
// floor + leakMBh*h. Three weeks → three sweeps, too few for MethodReclaim.
func sawtooth(gMBh, leakMBh float64) []Event {
	var ev []Event
	start := int64(1_700_000_000_000)
	for i := 0; i <= 21*24*6; i++ { // every 10 min for 3 weeks
		h := float64(i) / 6
		live := 80 + leakMBh*h
		since := h - float64(int(h/168))*168
		kind, after := KindYoung, live+gMBh*since
		if i > 0 && i%(168*6) == 0 {
			kind, after = KindMixed, live
		}
		ev = append(ev, Event{T: start + int64(i)*600000, Up: int64(i) * 600000, Kind: kind,
			After: int64(after * mb), Cap: 752 * mb, PauseMs: 5})
	}
	return ev
}

// The trunk loader shape: +38 MB/day, swept back weekly. Not a leak.
func TestAnalyzeSawtoothIsNotALeak(t *testing.T) {
	a := Analyze(sawtooth(38.0/24, 0), 12000*mb)
	if a.Method != MethodFewReclaims || a.Verdict != VerdictStable {
		t.Errorf("method=%s verdict=%s rise=%.1f pts=%d (%s)", a.Method, a.Verdict, a.RiseMB, a.Points, a.Headline)
	}
}

// Same sawtooth, but every sweep lands higher: flagged, yet no stronger than watch.
func TestAnalyzeSawtoothWithLeakCapsAtWatch(t *testing.T) {
	a := Analyze(sawtooth(38.0/24, 1), 12000*mb)
	if a.Method != MethodFewReclaims || a.Verdict != VerdictWatch {
		t.Errorf("method=%s verdict=%s rise=%.1f pts=%d (%s)", a.Method, a.Verdict, a.RiseMB, a.Points, a.Headline)
	}
}

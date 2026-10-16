package gclog

import (
	"fmt"
	"math"
	"sort"
)

// Verdicts, strongest first. The tool never says "leak": a log alone cannot tell a
// leak from a workload that grew (devices added, a cache warming for a week), so the
// strongest word is "suspect" and the text says what to check next.
const (
	VerdictSuspect      = "suspect"      // 누수 의심
	VerdictWatch        = "watch"        // 관찰 필요
	VerdictStable       = "stable"       // 안정
	VerdictHold         = "hold"         // 판정 보류 — rising, but Old has never been reclaimed
	VerdictInsufficient = "insufficient" // 데이터 부족
	VerdictNoLog        = "nolog"        // GC 로그를 남기지 않는 프로세스
)

// Run is one JVM lifetime. A restart resets uptime and empties the heap, so a slope
// fitted across a restart would measure the restart, not the leak.
type Run struct {
	StartMs int64 `json:"startMs"` // JVM start (first event's T - uptime)
	FromMs  int64 `json:"fromMs"`  // first event
	ToMs    int64 `json:"toMs"`    // last event
	Events  int   `json:"events"`
}

// SplitRuns groups time-ordered events by JVM lifetime: uptime going backwards, or
// the implied start moving by more than a few seconds, starts a new run.
func SplitRuns(ev []Event) [][]Event {
	var runs [][]Event
	var cur []Event
	var start int64
	for i, e := range ev {
		s := e.T - e.Up
		if i > 0 && (e.Up < ev[i-1].Up || abs64(s-start) > 5000) {
			runs = append(runs, cur)
			cur = nil
		}
		if len(cur) == 0 {
			start = s
		}
		cur = append(cur, e)
	}
	if len(cur) > 0 {
		runs = append(runs, cur)
	}
	return runs
}

// Analysis is the verdict for one run and the numbers it rests on.
type Analysis struct {
	Verdict     string   `json:"verdict"`
	Headline    string   `json:"headline"` // one Korean sentence for the table row
	Method      string   `json:"method"`   // how the floor was taken
	SlopeMBh    float64  `json:"slopeMBh"` // robust slope of the floor, MB per hour
	RiseMB      float64  `json:"riseMB"`   // fitted rise across the analysed window
	FloorFromMB float64  `json:"floorFromMB"`
	FloorToMB   float64  `json:"floorToMB"`
	Rho         float64  `json:"rho"`    // rank correlation of the floor with time: how steady the climb is
	FromMs      int64    `json:"fromMs"` // start of the analysed window (after warm-up)
	ToMs        int64    `json:"toMs"`
	WarmupMin   int      `json:"warmupMin"` // minutes excluded at the start
	SpanH       float64  `json:"spanH"`
	Points      int      `json:"points"`   // floor points used
	Reclaims    int      `json:"reclaims"` // mixed + full collections in the window
	FullGCs     int      `json:"fullGCs"`
	Exhausted   int      `json:"exhausted"` // G1 evacuation failures
	MaxPauseMs  float64  `json:"maxPauseMs"`
	GCTimePct   float64  `json:"gcTimePct"` // share of wall time spent paused
	CapMB       float64  `json:"capMB"`     // committed heap at the end
	MaxHeapMB   float64  `json:"maxHeapMB"`
	HoursToMax  float64  `json:"hoursToMax"` // at this slope, until the floor hits MaxHeap; 0 = n/a
	Notes       []string `json:"notes"`
	Floor       []Point  `json:"-"` // kept for the chart
}

// Point is one (time, MB) sample.
type Point struct {
	T  int64   `json:"t"`
	MB float64 `json:"mb"`
}

const mb = 1 << 20

// Thresholds. They are deliberately coarse: the point of the tool is to tell the
// operator which of ten modules to look at, not to certify one.
const (
	minSpanH       = 2.0 // below this after warm-up there is nothing to fit
	minPoints      = 6
	suspectRiseMB  = 64.0
	suspectRisePct = 0.10
	watchRiseMB    = 32.0
	watchRisePct   = 0.05
	steadyRho      = 0.6
)

// Analyze judges one run.
func Analyze(run []Event, maxHeap int64) Analysis {
	a := Analysis{MaxHeapMB: float64(maxHeap) / mb}
	if len(run) == 0 {
		a.Verdict, a.Headline = VerdictInsufficient, "GC 기록이 없습니다"
		return a
	}
	first, last := run[0].T, run[len(run)-1].T
	span := float64(last-first) / 3.6e6

	// Warm-up: caches fill, classes load, the heap sizes itself. That climb is real
	// and not a leak. 10% of the run, at least 30 minutes, at most 2 hours.
	warm := math.Min(2*60, math.Max(30, span*60*0.10))
	a.WarmupMin = int(warm)
	from := first + int64(warm*60000)
	var win []Event
	for _, e := range run {
		if e.T >= from {
			win = append(win, e)
		}
	}
	a.FromMs, a.ToMs = from, last
	if len(win) > 0 {
		a.FromMs = win[0].T
	}
	a.SpanH = float64(a.ToMs-a.FromMs) / 3.6e6
	if a.SpanH < 0 {
		a.SpanH = 0
	}

	var pauseSum float64
	for _, e := range win {
		switch e.Kind {
		case KindMixed:
			a.Reclaims++
		case KindFull:
			a.Reclaims++
			a.FullGCs++
		}
		if e.Exhaust {
			a.Exhausted++
		}
		if e.PauseMs > a.MaxPauseMs {
			a.MaxPauseMs = e.PauseMs
		}
		pauseSum += e.PauseMs
	}
	if a.SpanH > 0 {
		a.GCTimePct = pauseSum / (a.SpanH * 3.6e6) * 100
	}
	a.CapMB = float64(run[len(run)-1].Cap) / mb

	a.Floor, a.Method = floor(win)
	a.Points = len(a.Floor)

	need := minPoints
	if a.Method == MethodFewReclaims {
		need = 2
	}
	if a.SpanH < minSpanH || a.Points < need {
		a.Verdict = VerdictInsufficient
		a.Headline = fmt.Sprintf("분석 구간 %.1f시간 · 바닥점 %d개 — 판정하기에 짧습니다 (최소 %.0f시간)", a.SpanH, a.Points, minSpanH)
		a.Notes = append(a.Notes, fmt.Sprintf("기동 후 처음 %d분은 워밍업으로 보고 제외했습니다.", a.WarmupMin))
		addPressureNotes(&a)
		return a
	}

	a.SlopeMBh, a.FloorFromMB = theilSen(a.Floor)
	a.RiseMB = a.SlopeMBh * a.SpanH
	a.FloorToMB = a.FloorFromMB + a.RiseMB
	a.Rho = spearman(a.Floor)

	base := math.Max(a.FloorFromMB, 1)
	rising := a.RiseMB > 0 && a.Rho >= steadyRho
	switch {
	case rising && a.RiseMB >= math.Max(suspectRiseMB, base*suspectRisePct):
		a.Verdict = VerdictSuspect
	case rising && a.RiseMB >= math.Max(watchRiseMB, base*watchRisePct):
		a.Verdict = VerdictWatch
	default:
		a.Verdict = VerdictStable
	}

	// Without a mixed or Full collection in the window Old has never been swept, so
	// the floor is live data PLUS every dead object that was promoted. That floor
	// rises in a perfectly healthy JVM. Say so instead of calling it a leak.
	if a.Reclaims == 0 && a.Verdict != VerdictStable {
		a.Verdict = VerdictHold
	}
	// Two or three points can say "it came back to where it started"; they cannot
	// carry "suspect". Rank correlation also means nothing at n=2.
	if a.Method == MethodFewReclaims {
		if a.Points < 3 && a.RiseMB > 0 && a.RiseMB >= math.Max(watchRiseMB, base*watchRisePct) {
			a.Verdict = VerdictWatch
		}
		if a.Verdict == VerdictSuspect {
			a.Verdict = VerdictWatch
		}
	}

	if a.SlopeMBh > 0 && a.MaxHeapMB > 0 && a.FloorToMB < a.MaxHeapMB {
		a.HoursToMax = (a.MaxHeapMB - a.FloorToMB) / a.SlopeMBh
	}

	switch a.Verdict {
	case VerdictSuspect:
		a.Headline = fmt.Sprintf("GC 후 바닥선이 %.1f시간 동안 %s 올랐습니다 (%+.1f MB/h, 꾸준함 %.2f)", a.SpanH, fmtMB(a.RiseMB), a.SlopeMBh, a.Rho)
	case VerdictWatch:
		a.Headline = fmt.Sprintf("바닥선이 조금씩 오릅니다 — %.1f시간 동안 %s (%+.1f MB/h)", a.SpanH, fmtMB(a.RiseMB), a.SlopeMBh)
	case VerdictHold:
		a.Headline = fmt.Sprintf("바닥선이 오르지만(%+.1f MB/h) Old 회수가 한 번도 없어 판정할 수 없습니다", a.SlopeMBh)
	default:
		a.Headline = fmt.Sprintf("바닥선이 평평합니다 — %.1f시간 동안 %s (%+.1f MB/h)", a.SpanH, fmtMB(a.RiseMB), a.SlopeMBh)
	}

	a.Notes = append(a.Notes, fmt.Sprintf("기동 후 처음 %d분은 워밍업으로 보고 제외했습니다.", a.WarmupMin))
	if a.Method == MethodFewReclaims {
		a.Notes = append(a.Notes, fmt.Sprintf("Old 회수(mixed/Full GC)가 %d회뿐이라, 분석 시작 시점의 최저값과 회수 직후 값만 비교했습니다. 점이 적어 「관찰 필요」보다 강하게 판정하지 않습니다.", a.Reclaims))
	}
	if a.Method == MethodBucketMin {
		a.Notes = append(a.Notes, "바닥선은 구간별 GC 후 최소값입니다. Old 회수(mixed/Full GC) 직후 값이 충분치 않아 대신 쓴 것이라, 아직 안 치운 Old 쓰레기가 섞여 실제보다 높게 보일 수 있습니다.")
	}
	switch a.Verdict {
	case VerdictSuspect, VerdictWatch:
		a.Notes = append(a.Notes,
			"이 기간에 장비·정책 추가 등 부하가 늘었다면 그 증가일 수 있습니다. 부하가 일정했는지 먼저 확인하세요.",
			"확인하려면: 부하가 일정한 상태에서 더 오래 관찰하거나, 시간 간격을 두고 클래스 히스토그램을 두 번 떠서 비교합니다(JDK 8 은 매번 Full GC 가 일어나므로 공유 환경에서는 미리 알릴 것).")
	case VerdictHold:
		a.Notes = append(a.Notes, "Old 영역이 IHOP(기본 45%)에 닿지 않아 concurrent cycle·mixed GC 가 돌지 않았습니다. 더 오래 관찰하면 회수 직후 값이 생겨 판정할 수 있습니다.")
	}
	if a.HoursToMax > 0 && a.Verdict != VerdictStable {
		a.Notes = append(a.Notes, fmt.Sprintf("이 기울기가 계속되면 약 %s 뒤 바닥선이 최대 힙(%s)에 닿습니다.", fmtHours(a.HoursToMax), fmtMB(a.MaxHeapMB)))
	}
	addPressureNotes(&a)
	return a
}

func addPressureNotes(a *Analysis) {
	if a.FullGCs > 0 {
		a.Notes = append(a.Notes, fmt.Sprintf("Full GC %d회 — G1 에서 Full GC 는 힙이 모자라다는 신호입니다.", a.FullGCs))
	}
	if a.Exhausted > 0 {
		a.Notes = append(a.Notes, fmt.Sprintf("to-space exhausted %d회 — 옮길 공간이 없어 회수에 실패한 적이 있습니다.", a.Exhausted))
	}
}

// Methods for taking the floor.
const (
	MethodReclaim   = "reclaim"   // heap right after mixed/Full collections
	MethodBucketMin = "bucketmin" // lowest post-GC heap per time bucket
	// MethodFewReclaims: the start of the window plus the few post-sweep values
	// there are. Judged, but never stronger than "watch".
	MethodFewReclaims = "fewreclaims"
)

// floor picks the points that stand for "what is still alive".
//
// The honest figure is the heap right after Old was swept — the last mixed
// collection of a G1 cycle, or a Full GC. When the window has enough of those, spread
// over it, they ARE the floor. Otherwise take the lowest post-collection heap in each
// of ~24 time buckets: it follows the same line, just with the unswept Old garbage on
// top (the caller says so).
func floor(win []Event) ([]Point, string) {
	if len(win) == 0 {
		return nil, MethodBucketMin
	}
	var rec []Point
	for i, e := range win {
		switch e.Kind {
		case KindFull:
			rec = append(rec, Point{e.T, float64(e.After) / mb})
		case KindMixed:
			// Only the last mixed of a run of them: the earlier ones have swept part of
			// the candidate regions and would read as a floor that is still falling.
			if i+1 >= len(win) || win[i+1].Kind != KindMixed {
				rec = append(rec, Point{e.T, float64(e.After) / mb})
			}
		}
	}
	span := win[len(win)-1].T - win[0].T
	if len(rec) >= minPoints && span > 0 && rec[len(rec)-1].T-rec[0].T >= span/2 {
		return rec, MethodReclaim
	}

	n := 24
	b := span / int64(n)
	if b < 5*60000 {
		b = 5 * 60000
	}

	// A few sweeps are still worth more than any number of bucket minima. Between
	// two sweeps the bucket minimum climbs with the Old garbage and falls back at the
	// sweep — a sawtooth whose fitted slope is the garbage rate, not a leak (trunk
	// loader: +38 MB/day for a week, back to 101 MB at every sweep, called "suspect"
	// before this). So when Old HAS been swept, compare the post-sweep values with
	// where the heap started: the lowest post-GC heap in the first bucket after
	// warm-up, which is as close to "live only" as the start of a run gets.
	if len(rec) > 0 {
		anchor := Point{T: -1}
		for _, e := range win {
			if e.T-win[0].T >= b {
				break
			}
			if e.Kind == KindCleanup {
				continue
			}
			if v := float64(e.After) / mb; anchor.T < 0 || v < anchor.MB {
				anchor = Point{e.T, v}
			}
		}
		if anchor.T >= 0 && anchor.T < rec[0].T {
			return append([]Point{anchor}, rec...), MethodFewReclaims
		}
		if len(rec) >= 2 {
			return rec, MethodFewReclaims
		}
	}
	var out []Point
	cur := Point{T: -1}
	var bucket int64 = -1
	for _, e := range win {
		if e.Kind == KindCleanup {
			continue // cleanup only frees wholly-empty regions; its "after" is not a floor
		}
		k := (e.T - win[0].T) / b
		v := float64(e.After) / mb
		if k != bucket {
			if cur.T >= 0 {
				out = append(out, cur)
			}
			bucket, cur = k, Point{e.T, v}
			continue
		}
		if v < cur.MB {
			cur = Point{e.T, v}
		}
	}
	if cur.T >= 0 {
		out = append(out, cur)
	}
	return out, MethodBucketMin
}

// theilSen is the median of pairwise slopes: one odd point (a burst that survived a
// collection, a Full GC that dug deeper than usual) cannot swing it the way it swings
// least squares. Returns MB/h and the fitted value at the first point.
func theilSen(p []Point) (slopeMBh, at0 float64) {
	if len(p) < 2 {
		return 0, 0
	}
	// Cap the pair count: 24 buckets is trivial, a month of reclaim points is not.
	step := 1
	if len(p) > 400 {
		step = len(p) / 400
	}
	var sl []float64
	for i := 0; i < len(p); i += step {
		for j := i + step; j < len(p); j += step {
			dt := float64(p[j].T-p[i].T) / 3.6e6
			if dt > 0 {
				sl = append(sl, (p[j].MB-p[i].MB)/dt)
			}
		}
	}
	if len(sl) == 0 {
		return 0, p[0].MB
	}
	slopeMBh = median(sl)
	var ic []float64
	for _, q := range p {
		ic = append(ic, q.MB-slopeMBh*float64(q.T-p[0].T)/3.6e6)
	}
	return slopeMBh, median(ic)
}

// spearman is the rank correlation of the floor with time: 1 means every point is
// higher than the one before. A leak climbs steadily; a floor that wanders up and
// down with the workload does not, even when its fitted slope is positive.
func spearman(p []Point) float64 {
	n := len(p)
	if n < 3 {
		return 0
	}
	r := ranks(p)
	var d2 float64
	for i := 0; i < n; i++ {
		d := float64(i+1) - r[i]
		d2 += d * d
	}
	return 1 - 6*d2/(float64(n)*(float64(n)*float64(n)-1))
}

func ranks(p []Point) []float64 {
	idx := make([]int, len(p))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return p[idx[a]].MB < p[idx[b]].MB })
	r := make([]float64, len(p))
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && p[idx[j+1]].MB == p[idx[i]].MB {
			j++
		}
		avg := float64(i+j)/2 + 1
		for k := i; k <= j; k++ {
			r[idx[k]] = avg
		}
		i = j + 1
	}
	return r
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func fmtMB(v float64) string {
	if math.Abs(v) >= 1024 {
		return fmt.Sprintf("%.2f GB", v/1024)
	}
	return fmt.Sprintf("%.0f MB", v)
}

func fmtHours(h float64) string {
	if h >= 48 {
		return fmt.Sprintf("%.0f일", h/24)
	}
	return fmt.Sprintf("%.0f시간", h)
}

// Chart is what the detail view draws for one run, thinned to a drawable size.
type Chart struct {
	Run        Run     `json:"run"`
	After      []Point `json:"after"`    // post-GC heap (per-bucket min and max, so peaks survive thinning)
	Cap        []Point `json:"cap"`      // committed heap
	Floor      []Point `json:"floor"`    // the points the verdict was fitted to
	Reclaims   []Point `json:"reclaims"` // mixed/Full after-values
	FitFrom    Point   `json:"fitFrom"`
	FitTo      Point   `json:"fitTo"`
	WarmupToMs int64   `json:"warmupToMs"`
	MaxHeapMB  float64 `json:"maxHeapMB"`
}

// BuildChart thins a run to at most ~2×buckets points per series.
func BuildChart(run []Event, a Analysis, buckets int) Chart {
	c := Chart{MaxHeapMB: a.MaxHeapMB, Floor: a.Floor, WarmupToMs: a.FromMs}
	if len(run) == 0 {
		return c
	}
	c.Run = Run{StartMs: run[0].T - run[0].Up, FromMs: run[0].T, ToMs: run[len(run)-1].T, Events: len(run)}
	if a.Points > 0 && a.SpanH > 0 {
		c.FitFrom = Point{a.FromMs, a.FloorFromMB}
		c.FitTo = Point{a.ToMs, a.FloorToMB}
	}
	span := run[len(run)-1].T - run[0].T
	b := span / int64(buckets)
	if b < 1 {
		b = 1
	}
	type agg struct {
		lo, hi Point
		cap    Point
		has    bool
	}
	var cur agg
	var key int64 = -1
	emit := func() {
		if !cur.has {
			return
		}
		if cur.lo.T <= cur.hi.T {
			c.After = append(c.After, cur.lo)
			if cur.hi.T != cur.lo.T {
				c.After = append(c.After, cur.hi)
			}
		} else {
			c.After = append(c.After, cur.hi, cur.lo)
		}
		c.Cap = append(c.Cap, cur.cap)
	}
	for _, e := range run {
		if e.Kind == KindMixed || e.Kind == KindFull {
			c.Reclaims = append(c.Reclaims, Point{e.T, float64(e.After) / mb})
		}
		if e.Kind == KindCleanup {
			continue
		}
		k := (e.T - run[0].T) / b
		v := Point{e.T, float64(e.After) / mb}
		if k != key {
			emit()
			key = k
			cur = agg{lo: v, hi: v, cap: Point{e.T, float64(e.Cap) / mb}, has: true}
			continue
		}
		if v.MB < cur.lo.MB {
			cur.lo = v
		}
		if v.MB > cur.hi.MB {
			cur.hi = v
		}
		if cm := float64(e.Cap) / mb; cm > cur.cap.MB {
			cur.cap = Point{e.T, cm}
		}
	}
	emit()
	if len(c.Reclaims) > 4*buckets {
		c.Reclaims = thin(c.Reclaims, 4*buckets)
	}
	return c
}

func thin(p []Point, n int) []Point {
	step := float64(len(p)) / float64(n)
	out := make([]Point, 0, n)
	for i := 0.0; int(i) < len(p); i += step {
		out = append(out, p[int(i)])
	}
	return out
}

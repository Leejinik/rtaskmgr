// Package gclog reads the GC logs a JVM already writes and judges whether the heap
// that survives collection keeps growing — the signature of a leak.
//
// Nothing here talks to a JVM. It never attaches, never forces a collection and never
// dumps a heap: those stop the process or trigger a Full GC, and the servers this tool
// looks at are shared by several testers. The price is that the tool can only say
// what the log already says, and the log is wiped when the module restarts — which is
// why every fetch is merged into a local archive (store.go).
//
// Formats: JDK 8 -Xloggc with -XX:+PrintGCDetails/-XX:+PrintGCDateStamps (what MK119
// runs), for G1 as well as Parallel/CMS. JDK 9+ unified logging is not parsed.
package gclog

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Kind of collection an event records.
const (
	KindYoung   = "young"
	KindMixed   = "mixed"
	KindFull    = "full"
	KindCleanup = "cleanup" // G1 concurrent cycle cleanup: frees wholly-empty regions
)

// Event is one collection: the heap before and after it, and how long it paused.
type Event struct {
	T       int64   `json:"t"`  // unix millis (wall clock from the date stamp)
	Up      int64   `json:"up"` // JVM uptime millis at the event; resets on restart
	Kind    string  `json:"k"`
	Before  int64   `json:"b"`            // heap used before, bytes
	After   int64   `json:"a"`            // heap used after, bytes
	Cap     int64   `json:"c"`            // committed heap after, bytes
	PauseMs float64 `json:"p"`            // stop-the-world time; 0 for concurrent phases
	Exhaust bool    `json:"x,omitempty"`  // G1 to-space exhausted (evacuation failure)
	Approx  bool    `json:"ap,omitempty"` // no date stamp: T reconstructed from uptime
}

var (
	reDateStamp = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}[+-]\d{4}): (\d+\.\d+): `)
	reUpOnly    = regexp.MustCompile(`^(\d+\.\d+): `)
	reTrans     = regexp.MustCompile(`(\d+(?:\.\d+)?)([BKMG]?)->(\d+(?:\.\d+)?)([BKMG]?)\((\d+(?:\.\d+)?)([BKMG]?)\)`)
	reG1Heap    = regexp.MustCompile(`Heap: (\d+(?:\.\d+)?)([BKMG]?)\((\d+(?:\.\d+)?)([BKMG]?)\)->(\d+(?:\.\d+)?)([BKMG]?)\((\d+(?:\.\d+)?)([BKMG]?)\)`)
	reSecs      = regexp.MustCompile(`, (\d+\.\d+) secs\]`)
	reMaxHeap   = regexp.MustCompile(`-XX:MaxHeapSize=(\d+)`)
)

// Parsed is what one fetch of a log yields.
type Parsed struct {
	Events  []Event
	MaxHeap int64 // from the "CommandLine flags" header, 0 if absent
}

// Parse reads the reduced log text (header lines, their "Heap:" detail lines and the
// CommandLine flags line — see monitor.gcExtractScript). procStartMs is the process's
// start instant and is only used for logs written without -XX:+PrintGCDateStamps,
// where every event carries uptime alone; those events are flagged Approx because
// the reconstruction is only right for the run that is still alive.
func Parse(text string, procStartMs int64) Parsed {
	var out Parsed
	var pending *Event // G1 detailed header waiting for its Heap: line
	flush := func() {
		if pending != nil && pending.After > 0 {
			out.Events = append(out.Events, *pending)
		}
		pending = nil
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.Contains(line, "CommandLine flags:") {
			if m := reMaxHeap.FindStringSubmatch(line); m != nil {
				out.MaxHeap, _ = strconv.ParseInt(m[1], 10, 64)
			}
			continue
		}
		if m := reG1Heap.FindStringSubmatch(line); m != nil && pending != nil && !strings.HasPrefix(strings.TrimSpace(line), "20") {
			pending.Before = size(m[1], m[2])
			pending.After = size(m[5], m[6])
			pending.Cap = size(m[7], m[8])
			flush()
			continue
		}
		t, up, rest, approx, ok := stamp(line, procStartMs)
		if !ok {
			continue
		}
		kind := classify(rest)
		if kind == "" {
			continue
		}
		flush()
		ev := Event{T: t, Up: up, Kind: kind, Approx: approx}
		if m := lastMatch(reSecs, rest); m != nil {
			ev.PauseMs, _ = strconv.ParseFloat(m[1], 64)
			ev.PauseMs *= 1000
		}
		if kind == KindCleanup {
			ev.PauseMs = 0 // cleanup is a pause too, but a tiny bookkeeping one; not what an operator means by "GC pause"
		}
		ev.Exhaust = strings.Contains(rest, "to-space exhausted")
		if b, a, c, ok := totalTransition(rest); ok {
			ev.Before, ev.After, ev.Cap = b, a, c
			out.Events = append(out.Events, ev)
			continue
		}
		// G1 with PrintGCDetails puts the totals on a later "Heap:" line.
		e := ev
		pending = &e
	}
	flush()
	return out
}

// stamp splits the leading "date: uptime: " off a header line.
func stamp(line string, procStartMs int64) (t, up int64, rest string, approx, ok bool) {
	if m := reDateStamp.FindStringSubmatch(line); m != nil {
		ts, err := time.Parse("2006-01-02T15:04:05.000-0700", m[1])
		if err != nil {
			return 0, 0, "", false, false
		}
		upf, _ := strconv.ParseFloat(m[2], 64)
		return ts.UnixMilli(), int64(upf * 1000), line[len(m[0]):], false, true
	}
	if m := reUpOnly.FindStringSubmatch(line); m != nil && procStartMs > 0 {
		upf, _ := strconv.ParseFloat(m[1], 64)
		u := int64(upf * 1000)
		return procStartMs + u, u, line[len(m[0]):], true, true
	}
	return 0, 0, "", false, false
}

// classify names the collection a header records, or "" for lines that carry no
// heap figures worth a point (concurrent phases, remark).
func classify(rest string) string {
	switch {
	case strings.HasPrefix(rest, "[GC concurrent"), strings.HasPrefix(rest, "[GC remark"):
		return ""
	case strings.HasPrefix(rest, "[Full GC"):
		return KindFull
	case strings.HasPrefix(rest, "[GC cleanup"):
		return KindCleanup
	case strings.HasPrefix(rest, "[GC"):
		if strings.Contains(rest, "(mixed)") {
			return KindMixed
		}
		return KindYoung
	}
	return ""
}

// totalTransition finds the whole-heap "before->after(capacity)" on a header line.
//
// A Parallel or CMS line holds several transitions — per generation, the total, and
// Metaspace — and only the total is the heap. Per-generation and Metaspace figures sit
// inside a named bracket ("[PSYoungGen: …]", "[Metaspace: …]"); the total does not.
// So: the last transition that is not inside a "[Name: " bracket.
func totalTransition(rest string) (before, after, capacity int64, ok bool) {
	idx := reTrans.FindAllStringSubmatchIndex(rest, -1)
	for i := len(idx) - 1; i >= 0; i-- {
		at := idx[i][0]
		open := strings.LastIndex(rest[:at], "[")
		shut := strings.LastIndex(rest[:at], "]")
		if open > shut && strings.Contains(rest[open:at], ":") {
			continue
		}
		g := func(k int) string { return rest[idx[i][2*k]:idx[i][2*k+1]] }
		return size(g(1), g(2)), size(g(3), g(4)), size(g(5), g(6)), true
	}
	return 0, 0, 0, false
}

func lastMatch(re *regexp.Regexp, s string) []string {
	all := re.FindAllStringSubmatch(s, -1)
	if len(all) == 0 {
		return nil
	}
	return all[len(all)-1]
}

// size turns "624.0" + "M" into bytes. HotSpot omits the unit on some K figures.
func size(num, unit string) int64 {
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	switch unit {
	case "B":
	case "M":
		f *= 1 << 20
	case "G":
		f *= 1 << 30
	default: // "K" or none
		f *= 1 << 10
	}
	return int64(f)
}

// Wails bindings for the memory-trend view: is the heap that survives GC growing?
//
// The data is the GC log each JVM already writes. The tool only reads it — see
// internal/monitor/gcfetch.go for why it never attaches to a JVM — and keeps a local
// archive per module, because the JVM starts its GC log over on every restart and
// the history before a restart exists nowhere else.
package main

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"rtaskmgr/internal/gclog"
)

// firstFetchDays bounds the first fetch of a log that has no archive yet.
const firstFetchDays = 30

// GcModuleReport is one JVM row in the view.
type GcModuleReport struct {
	Key          string         `json:"key"`
	Name         string         `json:"name"`
	User         string         `json:"user"`
	PID          int            `json:"pid"`
	Running      bool           `json:"running"` // seen in the fetch that produced this report
	GcLog        string         `json:"gcLog"`
	Err          string         `json:"err"`
	UpSec        int64          `json:"upSec"`
	RSSMB        float64        `json:"rssMB"`
	Threads      int            `json:"threads"`
	FDs          int            `json:"fds"`
	Proc         ProcTrend      `json:"proc"` // RSS/threads across fetches of this same process
	Added        int            `json:"added"`
	Archives     int            `json:"archives"`     // restart archives (gc-logs-*.tar.gz) next to the log
	ArchivesRead int            `json:"archivesRead"` // opened in this fetch
	Archived     int            `json:"archived"`
	Runs         []gclog.Run    `json:"runs"`
	Analysis     gclog.Analysis `json:"analysis"`
}

// ProcTrend compares the first and last fetch of the current process. The GC log
// cannot see native memory, threads or file handles; these few samples are the only
// hint this tool has of those leaks, and they are only as dense as the fetches.
type ProcTrend struct {
	Samples    int     `json:"samples"`
	SpanH      float64 `json:"spanH"`
	RSSFromMB  float64 `json:"rssFromMB"`
	ThreadFrom int     `json:"threadFrom"`
	FDFrom     int     `json:"fdFrom"`
}

// GcHostReport is one server.
type GcHostReport struct {
	HostID    string           `json:"hostId"`
	HostName  string           `json:"hostName"`
	Err       string           `json:"err"`
	FetchedAt int64            `json:"fetchedAt"` // 0 = shown from the archive only
	Modules   []GcModuleReport `json:"modules"`
}

// GcChartResult is the detail view for one run.
type GcChartResult struct {
	Chart    gclog.Chart    `json:"chart"`
	Analysis gclog.Analysis `json:"analysis"`
}

func (a *App) gcStore() gclog.Store {
	home, _ := os.UserHomeDir()
	return gclog.Store{Root: filepath.Join(home, ".rtaskmgr", "gctrend")}
}

// GcTrendLoad shows what is archived, without touching any server — so the view
// opens instantly and still works for a host that is down or disconnected.
func (a *App) GcTrendLoad(hostIDs []string) []GcHostReport {
	st := a.gcStore()
	out := make([]GcHostReport, 0, len(hostIDs))
	for _, si := range a.serverIdents(hostIDs) {
		hr := GcHostReport{HostID: si.HostID, HostName: si.DisplayName}
		for _, key := range st.List(si.HostID) {
			arc, err := st.Load(si.HostID, key)
			if err != nil {
				continue
			}
			mr := reportFrom(arc)
			if len(arc.Samples) > 0 {
				s := arc.Samples[len(arc.Samples)-1]
				mr.PID, mr.UpSec, mr.Threads, mr.FDs = s.PID, s.UpSec, s.Threads, s.FDs
				mr.RSSMB = float64(s.RSSKB) / 1024
			}
			hr.Modules = append(hr.Modules, mr)
		}
		sortModules(hr.Modules)
		out = append(out, hr)
	}
	return out
}

// GcTrendFetch pulls the new part of every GC log on each host (concurrently), merges
// it into the archive and judges each module again.
func (a *App) GcTrendFetch(hostIDs []string) []GcHostReport {
	ids := a.serverIdents(hostIDs)
	out := make([]GcHostReport, len(ids))
	var wg sync.WaitGroup
	for i, si := range ids {
		wg.Add(1)
		go func(i int, hostID, name string) {
			defer wg.Done()
			out[i] = a.gcFetchHost(hostID, name)
		}(i, si.HostID, si.DisplayName)
	}
	wg.Wait()
	return out
}

func (a *App) gcFetchHost(hostID, hostName string) GcHostReport {
	hr := GcHostReport{HostID: hostID, HostName: hostName}
	st := a.gcStore()

	// Ask only for what is newer than the archive holds, per log path.
	since, arcSince := map[string]int64{}, map[string]int64{}
	for _, key := range st.List(hostID) {
		if arc, err := st.Load(hostID, key); err == nil && arc.GcLog != "" && arc.LastT() > 0 {
			since[arc.GcLog] = arc.LastT() / 1000
			if arc.ArcScannedTo > 0 {
				arcSince[arc.GcLog] = arc.ArcScannedTo
			}
		}
	}
	f, err := a.mgr.FetchGcLogs(hostID, since, arcSince, firstFetchDays)
	if err != nil {
		hr.Err = err.Error()
		return hr
	}

	hr.FetchedAt = f.NowMs
	for _, p := range f.Procs {
		name := moduleName(p.Args)
		key := gclog.ModuleKey(name, p.GcLog)
		arc, err := st.Load(hostID, key)
		if err != nil {
			arc = &gclog.Archive{Key: key}
		}
		arc.Key, arc.Name, arc.GcLog, arc.User = key, name, p.GcLog, p.User

		parsed := gclog.Parse(p.Text, f.NowMs-p.UpSec*1000)
		if parsed.MaxHeap > 0 {
			arc.MaxHeap = parsed.MaxHeap
		}
		added := arc.Merge(parsed.Events, f.NowMs)
		if p.Err == "" && p.GcLog != "" {
			// A minute of slack: an archive written while this fetch ran is read next time.
			arc.ArcScannedTo = f.NowMs/1000 - 60
		}
		arc.Samples = append(arc.Samples, gclog.Sample{
			T: f.NowMs, PID: p.PID, RSSKB: p.RSSKB, Threads: p.Threads, FDs: p.FDs, UpSec: p.UpSec,
		})
		if len(arc.Samples) > 2000 {
			arc.Samples = arc.Samples[len(arc.Samples)-2000:]
		}
		saveErr := st.Save(hostID, arc)

		mr := reportFrom(arc)
		mr.PID, mr.User, mr.Running = p.PID, p.User, true
		mr.UpSec, mr.Threads, mr.FDs = p.UpSec, p.Threads, p.FDs
		mr.RSSMB = float64(p.RSSKB) / 1024
		mr.Added = added
		mr.Archives, mr.ArchivesRead = p.Archives, p.ArchivesRead
		mr.Err = p.Err
		if saveErr != nil && mr.Err == "" {
			mr.Err = "로컬 보관 실패: " + saveErr.Error()
		}
		hr.Modules = append(hr.Modules, mr)
	}
	sortModules(hr.Modules)
	return hr
}

// reportFrom judges the latest run in an archive.
func reportFrom(arc *gclog.Archive) GcModuleReport {
	mr := GcModuleReport{Key: arc.Key, Name: arc.Name, User: arc.User, GcLog: arc.GcLog, Archived: len(arc.Events)}
	if mr.Name == "" {
		mr.Name = arc.Key
	}
	runs := gclog.SplitRuns(arc.Events)
	for _, r := range runs {
		mr.Runs = append(mr.Runs, gclog.Run{StartMs: r[0].T - r[0].Up, FromMs: r[0].T, ToMs: r[len(r)-1].T, Events: len(r)})
	}
	switch {
	case arc.GcLog == "":
		mr.Analysis = gclog.Analysis{Verdict: gclog.VerdictNoLog, Headline: "GC 로그를 남기지 않는 프로세스입니다 (-Xloggc 없음)"}
	case len(runs) == 0:
		mr.Analysis = gclog.Analysis{Verdict: gclog.VerdictInsufficient, Headline: "가져온 GC 기록이 없습니다"}
	default:
		mr.Analysis = gclog.Analyze(runs[len(runs)-1], arc.MaxHeap)
	}
	mr.Proc = procTrend(arc.Samples)
	return mr
}

func procTrend(ss []gclog.Sample) ProcTrend {
	if len(ss) == 0 {
		return ProcTrend{}
	}
	last := ss[len(ss)-1]
	i := len(ss) - 1
	for i > 0 && ss[i-1].PID == last.PID && ss[i-1].UpSec <= ss[i].UpSec {
		i--
	}
	first := ss[i]
	return ProcTrend{
		Samples:    len(ss) - i,
		SpanH:      float64(last.T-first.T) / 3.6e6,
		RSSFromMB:  float64(first.RSSKB) / 1024,
		ThreadFrom: first.Threads,
		FDFrom:     first.FDs,
	}
}

// GcTrendChart draws one run of one module. run < 0 means the latest.
func (a *App) GcTrendChart(hostID, key string, run int) (GcChartResult, error) {
	arc, err := a.gcStore().Load(hostID, key)
	if err != nil {
		return GcChartResult{}, err
	}
	runs := gclog.SplitRuns(arc.Events)
	if len(runs) == 0 {
		return GcChartResult{}, nil
	}
	if run < 0 || run >= len(runs) {
		run = len(runs) - 1
	}
	an := gclog.Analyze(runs[run], arc.MaxHeap)
	return GcChartResult{Chart: gclog.BuildChart(runs[run], an, 240), Analysis: an}, nil
}

// GcTrendOpenFolder reveals the local archive.
func (a *App) GcTrendOpenFolder() error {
	root := a.gcStore().Root
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	return revealInFolder(root)
}

// moduleName turns argv into the name an MK119 operator would use.
func moduleName(args []string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "-Dmodule.name="); ok && v != "" {
			return v
		}
	}
	skipNext := false
	for i, a := range args {
		if i == 0 {
			continue // the java binary
		}
		if skipNext {
			skipNext = false
			continue
		}
		switch a {
		case "-cp", "-classpath", "--class-path", "--module-path", "-p":
			skipNext = true
			continue
		case "-jar":
			if i+1 < len(args) {
				return jarName(args[i+1])
			}
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if strings.HasSuffix(a, ".jar") || strings.HasSuffix(a, ".war") {
			return jarName(a)
		}
		switch a {
		case "org.apache.catalina.startup.Bootstrap":
			return "tomcat"
		case "kafka.Kafka":
			return "kafka"
		case "org.apache.zookeeper.server.quorum.QuorumPeerMain":
			return "zookeeper"
		}
		if j := strings.LastIndex(a, "."); j >= 0 && j+1 < len(a) {
			return a[j+1:]
		}
		return a
	}
	return "java"
}

func jarName(p string) string {
	b := path.Base(strings.ReplaceAll(p, "\\", "/"))
	return strings.TrimSuffix(strings.TrimSuffix(b, ".jar"), ".war")
}

func sortModules(ms []GcModuleReport) {
	sort.SliceStable(ms, func(i, j int) bool {
		// Modules with a GC log first — those are the ones this view can judge.
		li, lj := ms[i].GcLog != "", ms[j].GcLog != ""
		if li != lj {
			return li
		}
		return ms[i].Name < ms[j].Name
	})
}

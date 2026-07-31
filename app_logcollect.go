// Wails bindings for bulk log collection (OQT-323 "두 번째: 로그 파일 복제 기능").
//
// The cluster is the unit here, not the host: the ticket asks for the files to be
// separated by hostname, and that only means anything when several servers are
// collected together. Each server stages, archives and hands over its OWN logs —
// nothing is copied between servers, because pushing gigabytes across the data
// centre network twice to build one archive would tax the very cluster being
// diagnosed. The archives are merged HERE instead, and that merge doubles as the
// verification pass.
//
// The order of the last three steps is the part worth defending: download, then
// verify what arrived against the manifest, and only then delete anything on the
// server. An interrupted transfer therefore leaves the archive on the host to retry
// from, and an archive that does not match its manifest leaves the evidence exactly
// where it is instead of destroying it.
package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"rtaskmgr/internal/monitor"
)

// LogCollectTask is one host's share of a cluster collection. The request is
// per-host because the staging filesystem is: /data may be roomy on one box and
// nearly full on the next, and the operator resolves that per server in the tree.
type LogCollectTask struct {
	HostID string             `json:"hostId"`
	Req    monitor.LogRequest `json:"req"`
}

// LogCollectHostResult is what happened to one server.
type LogCollectHostResult struct {
	HostID    string `json:"hostId"`
	Name      string `json:"name"`
	ServerDir string `json:"serverDir"`
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	Files     int    `json:"files"`
	Cleaned   bool   `json:"cleaned"`
	Err       string `json:"err,omitempty"`
	// LeftOnHost names an archive still occupying space on the server because the
	// download or the verification did not finish. It is surfaced rather than
	// swallowed: this is the way the feature could quietly eat a data partition.
	LeftOnHost string `json:"leftOnHost,omitempty"`
	CollectID  string `json:"collectId,omitempty"`
	Target     string `json:"target,omitempty"`
}

// LogCollectResult is the whole run.
type LogCollectResult struct {
	Dir    string                 `json:"dir"`
	Hosts  []LogCollectHostResult `json:"hosts"`
	OK     int                    `json:"ok"`
	Failed int                    `json:"failed"`
}

// ---- catalog ------------------------------------------------------------

func logCatalogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".rtaskmgr", "logsets.json"), nil
}

// LogCollectCatalog returns the operator's catalog, or the shipped one.
//
// It is editable on purpose: middleware moves, a new liz module appears, a site
// mounts its logs somewhere else. A path change must not require a new build.
func (a *App) LogCollectCatalog() monitor.LogCatalog {
	p, err := logCatalogPath()
	if err != nil {
		return monitor.DefaultLogCatalog()
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return monitor.DefaultLogCatalog()
	}
	var cat monitor.LogCatalog
	if err := json.Unmarshal(b, &cat); err != nil || len(cat.Categories) == 0 {
		// A corrupt or empty file must not leave the operator with nothing to collect.
		return monitor.DefaultLogCatalog()
	}
	return cat
}

// SaveLogCollectCatalog persists an edited catalog.
func (a *App) SaveLogCollectCatalog(cat monitor.LogCatalog) error {
	if len(cat.Categories) == 0 {
		return fmt.Errorf("빈 카탈로그는 저장할 수 없습니다")
	}
	p, err := logCatalogPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// ResetLogCollectCatalog restores the shipped catalog.
func (a *App) ResetLogCollectCatalog() monitor.LogCatalog {
	if p, err := logCatalogPath(); err == nil {
		_ = os.Remove(p)
	}
	return monitor.DefaultLogCatalog()
}

// ---- survey -------------------------------------------------------------

// LogCollectSurvey draws the collection tree: what each host holds, how big it is,
// and where a collection could be staged. Hosts are surveyed concurrently — a
// cluster of six that answered one after another would keep the operator waiting on
// the slowest link for no reason.
func (a *App) LogCollectSurvey(hostIDs []string, recentDays int) []monitor.LogSurvey {
	idents := a.serverIdents(hostIDs)
	fromMs := int64(0)
	if recentDays > 0 {
		start := time.Now().AddDate(0, 0, -recentDays)
		y, m, d := start.Date()
		fromMs = time.Date(y, m, d, 0, 0, 0, 0, time.Local).UnixMilli()
	}
	return a.mgr.LogSurveyCluster(idents, a.LogCollectCatalog(), fromMs)
}

// serverIdents resolves this app's label and address for each host.
func (a *App) serverIdents(hostIDs []string) []monitor.ServerIdent {
	out := make([]monitor.ServerIdent, 0, len(hostIDs))
	for _, id := range hostIDs {
		si := monitor.ServerIdent{HostID: id, DisplayName: id}
		if a.hosts != nil {
			if h, ok, _ := a.hosts.Get(id); ok {
				if h.Name != "" {
					si.DisplayName = h.Name
				}
				si.Addr = h.Addr
			}
		}
		out = append(out, si)
	}
	return out
}

// LogCollectPlan is the pre-flight for one host: the file list, the totals, and
// whether it fits on the chosen partition.
func (a *App) LogCollectPlan(hostID string, req monitor.LogRequest) (monitor.LogPlan, error) {
	return a.mgr.LogPlanFor(hostID, a.fillIdent(hostID, req), a.LogCollectCatalog())
}

func (a *App) fillIdent(hostID string, req monitor.LogRequest) monitor.LogRequest {
	if a.hosts != nil {
		if h, ok, _ := a.hosts.Get(hostID); ok {
			if req.HostName == "" {
				req.HostName = h.Name
			}
			if req.Addr == "" {
				req.Addr = h.Addr
			}
		}
	}
	if req.HostName == "" {
		req.HostName = hostID
	}
	return req
}

// ---- collection ---------------------------------------------------------

// ChooseLogCollectFolder asks where the archives should land. It is a separate call
// from the collection itself so the UI does not sit at "0%" while the operator
// browses for a folder.
func (a *App) ChooseLogCollectFolder() (string, error) {
	return wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title:            "로그 수집 저장 폴더",
		DefaultDirectory: desktopDir(),
	})
}

// CollectLogs runs the whole thing for a set of hosts and returns what each one
// produced. Hosts run concurrently: they do not share anything but this PC's disk,
// and a six-server collection that ran serially would take six times as long for no
// benefit.
func (a *App) CollectLogs(tasks []LogCollectTask, dir string) (LogCollectResult, error) {
	if len(tasks) == 0 {
		return LogCollectResult{}, fmt.Errorf("수집할 서버가 없습니다")
	}
	if dir == "" {
		return LogCollectResult{}, fmt.Errorf("저장할 폴더가 없습니다")
	}
	if a.logDL.busy() {
		return LogCollectResult{}, fmt.Errorf("이미 로그 수집이 진행 중입니다. 완료 후 다시 시도하세요")
	}
	// One host may only appear once. Two goroutines collecting the same server would
	// race for the same local filename and each verify a file the other had already
	// replaced — and the loser's archive would then be deleted from the server after
	// a verification that passed against somebody else's bytes.
	seen := map[string]bool{}
	uniq := make([]LogCollectTask, 0, len(tasks))
	for _, t := range tasks {
		if t.HostID == "" || seen[t.HostID] {
			continue
		}
		seen[t.HostID] = true
		uniq = append(uniq, t)
	}
	tasks = uniq
	if len(tasks) == 0 {
		return LogCollectResult{}, fmt.Errorf("수집할 서버가 없습니다")
	}

	// Archive directory names are assigned across the WHOLE set, once, before any
	// host starts. serverDirNames can only break a collision it can see, and called
	// per host it sees a one-element list — so two cloned boxes both answering
	// localhost.localdomain would otherwise be given the same name and merge.
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.HostID
	}
	dirs := monitor.ServerDirNames(a.serverIdents(ids))
	for i := range tasks {
		if tasks[i].Req.ServerDir == "" {
			tasks[i].Req.ServerDir = dirs[tasks[i].HostID]
		}
	}

	stamp := time.Now().Format("20060102-150405")
	outDir := filepath.Join(dir, "rtaskmgr-logs-"+stamp)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return LogCollectResult{}, fmt.Errorf("저장 폴더를 만들 수 없습니다: %w", err)
	}

	res := LogCollectResult{Dir: outDir, Hosts: make([]LogCollectHostResult, len(tasks))}
	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		go func(i int, t LogCollectTask) {
			defer wg.Done()
			res.Hosts[i] = a.collectOneHost(t, outDir, stamp)
		}(i, t)
	}
	wg.Wait()

	for _, h := range res.Hosts {
		if h.Err == "" {
			res.OK++
		} else {
			res.Failed++
		}
	}
	sort.SliceStable(res.Hosts, func(i, j int) bool { return res.Hosts[i].Name < res.Hosts[j].Name })
	return res, nil
}

// collectOneHost is stage → copy → archive → download → verify → cleanup for one
// server, reporting each transition to the UI.
func (a *App) collectOneHost(t LogCollectTask, outDir, stamp string) LogCollectHostResult {
	req := a.fillIdent(t.HostID, t.Req)
	out := LogCollectHostResult{HostID: t.HostID, Name: req.HostName}

	emit := func(stage string, done, total int64, errMsg string) {
		pct := 0.0
		if total > 0 {
			pct = float64(done) / float64(total) * 100
			if pct > 100 {
				pct = 100
			}
		}
		wruntime.EventsEmit(a.ctx, "logCollect", map[string]interface{}{
			"hostId": t.HostID, "name": out.Name, "stage": stage,
			"done": done, "total": total, "pct": pct, "err": errMsg,
		})
	}

	job, err := a.mgr.StartLogCollect(t.HostID, req, a.LogCollectCatalog(),
		func(stage string, done, total int64) { emit(stage, done, total, "") })
	out.ServerDir, out.CollectID, out.Target = job.ServerDir, job.ID, job.Target
	out.Files = job.Files
	if err != nil {
		out.Err = err.Error()
		// StartLogCollect takes its own staging tree back when it fails before the
		// archive exists; what it could not remove — or deliberately kept, as with a
		// tar that failed gzip -t — it names. Cleaning up by collection id removes
		// both the staging tree and the archive, so this one path is enough.
		out.LeftOnHost = job.Leftover
		emit(job.Stage, 0, 0, out.Err)
		return out
	}

	// Claim the local name before a byte moves. The download ends in an os.Rename,
	// which replaces whatever is there — so two servers resolving to the same
	// filename would each verify an archive the other had already overwritten, and
	// the first one to finish would then delete its own archive from the server after
	// passing a check against somebody else's bytes. Claiming with O_EXCL makes the
	// collision impossible rather than unlikely.
	local, err := claimLocalPath(outDir, "rtaskmgr-logs-"+job.ServerDir+"-"+stamp, ".tar.gz")
	if err != nil {
		out.Err = err.Error()
		out.LeftOnHost = job.Archive
		emit(monitor.LogStageDownload, 0, job.ArchiveBytes, out.Err)
		return out
	}
	key := t.HostID + "/" + job.ID
	ctx, cancel := context.WithCancel(a.ctx)
	a.logDL.add(key, cancel)
	defer func() {
		a.logDL.done(key)
		cancel()
	}()

	emit(monitor.LogStageDownload, 0, job.ArchiveBytes, "")
	n, err := a.mgr.DownloadLogArchiveTo(ctx, t.HostID, job.ID, job.Target, local,
		func(copied, total int64) { emit(monitor.LogStageDownload, copied, total, "") })
	if err != nil {
		out.Err = err.Error()
		// The archive is still on the host. That is deliberate — it is what makes a
		// failed transfer retryable instead of a collection that has to be redone.
		out.LeftOnHost = job.Archive
		emit(monitor.LogStageDownload, n, job.ArchiveBytes, out.Err)
		return out
	}
	out.Path, out.Bytes = local, n

	emit(monitor.LogStageVerify, 0, 0, "")
	if err := monitor.VerifyLogArchive(local, job.ServerDir, job.Entries); err != nil {
		out.Err = err.Error()
		out.LeftOnHost = job.Archive
		emit(monitor.LogStageVerify, 0, 0, out.Err)
		return out
	}

	emit(monitor.LogStageCleanup, 0, 0, "")
	if err := a.mgr.CleanupLogCollect(t.HostID, job.ID, job.Target); err != nil {
		// The archive downloaded and verified, so the operator has what they came
		// for; the only loss is disk on the server, which the leftovers section can
		// reclaim. Reporting it as a failed collection would be wrong.
		out.LeftOnHost = job.Archive
		emit(monitor.LogStageCleanup, 0, 0, err.Error())
		return out
	}
	out.Cleaned = true
	emit(monitor.LogStageDone, n, n, "")
	return out
}

// CancelLogCollect aborts in-flight transfers. What is already on the host stays
// there and appears in the leftovers section.
func (a *App) CancelLogCollect() { a.logDL.cancelAll() }

// ---- leftovers ----------------------------------------------------------

// LogCollectLeftovers lists collections still occupying space on the given hosts.
// This is the main way the feature could eat a data partition — a download that
// died, an app that was closed mid-collection — and it is invisible unless
// something goes looking, so it gets its own section in the UI.
func (a *App) LogCollectLeftovers(hostIDs []string) []monitor.LogLeftover {
	var mu sync.Mutex
	var all []monitor.LogLeftover
	var wg sync.WaitGroup
	for _, id := range hostIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			list, err := a.mgr.ListLogCollects(id)
			if err != nil {
				return
			}
			mu.Lock()
			all = append(all, list...)
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].HostID != all[j].HostID {
			return all[i].HostID < all[j].HostID
		}
		return all[i].MtimeMs > all[j].MtimeMs
	})
	return all
}

// DeleteLogCollectLeftover removes one collection's leftovers from a host. The
// target is derived from the path we listed, never taken from the UI.
func (a *App) DeleteLogCollectLeftover(hostID, leftoverPath string) error {
	base, id, ok := splitLeftoverPath(leftoverPath)
	if !ok {
		return fmt.Errorf("삭제할 수 없는 경로입니다: %s", leftoverPath)
	}
	return a.mgr.CleanupLogCollect(hostID, id, base)
}

// claimLocalPath reserves a filename atomically, adding -2, -3 … if it is taken.
// The file it leaves behind is empty; the download's rename replaces it.
func claimLocalPath(dir, stem, ext string) (string, error) {
	for n := 1; n <= 50; n++ {
		p := filepath.Join(dir, stem+ext)
		if n > 1 {
			p = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, n, ext))
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			return p, nil
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("저장 파일을 만들 수 없습니다: %w", err)
		}
	}
	return "", fmt.Errorf("저장 파일 이름을 정할 수 없습니다: %s", stem+ext)
}

// splitLeftoverPath turns "<base>/.rtaskmgr-logs/log-…[.tar.gz]" back into its base
// and collection id. Anything that does not have exactly that shape is refused —
// the delete is reconstructed from these two values, so this is where a path from
// outside stops being trusted.
func splitLeftoverPath(p string) (base, id string, ok bool) {
	const marker = "/.rtaskmgr-logs/"
	i := strings.LastIndex(p, marker)
	if i <= 0 {
		return "", "", false
	}
	base, id = p[:i], p[i+len(marker):]
	id = strings.TrimSuffix(id, ".tar.gz")
	if base == "" || id == "" || strings.Contains(id, "/") {
		return "", "", false
	}
	return base, id, true
}

// ---- local merge --------------------------------------------------------

// OpenLogCollectFolder reveals the downloaded archives in the OS file manager —
// the actual last step of the job, which is handing them to somebody.
func (a *App) OpenLogCollectFolder(dir string) error {
	if dir == "" {
		return fmt.Errorf("폴더가 없습니다")
	}
	return revealInFolder(dir)
}

// BundleLogCollect zips one run's archives into a single file, for when the ticket
// wants one attachment. The members are already gzip, so they are STORED rather
// than deflated: recompressing them would spend minutes to gain nothing.
func (a *App) BundleLogCollect(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("폴더가 없습니다")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var members []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tar.gz") {
			members = append(members, e.Name())
		}
	}
	if len(members) == 0 {
		return "", fmt.Errorf("묶을 아카이브가 없습니다")
	}
	sort.Strings(members)

	out := filepath.Join(dir, filepath.Base(dir)+".zip")
	tmp, err := os.CreateTemp(dir, ".rtm-zip-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	fail := func(err error) (string, error) {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	zw := zip.NewWriter(tmp)
	for _, name := range members {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			return fail(err)
		}
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return fail(err)
		}
		_, cerr := io.Copy(w, f)
		f.Close()
		if cerr != nil {
			return fail(cerr)
		}
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, out); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return out, nil
}

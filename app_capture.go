// Wails bindings for remote packet capture.
//
// This file deliberately does NOT import the (phase 2) pcap analysis package, and
// must not start to: listing, downloading, deleting, revealing in the file manager
// and handing a file to Wireshark all have to keep working on a PC where no
// analysis engine is installed. Keeping the import out lets the compiler enforce
// that instead of a comment.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"rtaskmgr/internal/monitor"
	"rtaskmgr/internal/store"
)

// capDownloads tracks in-flight downloads so the UI can cancel one and so
// beforeClose can stop them (a capture on the host keeps running — only the
// transfer is ours to abort).
type capDownloads struct {
	mu sync.Mutex
	m  map[string]context.CancelFunc
}

func (d *capDownloads) add(key string, cancel context.CancelFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.m == nil {
		d.m = map[string]context.CancelFunc{}
	}
	d.m[key] = cancel
}

func (d *capDownloads) done(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.m, key)
}

func (d *capDownloads) cancel(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.m[key]; ok {
		c()
		return true
	}
	return false
}

func (d *capDownloads) cancelAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.m {
		c()
	}
}

func (d *capDownloads) busy() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.m) > 0
}

// onCapture forwards a capture transition to the UI, adding the one thing the
// monitor cannot know: how many captures on that host have no local copy yet.
func (a *App) onCapture(ev monitor.CaptureEvent) {
	undownloaded := 0
	if a.pcapLocal != nil {
		for _, id := range ev.IDs {
			if _, ok := a.pcapLocal.Get(ev.HostID, id); !ok {
				undownloaded++
			}
		}
	}
	wruntime.EventsEmit(a.ctx, "capture", map[string]interface{}{
		"hostId": ev.HostID, "id": ev.CapID, "state": ev.State, "status": ev.Status,
		"reason": ev.Reason, "msg": ev.Msg, "running": ev.Running, "total": ev.Total,
		"undownloaded": undownloaded,
	})
}

// ---- capture control ----------------------------------------------------

// CaptureEnv returns the modal's pre-flight for one host: interfaces, storage
// targets, tcpdump availability and what is already captured there.
func (a *App) CaptureEnv(hostID string) (monitor.CapEnv, error) {
	return a.mgr.CaptureEnv(hostID)
}

// StartCapture launches a detached capture. The operator identity is stamped so a
// shared login still shows who started it.
func (a *App) StartCapture(hostID string, req monitor.CapRequest) (monitor.CapMeta, error) {
	name := hostID
	if a.hosts != nil {
		if h, ok, _ := a.hosts.Get(hostID); ok && h.Name != "" {
			name = h.Name
		}
	}
	return a.mgr.StartCapture(hostID, req, name, localOperator())
}

// ListCaptures returns the host's captures with live status, plus the local path
// for any we have already downloaded.
func (a *App) ListCaptures(hostID string) ([]monitor.CapMeta, error) {
	list, err := a.mgr.ListCaptures(hostID)
	if err != nil {
		return nil, err
	}
	if a.pcapLocal != nil {
		for i := range list {
			if e, ok := a.pcapLocal.Get(hostID, list[i].ID); ok {
				list[i].LocalPath = e.Path
			}
		}
	}
	return list, nil
}

// StopCapture drops the stop sentinel. It returns quickly: a capture that needs
// longer to flush comes back as "stopping" and the watcher reports the end.
func (a *App) StopCapture(hostID, id string) (monitor.CapMeta, error) {
	return a.mgr.StopCapture(hostID, id)
}

// StopCaptureForce signals the capture's own recorded PIDs. Only for a capture
// that will not stop on its own.
func (a *App) StopCaptureForce(hostID, id string) (monitor.CapMeta, error) {
	return a.mgr.StopCaptureForce(hostID, id)
}

// DeleteCapture removes a capture from the host (the local copy, if any, is left
// alone — only the ledger entry goes).
func (a *App) DeleteCapture(hostID, id string) error {
	if err := a.mgr.DeleteCapture(hostID, id); err != nil {
		return err
	}
	if a.pcapLocal != nil {
		_ = a.pcapLocal.Forget(hostID, id)
	}
	return nil
}

// ForgetCapture stops tracking a capture whose real state cannot be determined
// (a hidepid host, or a stale "orphan" marker) WITHOUT deleting its .pcap. It is
// the only way out of a row that would otherwise report "capturing" forever and
// block every future capture on that host.
func (a *App) ForgetCapture(hostID, id string) error {
	if err := a.mgr.ForgetCapture(hostID, id); err != nil {
		return err
	}
	if a.pcapLocal != nil {
		_ = a.pcapLocal.Forget(hostID, id)
	}
	return nil
}

// CaptureSHA256 hashes the capture on the host (on demand; the routine integrity
// check is the gzip trailer verified during download).
func (a *App) CaptureSHA256(hostID, id string) (string, error) {
	return a.mgr.CaptureSHA256(hostID, id)
}

// ---- download -----------------------------------------------------------

// ChooseCaptureSavePath opens the native Save As dialog and returns the chosen
// path ("" if cancelled). It is a SEPARATE binding from DownloadCapture on
// purpose: bundled together, the UI would sit at "downloading 0%" for as long as
// the operator takes to pick a folder.
func (a *App) ChooseCaptureSavePath(hostID, capID, iface string, running bool) (string, error) {
	name := hostID
	if a.hosts != nil {
		if h, ok, _ := a.hosts.Get(hostID); ok && h.Name != "" {
			name = h.Name
		}
	}
	return wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:            "패킷 캡쳐 저장 위치",
		DefaultDirectory: desktopDir(),
		DefaultFilename:  defaultPcapName(name, iface, running),
		Filters:          []wruntime.FileFilter{{DisplayName: "패킷 캡쳐 (*.pcap)", Pattern: "*.pcap"}},
	})
}

// safeFileChars strips anything that would be awkward in a filename; the host's
// own basename is never reused.
var safeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// defaultPcapName follows the log-naming convention already used for recordings.
// A snapshot of a still-running capture is marked so a truncated file is never
// mistaken for a complete one.
func defaultPcapName(hostName, iface string, running bool) string {
	h := safeFileChars.ReplaceAllString(hostName, "_")
	if h == "" {
		h = "host"
	}
	i := safeFileChars.ReplaceAllString(iface, "_")
	if i == "" {
		i = "any"
	}
	suffix := ""
	if running {
		suffix = "-partial"
	}
	return fmt.Sprintf("rtaskmgr-%s-%s-%s%s.pcap", h, i, time.Now().Format("20060102-150405"), suffix)
}

// DownloadCapture streams one capture to localPath, emitting "pcapDl" progress
// events. Progress is reported at the app level rather than inside the modal so
// the operator can close the dialog and keep monitoring while gigabytes move.
func (a *App) DownloadCapture(hostID, capID, localPath string) (string, error) {
	if localPath == "" {
		return "", fmt.Errorf("저장할 파일 경로가 없습니다")
	}
	key := hostID + "/" + capID
	if a.capDL.busy() {
		return "", fmt.Errorf("이미 다운로드가 진행 중입니다. 완료 후 다시 시도하세요")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.capDL.add(key, cancel)
	defer func() {
		a.capDL.done(key)
		cancel()
	}()

	emit := func(copied, total int64, done bool, errMsg string) {
		pct := 0.0
		if total > 0 {
			pct = float64(copied) / float64(total) * 100
			if pct > 100 {
				pct = 100
			}
		}
		wruntime.EventsEmit(a.ctx, "pcapDl", map[string]interface{}{
			"hostId": hostID, "id": capID, "copied": copied, "total": total,
			"pct": pct, "done": done, "err": errMsg,
		})
	}
	started := time.Now()
	emit(0, 0, false, "")

	n, err := a.mgr.DownloadCaptureTo(ctx, hostID, capID, localPath, func(copied, total int64) {
		emit(copied, total, false, "")
	})
	if err != nil {
		emit(n, n, true, err.Error())
		return "", err
	}
	emit(n, n, true, "")
	_ = started // kept for future rate reporting; the UI derives its own

	if a.pcapLocal != nil {
		name := hostID
		if a.hosts != nil {
			if h, ok, _ := a.hosts.Get(hostID); ok && h.Name != "" {
				name = h.Name
			}
		}
		_ = a.pcapLocal.Put(store.PcapEntry{
			HostID: hostID, HostName: name, CapID: capID, Path: localPath, Bytes: n,
		})
	}
	return localPath, nil
}

// CancelCaptureDownload aborts an in-flight transfer. The capture on the host is
// untouched.
func (a *App) CancelCaptureDownload(hostID, capID string) {
	a.capDL.cancel(hostID + "/" + capID)
}

// A download deliberately produces ONE file. An earlier revision also wrote a
// "<pcap>.rtmeta.json" sidecar (host, NIC, filter, stop reason, drop counters) as
// context to hand to the development team, but in practice the operator is the one
// forwarding the file and does not want a second attachment to explain. The
// capture list already shows all of that while the capture exists on the host, and
// the analysis view is required to open a pcap that arrived with no metadata at
// all — so nothing depends on the sidecar. Removing it also drops one SSH round
// trip per download.

// ---- local file actions -------------------------------------------------

// OpenCaptureFolder reveals a downloaded capture in the OS file manager. This is
// the main path for the actual job: handing the pcap to the development team.
func (a *App) OpenCaptureFolder(hostID, capID string) error {
	e, ok := a.localCapture(hostID, capID)
	if !ok {
		return fmt.Errorf("이 PC에 내려받은 파일이 없습니다. 먼저 다운로드하세요")
	}
	return revealInFolder(e.Path)
}

// OpenInWireshark hands a downloaded capture to a locally installed Wireshark.
// Phase 1 only looks in the standard install locations; the full detection (with
// the "blocked by security software" distinction) arrives with the analysis view.
func (a *App) OpenInWireshark(hostID, capID string) error {
	e, ok := a.localCapture(hostID, capID)
	if !ok {
		return fmt.Errorf("이 PC에 내려받은 파일이 없습니다. 먼저 다운로드하세요")
	}
	return openWithWireshark(e.Path)
}

// LocalOperator identifies this PC's operator the same way StartCapture stamps
// it, so the UI can tell "started by me" from "started by a colleague" on a
// shared login without guessing.
func (a *App) LocalOperator() string { return localOperator() }

// LocalCapturePath returns the local file for a capture, or "" if we don't have
// it (the UI uses this to decide which row actions to show).
func (a *App) LocalCapturePath(hostID, capID string) string {
	if e, ok := a.localCapture(hostID, capID); ok {
		return e.Path
	}
	return ""
}

func (a *App) localCapture(hostID, capID string) (store.PcapEntry, bool) {
	if a.pcapLocal == nil {
		return store.PcapEntry{}, false
	}
	return a.pcapLocal.Get(hostID, capID)
}

// localOperator identifies who pressed start, for captures run from a shared
// login ("logan.lee@DESK-07").
func localOperator() string {
	user := os.Getenv("USERNAME")
	if user == "" {
		user = os.Getenv("USER")
	}
	hostname, _ := os.Hostname()
	switch {
	case user != "" && hostname != "":
		return user + "@" + hostname
	case user != "":
		return user
	case hostname != "":
		return hostname
	}
	return "unknown"
}

// hasWiresharkDir reports whether dir looks like a Wireshark install and returns
// the executable path.
func wiresharkIn(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	exe := filepath.Join(dir, "Wireshark", wiresharkExe)
	if fi, err := os.Stat(exe); err == nil && !fi.IsDir() {
		return exe, true
	}
	return "", false
}

// pathHasNoQuotes guards the one place we build a raw command line by hand.
func pathHasNoQuotes(p string) bool {
	return !strings.ContainsAny(p, "\"\x00")
}

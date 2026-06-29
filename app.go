package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"rtaskmgr/internal/host"
	"rtaskmgr/internal/monitor"
	"rtaskmgr/internal/record"
)

type App struct {
	ctx   context.Context
	hosts *host.Store
	mgr   *monitor.Manager
	rec   *record.Recorder
	rpmFS fs.FS

	logMu     sync.Mutex
	logFrames map[string][]monitor.Frame // currently opened log: hostId -> frames in order
}

// LogHostInfo summarizes one host's track within an opened log.
type LogHostInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Frames int    `json:"frames"`
	StartT int64  `json:"startT"`
	EndT   int64  `json:"endT"`
}

// LogMeta describes an opened .ndjson capture for the playback UI.
type LogMeta struct {
	Path  string        `json:"path"`
	Hosts []LogHostInfo `json:"hosts"`
}

func NewApp(rpmFS fs.FS) *App {
	return &App{rpmFS: rpmFS}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	hs, err := host.New()
	if err != nil {
		fmt.Println("host store init failed:", err)
	}
	a.hosts = hs

	rec, err := record.New()
	if err != nil {
		fmt.Println("recorder init failed:", err)
	}
	a.rec = rec

	a.mgr = monitor.NewManager(a.onFrame, a.onStatus, a.onNethogs, a.rpmFS)
}

// onFrame is invoked by the monitor for every 1s frame: persist it and push it
// to the UI.
func (a *App) onFrame(f monitor.Frame) {
	if a.rec != nil {
		a.rec.Feed(f)
	}
	wruntime.EventsEmit(a.ctx, "frame", f)
}

func (a *App) onStatus(hostID, state, detail string) {
	// Safety: if the host we're recording (immediate, client-side) drops, stop
	// the recording so nothing keeps writing to a half-dead stream.
	if (state == "stopped" || state == "error") && a.rec != nil &&
		a.rec.IsRecording() && a.rec.RecordingHost() == hostID {
		path := a.rec.StopFile()
		a.emitRecording(false, "", path, true)
	}
	wruntime.EventsEmit(a.ctx, "status", map[string]string{
		"hostId": hostID, "state": state, "detail": detail,
	})
}

func (a *App) emitRecording(active bool, hostID, path string, auto bool) {
	wruntime.EventsEmit(a.ctx, "recording", map[string]interface{}{
		"active": active, "hostId": hostID, "path": path, "auto": auto,
	})
}

// onNethogs forwards per-host network-collection state to the UI so the
// install/rollback button can reflect it.
func (a *App) onNethogs(hostID string, active, installedByUs bool, msg string) {
	wruntime.EventsEmit(a.ctx, "nethogs", map[string]interface{}{
		"hostId": hostID, "active": active, "installedByUs": installedByUs, "msg": msg,
	})
}

// ---- Host CRUD ----

func (a *App) ListHosts() ([]host.Host, error) {
	if a.hosts == nil {
		return nil, fmt.Errorf("host store unavailable")
	}
	return a.hosts.List()
}

func (a *App) SaveHost(h host.Host) (host.Host, error) {
	if a.hosts == nil {
		return h, fmt.Errorf("host store unavailable")
	}
	return a.hosts.Save(h)
}

func (a *App) DeleteHost(id string) error {
	if a.hosts == nil {
		return fmt.Errorf("host store unavailable")
	}
	a.mgr.Stop(id)
	return a.hosts.Delete(id)
}

// ---- Monitoring ----

// Connect dials the host, probes it, uploads the sampler and starts streaming.
// Frames thereafter arrive via the "frame" event. Returns probed capabilities.
func (a *App) Connect(id string, intervalSec int) (monitor.Capabilities, error) {
	h, ok, err := a.hosts.Get(id)
	if err != nil {
		return monitor.Capabilities{}, err
	}
	if !ok {
		return monitor.Capabilities{}, fmt.Errorf("host %s not found", id)
	}
	return a.mgr.Start(a.ctx, h, intervalSec)
}

func (a *App) Disconnect(id string) {
	a.mgr.Stop(id)
}

// SetInterval changes the live refresh interval (seconds, 1–60) for a connected
// host, restarting just the sampler stream.
func (a *App) SetInterval(id string, intervalSec int) error {
	return a.mgr.SetInterval(id, intervalSec)
}

// NethogsInstall (manual button): install nethogs offline from the embedded
// bundle if needed, then start the per-process network stream. The "nethogs"
// event reports progress/state to the UI.
func (a *App) NethogsInstall(id string) error {
	return a.mgr.InstallNethogs(id)
}

// NethogsRollback (manual button): stop the network stream and, if this app
// installed nethogs, undo the dnf transaction to restore prior dependencies.
func (a *App) NethogsRollback(id string) error {
	return a.mgr.RollbackNethogs(id)
}

// ProcessHistory returns the in-memory 1s timeline for one process (detail view).
func (a *App) ProcessHistory(hostID string, pid int) []record.Point {
	if a.rec == nil {
		return nil
	}
	return a.rec.History(hostID, pid)
}

// desktopDir returns the current OS user's Desktop, falling back to the home
// directory. Works the same on Windows (C:\Users\<u>\Desktop) and macOS
// (/Users/<u>/Desktop).
func desktopDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	d := filepath.Join(home, "Desktop")
	if fi, err := os.Stat(d); err == nil && fi.IsDir() {
		return d
	}
	return home
}

func defaultLogName() string {
	return "rtaskmgr-log-" + time.Now().Format("20060102-150405") + ".ndjson"
}

// ---- Immediate recording (client-side, explicit) ----

func (a *App) IsRecording() bool { return a.rec != nil && a.rec.IsRecording() }

// StartRecording opens a native Save As (Desktop default) and begins writing the
// given host's frames there. Recording is explicit — nothing records until this
// is called. Returns the chosen path, or "" if cancelled.
func (a *App) StartRecording(hostID string) (string, error) {
	if a.rec == nil {
		return "", fmt.Errorf("recorder unavailable")
	}
	if hostID == "" {
		return "", fmt.Errorf("기록할 호스트를 먼저 선택하세요")
	}
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:            "실시간 기록 저장 위치",
		DefaultDirectory: desktopDir(),
		DefaultFilename:  defaultLogName(),
		Filters:          []wruntime.FileFilter{{DisplayName: "rtaskmgr 로그 (*.ndjson)", Pattern: "*.ndjson"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	if err := a.rec.StartFile(hostID, path); err != nil {
		return "", err
	}
	a.emitRecording(true, hostID, path, false)
	return path, nil
}

// StopRecording finalizes the active immediate recording.
func (a *App) StopRecording() string {
	if a.rec == nil {
		return ""
	}
	path := a.rec.StopFile()
	a.emitRecording(false, "", path, false)
	return path
}

// ---- Scheduled recording (server-side, detached) ----

func (a *App) StartScheduled(hostID string, durationSec int) (monitor.RecMeta, error) {
	name := hostID
	if a.hosts != nil {
		if h, ok, _ := a.hosts.Get(hostID); ok && h.Name != "" {
			name = h.Name
		}
	}
	return a.mgr.StartScheduled(hostID, durationSec, name)
}

func (a *App) ListScheduled(hostID string) ([]monitor.RecMeta, error) {
	return a.mgr.ListScheduled(hostID)
}

func (a *App) StopScheduled(hostID, id string) error {
	return a.mgr.StopScheduled(hostID, id)
}

func (a *App) DeleteScheduled(hostID, id string) error {
	return a.mgr.DeleteScheduled(hostID, id)
}

// DownloadScheduledAndPlay fetches a server-side recording, decompresses it, and
// loads it into the playback buffer — returns LogMeta so the UI can open it like
// a local log.
func (a *App) DownloadScheduledAndPlay(hostID, id string) (LogMeta, error) {
	frames, err := a.mgr.DownloadScheduled(hostID, id)
	if err != nil {
		return LogMeta{}, err
	}
	if len(frames) == 0 {
		return LogMeta{}, fmt.Errorf("기록에 프레임이 없습니다 (아직 데이터가 쌓이지 않았을 수 있음)")
	}
	byHost := map[string][]monitor.Frame{}
	for _, f := range frames {
		byHost[f.HostID] = append(byHost[f.HostID], f)
	}
	a.logMu.Lock()
	a.logFrames = byHost
	a.logMu.Unlock()

	meta := LogMeta{Path: id}
	for hid, fr := range byHost {
		name := hid
		if a.hosts != nil {
			if h, ok, _ := a.hosts.Get(hid); ok && h.Name != "" {
				name = h.Name
			}
		}
		meta.Hosts = append(meta.Hosts, LogHostInfo{
			ID: hid, Name: name, Frames: len(fr), StartT: fr[0].T, EndT: fr[len(fr)-1].T,
		})
	}
	return meta, nil
}

// ---- Log playback (reader) ----

// OpenLogDialog shows a native open dialog (defaulting to the Desktop), parses
// the selected .ndjson capture, and returns its per-host summary for the
// playback UI. Frames are held in memory and served via LogFrames.
func (a *App) OpenLogDialog() (LogMeta, error) {
	path, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title:            "기록 로그 열기",
		DefaultDirectory: desktopDir(),
		Filters:          []wruntime.FileFilter{{DisplayName: "rtaskmgr 로그 (*.ndjson)", Pattern: "*.ndjson"}},
	})
	if err != nil || path == "" {
		return LogMeta{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return LogMeta{}, fmt.Errorf("로그 읽기 실패: %w", err)
	}

	byHost := map[string][]monitor.Frame{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var f monitor.Frame
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			continue
		}
		byHost[f.HostID] = append(byHost[f.HostID], f)
	}
	if len(byHost) == 0 {
		return LogMeta{}, fmt.Errorf("유효한 프레임이 없습니다 (.ndjson 형식 확인)")
	}

	a.logMu.Lock()
	a.logFrames = byHost
	a.logMu.Unlock()

	meta := LogMeta{Path: path}
	for id, frames := range byHost {
		name := id
		if a.hosts != nil {
			if h, ok, _ := a.hosts.Get(id); ok && h.Name != "" {
				name = h.Name
			}
		}
		meta.Hosts = append(meta.Hosts, LogHostInfo{
			ID: id, Name: name, Frames: len(frames),
			StartT: frames[0].T, EndT: frames[len(frames)-1].T,
		})
	}
	return meta, nil
}

// LogFrames returns all frames for one host in the opened log (the playback UI
// scrubs through them in memory).
func (a *App) LogFrames(hostID string) []monitor.Frame {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	return a.logFrames[hostID]
}

// beforeClose finalizes any active immediate recording (the file the user chose
// is already real, so just close it cleanly) and tears down sessions. Scheduled
// server-side recordings intentionally keep running on the host. Returns false
// to allow the window to close.
//
// Note: immediate recording is client-side; closing the app stops it. Scheduled
// recordings survive on the server until their deadline.
func (a *App) beforeClose(ctx context.Context) bool {
	if a.rec != nil && a.rec.IsRecording() {
		a.rec.StopFile()
	}
	a.mgr.StopAll()
	return false
}

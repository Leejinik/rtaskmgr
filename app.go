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

	mu        sync.Mutex
	allowQuit bool // set once the user has resolved the on-exit save prompt

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
		a.rec.Record(f)
	}
	wruntime.EventsEmit(a.ctx, "frame", f)
}

func (a *App) onStatus(hostID, state, detail string) {
	wruntime.EventsEmit(a.ctx, "status", map[string]string{
		"hostId": hostID, "state": state, "detail": detail,
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
func (a *App) Connect(id string) (monitor.Capabilities, error) {
	h, ok, err := a.hosts.Get(id)
	if err != nil {
		return monitor.Capabilities{}, err
	}
	if !ok {
		return monitor.Capabilities{}, fmt.Errorf("host %s not found", id)
	}
	return a.mgr.Start(a.ctx, h)
}

func (a *App) Disconnect(id string) {
	a.mgr.Stop(id)
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

// ---- Logging ----

func (a *App) IsLogNamed() bool {
	return a.rec != nil && a.rec.IsNamed()
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

// defaultLogName suggests a timestamped filename for the save dialog.
func defaultLogName() string {
	return "rtaskmgr-log-" + time.Now().Format("20060102-150405") + ".ndjson"
}

// SaveLogDialog is the Ctrl+S path: it opens a native Save As dialog defaulting
// to the user's Desktop, then writes (and keeps appending) the capture there.
// Returns the chosen absolute path, or "" if the user cancelled.
func (a *App) SaveLogDialog() (string, error) {
	if a.rec == nil {
		return "", fmt.Errorf("recorder unavailable")
	}
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:            "기록 저장 위치 선택",
		DefaultDirectory: desktopDir(),
		DefaultFilename:  defaultLogName(),
		Filters:          []wruntime.FileFilter{{DisplayName: "rtaskmgr 로그 (*.ndjson)", Pattern: "*.ndjson"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	return a.rec.SaveAs(path)
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

// beforeClose handles the unsaved-capture prompt entirely with native dialogs:
// a save/don't-save/cancel question, and (on save) a Save As dialog defaulting
// to the Desktop. Returns true to keep the window open.
func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	allow := a.allowQuit
	a.mu.Unlock()
	if allow {
		a.mgr.StopAll()
		return false
	}
	if a.rec == nil || a.rec.IsNamed() || !a.rec.HasData() {
		if a.rec != nil && !a.rec.IsNamed() {
			_ = a.rec.Discard()
		}
		a.mgr.StopAll()
		return false
	}

	choice, err := wruntime.MessageDialog(ctx, wruntime.MessageDialogOptions{
		Type:          wruntime.QuestionDialog,
		Title:         "기록 저장",
		Message:       "이번 세션의 1초 단위 기록을 저장할까요?\n저장하지 않으면 삭제됩니다.",
		Buttons:       []string{"저장", "저장 안 함", "취소"},
		DefaultButton: "저장",
		CancelButton:  "취소",
	})
	if err != nil {
		return true // couldn't ask — stay open rather than lose data
	}
	switch choice {
	case "저장", "Yes":
		path, perr := a.SaveLogDialog()
		if perr != nil || path == "" {
			return true // save cancelled/failed — keep the window open
		}
		a.allowQuitNow()
		return false
	case "저장 안 함", "No":
		_ = a.rec.Discard()
		a.allowQuitNow()
		return false
	default: // "취소" / closed
		return true
	}
}

func (a *App) allowQuitNow() {
	a.mu.Lock()
	a.allowQuit = true
	a.mu.Unlock()
	a.mgr.StopAll()
}

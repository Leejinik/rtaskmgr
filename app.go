package main

import (
	"context"
	"fmt"
	"sync"

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

	mu        sync.Mutex
	allowQuit bool // set once the user has resolved the on-exit save prompt
}

func NewApp() *App {
	return &App{}
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

	a.mgr = monitor.NewManager(a.onFrame, a.onStatus)
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

// ProcessHistory returns the in-memory 1s timeline for one process (detail view).
func (a *App) ProcessHistory(hostID string, pid int) []record.Point {
	if a.rec == nil {
		return nil
	}
	return a.rec.History(hostID, pid)
}

// ---- Logging ----

// SaveLog is the Ctrl+S path: name the capture and keep appending to it.
func (a *App) SaveLog(name string) (string, error) {
	if a.rec == nil {
		return "", fmt.Errorf("recorder unavailable")
	}
	return a.rec.SaveAs(name)
}

func (a *App) IsLogNamed() bool {
	return a.rec != nil && a.rec.IsNamed()
}

// KeepLogAndQuit (exit dialog → "save"): name the capture, then quit.
func (a *App) KeepLogAndQuit(name string) error {
	if a.rec != nil {
		if _, err := a.rec.SaveAs(name); err != nil {
			return err
		}
	}
	a.finishQuit()
	return nil
}

// DiscardAndQuit (exit dialog → "don't save"): delete the temp capture, quit.
func (a *App) DiscardAndQuit() error {
	if a.rec != nil {
		_ = a.rec.Discard()
	}
	a.finishQuit()
	return nil
}

func (a *App) finishQuit() {
	a.mu.Lock()
	a.allowQuit = true
	a.mu.Unlock()
	a.mgr.StopAll()
	wruntime.Quit(a.ctx)
}

// beforeClose blocks the first close so the UI can ask whether to keep an
// unsaved capture. If the session is already named (or the user already
// resolved the prompt), the close proceeds.
func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	allow := a.allowQuit
	a.mu.Unlock()
	if allow {
		a.mgr.StopAll()
		return false
	}
	if a.rec == nil || a.rec.IsNamed() || !a.rec.HasData() {
		// Nothing to lose: already saved, or an empty session. Discard the
		// empty temp file and let the window close.
		if a.rec != nil && !a.rec.IsNamed() {
			_ = a.rec.Discard()
		}
		a.mgr.StopAll()
		return false
	}
	// Unsaved capture exists: ask the frontend, prevent close for now.
	wruntime.EventsEmit(ctx, "confirm-save", nil)
	return true
}

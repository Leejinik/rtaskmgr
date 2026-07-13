package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"rtaskmgr/internal/pwledger"
	"rtaskmgr/internal/record"
	"rtaskmgr/internal/updater"
)

type App struct {
	ctx   context.Context
	hosts *host.Store
	mgr   *monitor.Manager
	rec   *record.Recorder
	led   *pwledger.Store
	rpmFS fs.FS

	// version is set from main() after NewApp(); the updater is built in
	// startup() once the config dir is known. Empty version → "dev" → updater off.
	version string
	updater *updater.Updater

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

	led, err := pwledger.New()
	if err != nil {
		fmt.Println("pwledger init failed:", err)
	}
	a.led = led

	a.mgr = monitor.NewManager(a.onFrame, a.onStatus, a.onNethogs, a.rpmFS)

	// Auto-updater: default a "dev" version, build from the app-specific config
	// dir, and sweep any leftover swap files from a previous update.
	if a.version == "" {
		a.version = "dev"
	}
	home, _ := os.UserHomeDir()
	cfgDir := filepath.Join(home, ".rtaskmgr")
	a.updater = updater.New(updater.Config{
		Owner:          "Leejinik",
		Repo:           "rtaskmgr",
		AssetName:      "rtaskmgr.exe",
		CurrentVersion: a.version,
		ConfigDir:      cfgDir,
	})
	if exe, err := os.Executable(); err == nil {
		a.updater.CleanupLeftovers(exe)
	}
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
	caps, err := a.mgr.Start(a.ctx, h, intervalSec)
	if err == nil {
		go a.refreshPwStatus(id)
	}
	return caps, err
}

func (a *App) Disconnect(id string) {
	a.mgr.Stop(id)
}

// ---- Cluster (multi-host) ----

// SaveHosts upserts a batch of hosts in one call and returns the stored records.
// Used when registering a cluster: the caller assigns a shared ClusterID/Name to
// each host before saving.
func (a *App) SaveHosts(hosts []host.Host) ([]host.Host, error) {
	if a.hosts == nil {
		return nil, fmt.Errorf("host store unavailable")
	}
	out := make([]host.Host, 0, len(hosts))
	for _, h := range hosts {
		saved, err := a.hosts.Save(h)
		if err != nil {
			return out, err
		}
		out = append(out, saved)
	}
	return out, nil
}

// ClusterConnectResult reports the outcome of connecting one host in a batch.
type ClusterConnectResult struct {
	HostID string               `json:"hostId"`
	Caps   monitor.Capabilities `json:"caps"`
	Err    string               `json:"err"`
}

// ---- Managed-account password management (liz / root) ----

// pwRecorder journals rotation progress into the ledger and mirrors each step to
// the UI via the "pwrotate" event. One instance is created per rotation call.
type pwRecorder struct {
	app      *App
	hostID   string
	hostName string
	addr     string
}

func (r *pwRecorder) Begin(account, op, step, password string) string {
	var id string
	if r.app.led != nil {
		id, _ = r.app.led.Append(pwledger.Entry{
			HostID: r.hostID, HostName: r.hostName, Addr: r.addr,
			Account: account, Op: op, Step: step, Password: password, Status: "pending",
		})
	}
	wruntime.EventsEmit(r.app.ctx, "pwrotate", map[string]interface{}{
		"id": id, "hostId": r.hostID, "account": account,
		"op": op, "step": step, "status": "pending",
	})
	return id
}

func (r *pwRecorder) Done(id, status, errMsg string) {
	if r.app.led != nil {
		_ = r.app.led.SetStatus(id, status, errMsg)
	}
	wruntime.EventsEmit(r.app.ctx, "pwrotate", map[string]interface{}{
		"id": id, "hostId": r.hostID, "status": status, "err": errMsg,
	})
}

// refreshPwStatus reads liz/root expiry over the live session, caches it on the
// host record (so the sidebar/hover show it without reconnecting) and emits a
// "pwstatus" event. Safe to call in a goroutine; failures are reported, not fatal.
func (a *App) refreshPwStatus(id string) {
	st, err := a.mgr.PasswordStatus(id)
	if err != nil {
		wruntime.EventsEmit(a.ctx, "pwstatus", map[string]interface{}{
			"hostId": id, "err": err.Error(),
		})
		return
	}
	if a.hosts != nil {
		_ = a.hosts.UpdateExpiry(id, st.LizExpDays, st.RootExpDays, time.Now().UTC())
	}
	wruntime.EventsEmit(a.ctx, "pwstatus", map[string]interface{}{
		"hostId": id, "hasLiz": st.HasLiz, "hasRoot": st.HasRoot,
		"lizExpDays": st.LizExpDays, "rootExpDays": st.RootExpDays, "todayDays": st.TodayDays,
	})
}

// PasswordStatus reads liz/root expiry on demand (requires a live session).
func (a *App) PasswordStatus(id string) (monitor.PwStatus, error) {
	return a.mgr.PasswordStatus(id)
}

// RenewPasswords refreshes the expiry date on liz+root without changing the
// effective password (current -> temp -> current). Requires a live session.
func (a *App) RenewPasswords(id string) error {
	h, ok, err := a.hosts.Get(id)
	if err != nil || !ok {
		return fmt.Errorf("host %s not found", id)
	}
	cfg := pwledger.Config{TempPassword: pwledger.DefaultTempPassword}
	if a.led != nil {
		if c, e := a.led.Config(); e == nil {
			cfg = c
		}
	}
	rec := &pwRecorder{app: a, hostID: id, hostName: h.Name, addr: h.Addr}
	err = a.mgr.RenewPasswords(id, cfg.TempPassword, rec)
	// Sync the stored login credential to whatever the login account actually
	// ended on — even on a mid-way failure — so reconnects keep working.
	a.reconcileLoginPassword(id, h.User)
	a.refreshPwStatus(id)
	return err
}

// reconcileLoginPassword sets the host's stored password to the most recent
// successfully-applied password for the login account, read from the ledger.
// This keeps hosts.json in sync with reality after any rotation, including one
// that failed partway (e.g. the renew restore step never landed).
func (a *App) reconcileLoginPassword(id, user string) {
	if a.hosts == nil || a.led == nil || (user != "liz" && user != "root") {
		return
	}
	entries, err := a.led.Entries(id) // newest first
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Account == user && e.Status == "ok" {
			_ = a.hosts.UpdatePassword(id, e.Password)
			return
		}
	}
}

// ChangePasswords sets a new password on BOTH liz and root (current -> new) and,
// on success, persists it as the host's login password so reconnects work.
func (a *App) ChangePasswords(id, newPassword string) error {
	h, ok, err := a.hosts.Get(id)
	if err != nil || !ok {
		return fmt.Errorf("host %s not found", id)
	}
	rec := &pwRecorder{app: a, hostID: id, hostName: h.Name, addr: h.Addr}
	err = a.mgr.ChangePasswords(id, newPassword, rec)
	// Sync the stored login credential to what actually landed (the new password
	// on success, or the last account state reached on a mid-way failure).
	a.reconcileLoginPassword(id, h.User)
	a.refreshPwStatus(id)
	return err
}

// PwLedger returns the rotation history, newest first. Empty hostID = all hosts.
func (a *App) PwLedger(hostID string) ([]pwledger.Entry, error) {
	if a.led == nil {
		return nil, fmt.Errorf("ledger unavailable")
	}
	return a.led.Entries(hostID)
}

// PwConfig returns the password-manager config (temp password, warn-days).
func (a *App) PwConfig() (pwledger.Config, error) {
	if a.led == nil {
		return pwledger.Config{TempPassword: pwledger.DefaultTempPassword, ExpiryWarnDays: pwledger.DefaultWarnDays}, nil
	}
	return a.led.Config()
}

// SetPwConfig updates the password-manager config.
func (a *App) SetPwConfig(c pwledger.Config) error {
	if a.led == nil {
		return fmt.Errorf("ledger unavailable")
	}
	return a.led.SetConfig(c)
}

// ConnectMany dials, probes and starts streaming for several hosts concurrently
// (9 sequential SSH dials would be slow). Each host reuses the same per-host
// session machinery as Connect; frames/status arrive via the usual events.
func (a *App) ConnectMany(ids []string, intervalSec int) []ClusterConnectResult {
	results := make([]ClusterConnectResult, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			r := ClusterConnectResult{HostID: id}
			h, ok, err := a.hosts.Get(id)
			if err != nil {
				r.Err = err.Error()
			} else if !ok {
				r.Err = fmt.Sprintf("host %s not found", id)
			} else if caps, err := a.mgr.Start(a.ctx, h, intervalSec); err != nil {
				r.Err = err.Error()
			} else {
				r.Caps = caps
				go a.refreshPwStatus(id)
			}
			results[i] = r
		}(i, id)
	}
	wg.Wait()
	return results
}

// DisconnectMany stops streaming for several hosts at once.
func (a *App) DisconnectMany(ids []string) {
	for _, id := range ids {
		a.mgr.Stop(id)
	}
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

// KillProcess ends a process on the connected host. force=true sends SIGKILL
// instead of the default graceful SIGTERM. The confirmation prompt lives in the
// UI — by the time this is called the operator has already approved.
func (a *App) KillProcess(hostID string, pid int, force bool) error {
	return a.mgr.KillProcess(hostID, pid, force)
}

// ServiceAction runs systemctl stop/restart for a process's systemd .service
// unit. action is "stop" or "restart". The confirmation prompt lives in the UI.
func (a *App) ServiceAction(hostID, unit, action string) error {
	return a.mgr.ServiceAction(hostID, unit, action)
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

// EstimateScheduled returns candidate recording filesystems (with free space)
// and a measured projection of daily disk use for this host.
func (a *App) EstimateScheduled(hostID string) (monitor.RecEstimate, error) {
	return a.mgr.EstimateScheduled(hostID)
}

func (a *App) StartScheduled(hostID string, durationSec, intervalSec int, targetDir string) (monitor.RecMeta, error) {
	name := hostID
	if a.hosts != nil {
		if h, ok, _ := a.hosts.Get(hostID); ok && h.Name != "" {
			name = h.Name
		}
	}
	return a.mgr.StartScheduled(hostID, durationSec, intervalSec, name, targetDir)
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
	// The sampler's NDJSON carries no hostId (it doesn't know it), so stamp the
	// host we downloaded from — otherwise frames bucket under "" and the playback
	// UI can't resolve them (meta.Hosts[0].ID == "" is treated as falsy).
	byHost := map[string][]monitor.Frame{}
	for _, f := range frames {
		f.HostID = hostID
		byHost[hostID] = append(byHost[hostID], f)
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

// ---- Auto-update --------------------------------------------------------

// GetCurrentVersion returns the build-time version (or "dev" for local builds).
func (a *App) GetCurrentVersion() string {
	if a.updater == nil {
		return a.version
	}
	return a.updater.CurrentVersion()
}

// CheckForUpdate queries GitHub Releases for a newer build (pure check — it
// records no attempt and applies nothing).
func (a *App) CheckForUpdate() (updater.UpdateInfo, error) {
	if a.updater == nil {
		return updater.UpdateInfo{CurrentVersion: a.version}, nil
	}
	return a.updater.Check(a.ctx)
}

// ApplyUpdate downloads the new exe, stashes the release notes, swaps the
// binary in place, and quits the app so the swapped-in binary relaunches. This
// is the manual/unguarded path (clicking the update badge).
func (a *App) ApplyUpdate(info updater.UpdateInfo) error {
	if a.updater == nil {
		return errors.New("updater not initialised")
	}
	if err := a.updater.Apply(a.ctx, info); err != nil {
		return err
	}
	// Hand off to the relaunch. Quit in a goroutine so this call can return
	// cleanly to the frontend before the runtime shuts down.
	go func() {
		wruntime.Quit(a.ctx)
	}()
	return nil
}

// AutoUpdate is the GUARDED silent startup path. It checks for a newer build
// and, if one is available AND the loop guard green-lights it, applies it and
// quits so the swapped-in binary relaunches. If the guard trips (5 attempts at
// the same target without the running version converging), it returns
// Blocked=true so the frontend can show a manual-update badge instead of
// looping forever.
func (a *App) AutoUpdate() updater.AutoUpdateResult {
	if a.updater == nil {
		return updater.AutoUpdateResult{}
	}
	info, err := a.updater.Check(a.ctx)
	if err != nil || !info.Available {
		return updater.AutoUpdateResult{Info: info}
	}
	if !a.updater.ShouldAutoApply(info) {
		return updater.AutoUpdateResult{Blocked: true, Info: info}
	}
	if err := a.updater.Apply(a.ctx, info); err != nil {
		return updater.AutoUpdateResult{Blocked: true, Info: info}
	}
	go wruntime.Quit(a.ctx)
	return updater.AutoUpdateResult{Applying: true, Info: info}
}

// GetPendingReleaseNotes returns release notes stashed by the previous version
// right before it triggered the update, iff they belong to the current binary
// version. Returns nil when there's nothing to show.
func (a *App) GetPendingReleaseNotes() *updater.PendingNotes {
	if a.updater == nil {
		return nil
	}
	notes, ok, _ := a.updater.LoadPendingNotes()
	if !ok {
		return nil
	}
	return &notes
}

// MarkReleaseNotesSeen deletes the stashed notes file so it won't pop again.
func (a *App) MarkReleaseNotesSeen() error {
	if a.updater == nil {
		return nil
	}
	return a.updater.ClearPendingNotes()
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

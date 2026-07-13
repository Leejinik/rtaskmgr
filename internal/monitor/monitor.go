// Package monitor owns the live SSH connection to each host: it probes the
// host's capabilities, uploads the embedded sampler, then reads the sampler's
// 1-second NDJSON frames off a single streaming session and hands each frame to
// a callback. One *session per host; the Manager fans out across many.
//
// It also drives the optional per-process network column: on demand it installs
// nethogs from the embedded offline RPM bundle, streams `nethogs -t`, and
// overlays each process's throughput onto the frames. Install and rollback are
// explicit, operator-triggered actions (see InstallNethogs / RollbackNethogs).
package monitor

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"rtaskmgr/internal/agent"
	"rtaskmgr/internal/host"
)

// MaxScheduledSeconds caps scheduled recordings at 7 days — a hard upper bound
// enforced here and in the UI so a forgotten recording can't fill the disk.
const MaxScheduledSeconds = 7 * 24 * 3600

// RecMeta describes one server-side scheduled recording.
type RecMeta struct {
	ID          string `json:"id"`
	HostID      string `json:"hostId"`
	HostName    string `json:"hostName"`
	File        string `json:"file"` // absolute path on the host
	StartT      int64  `json:"startT"`
	PlannedEndT int64  `json:"plannedEndT"`
	DurationSec int    `json:"durationSec"`
	IntervalSec int    `json:"intervalSec"`
	Status      string `json:"status"` // "running" | "done"
	SizeBytes   int64  `json:"sizeBytes"`
}

// RecTarget is one candidate filesystem a scheduled recording could be written
// to. The UI lets the user pick one so recordings land on a roomy data/home
// partition instead of filling "/".
type RecTarget struct {
	Path       string `json:"path"`       // base dir we'd record into (.rtaskmgr-rec is appended)
	Mount      string `json:"mount"`      // filesystem mountpoint containing Path
	TotalBytes int64  `json:"totalBytes"` // filesystem size
	FreeBytes  int64  `json:"freeBytes"`  // available space
	Writable   bool   `json:"writable"`   // login user can write Path directly
	NeedsSudo  bool   `json:"needsSudo"`  // not writable, but sudo can create a user-owned subdir
}

// RecEstimate is the pre-flight panel for scheduled recording: where it can be
// stored (with free space) plus a measured projection of how much disk one day
// of 1-second sampling will consume on this host.
type RecEstimate struct {
	Targets      []RecTarget `json:"targets"`
	ProbeSec     int         `json:"probeSec"`     // wall seconds the probe sampled
	ProbeBytes   int64       `json:"probeBytes"`   // gzip bytes the probe produced
	Frames       int         `json:"frames"`       // frames captured during the probe
	BytesPerHour int64       `json:"bytesPerHour"` // projected, gzip
	BytesPerDay  int64       `json:"bytesPerDay"`  // projected, gzip
}

// samplerName is content-addressed (RemoteName + short hash of the binary), so
// every app instance / user stages the SAME file. That lets concurrent sessions
// share one binary instead of overwriting a fixed path — overwriting a running
// executable fails with ETXTBSY ("Text file busy") and would block the second
// connection. Uploads are skipped when the file is already present (see Start).
var samplerName = func() string {
	sum := sha256.Sum256(agent.SamplerBinary)
	return agent.RemoteName + "-" + hex.EncodeToString(sum[:4])
}()

// Proc mirrors one row emitted by cmd/sampler. DiskR/DiskW are -1 when the
// sampler could not read /proc/<pid>/io (no permission); Net is -1 when nethogs
// is not active, 0+ (bytes/s) when it is.
type Proc struct {
	PID     int     `json:"pid"`
	PPID    int     `json:"ppid"`
	Name    string  `json:"name"`
	User    string  `json:"user"`
	Service string  `json:"service"`
	State   string  `json:"state"`
	CPU     float64 `json:"cpu"`
	MemPct  float64 `json:"memPct"`
	RSSKiB  int64   `json:"rssKiB"`
	DiskR   int64   `json:"diskR"`
	DiskW   int64   `json:"diskW"`
	Net     int64   `json:"net"`
	Threads int     `json:"threads"`
	Start   int64   `json:"start"` // process start, unix seconds (0 if unknown)
}

// Frame mirrors one whole-machine snapshot emitted by cmd/sampler, plus the
// HostID the Manager stamps on it before forwarding.
type Frame struct {
	HostID    string     `json:"hostId"`
	T         int64      `json:"t"`
	NCPU      int        `json:"ncpu"`
	MemTotal  int64      `json:"memTotal"`
	MemUsed   int64      `json:"memUsed"`
	CPU       float64    `json:"cpu"`
	Mem       float64    `json:"mem"`
	SwapTotal int64      `json:"swapTotal"`
	SwapUsed  int64      `json:"swapUsed"`
	NetRx     int64      `json:"netRx"`
	NetTx     int64      `json:"netTx"`
	NetSpeed  int64      `json:"netSpeed"`
	Nets      []NetStat  `json:"nets"`
	Disks     []DiskStat `json:"disks"`
	Procs     []Proc     `json:"procs"`
}

// NetStat mirrors one network interface's throughput from the sampler.
type NetStat struct {
	Name  string `json:"name"`
	RxBps int64  `json:"rxBps"`
	TxBps int64  `json:"txBps"`
	Speed int64  `json:"speed"`
}

// DiskStat mirrors one mounted filesystem from the sampler (usage + I/O).
type DiskStat struct {
	Mount  string  `json:"mount"`
	Dev    string  `json:"dev"`
	FSType string  `json:"fsType"`
	Total  int64   `json:"total"`
	Free   int64   `json:"free"`
	Used   int64   `json:"used"`
	RBps   int64   `json:"rBps"`
	WBps   int64   `json:"wBps"`
	Busy   float64 `json:"busy"`
	Kind   string  `json:"kind"`
}

// Capabilities is what the probe discovered about a freshly connected host.
type Capabilities struct {
	UID       int    `json:"uid"`
	OS        string `json:"os"`       // e.g. "rhel:9.3"
	RHELMajor string `json:"rhel"`     // "8" / "9" / ""
	Nethogs   bool   `json:"nethogs"`  // per-process network already available
	Pidstat   bool   `json:"pidstat"`  // sysstat present
	Sudo      bool   `json:"sudo"`     // elevated access (root or sudo) available
	StageDir  string `json:"stageDir"` // exec-capable dir for the sampler (/tmp is often noexec)
}

type FrameFunc func(f Frame)

// StatusFunc reports lifecycle transitions for the UI: state is one of
// "connecting", "probing", "uploading", "streaming", "stopped", "error".
type StatusFunc func(hostID, state, detail string)

// NethogsFunc reports the per-host network-collection state to the UI.
type NethogsFunc func(hostID string, active, installedByUs bool, msg string)

type session struct {
	client       *ssh.Client
	ctx          context.Context
	cancel       context.CancelFunc
	streamCancel context.CancelFunc // cancels just the current sampler run (interval changes)
	interval     int                // live sampling interval, seconds
	bin          string             // staged sampler path
	stageDir     string             // user-owned, exec-capable dir (e.g. /home/liz)
	useSudo      bool               // wrap remote commands in sudo
	elevated     bool               // root or working sudo (required for nethogs/dnf)
	password     string
	user         string             // login user (for sudo chown of recording dirs)
	rhelMajor    string

	netMu           sync.Mutex
	net             map[int]int64 // pid -> bytes/s (sent+recv); valid while nhActive
	nhActive        bool
	nhInstalledByUs bool
	nhTxID          string
	nhCancel        context.CancelFunc
}

func (s *session) setNet(m map[int]int64) {
	s.netMu.Lock()
	s.net = m
	s.netMu.Unlock()
}

func (s *session) lookupNet(pid int) (int64, bool) {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	v, ok := s.net[pid]
	return v, ok
}

func (s *session) setActive(v bool) {
	s.netMu.Lock()
	s.nhActive = v
	if !v {
		s.net = nil
	}
	s.netMu.Unlock()
}

func (s *session) active() bool {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	return s.nhActive
}

// nhDir is the user-owned staging directory for the nethogs RPM bundle. Using
// the exec-capable StageDir (the user's home) avoids /tmp's sticky-bit ownership
// pitfalls: the login user can write the RPMs and root/dnf can read them.
func (s *session) nhDir() string {
	d := s.stageDir
	if d == "" {
		d = "/var/tmp"
	}
	return d + "/.rtaskmgr-nh"
}

type Manager struct {
	mu        sync.Mutex
	sessions  map[string]*session
	onFrame   FrameFunc
	onStatus  StatusFunc
	onNethogs NethogsFunc
	rpmFS     fs.FS // embedded offline RPM bundle (rpms/rhel8, rpms/rhel9)
}

func NewManager(onFrame FrameFunc, onStatus StatusFunc, onNethogs NethogsFunc, rpmFS fs.FS) *Manager {
	return &Manager{
		sessions:  map[string]*session{},
		onFrame:   onFrame,
		onStatus:  onStatus,
		onNethogs: onNethogs,
		rpmFS:     rpmFS,
	}
}

func (m *Manager) status(id, state, detail string) {
	if m.onStatus != nil {
		m.onStatus(id, state, detail)
	}
}

func (m *Manager) nethogs(id string, active, byUs bool, msg string) {
	if m.onNethogs != nil {
		m.onNethogs(id, active, byUs, msg)
	}
}

func (m *Manager) get(hostID string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[hostID]
}

// clampInterval keeps the live sampling interval within 1..60 seconds.
func clampInterval(sec int) int {
	if sec < 1 {
		return 10
	}
	if sec > 60 {
		return 60
	}
	return sec
}

// Start connects to h, probes it, uploads the sampler, and launches the
// streaming goroutine at the given live sampling interval (seconds). It returns
// the probed capabilities. Any existing session for the same host is replaced.
func (m *Manager) Start(parent context.Context, h host.Host, intervalSec int) (Capabilities, error) {
	m.Stop(h.ID)
	m.status(h.ID, "connecting", h.Addr)

	client, err := dial(h)
	if err != nil {
		m.status(h.ID, "error", err.Error())
		return Capabilities{}, err
	}

	m.status(h.ID, "probing", "")
	caps, err := probe(client, h.Password)
	if err != nil {
		client.Close()
		m.status(h.ID, "error", "probe: "+err.Error())
		return Capabilities{}, err
	}
	useSudoWrapper := caps.UID != 0 && caps.Sudo
	if caps.StageDir == "" {
		client.Close()
		err := fmt.Errorf("no executable directory found on host (/tmp may be noexec)")
		m.status(h.ID, "error", err.Error())
		return caps, err
	}

	bin := caps.StageDir + "/" + samplerName
	// Skip the upload if an identical sampler is already staged (e.g. by another
	// session/user). Crucially this avoids overwriting a running binary, which
	// would fail with ETXTBSY and block concurrent connections.
	if !samplerPresent(client, bin, len(agent.SamplerBinary)) {
		m.status(h.ID, "uploading", "sampler → "+caps.StageDir)
		if err := uploadSampler(client, bin); err != nil {
			client.Close()
			m.status(h.ID, "error", "upload: "+err.Error())
			return caps, err
		}
	}

	ctx, cancel := context.WithCancel(parent)
	s := &session{
		client:    client,
		ctx:       ctx,
		cancel:    cancel,
		interval:  clampInterval(intervalSec),
		bin:       bin,
		stageDir:  caps.StageDir,
		useSudo:   useSudoWrapper,
		elevated:  caps.Sudo,
		password:  h.Password,
		user:      h.User,
		rhelMajor: caps.RHELMajor,
	}
	m.mu.Lock()
	m.sessions[h.ID] = s
	m.mu.Unlock()

	m.launchStream(h.ID, s)
	m.status(h.ID, "streaming", "")
	return caps, nil
}

// SetInterval changes the live sampling interval for a connected host by
// restarting just the sampler stream (the SSH client and any nethogs stream stay
// up).
func (m *Manager) SetInterval(hostID string, intervalSec int) error {
	s := m.get(hostID)
	if s == nil {
		return fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	s.interval = clampInterval(intervalSec)
	if s.streamCancel != nil {
		s.streamCancel() // stop the current sampler run; launchStream starts a new one
	}
	m.launchStream(hostID, s)
	return nil
}

// teardown removes a session and closes its client (real disconnect).
func (m *Manager) teardown(hostID string, s *session) {
	m.mu.Lock()
	if cur, ok := m.sessions[hostID]; ok && cur == s {
		delete(m.sessions, hostID)
		s.client.Close()
	}
	m.mu.Unlock()
}

// launchStream starts a sampler run under a child context so it can be restarted
// (interval changes) without dropping the SSH session. The session is only torn
// down when the run ends on its own (connection dropped) — not when we cancel it
// for a restart.
func (m *Manager) launchStream(hostID string, s *session) {
	sctx, scancel := context.WithCancel(s.ctx)
	s.streamCancel = scancel
	go func() {
		m.runStream(sctx, hostID, s)
		if sctx.Err() == nil {
			// Ended on its own: the connection dropped or the sampler died.
			m.teardown(hostID, s)
			m.status(hostID, "stopped", "stream ended")
		}
		// Otherwise cancelled (interval restart or Stop) — leave teardown to them.
	}()
}

// runStream runs the sampler and forwards each NDJSON line as a Frame, overlaying
// nethogs throughput onto each process's Net field, until ctx is cancelled or
// the connection drops. It does NOT tear down the session (the caller decides).
func (m *Manager) runStream(ctx context.Context, hostID string, s *session) {
	sess, err := s.client.NewSession()
	if err != nil {
		m.status(hostID, "error", "session: "+err.Error())
		return
	}
	defer sess.Close()

	cmd := fmt.Sprintf("%s -i %d", s.bin, s.interval)
	if s.useSudo {
		sess.Stdin = strings.NewReader(s.password + "\n")
		cmd = fmt.Sprintf("sudo -S -p '' %s -i %d", s.bin, s.interval)
	}

	stdout, err := sess.StdoutPipe()
	if err != nil {
		m.status(hostID, "error", "stdout: "+err.Error())
		return
	}
	if err := sess.Start(cmd); err != nil {
		m.status(hostID, "error", "start: "+err.Error())
		return
	}

	go func() {
		<-ctx.Done()
		sess.Signal(ssh.SIGTERM)
		sess.Close()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := scanner.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var f Frame
		if err := json.Unmarshal(line, &f); err != nil {
			continue
		}
		f.HostID = hostID
		netActive := s.active()
		for i := range f.Procs {
			if !netActive {
				f.Procs[i].Net = -1 // N/A: nethogs not running
				continue
			}
			if bps, ok := s.lookupNet(f.Procs[i].PID); ok {
				f.Procs[i].Net = bps
			} else {
				f.Procs[i].Net = 0 // active but this pid had no traffic this tick
			}
		}
		m.onFrame(f)
	}
}

// Stop tears down the session for one host (best effort), including any nethogs
// stream (but NOT uninstalling nethogs — that's an explicit rollback action).
func (m *Manager) Stop(hostID string) {
	m.mu.Lock()
	s, ok := m.sessions[hostID]
	if ok {
		delete(m.sessions, hostID)
	}
	m.mu.Unlock()
	if ok {
		if s.nhCancel != nil {
			s.nhCancel()
		}
		s.cancel()
		s.client.Close()
		m.status(hostID, "stopped", "")
	}
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Stop(id)
	}
}

// KillProcess sends a termination signal to one process on a connected host.
// force selects SIGKILL (-9, uncatchable) over the default graceful SIGTERM
// (-15). PIDs <= 1 are refused as a guardrail (0 = kernel, 1 = init/systemd — a
// stray kill there takes the box down). The PID arrives as a validated int from
// the Wails binding, so it is formatted with %d and carries no shell-injection
// surface. Elevation follows the session: sudoRun wraps in sudo when we logged
// in as a non-root user with working sudo, and runs the bare kill otherwise —
// so an unprivileged session can still end its own processes and gets a clear
// "Operation not permitted" for others'.
func (m *Manager) KillProcess(hostID string, pid int, force bool) error {
	s := m.get(hostID)
	if s == nil {
		return fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	if pid <= 1 {
		return fmt.Errorf("PID %d 은(는) 종료할 수 없습니다 (init/커널 보호)", pid)
	}
	sig := "TERM"
	if force {
		sig = "KILL"
	}
	out, err := m.sudoRun(s, fmt.Sprintf("kill -%s %d", sig, pid))
	if err != nil {
		detail := tailLines(out, 2)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("종료 실패: %s", detail)
	}
	return nil
}

// validUnit allows only characters that appear in real systemd unit names, so an
// attacker-influenced /proc/<pid>/cgroup value can never inject shell syntax into
// the systemctl command.
func validUnit(u string) bool {
	if u == "" || len(u) > 128 {
		return false
	}
	for _, r := range u {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '@' || r == '.' || r == '_' || r == ':' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

// ServiceAction runs `systemctl <action> <unit>` on the connected host for a
// process that belongs to a systemd unit. action is "stop" or "restart". Only
// .service units are accepted: a stray stop on a .scope/.slice could tear down a
// login session (including our own SSH). The confirmation prompt lives in the UI.
func (m *Manager) ServiceAction(hostID, unit, action string) error {
	s := m.get(hostID)
	if s == nil {
		return fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	unit = strings.TrimSpace(unit)
	if !validUnit(unit) || !strings.HasSuffix(unit, ".service") {
		return fmt.Errorf("서비스(.service) 유닛이 아닙니다: %q", unit)
	}
	if action != "stop" && action != "restart" {
		return fmt.Errorf("허용되지 않는 동작입니다: %q", action)
	}
	out, err := m.sudoRun(s, fmt.Sprintf("systemctl %s %s", action, unit))
	if err != nil {
		detail := tailLines(out, 3)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("서비스 %s 실패: %s", action, detail)
	}
	return nil
}

// ---- nethogs: install / stream / rollback ----

// InstallNethogs makes the per-process network column live for one host. If
// nethogs is missing it installs it offline from the embedded RPM bundle for the
// host's RHEL major version (recording the dnf transaction so it can be undone),
// then starts streaming `nethogs -t` and overlaying throughput onto frames.
func (m *Manager) InstallNethogs(hostID string) error {
	s := m.get(hostID)
	if s == nil {
		return fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	if !s.elevated {
		return fmt.Errorf("nethogs 설치/실행에는 root 또는 sudo 권한이 필요합니다")
	}
	if s.active() {
		return nil // already streaming
	}

	present := m.commandExists(s, "nethogs")
	if !present {
		major := s.rhelMajor
		if major != "8" && major != "9" {
			return fmt.Errorf("지원하지 않는 OS입니다 (RHEL 8/9 번들만 보유). 감지된 버전: %q", major)
		}
		m.nethogs(hostID, false, false, "RPM 업로드 중…")
		if err := m.uploadBundle(s, major); err != nil {
			return fmt.Errorf("RPM 업로드 실패: %w", err)
		}
		m.nethogs(hostID, false, false, "오프라인 설치 중…")
		out, err := m.sudoRun(s, "dnf install --disablerepo='*' --nogpgcheck -y "+s.nhDir()+"/*.rpm")
		if err != nil {
			return fmt.Errorf("dnf install 실패: %s", tailLines(out, 3))
		}
		// Record the transaction id so rollback removes exactly what we added.
		tx, _ := m.sudoRun(s, "dnf history info last 2>/dev/null | awk -F: '/Transaction ID/{gsub(/ /,\"\",$2);print $2; exit}'")
		s.nhTxID = strings.TrimSpace(tx)
		s.nhInstalledByUs = true
	} else {
		s.nhInstalledByUs = false // pre-existing install — never uninstall it
	}

	if err := m.startNethogs(s, hostID); err != nil {
		return fmt.Errorf("nethogs 실행 실패: %w", err)
	}
	msg := "네트워크 수집 시작"
	if s.nhInstalledByUs {
		msg = fmt.Sprintf("nethogs 설치 후 수집 시작 (tx %s)", s.nhTxID)
	}
	m.nethogs(hostID, true, s.nhInstalledByUs, msg)
	return nil
}

// RollbackNethogs stops the network stream and, if this app installed nethogs,
// undoes the dnf transaction to restore the host's prior dependency state.
func (m *Manager) RollbackNethogs(hostID string) error {
	s := m.get(hostID)
	if s == nil {
		return fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	if s.nhCancel != nil {
		s.nhCancel()
		s.nhCancel = nil
	}
	s.setActive(false)
	// Make sure nethogs is really gone before we touch the package (the watchdog
	// stop is async).
	_, _ = m.sudoRun(s, "pkill -TERM -x nethogs 2>/dev/null; true")

	if s.nhInstalledByUs {
		cmd := "dnf remove --disablerepo='*' -y nethogs"
		if s.nhTxID != "" {
			cmd = "dnf history undo " + s.nhTxID + " --disablerepo='*' -y"
		}
		m.nethogs(hostID, false, true, "롤백(제거) 중…")
		out, err := m.sudoRun(s, cmd)
		if err != nil {
			return fmt.Errorf("롤백 실패: %s", tailLines(out, 4))
		}
		s.nhInstalledByUs = false
		s.nhTxID = ""
		_, _ = m.plainRun(s, "rm -rf "+s.nhDir())
		m.nethogs(hostID, false, false, "nethogs 제거 완료 (의존성 원복)")
		return nil
	}
	m.nethogs(hostID, false, false, "네트워크 수집 중지")
	return nil
}

// startNethogs runs `nethogs -t -d 1` and parses its periodic refresh blocks
// into a pid -> bytes/s map. Each "Refreshing:" line begins a fresh block, so we
// accumulate and swap atomically to reflect the current instant.
func (m *Manager) startNethogs(s *session, hostID string) error {
	// Clear any stale nethogs orphaned by a previously crashed/closed session
	// before starting a fresh one (best effort; -x matches the process name only).
	_, _ = m.sudoRun(s, "pkill -TERM -x nethogs 2>/dev/null; true")

	nctx, ncancel := context.WithCancel(s.ctx)
	sess, err := s.client.NewSession()
	if err != nil {
		ncancel()
		return err
	}

	// Watchdog wrapper with a heartbeat dead-man's switch. The client writes a
	// newline at least every 10s (see the writer goroutine below); `read -t 30`
	// exits the loop when that heartbeat stops for 30s. So nethogs is killed on:
	//   - clean stop (we close the pipe → immediate EOF), and
	//   - ANY silent client death — crash-without-FIN, freeze, network drop — even
	//     if the server never closes the channel.
	// This deliberately does NOT depend on the SSH channel closing: OpenSSH's sshd
	// ignores Signal(SIGTERM) on exec channels, and a host with ClientAliveInterval
	// 0 never reaps a vanished client — both previously left nethogs reparented to
	// PID 1, pegging CPU/memory for days. `read -t` needs bash (not dash).
	inner := "nethogs -t -d 1 & np=$!; while read -t 30 _x; do :; done; kill -TERM $np 2>/dev/null; wait $np 2>/dev/null"
	cmd := "bash -c " + shellQuote(inner)
	if s.useSudo {
		cmd = "sudo -S -p '' bash -c " + shellQuote(inner)
	}

	pr, pw := io.Pipe()
	sess.Stdin = pr
	stdout, err := sess.StdoutPipe()
	if err != nil {
		ncancel()
		_ = pw.Close()
		sess.Close()
		return err
	}
	if err := sess.Start(cmd); err != nil {
		ncancel()
		_ = pw.Close()
		sess.Close()
		return err
	}
	s.nhCancel = ncancel
	s.setActive(true)
	s.setNet(map[int]int64{})

	// One writer drives both the sudo password (first line) and the heartbeat.
	// On a clean stop (nctx cancelled) we close the pipe for an immediate EOF kill;
	// otherwise a newline every 10s keeps the remote `read -t 30` alive. If this
	// process dies or freezes, the heartbeats stop and the remote watchdog
	// self-terminates within 30s regardless of connection/sshd state.
	go func() {
		if s.useSudo {
			if _, err := io.WriteString(pw, s.password+"\n"); err != nil {
				return
			}
		}
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-nctx.Done():
				_ = pw.Close() // EOF → remote watchdog kills nethogs by PID
				sess.Close()
				return
			case <-t.C:
				if _, err := io.WriteString(pw, "\n"); err != nil {
					return
				}
			}
		}
	}()
	go func() {
		defer s.setActive(false)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		acc := map[int]int64{}
		for sc.Scan() {
			if nctx.Err() != nil {
				return
			}
			line := sc.Text()
			if strings.HasPrefix(line, "Refreshing:") {
				s.setNet(acc)
				acc = map[int]int64{}
				continue
			}
			if pid, bps, ok := parseNethogsLine(line); ok {
				acc[pid] += bps
			}
		}
	}()
	return nil
}

// parseNethogsLine parses a trace line "<prog>/<pid>/<uid>\t<sentKB>\t<recvKB>"
// into (pid, bytes/s). The program path itself can contain slashes, so the pid
// is the second-to-last slash-separated field.
func parseNethogsLine(line string) (pid int, bps int64, ok bool) {
	parts := strings.Split(line, "\t")
	if len(parts) < 3 {
		return 0, 0, false
	}
	segs := strings.Split(strings.TrimSpace(parts[0]), "/")
	if len(segs) < 2 {
		return 0, 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(segs[len(segs)-2]))
	if err != nil || pid <= 0 {
		return 0, 0, false
	}
	sent, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	recv, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	return pid, int64((sent + recv) * 1024), true // KB/s -> bytes/s
}

// uploadBundle stages every RPM in rpms/rhel<major> into /tmp/nh on the host.
func (m *Manager) uploadBundle(s *session, major string) error {
	dir := "rpms/rhel" + major
	entries, err := fs.ReadDir(m.rpmFS, dir)
	if err != nil {
		return err
	}
	// Create the staging dir AS THE LOGIN USER so the (non-sudo) base64 upload
	// can write into it. root/dnf can still read these files afterwards.
	nh := s.nhDir()
	if _, err := m.plainRun(s, "rm -rf "+nh+" && mkdir -p "+nh); err != nil {
		return err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".rpm") {
			continue
		}
		data, err := fs.ReadFile(m.rpmFS, dir+"/"+e.Name())
		if err != nil {
			return err
		}
		if err := uploadBytes(s.client, data, nh+"/"+e.Name(), false); err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return fmt.Errorf("번들에 RPM이 없습니다: %s", dir)
	}
	return nil
}

// sudoRun executes a shell command, elevated when the session isn't already root.
func (m *Manager) sudoRun(s *session, inner string) (string, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	cmd := "bash -c " + shellQuote(inner)
	if s.useSudo {
		sess.Stdin = strings.NewReader(s.password + "\n")
		cmd = "sudo -S -p '' bash -c " + shellQuote(inner)
	}
	out, err := sess.CombinedOutput(cmd)
	return string(out), err
}

// plainRun executes a command as the login user (never elevated).
func (m *Manager) plainRun(s *session, inner string) (string, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput("bash -c " + shellQuote(inner))
	return string(out), err
}

func (m *Manager) commandExists(s *session, name string) bool {
	out, _ := m.sudoRun(s, "command -v "+name+" >/dev/null 2>&1 && echo YES || echo NO")
	return strings.Contains(out, "YES")
}

// ---- scheduled (server-side, detached) recording ----

// probeSeconds is how long EstimateScheduled samples to project daily disk use.
// 6s yields ~5 one-second frames — enough to project from while staying quick.
const probeSeconds = 6

func recDirFor(s *session) string {
	d := s.stageDir
	if d == "" {
		d = "/var/tmp"
	}
	return d + "/.rtaskmgr-rec"
}

// recBasesSh is the shell-expanded list of base dirs a scheduled recording may
// live under: the staging dir / login home (default) plus the roomy /data and
// /home partitions the UI can target. Paths are app-derived (no spaces).
func recBasesSh(s *session) string {
	stage := s.stageDir
	if stage == "" {
		stage = "/var/tmp"
	}
	return stage + ` "$HOME" /data /home`
}

// resolveRecFile locates a recording's on-host gz path across the candidate
// bases (recordings may live on different partitions now). id is app-generated
// (rec-<millis>), so it is safe to interpolate.
func (m *Manager) resolveRecFile(s *session, id string) (string, error) {
	script := `for b in ` + recBasesSh(s) + `; do f="$b/.rtaskmgr-rec/` + id + `.ndjson.gz"; [ -e "$f" ] && { echo "$f"; exit 0; }; done`
	out, _ := m.plainRun(s, script)
	f := strings.TrimSpace(out)
	if f == "" {
		return "", fmt.Errorf("기록 파일을 찾지 못했습니다: %s", id)
	}
	return f, nil
}

// recTargets probes the candidate filesystems for scheduled recordings,
// reporting free space and whether the login user can write there directly (or
// via sudo). Mountpoints are de-duplicated, preferring a directly-writable path.
func (m *Manager) recTargets(s *session) []RecTarget {
	script := `for d in ` + recBasesSh(s) + `; do [ -d "$d" ] || continue; ` +
		`w=0; [ -w "$d" ] && w=1; ` +
		`df -P -B1 "$d" 2>/dev/null | awk -v p="$d" -v w="$w" 'NR==2{print "T|"p"|"$6"|"$2"|"$4"|"w}'; done`
	out, _ := m.plainRun(s, script)
	seen := map[string]int{} // mountpoint -> index in ts
	var ts []RecTarget
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "T|") {
			continue
		}
		p := strings.Split(line[2:], "|")
		if len(p) != 5 {
			continue
		}
		mount, w := p[1], p[4] == "1"
		if i, ok := seen[mount]; ok {
			// Same filesystem reached via another base — keep a writable path.
			if w && !ts[i].Writable {
				ts[i].Path = p[0]
				ts[i].Writable = true
				ts[i].NeedsSudo = false
			}
			continue
		}
		total, _ := strconv.ParseInt(p[2], 10, 64)
		avail, _ := strconv.ParseInt(p[3], 10, 64)
		seen[mount] = len(ts)
		ts = append(ts, RecTarget{
			Path: p[0], Mount: mount, TotalBytes: total, FreeBytes: avail,
			Writable: w, NeedsSudo: !w && s.elevated,
		})
	}
	return ts
}

// EstimateScheduled reports where scheduled recordings can be stored (with free
// space) and projects daily disk use by running a short gzip probe with the real
// sampler — so the user can size a recording against the chosen partition.
func (m *Manager) EstimateScheduled(hostID string) (RecEstimate, error) {
	s := m.get(hostID)
	if s == nil {
		return RecEstimate{}, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	if s.stageDir == "" {
		return RecEstimate{}, fmt.Errorf("기록을 저장할 디렉터리를 찾지 못했습니다")
	}
	if !samplerPresent(s.client, s.bin, len(agent.SamplerBinary)) {
		if err := uploadSampler(s.client, s.bin); err != nil {
			return RecEstimate{}, fmt.Errorf("샘플러 업로드 실패: %w", err)
		}
	}
	est := RecEstimate{ProbeSec: probeSeconds}
	est.Targets = m.recTargets(s)

	// Short gzip probe (same sampler/flags as a real recording) to measure size.
	probe := s.stageDir + "/.rtaskmgr-probe.gz"
	run := fmt.Sprintf("%s -i 1 -max %d -o %s -minfree 64 >/dev/null 2>&1; stat -c%%s %s 2>/dev/null || echo 0",
		s.bin, probeSeconds, probe, probe)
	out, err := m.plainRun(s, run)
	if err != nil {
		return est, fmt.Errorf("probe 실행 실패: %s", strings.TrimSpace(out))
	}
	if f := strings.Fields(strings.TrimSpace(out)); len(f) > 0 {
		est.ProbeBytes, _ = strconv.ParseInt(f[len(f)-1], 10, 64)
	}

	// Count frames by decompressing the probe — gives a cadence-accurate per-frame
	// size to project from (recording is one frame per second).
	if b64, derr := m.plainRun(s, "base64 -w0 "+probe); derr == nil {
		if gzBytes, e := base64.StdEncoding.DecodeString(strings.TrimSpace(b64)); e == nil {
			if gzr, e2 := gzip.NewReader(bytes.NewReader(gzBytes)); e2 == nil {
				sc := bufio.NewScanner(gzr)
				sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
				for sc.Scan() {
					if b := sc.Bytes(); len(b) > 0 && b[0] == '{' {
						est.Frames++
					}
				}
				gzr.Close()
			}
		}
	}
	_, _ = m.plainRun(s, "rm -f "+probe+" "+probe+".done")

	if est.Frames > 0 && est.ProbeBytes > 0 {
		perFrame := float64(est.ProbeBytes) / float64(est.Frames)
		est.BytesPerHour = int64(perFrame * 3600)
		est.BytesPerDay = int64(perFrame * 86400)
	}
	return est, nil
}

// StartScheduled launches a detached server-side recording that survives the
// client disconnecting and self-stops after durationSec (hard-capped at 7 days).
// The sampler writes gzip NDJSON and aborts early if the disk runs low. targetDir
// chooses the filesystem to record onto (empty = default staging dir); if it is
// not directly writable, a user-owned subdir is created with sudo. intervalSec is
// the recording cadence (1..60s) — a coarser interval trades resolution for less
// disk use.
func (m *Manager) StartScheduled(hostID string, durationSec, intervalSec int, hostName, targetDir string) (RecMeta, error) {
	s := m.get(hostID)
	if s == nil {
		return RecMeta{}, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	if durationSec <= 0 {
		return RecMeta{}, fmt.Errorf("기록 시간이 0입니다")
	}
	if durationSec > MaxScheduledSeconds {
		durationSec = MaxScheduledSeconds
	}
	if intervalSec < 1 {
		intervalSec = 1
	}
	if intervalSec > 60 {
		intervalSec = 60
	}
	if s.stageDir == "" {
		return RecMeta{}, fmt.Errorf("기록을 저장할 디렉터리를 찾지 못했습니다")
	}
	if !samplerPresent(s.client, s.bin, len(agent.SamplerBinary)) {
		if err := uploadSampler(s.client, s.bin); err != nil {
			return RecMeta{}, fmt.Errorf("샘플러 업로드 실패: %w", err)
		}
	}

	base := targetDir
	if base == "" {
		base = s.stageDir
	}
	recDir := base + "/.rtaskmgr-rec"
	now := time.Now()
	id := fmt.Sprintf("rec-%d", now.UnixMilli())
	file := recDir + "/" + id + ".ndjson.gz"
	meta := RecMeta{
		ID: id, HostID: hostID, HostName: hostName, File: file,
		StartT:      now.UnixMilli(),
		PlannedEndT: now.Add(time.Duration(durationSec) * time.Second).UnixMilli(),
		DurationSec: durationSec, IntervalSec: intervalSec, Status: "running",
	}
	metaJSON, _ := json.Marshal(meta)

	// Ensure the recording dir exists and the login user (which runs the detached
	// sampler) can write it. If not, fall back to sudo: create + chown to the user.
	mk := "mkdir -p " + recDir + " 2>/dev/null && test -w " + recDir + " && echo OK"
	if out, _ := m.plainRun(s, mk); !strings.Contains(out, "OK") {
		if !s.elevated {
			return meta, fmt.Errorf("기록 위치에 쓸 수 없습니다(권한 없음): %s", base)
		}
		if s.user == "" {
			return meta, fmt.Errorf("기록 위치를 생성할 사용자명을 알 수 없습니다")
		}
		sudoMk := fmt.Sprintf("mkdir -p %s && chown %s %s && echo OK", recDir, s.user, recDir)
		if out2, err := m.sudoRun(s, sudoMk); err != nil || !strings.Contains(out2, "OK") {
			return meta, fmt.Errorf("sudo로 기록 위치 생성 실패: %s", strings.TrimSpace(out2))
		}
	}
	if err := uploadBytes(s.client, metaJSON, recDir+"/"+id+".meta.json", false); err != nil {
		return meta, err
	}
	// setsid detaches from the SSH session's process group so it keeps running
	// after we disconnect. Paths are app-controlled (no spaces) so no quoting.
	launch := fmt.Sprintf(
		"setsid %s -i %d -max %d -o %s -minfree 512 >/dev/null 2>&1 </dev/null & echo OK",
		s.bin, intervalSec, durationSec, file)
	if out, err := m.plainRun(s, launch); err != nil {
		return meta, fmt.Errorf("실행 실패: %s", strings.TrimSpace(out))
	}
	return meta, nil
}

// ListScheduled returns the scheduled recordings present on the host with live
// size/status.
func (m *Manager) ListScheduled(hostID string) ([]RecMeta, error) {
	s := m.get(hostID)
	if s == nil {
		return nil, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	script := `for b in ` + recBasesSh(s) + `; do d="$b/.rtaskmgr-rec"; [ -d "$d" ] || continue; ` +
		`for mf in "$d"/*.meta.json; do [ -e "$mf" ] || continue; ` +
		`id=$(basename "$mf" .meta.json); f="$d/$id.ndjson.gz"; ` +
		`sz=$(stat -c%s "$f" 2>/dev/null || echo 0); ` +
		`run=0; pgrep -f "$f" >/dev/null 2>&1 && run=1; ` +
		`echo "STAT|$id|$sz|$run"; echo "META|$(base64 -w0 "$mf")"; done; done`
	out, err := m.plainRun(s, script)
	if err != nil {
		return nil, err
	}

	metas := map[string]*RecMeta{}
	order := []string{}
	stats := map[string][2]int64{} // id -> {size, running}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "META|"):
			raw, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(line[5:]))
			if derr != nil {
				continue
			}
			var rm RecMeta
			if json.Unmarshal(raw, &rm) == nil && rm.ID != "" {
				if _, ok := metas[rm.ID]; !ok {
					order = append(order, rm.ID)
				}
				metas[rm.ID] = &rm
			}
		case strings.HasPrefix(line, "STAT|"):
			parts := strings.Split(line[5:], "|")
			if len(parts) == 3 {
				sz, _ := strconv.ParseInt(parts[1], 10, 64)
				run, _ := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
				stats[parts[0]] = [2]int64{sz, run}
			}
		}
	}
	out2 := make([]RecMeta, 0, len(order))
	for _, id := range order {
		rm := metas[id]
		if st, ok := stats[id]; ok {
			rm.SizeBytes = st[0]
			if st[1] == 1 {
				rm.Status = "running"
			} else {
				rm.Status = "done"
			}
		}
		out2 = append(out2, *rm)
	}
	return out2, nil
}

// StopScheduled signals the sampler for one recording to stop; it finalizes the
// gzip file cleanly on SIGTERM.
func (m *Manager) StopScheduled(hostID, id string) error {
	s := m.get(hostID)
	if s == nil {
		return fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	file, err := m.resolveRecFile(s, id)
	if err != nil {
		return err
	}
	_, err = m.plainRun(s,
		"pkill -TERM -f "+file+" 2>/dev/null; sleep 1; "+
			"pgrep -f "+file+" >/dev/null 2>&1 && pkill -KILL -f "+file+"; echo done")
	return err
}

// DeleteScheduled removes a recording and its sidecars from the host.
func (m *Manager) DeleteScheduled(hostID, id string) error {
	s := m.get(hostID)
	if s == nil {
		return fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	file, err := m.resolveRecFile(s, id)
	if err != nil {
		return err
	}
	// file is "<dir>/<id>.ndjson.gz"; remove it and its .done/.meta.json sidecars.
	d := strings.TrimSuffix(file, "/"+id+".ndjson.gz")
	_, err = m.plainRun(s, fmt.Sprintf("rm -f %s/%s.ndjson.gz %s/%s.ndjson.gz.done %s/%s.meta.json; echo done",
		d, id, d, id, d, id))
	return err
}

// DownloadScheduled fetches a recording, decompresses it, and returns its
// frames for playback. The gz is base64-streamed over the existing SSH session.
func (m *Manager) DownloadScheduled(hostID, id string) ([]Frame, error) {
	s := m.get(hostID)
	if s == nil {
		return nil, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	file, err := m.resolveRecFile(s, id)
	if err != nil {
		return nil, err
	}
	out, err := m.plainRun(s, "base64 -w0 "+file)
	if err != nil {
		return nil, fmt.Errorf("다운로드 실패: %s", tailLines(out, 2))
	}
	gzBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		return nil, fmt.Errorf("디코드 실패: %w", err)
	}
	gzr, err := gzip.NewReader(bytes.NewReader(gzBytes))
	if err != nil {
		return nil, fmt.Errorf("gzip 열기 실패: %w", err)
	}
	defer gzr.Close()
	var frames []Frame
	sc := bufio.NewScanner(gzr)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var f Frame
		if json.Unmarshal(line, &f) == nil {
			frames = append(frames, f)
		}
	}
	return frames, nil
}

// ---- SSH plumbing ----

func dial(h host.Host) (*ssh.Client, error) {
	if h.User == "" {
		return nil, fmt.Errorf("ssh user is required")
	}
	var auths []ssh.AuthMethod
	if h.KeyPath != "" {
		key, err := os.ReadFile(h.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read key %s: %w", h.KeyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if h.Password != "" {
		auths = append(auths, ssh.Password(h.Password))
	}
	if len(auths) == 0 {
		return nil, fmt.Errorf("no password or key provided")
	}
	port := h.Port
	if port == 0 {
		port = 22
	}
	cfg := &ssh.ClientConfig{
		User:            h.User,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         8 * time.Second,
	}
	return ssh.Dial("tcp", fmt.Sprintf("%s:%d", h.Addr, port), cfg)
}

const probeScript = `echo "uid=$(id -u)"; ` +
	`command -v nethogs >/dev/null 2>&1 && echo nethogs=1; ` +
	`command -v pidstat >/dev/null 2>&1 && echo pidstat=1; ` +
	`. /etc/os-release 2>/dev/null; echo "os=${ID}:${VERSION_ID}"; ` +
	`for d in "$HOME" /var/tmp "/run/user/$(id -u)" /dev/shm /tmp; do ` +
	`[ -d "$d" ] && [ -w "$d" ] || continue; ` +
	`t="$d/.rtx.$$"; ` +
	`printf '#!/bin/sh\necho ok\n' > "$t" 2>/dev/null && chmod 0700 "$t" 2>/dev/null && ` +
	`out=$("$t" 2>/dev/null) && [ "$out" = ok ] && { echo "stagedir=$d"; rm -f "$t"; break; }; ` +
	`rm -f "$t" 2>/dev/null; done`

func probe(client *ssh.Client, password string) (Capabilities, error) {
	sess, err := client.NewSession()
	if err != nil {
		return Capabilities{}, err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(probeScript)
	if err != nil && len(out) == 0 {
		return Capabilities{}, err
	}
	caps := Capabilities{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "uid="):
			fmt.Sscanf(line, "uid=%d", &caps.UID)
		case line == "nethogs=1":
			caps.Nethogs = true
		case line == "pidstat=1":
			caps.Pidstat = true
		case strings.HasPrefix(line, "os="):
			caps.OS = strings.TrimPrefix(line, "os=")
			caps.RHELMajor = rhelMajor(caps.OS)
		case strings.HasPrefix(line, "stagedir="):
			caps.StageDir = strings.TrimPrefix(line, "stagedir=")
		}
	}
	caps.Sudo = caps.UID == 0 || sudoWorks(client, password)
	return caps, nil
}

func sudoWorks(client *ssh.Client, password string) bool {
	sess, err := client.NewSession()
	if err != nil {
		return false
	}
	defer sess.Close()
	sess.Stdin = strings.NewReader(password + "\n")
	out, _ := sess.CombinedOutput("sudo -S -p '' id -u")
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "0" {
			return true
		}
	}
	return false
}

// rhelMajor extracts "8" or "9" from an os-release "id:version" string such as
// "rhel:9.3", "centos:8", "rocky:9.2", "almalinux:8.9".
func rhelMajor(os string) string {
	parts := strings.SplitN(os, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	ver := parts[1]
	if i := strings.IndexByte(ver, '.'); i >= 0 {
		ver = ver[:i]
	}
	switch ver {
	case "8", "9":
		return ver
	}
	return ""
}

// samplerPresent reports whether an executable of the exact expected size is
// already staged at path (content is implied by the content-addressed name).
func samplerPresent(client *ssh.Client, path string, size int) bool {
	sess, err := client.NewSession()
	if err != nil {
		return false
	}
	defer sess.Close()
	cmd := fmt.Sprintf(`[ -x %s ] && [ "$(stat -c%%s %s 2>/dev/null)" = "%d" ] && echo OK`, path, path, size)
	out, _ := sess.CombinedOutput(cmd)
	return strings.Contains(string(out), "OK")
}

// uploadSampler writes the sampler to a per-session temp file and atomically
// renames it into place. Renaming over a path that another session is currently
// executing is safe on Linux (the running process keeps the old inode), so this
// never hits ETXTBSY — unlike truncating the target directly.
func uploadSampler(client *ssh.Client, finalPath string) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString(agent.SamplerBinary))
	// $$ is the remote shell's PID — unique per session.
	cmd := fmt.Sprintf(`t=%q.$$.tmp; base64 -d > "$t" && chmod 0700 "$t" && mv -f "$t" %q`, finalPath, finalPath)
	if out, err := sess.CombinedOutput(cmd); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// uploadBytes writes data to an absolute remote path via a base64 stream
// (no sftp dependency). exec makes the file 0700, otherwise 0600.
func uploadBytes(client *ssh.Client, data []byte, target string, exec bool) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString(data))
	mode := "0600"
	if exec {
		mode = "0700"
	}
	cmd := fmt.Sprintf("base64 -d > %s && chmod %s %s", target, mode, target)
	if out, err := sess.CombinedOutput(cmd); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// shellQuote single-quotes a string for safe embedding in `bash -c '...'`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// tailLines returns the last n non-empty lines of s, joined by " | ".
func tailLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

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
}

// Frame mirrors one whole-machine snapshot emitted by cmd/sampler, plus the
// HostID the Manager stamps on it before forwarding.
type Frame struct {
	HostID   string  `json:"hostId"`
	T        int64   `json:"t"`
	NCPU     int     `json:"ncpu"`
	MemTotal int64   `json:"memTotal"`
	MemUsed  int64   `json:"memUsed"`
	CPU      float64 `json:"cpu"`
	Mem      float64 `json:"mem"`
	Procs    []Proc  `json:"procs"`
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
	nctx, ncancel := context.WithCancel(s.ctx)
	sess, err := s.client.NewSession()
	if err != nil {
		ncancel()
		return err
	}
	cmd := "nethogs -t -d 1"
	if s.useSudo {
		sess.Stdin = strings.NewReader(s.password + "\n")
		cmd = "sudo -S -p '' nethogs -t -d 1"
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		ncancel()
		sess.Close()
		return err
	}
	if err := sess.Start(cmd); err != nil {
		ncancel()
		sess.Close()
		return err
	}
	s.nhCancel = ncancel
	s.setActive(true)
	s.setNet(map[int]int64{})

	go func() {
		<-nctx.Done()
		sess.Signal(ssh.SIGTERM)
		sess.Close()
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

func recDirFor(s *session) string {
	d := s.stageDir
	if d == "" {
		d = "/var/tmp"
	}
	return d + "/.rtaskmgr-rec"
}

// StartScheduled launches a detached server-side recording that survives the
// client disconnecting and self-stops after durationSec (hard-capped at 7 days).
// The sampler writes gzip NDJSON and aborts early if the disk runs low.
func (m *Manager) StartScheduled(hostID string, durationSec int, hostName string) (RecMeta, error) {
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
	if s.stageDir == "" {
		return RecMeta{}, fmt.Errorf("기록을 저장할 디렉터리를 찾지 못했습니다")
	}
	if !samplerPresent(s.client, s.bin, len(agent.SamplerBinary)) {
		if err := uploadSampler(s.client, s.bin); err != nil {
			return RecMeta{}, fmt.Errorf("샘플러 업로드 실패: %w", err)
		}
	}

	recDir := recDirFor(s)
	now := time.Now()
	id := fmt.Sprintf("rec-%d", now.UnixMilli())
	file := recDir + "/" + id + ".ndjson.gz"
	meta := RecMeta{
		ID: id, HostID: hostID, HostName: hostName, File: file,
		StartT:      now.UnixMilli(),
		PlannedEndT: now.Add(time.Duration(durationSec) * time.Second).UnixMilli(),
		DurationSec: durationSec, IntervalSec: 1, Status: "running",
	}
	metaJSON, _ := json.Marshal(meta)

	if _, err := m.plainRun(s, "mkdir -p "+recDir); err != nil {
		return meta, err
	}
	if err := uploadBytes(s.client, metaJSON, recDir+"/"+id+".meta.json", false); err != nil {
		return meta, err
	}
	// setsid detaches from the SSH session's process group so it keeps running
	// after we disconnect. Paths are app-controlled (no spaces) so no quoting.
	launch := fmt.Sprintf(
		"setsid %s -i 1 -max %d -o %s -minfree 512 >/dev/null 2>&1 </dev/null & echo OK",
		s.bin, durationSec, file)
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
	recDir := recDirFor(s)
	script := `d=` + recDir + `; [ -d "$d" ] || exit 0; ` +
		`for mf in "$d"/*.meta.json; do [ -e "$mf" ] || continue; ` +
		`id=$(basename "$mf" .meta.json); f="$d/$id.ndjson.gz"; ` +
		`sz=$(stat -c%s "$f" 2>/dev/null || echo 0); ` +
		`run=0; pgrep -f "$f" >/dev/null 2>&1 && run=1; ` +
		`echo "STAT|$id|$sz|$run"; echo "META|$(base64 -w0 "$mf")"; done`
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
	file := recDirFor(s) + "/" + id + ".ndjson.gz"
	_, err := m.plainRun(s,
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
	d := recDirFor(s)
	_, err := m.plainRun(s, fmt.Sprintf("rm -f %s/%s.ndjson.gz %s/%s.ndjson.gz.done %s/%s.meta.json; echo done",
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
	file := recDirFor(s) + "/" + id + ".ndjson.gz"
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

// Package monitor owns the live SSH connection to each host: it probes the
// host's capabilities, uploads the embedded sampler, then reads the sampler's
// 1-second NDJSON frames off a single streaming session and hands each frame to
// a callback. One *session per host; the Manager fans out across many.
package monitor

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"rtaskmgr/internal/agent"
	"rtaskmgr/internal/host"
)

// Proc mirrors one row emitted by cmd/sampler. DiskR/DiskW are -1 when the
// sampler could not read /proc/<pid>/io (no permission); Net is -1 until a
// per-process network source (nethogs) is wired in.
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
	HostID   string `json:"hostId"`
	T        int64  `json:"t"`
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
	Nethogs   bool   `json:"nethogs"`  // per-process network available
	Pidstat   bool   `json:"pidstat"`  // sysstat present
	Sudo      bool   `json:"sudo"`     // sampler will run elevated
	StageDir  string `json:"stageDir"` // exec-capable dir for the sampler (/tmp is often noexec)
}

type FrameFunc func(f Frame)

// StatusFunc reports lifecycle transitions for the UI: state is one of
// "connecting", "probing", "uploading", "streaming", "stopped", "error".
type StatusFunc func(hostID, state, detail string)

type session struct {
	client *ssh.Client
	cancel context.CancelFunc
	bin    string // absolute path of the staged sampler on the remote host
}

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*session
	onFrame  FrameFunc
	onStatus StatusFunc
}

func NewManager(onFrame FrameFunc, onStatus StatusFunc) *Manager {
	return &Manager{
		sessions: map[string]*session{},
		onFrame:  onFrame,
		onStatus: onStatus,
	}
}

func (m *Manager) status(id, state, detail string) {
	if m.onStatus != nil {
		m.onStatus(id, state, detail)
	}
}

// Start connects to h, probes it, uploads the sampler, and launches the
// streaming goroutine. It returns the probed capabilities so the UI can render
// which columns are live. Any existing session for the same host is replaced.
func (m *Manager) Start(parent context.Context, h host.Host) (Capabilities, error) {
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
	// Elevated access (= full per-process disk I/O) is available when we're
	// already root or sudo accepted the login password. Wrap the sampler in
	// sudo only when we're not already root.
	useSudoWrapper := caps.UID != 0 && caps.Sudo
	if caps.StageDir == "" {
		client.Close()
		err := fmt.Errorf("no executable directory found on host (/tmp may be noexec)")
		m.status(h.ID, "error", err.Error())
		return caps, err
	}

	bin := caps.StageDir + "/" + agent.RemoteName
	m.status(h.ID, "uploading", "sampler → "+caps.StageDir)
	if err := upload(client, bin); err != nil {
		client.Close()
		m.status(h.ID, "error", "upload: "+err.Error())
		return caps, err
	}

	ctx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	m.sessions[h.ID] = &session{client: client, cancel: cancel, bin: bin}
	m.mu.Unlock()

	go m.stream(ctx, h, client, bin, useSudoWrapper)
	m.status(h.ID, "streaming", "")
	return caps, nil
}

// stream runs the sampler and forwards each NDJSON line as a Frame until ctx is
// cancelled or the connection drops.
func (m *Manager) stream(ctx context.Context, h host.Host, client *ssh.Client, bin string, useSudo bool) {
	defer func() {
		m.mu.Lock()
		if s, ok := m.sessions[h.ID]; ok && s.client == client {
			delete(m.sessions, h.ID)
			client.Close()
		}
		m.mu.Unlock()
	}()

	sess, err := client.NewSession()
	if err != nil {
		m.status(h.ID, "error", "session: "+err.Error())
		return
	}
	defer sess.Close()

	cmd := bin + " 1"
	if useSudo {
		sess.Stdin = strings.NewReader(h.Password + "\n")
		cmd = fmt.Sprintf("sudo -S -p '' %s 1", bin)
	}

	stdout, err := sess.StdoutPipe()
	if err != nil {
		m.status(h.ID, "error", "stdout: "+err.Error())
		return
	}
	if err := sess.Start(cmd); err != nil {
		m.status(h.ID, "error", "start: "+err.Error())
		return
	}

	// Kill the remote sampler when the context is cancelled.
	go func() {
		<-ctx.Done()
		sess.Signal(ssh.SIGTERM)
		sess.Close()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // frames can be large
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
		f.HostID = h.ID
		// Per-process network needs nethogs/eBPF; mark N/A until that path lands.
		for i := range f.Procs {
			f.Procs[i].Net = -1
		}
		m.onFrame(f)
	}
	if ctx.Err() == nil {
		m.status(h.ID, "stopped", "stream ended")
	}
}

// Stop tears down the session for one host (best effort).
func (m *Manager) Stop(hostID string) {
	m.mu.Lock()
	s, ok := m.sessions[hostID]
	if ok {
		delete(m.sessions, hostID)
	}
	m.mu.Unlock()
	if ok {
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

// probeScript reports the host's uid, tooling, OS, and — crucially — the first
// directory that actually allows execution. Hardened RHEL mounts /tmp noexec,
// so we test candidate dirs by writing a tiny script and running it.
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
	// Elevated access: already root, or sudo accepts the login password.
	caps.Sudo = caps.UID == 0 || sudoWorks(client, password)
	return caps, nil
}

// sudoWorks tests whether `sudo -S` accepts the login password (RHEL ops
// accounts typically reuse it). The sampler is then run elevated so it can read
// every process's disk I/O.
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

// upload stages the embedded sampler at the given absolute path by piping a
// base64 stream through `base64 -d` (avoids an sftp dependency and survives any
// shell). Then it makes it executable.
func upload(client *ssh.Client, target string) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	b64 := base64.StdEncoding.EncodeToString(agent.SamplerBinary)
	sess.Stdin = strings.NewReader(b64)
	cmd := fmt.Sprintf("base64 -d > %s && chmod 0700 %s", target, target)
	if out, err := sess.CombinedOutput(cmd); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

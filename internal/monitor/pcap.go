// Remote packet capture (tcpdump) for one connected host.
//
// The capture runs DETACHED on the host (setsid, root) so it survives the app
// closing or the SSH connection dropping, and stops itself on any of three
// guards: max wall time, max file size, minimum free disk. pcapd.sh owns the
// tcpdump child and records why it stopped in "<pcap>.done".
//
// Everything in this file obeys the same invariants (they are why the code looks
// paranoid):
//
//	I1 strings that come back FROM the host are adversarial — any local user on
//	   that box can plant a filename or a df path — so nothing returns to a shell
//	   unvalidated.
//	I2 shellQuote protects ONE layer. Inside `sudo -S bash -c '<inner>'` the inner
//	   text is code to the inner bash, so every interpolated value is quoted and
//	   every generated script starts with `set -u` (never `set -e`: a benign
//	   non-zero in a poll loop must not kill the wrapper).
//	I3 never exec a file from an untrusted directory as root — the wrapper rides
//	   inside the sudo command line as base64 and root unpacks it under /run.
//	I4 the password only ever travels on sudoRun's session stdin, and sudo is used
//	   exactly once per capture (a capture lives for hours; /proc/<pid>/cmdline is
//	   world-readable).
//	I5 never kill someone else's process: no `pkill -x`, and no `pkill -f <path>`
//	   either (that matches our own parent shell, another session's stop script,
//	   and the user's scp/tail — the v1.4.2 F22 family). A normal stop sends no
//	   signal at all (".stop" sentinel); escalation only signals PIDs recorded in
//	   .pid/.tpid after confirming /proc/<pid>/comm.
//	I6 never fabricate state: a stop that never managed to signal anything does
//	   NOT write .done, so it stays honestly "interrupted".
//	I7 every cap has a second enforcer outside the wrapper (`timeout` for time,
//	   `ulimit -f` for bytes) because a detached wrapper cannot be heartbeated.
//	I8 never hold a capture in memory: no CombinedOutput/base64 -w0 on a GB-scale
//	   pcap. Downloads stream.
//
// This file holds the pure, testable half (validation + parsing); the session
// plumbing that uses it lives alongside it.
package monitor

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
)

// pcapdScript is the remote wrapper. It is never staged as a file we then exec as
// root (I3): it rides inside the sudo command line as base64 and root unpacks it
// into a fresh root-only directory under /run.
//
//go:embed pcapd.sh
var pcapdScript []byte

// Capture limits. The time cap is 4h rather than a day because a 24-hour pcap is
// too big to download or analyse, and a longer window only widens the exposure
// of a forgotten capture; long-running observation is a ring-buffer feature, not
// this one.
const (
	MaxCaptureSeconds     = 4 * 3600
	DefaultCaptureSeconds = 600
	MaxCaptureMB          = 10240
	DefaultCaptureMB      = 512
	DefaultMinFreeMB      = 1024
	MinMinFreeMB          = 256
	MaxCapturePackets     = 20000000
	// DefaultCapturePackets is a UI hint, not a default: it is roughly what the
	// phase-2 tshark index handles comfortably (measured ~20.5k pkt/s, ~1.6 KB
	// of tshark RSS per packet). A capture with no packet cap is still bounded by
	// the three real guards.
	DefaultCapturePackets = 2000000
)

// Sidecar/stop-reason vocabulary written by pcapd.sh into "<pcap>.done".
const (
	capReasonDone        = "done"
	capReasonSignal      = "signal"
	capReasonSignalForce = "signal-forced"
	capReasonDeadline    = "deadline"
	capReasonMaxSize     = "maxsize"
	capReasonMaxPackets  = "maxpackets"
	capReasonLowDisk     = "low-disk"
	capReasonOrphan      = "orphan"
	capReasonError       = "error"
	capReasonNoTcpdump   = "no-tcpdump"
)

// NIC is one capturable interface on the host. "any" is a synthesised entry.
type NIC struct {
	Name     string `json:"name"`
	State    string `json:"state"` // operstate: up/down/unknown
	MAC      string `json:"mac"`
	IPv4     string `json:"ipv4"` // CIDR as reported by `ip -4 -o addr`
	SpeedMb  int    `json:"speedMb"`
	MTU      int    `json:"mtu"`
	Up       bool   `json:"up"`
	Virtual  bool   `json:"virtual"`
	Loopback bool   `json:"loopback"`
	Master   string `json:"master"`   // bond0/br0 when this NIC is a slave
	IsMaster bool   `json:"isMaster"` // this NIC is itself a bond/bridge master
}

// CapRequest is what the modal asks for. Every field is clamped and re-validated
// server-side; the frontend gate is UX only.
type CapRequest struct {
	Iface      string `json:"iface"`     // "" or "any" = all interfaces
	PortSpec   string `json:"portSpec"`  // user's raw text, e.g. "3306, 8080-8090"
	HostSpec   string `json:"hostSpec"`  // user's raw text, e.g. "10.0.0.5, 10.1.0.0/24"
	TargetDir  string `json:"targetDir"` // must equal one of CaptureTargets' Paths
	MaxSec     int    `json:"maxSec"`
	MaxMB      int64  `json:"maxMB"`
	MinFreeMB  int    `json:"minFreeMB"`
	MaxPackets int    `json:"maxPackets"` // 0 = no packet cap
	SnapLen    int    `json:"snapLen"`    // 0 = full packet, 96 = headers only, 256
	Promisc    bool   `json:"promisc"`    // default false → tcpdump -p
	NoFilter   bool   `json:"noFilter"`   // explicit opt-in to capture with no port filter
}

// CapMeta describes one capture. The first block is uploaded to the host as
// "<id>.meta.json" at start; the second is derived by ListCaptures and never
// stored (so a host can't dictate it).
type CapMeta struct {
	ID          string `json:"id"`
	HostID      string `json:"hostId"`
	HostName    string `json:"hostName"`
	File        string `json:"file"` // absolute .pcap path on the host
	Iface       string `json:"iface"`
	Filter      string `json:"filter"`   // compiled BPF ("" = no filter)
	PortSpec    string `json:"portSpec"` // canonical port list, for display
	PortsRaw    string `json:"portsRaw"` // exactly what the user typed; never re-expanded
	HostSpec    string `json:"hostSpec"` // canonical address list, for display
	HostsRaw    string `json:"hostsRaw"` // exactly what the user typed; never re-expanded
	SnapLen     int    `json:"snapLen"`
	MaxSec      int    `json:"maxSec"`
	MinFreeMB   int    `json:"minFreeMB"`
	MaxPackets  int    `json:"maxPackets"`
	MaxBytes    int64  `json:"maxBytes"`
	Promisc     bool   `json:"promisc"`
	StartT      int64  `json:"startT"`      // unix millis
	PlannedEndT int64  `json:"plannedEndT"` // unix millis
	Owner       string `json:"owner"`       // login user that owns the files
	OwnerUID    int    `json:"ownerUid"`
	StartedBy   string `json:"startedBy"` // "logan.lee@DESK-07" — who pressed start

	// ---- derived, filled by ListCaptures; not persisted on the host ----
	Status      string `json:"status"`
	DoneReason  string `json:"doneReason"`
	Err         string `json:"err"`
	LinkType    string `json:"linkType"`
	SizeBytes   int64  `json:"sizeBytes"`
	LastT       int64  `json:"lastT"` // file mtime, unix millis
	Uncertain   bool   `json:"uncertain"`
	Captured    int64  `json:"captured"`    // -1 unknown
	Received    int64  `json:"received"`    // -1 unknown
	DroppedKern int64  `json:"droppedKern"` // -1 unknown
	DroppedIf   int64  `json:"droppedIf"`   // -1 unknown (absent on tcpdump 4.9)
	// LocalPath is ALWAYS filled from the local ledger, never from the host's
	// JSON: opening a host-supplied path would hand it a UNC-credential channel.
	LocalPath string `json:"localPath"`
}

// CapEnv is the modal's pre-flight: what this host can do, where a capture can
// go, and what is already there.
type CapEnv struct {
	Elevated      bool        `json:"elevated"`
	Tcpdump       bool        `json:"tcpdump"`
	TcpdumpPath   string      `json:"tcpdumpPath"`
	Version       string      `json:"version"`
	NICs          []NIC       `json:"nics"`
	Targets       []RecTarget `json:"targets"`
	MaxSec        int         `json:"maxSec"`
	MaxMB         int64       `json:"maxMB"`
	MaxPackets    int         `json:"maxPackets"`
	ExistingCount int         `json:"existingCount"`
	ExistingBytes int64       `json:"existingBytes"`
	Running       int         `json:"running"`
	Owner         string      `json:"owner"`
	OwnerUID      int         `json:"ownerUid"`
}

// CapCounters is what tcpdump's stderr tells us after the fact.
type CapCounters struct {
	Captured    int64  `json:"captured"`
	Received    int64  `json:"received"`
	DroppedKern int64  `json:"droppedKern"`
	DroppedIf   int64  `json:"droppedIf"`
	LinkType    string `json:"linkType"`
	Err         string `json:"err"`
}

// PortSpec is a validated port selection. Ranges is normalised (sorted, merged,
// every lo<=hi) and is the ONLY thing the BPF filter is built from — Raw exists
// for the meta file and the screen and is never re-parsed into a command.
type PortSpec struct {
	All    bool
	Ranges [][2]int
	Raw    string
}

// hostTerm is one validated address term of the filter: an address (`host X`) or
// a network (`net X/len`).
type hostTerm struct {
	Net   bool
	Value string // canonical: net.IP.String() or net.IPNet.String() or a hostname
}

// HostSpec is a validated address selection, the other half of what an operator
// types at a shell as `host 10.0.0.5 and port 3306`. Like PortSpec, the filter is
// rebuilt from the parsed terms and Raw never reaches a command.
type HostSpec struct {
	All   bool
	Terms []hostTerm
	Raw   string
}

// Every pattern is anchored: a partial match is not a validation.
var (
	capIDRe     = regexp.MustCompile(`^pcap-[0-9]{10,17}-[0-9a-f]{8}$`)
	ifNameRe    = regexp.MustCompile(`^[A-Za-z0-9._:@-]{1,15}$`)
	unixUserRe  = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
	portTokRe   = regexp.MustCompile(`^([0-9]{1,5})(?:-([0-9]{1,5}))?$`)
	hostLabelRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	digitsDotRe = regexp.MustCompile(`^[0-9.]+$`)
	// bpfOutRe is the post-assembly whitelist. It has to admit the characters an
	// address term needs (dots, colons, slashes) and the parentheses that force
	// `(hosts) and (ports)` precedence — `and` binds tighter than `or` in pcap
	// syntax, so without them "host A or host B and port 80" would silently mean
	// "host A or (host B and port 80)". Every shell metacharacter stays excluded.
	bpfOutRe   = regexp.MustCompile(`^[0-9A-Za-z .:/()\- ]*$`)
	linkTypeRe = regexp.MustCompile(`link-type ([A-Za-z0-9_]+)`)
)

// maxPortRanges caps how many "or" clauses the BPF may hold. A huge filter is
// both a compile risk in the kernel and a sign of a paste accident.
const maxPortRanges = 64

// maxHostTerms caps the address list for the same reason.
const maxHostTerms = 32

// shArgs single-quotes each element and joins them with spaces, so a value can
// never split into two arguments inside the remote `bash -c`.
func shArgs(v ...string) string {
	out := make([]string, len(v))
	for i, s := range v {
		out[i] = shellQuote(s)
	}
	return strings.Join(out, " ")
}

// validCapID gates every id that reaches a shell. Ids come back from the
// frontend AND are read off host filenames, so a directory holding
// "$(reboot).meta.json" would otherwise be a root shell injection.
func validCapID(id string) bool {
	return len(id) <= 40 && capIDRe.MatchString(id)
}

// validIfName mirrors the kernel's IFNAMSIZ-1 limit and the character set real
// interface names use (eth0, ens192, bond0.100, br-ex, any, lo).
func validIfName(n string) bool { return ifNameRe.MatchString(n) }

// validUnixUser accepts the shapes useradd produces. A name that fails this is
// not fatal: the capture path falls back to the numeric uid (which the probe
// always has) and simply skips tcpdump's -Z.
func validUnixUser(u string) bool { return unixUserRe.MatchString(u) }

// validAbsPath accepts a canonical absolute path built from plain segments. It
// deliberately rejects spaces, "..", "." and trailing slashes: everything we
// interpolate is either app-generated or one of the probe's base dirs, and a
// path we cannot vouch for should fail loudly at the start instead of quietly
// resolving somewhere else later.
func validAbsPath(p string) bool {
	if p == "" || len(p) > 4096 || p[0] != '/' {
		return false
	}
	for _, seg := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for _, r := range seg {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
				r == '.' || r == '_' || r == '-'
			if !ok {
				return false
			}
		}
	}
	return true
}

// parsePortSpec turns the user's text into a normalised port selection.
//
// The separators are ONLY "," and whitespace. That is the whole trick: because
// ";", "|", "$" and "`" are not separators, an injection attempt stays inside a
// single token and is rejected by the token pattern instead of quietly becoming
// two valid ports. Reversed ranges are refused rather than swapped — silently
// "fixing" 1237-1234 hides a typo in a filter the user will trust.
func parsePortSpec(in string) (PortSpec, error) {
	if len(in) > 1024 {
		return PortSpec{}, fmt.Errorf("포트 입력이 너무 깁니다 (최대 1024자)")
	}
	if strings.TrimSpace(in) == "" {
		return PortSpec{All: true}, nil
	}
	toks := strings.FieldsFunc(in, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	if len(toks) == 0 {
		return PortSpec{All: true}, nil
	}
	if len(toks) > 256 {
		return PortSpec{}, fmt.Errorf("포트 항목이 너무 많습니다 (최대 256개)")
	}
	rs := make([][2]int, 0, len(toks))
	for _, t := range toks {
		mm := portTokRe.FindStringSubmatch(t)
		if mm == nil {
			return PortSpec{}, fmt.Errorf("포트 형식이 아닙니다: %q (예: 3306 또는 8080-8090)", t)
		}
		lo, _ := strconv.Atoi(mm[1])
		hi := lo
		if mm[2] != "" {
			hi, _ = strconv.Atoi(mm[2])
		}
		if lo < 1 || lo > 65535 || hi < 1 || hi > 65535 {
			return PortSpec{}, fmt.Errorf("포트는 1~65535 범위여야 합니다: %q", t)
		}
		if hi < lo {
			return PortSpec{}, fmt.Errorf("포트 범위의 시작이 끝보다 큽니다: %q (자동으로 바꾸지 않습니다)", t)
		}
		rs = append(rs, [2]int{lo, hi})
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i][0] != rs[j][0] {
			return rs[i][0] < rs[j][0]
		}
		return rs[i][1] < rs[j][1]
	})
	merged := make([][2]int, 0, len(rs))
	for _, r := range rs {
		if n := len(merged); n > 0 && r[0] <= merged[n-1][1]+1 {
			if r[1] > merged[n-1][1] {
				merged[n-1][1] = r[1]
			}
			continue
		}
		merged = append(merged, r)
	}
	if len(merged) > maxPortRanges {
		return PortSpec{}, fmt.Errorf("포트 구간이 너무 많습니다 (병합 후 %d개, 최대 %d개)", len(merged), maxPortRanges)
	}
	return PortSpec{Ranges: merged, Raw: in}, nil
}

// parseHostSpec turns the user's text into a validated address selection — the
// `host 10.0.0.5` half of what they would type at a shell.
//
// It accepts an IPv4/IPv6 address, a CIDR network, or a hostname, separated the
// same way ports are (comma or whitespace and nothing else, so an injection
// attempt stays inside one token and is rejected by the pattern). A CIDR is
// canonicalised to its network address, because that is what `net` actually
// matches — showing the operator "10.0.0.0/24" when they typed "10.0.0.5/24"
// is the honest thing to do.
//
// A token of only digits and dots that is not a valid IP is reported as a bad
// address rather than quietly becoming a DNS lookup: "10.0.0" is a typo, not a
// hostname.
func parseHostSpec(in string) (HostSpec, error) {
	if len(in) > 1024 {
		return HostSpec{}, fmt.Errorf("호스트 입력이 너무 깁니다 (최대 1024자)")
	}
	if strings.TrimSpace(in) == "" {
		return HostSpec{All: true}, nil
	}
	toks := strings.FieldsFunc(in, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	if len(toks) == 0 {
		return HostSpec{All: true}, nil
	}
	if len(toks) > maxHostTerms {
		return HostSpec{}, fmt.Errorf("호스트 항목이 너무 많습니다 (최대 %d개)", maxHostTerms)
	}
	terms := make([]hostTerm, 0, len(toks))
	seen := map[string]bool{}
	for _, t := range toks {
		term, err := parseHostTerm(t)
		if err != nil {
			return HostSpec{}, err
		}
		key := strconv.FormatBool(term.Net) + "|" + term.Value
		if seen[key] {
			continue
		}
		seen[key] = true
		terms = append(terms, term)
	}
	return HostSpec{Terms: terms, Raw: in}, nil
}

// bpfKeywords are the pcap-filter words an operator is most likely to paste in
// out of habit ("host 10.0.0.5"). They are technically legal short hostnames, so
// without this they would sail through and only fail much later as an
// unresolvable name — after the capture appeared to start. Rejecting them here
// turns that into an instant, instructive inline error.
var bpfKeywords = map[string]bool{
	"host": true, "net": true, "port": true, "portrange": true, "src": true,
	"dst": true, "and": true, "or": true, "not": true, "ip": true, "ip6": true,
	"tcp": true, "udp": true, "icmp": true, "ether": true, "gateway": true,
	"less": true, "greater": true, "vlan": true, "mask": true, "proto": true,
}

func parseHostTerm(t string) (hostTerm, error) {
	if bpfKeywords[strings.ToLower(t)] {
		return hostTerm{}, fmt.Errorf("%q 는 tcpdump 필터 키워드입니다 — 여기에는 주소만 적으세요 "+
			"(예: `host 10.0.0.5` 대신 `10.0.0.5`)", t)
	}
	if strings.Contains(t, "/") {
		_, ipnet, err := net.ParseCIDR(t)
		if err != nil {
			return hostTerm{}, fmt.Errorf("네트워크 표기가 아닙니다: %q (예: 10.0.0.0/24)", t)
		}
		return hostTerm{Net: true, Value: ipnet.String()}, nil
	}
	if ip := net.ParseIP(t); ip != nil {
		return hostTerm{Value: ip.String()}, nil
	}
	if digitsDotRe.MatchString(t) {
		return hostTerm{}, fmt.Errorf("IP 주소 형식이 아닙니다: %q", t)
	}
	if err := validHostname(t); err != nil {
		return hostTerm{}, fmt.Errorf("호스트 형식이 아닙니다: %q — %v (예: 10.0.0.5, 10.0.0.0/24, db1.example.com)", t, err)
	}
	return hostTerm{Value: strings.ToLower(t)}, nil
}

// validHostname allows the shapes a resolver will actually accept. tcpdump
// resolves the name when it compiles the filter, so a name that does not resolve
// fails the capture start with tcpdump's own message.
func validHostname(h string) error {
	if h == "" || len(h) > 253 {
		return fmt.Errorf("길이가 1~253자여야 합니다")
	}
	h = strings.TrimSuffix(h, ".")
	for _, label := range strings.Split(h, ".") {
		if !hostLabelRe.MatchString(label) {
			return fmt.Errorf("허용되지 않는 라벨: %q", label)
		}
	}
	return nil
}

// bpfWords renders the address terms as argv words built from fixed keywords and
// canonical values only.
func (h HostSpec) bpfWords() ([]string, error) {
	if h.All || len(h.Terms) == 0 {
		return nil, nil
	}
	if len(h.Terms) > maxHostTerms {
		return nil, fmt.Errorf("내부 오류: 호스트 항목 %d개", len(h.Terms))
	}
	words := make([]string, 0, len(h.Terms)*3)
	for i, t := range h.Terms {
		if t.Value == "" {
			return nil, fmt.Errorf("내부 오류: 빈 호스트 항목")
		}
		if i > 0 {
			words = append(words, "or")
		}
		if t.Net {
			words = append(words, "net", t.Value)
		} else {
			words = append(words, "host", t.Value)
		}
	}
	return words, nil
}

// canonical renders the normalised address selection for the UI ("" = any host).
func (h HostSpec) canonical() string {
	if h.All || len(h.Terms) == 0 {
		return ""
	}
	parts := make([]string, 0, len(h.Terms))
	for _, t := range h.Terms {
		parts = append(parts, t.Value)
	}
	return strings.Join(parts, ", ")
}

// captureFilter combines the two halves the way an operator would write them by
// hand — `host X and port Y` — and re-validates the result.
//
// The parentheses are load-bearing: `and` binds tighter than `or` in pcap syntax,
// so two hosts and one port without them would parse as "host A or (host B and
// port 3306)" and quietly capture everything from host A.
func captureFilter(hs HostSpec, ps PortSpec) (words []string, display string, err error) {
	hw, err := hs.bpfWords()
	if err != nil {
		return nil, "", err
	}
	pw, err := ps.bpfWords()
	if err != nil {
		return nil, "", err
	}
	group := func(w []string) []string {
		// One term needs no parentheses; an "or" list does.
		if len(w) <= 2 {
			return w
		}
		out := make([]string, 0, len(w)+2)
		out = append(out, "(")
		out = append(out, w...)
		return append(out, ")")
	}
	switch {
	case len(hw) == 0 && len(pw) == 0:
		return nil, "", nil
	case len(pw) == 0:
		words = hw
	case len(hw) == 0:
		words = pw
	default:
		words = append(append(group(hw), "and"), group(pw)...)
	}
	display = strings.Join(words, " ")
	if !bpfOutRe.MatchString(display) {
		return nil, "", fmt.Errorf("내부 오류: BPF 필터 검증 실패")
	}
	return words, display, nil
}

// bpfWords builds the tcpdump filter as separate argv words, assembled purely
// from integers and fixed keywords. There is no path by which the user's text
// reaches tcpdump or a shell — which is what TestPortSpecBPFNeverLeaksInput
// pins down.
func (p PortSpec) bpfWords() ([]string, error) {
	if p.All || len(p.Ranges) == 0 {
		return nil, nil
	}
	if len(p.Ranges) > maxPortRanges {
		return nil, fmt.Errorf("내부 오류: 포트 구간 %d개", len(p.Ranges))
	}
	words := make([]string, 0, len(p.Ranges)*3)
	for i, r := range p.Ranges {
		if r[0] < 1 || r[1] > 65535 || r[1] < r[0] {
			return nil, fmt.Errorf("내부 오류: 잘못된 포트 구간 %d-%d", r[0], r[1])
		}
		if i > 0 {
			words = append(words, "or")
		}
		if r[0] == r[1] {
			words = append(words, "port", strconv.Itoa(r[0]))
		} else {
			words = append(words, "portrange", strconv.Itoa(r[0])+"-"+strconv.Itoa(r[1]))
		}
	}
	return words, nil
}

// bpf is bpfWords joined for display, storage and audit, re-validated after the
// fact so a future edit that lets anything else through fails here.
func (p PortSpec) bpf() (string, error) {
	words, err := p.bpfWords()
	if err != nil {
		return "", err
	}
	out := strings.Join(words, " ")
	if !bpfOutRe.MatchString(out) {
		return "", fmt.Errorf("내부 오류: BPF 필터 검증 실패")
	}
	return out, nil
}

// canonical renders the normalised selection back for the UI ("" = all ports).
func (p PortSpec) canonical() string {
	if p.All || len(p.Ranges) == 0 {
		return ""
	}
	parts := make([]string, 0, len(p.Ranges))
	for _, r := range p.Ranges {
		if r[0] == r[1] {
			parts = append(parts, strconv.Itoa(r[0]))
		} else {
			parts = append(parts, strconv.Itoa(r[0])+"-"+strconv.Itoa(r[1]))
		}
	}
	return strings.Join(parts, ", ")
}

// clampCapRequest normalises the request into the supported envelope. A packet
// cap of 0 stays 0 (unlimited): the three guards the user asked for — time, size,
// free disk — are what bound a capture, and silently capping packets would cut a
// small-packet capture off at a fraction of the size the user chose.
func clampCapRequest(r CapRequest) CapRequest {
	r.Iface = strings.TrimSpace(r.Iface)
	r.TargetDir = strings.TrimSpace(r.TargetDir)

	if r.MaxSec <= 0 {
		r.MaxSec = DefaultCaptureSeconds
	}
	if r.MaxSec > MaxCaptureSeconds {
		r.MaxSec = MaxCaptureSeconds
	}
	if r.MaxMB <= 0 {
		r.MaxMB = DefaultCaptureMB
	}
	if r.MaxMB > MaxCaptureMB {
		r.MaxMB = MaxCaptureMB
	}
	if r.MinFreeMB <= 0 {
		r.MinFreeMB = DefaultMinFreeMB
	}
	if r.MinFreeMB < MinMinFreeMB {
		r.MinFreeMB = MinMinFreeMB
	}
	if r.MaxPackets < 0 {
		r.MaxPackets = 0
	}
	if r.MaxPackets > MaxCapturePackets {
		r.MaxPackets = MaxCapturePackets
	}
	if r.SnapLen < 0 {
		r.SnapLen = 0
	}
	if r.SnapLen > 0 && r.SnapLen < 64 {
		r.SnapLen = 64
	}
	if r.SnapLen > 262144 {
		r.SnapLen = 262144
	}
	return r
}

// parseNICLine parses one "N|" row of the interface probe:
//
//	N|name|operstate|flags|mac|mtu|speed|virtual|master|isMaster|ipv4
//
// A row whose name would not survive validIfName is dropped, so the UI can never
// offer an interface we would refuse to start on.
func parseNICLine(line string) (NIC, bool) {
	if !strings.HasPrefix(line, "N|") {
		return NIC{}, false
	}
	// The ipv4 field is last and takes the SplitN remainder, so an unexpected "|"
	// in it cannot shift every other column.
	p := strings.SplitN(line[2:], "|", 10)
	if len(p) != 10 {
		return NIC{}, false
	}
	name := strings.TrimSpace(p[0])
	if !validIfName(name) {
		return NIC{}, false
	}
	n := NIC{
		Name:  name,
		State: strings.TrimSpace(p[1]),
		MAC:   strings.TrimSpace(p[3]),
		IPv4:  strings.TrimSpace(p[9]),
	}
	// /sys/class/net/<n>/flags is hex ("0x1003"); bit 0 = IFF_UP, bit 3 =
	// IFF_LOOPBACK. operstate alone is not enough — tun/bond devices report
	// "unknown" while being perfectly up.
	flags := int64(0)
	if f := strings.TrimSpace(p[2]); strings.HasPrefix(f, "0x") || strings.HasPrefix(f, "0X") {
		flags, _ = strconv.ParseInt(f[2:], 16, 64)
	} else {
		flags, _ = strconv.ParseInt(f, 16, 64)
	}
	n.Up = n.State == "up" || (n.State != "down" && flags&0x1 != 0)
	n.Loopback = flags&0x8 != 0 || name == "lo"
	n.MTU, _ = strconv.Atoi(strings.TrimSpace(p[4]))
	if sp, err := strconv.Atoi(strings.TrimSpace(p[5])); err == nil && sp > 0 {
		n.SpeedMb = sp
	} else {
		n.SpeedMb = -1
	}
	n.Virtual = strings.TrimSpace(p[6]) == "1"
	if ms := strings.TrimSpace(p[7]); ms != "" && validIfName(ms) {
		n.Master = ms
	}
	n.IsMaster = strings.TrimSpace(p[8]) == "1"
	return n, true
}

// parseCapLog reads tcpdump's stderr (kept in "<pcap>.log") for the packet
// counters, the link type and the first real error. tcpdump 4.9 on RHEL 8 does
// not print "dropped by interface" at all, so an absent counter stays -1 (N/A)
// rather than becoming a misleading 0.
func parseCapLog(s string) CapCounters {
	c := CapCounters{Captured: -1, Received: -1, DroppedKern: -1, DroppedIf: -1}
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" {
			continue
		}
		if m := linkTypeRe.FindStringSubmatch(line); m != nil {
			c.LinkType = m[1]
		}
		if n, ok := packetCount(line, "packets captured"); ok {
			c.Captured = n
			continue
		}
		if n, ok := packetCount(line, "packets received by filter"); ok {
			c.Received = n
			continue
		}
		if n, ok := packetCount(line, "packets dropped by kernel"); ok {
			c.DroppedKern = n
			continue
		}
		if n, ok := packetCount(line, "packets dropped by interface"); ok {
			c.DroppedIf = n
			continue
		}
		if c.Err == "" && capLogIsError(line) {
			c.Err = line
		}
	}
	return c
}

// packetCount reads the leading integer of a "<n> <suffix>" counter line.
func packetCount(line, suffix string) (int64, bool) {
	if !strings.HasSuffix(line, suffix) {
		return 0, false
	}
	num := strings.TrimSpace(strings.TrimSuffix(line, suffix))
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// capLogIsError distinguishes tcpdump's real failures from its normal chatter
// ("listening on eth0, link-type ...", "verbose output suppressed").
func capLogIsError(line string) bool {
	low := strings.ToLower(line)
	for _, noise := range []string{
		"listening on", "verbose output", "reading from file", "data link type",
		"packets captured", "packets received", "packets dropped", "warning:",
	} {
		if strings.Contains(low, noise) {
			return false
		}
	}
	return strings.HasPrefix(line, "tcpdump: ") || strings.HasPrefix(line, "rtm-pcapd: ") ||
		strings.Contains(low, "error") || strings.Contains(low, "permission denied") ||
		strings.Contains(low, "no such device") || strings.Contains(low, "syntax error")
}

// capStatusFrom turns the three host-side observations into a status.
//
// alive is deliberately three-valued, and that is the whole point: 0 = /proc has
// no such pid (really dead), 1 = alive and confirmed ours, 2 = /proc entry exists
// but is unreadable (hidepid). Collapsing 0 and 2 would freeze a capture killed
// by a reboot as "running" forever, and the next Stop would then write
// .done=signal — promoting a truncated file to "cleanly stopped" (I6).
//
// haveStop is needed for the "stopping" row: the sentinel is there but the
// wrapper has not finished flushing yet.
func capStatusFrom(alive int, haveDone, haveStop bool, reason string) (status string, uncertain bool) {
	if haveDone {
		switch reason {
		case capReasonSignal, capReasonSignalForce:
			return "stopped", false
		case capReasonDeadline:
			return capReasonDeadline, false
		case capReasonMaxSize:
			return capReasonMaxSize, false
		case capReasonMaxPackets:
			return capReasonMaxPackets, false
		case capReasonLowDisk:
			return capReasonLowDisk, false
		case capReasonOrphan:
			return capReasonOrphan, false
		case capReasonError, capReasonNoTcpdump:
			return capReasonError, false
		default:
			return capReasonDone, false
		}
	}
	if alive == 0 {
		// The process is gone and it never wrote a reason → killed abnormally
		// (reboot / OOM / SIGKILL). The pcap is truncated; say so.
		return "interrupted", false
	}
	if haveStop {
		return "stopping", alive == 2
	}
	return "running", alive == 2
}

// newCapID mints "pcap-<unix millis>-<8 hex>". The random tail keeps two clients
// starting in the same millisecond from colliding on a filename, and the whole
// thing is shaped to satisfy validCapID.
func newCapID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to the clock's low bits; uniqueness, not secrecy, is the goal.
		n := time.Now().UnixNano()
		b = [4]byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
	}
	return fmt.Sprintf("pcap-%d-%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}

// ---- host-side layout --------------------------------------------------

// pcapDirName is appended to the chosen target so captures never share a
// directory with anything else we might delete.
const pcapDirName = ".rtaskmgr-pcap"

// capLite is the cheap per-capture observation the session watcher polls.
type capLite struct {
	size     int64
	alive    int // 0 = gone, 1 = confirmed ours, 2 = unreadable (hidepid)
	haveDone bool
	haveStop bool
	reason   string
}

// pcapBasesSh is the shell-expanded list of base dirs to SEARCH for captures.
//
// It is deliberately wider than the set we allow writing to: probeScript
// recomputes stageDir on every reconnect, so a capture started in
// /run/user/1000 during one session would become an orphan in the next if we
// only looked where we would write today.
func pcapBasesSh(s *session) string {
	b := ` "$HOME" /var/tmp "/run/user/$(id -u)" /dev/shm /tmp /data /home`
	if s.stageDir != "" && validAbsPath(s.stageDir) {
		b = shellQuote(s.stageDir) + b
	}
	return b
}

// trustedDirSh emits a shell function that decides whether a capture directory is
// one we may act on at all: a real directory (not a symlink) owned by the login
// uid with no group/other write bit — the same test ensureUserDirStrict applies
// when it CREATES one.
//
// Every scan and every resolve has to run this, not just the create path. The
// search bases deliberately include /tmp, /var/tmp and /dev/shm so a capture
// started under an earlier stageDir is never orphaned — and those are mode 1777.
// Without this test any local user could mkdir /tmp/.rtaskmgr-pcap, plant a
// plausible <id>.meta.json plus a fake pid whose cmdline contains the path, and
// have the row appear as "stopping". The operator then presses 강제 중지 and root
// redirects into a path that user controls: `> <id>.pcap.lock` pointed at
// /etc/shadow truncates it. Refusing the directory closes the whole family.
func trustedDirSh(s *session) string {
	return `rtm_trusted(){ d=$1; ` +
		`[ -L "$d" ] && return 1; ` +
		`[ -d "$d" ] || return 1; ` +
		`o=$(stat -c%u "$d" 2>/dev/null) || return 1; ` +
		`[ "$o" = "` + strconv.Itoa(s.uid) + `" ] || return 1; ` +
		`pm=$(stat -c%a "$d" 2>/dev/null) || return 1; ` +
		`[ "$(( 0$pm & 022 ))" = 0 ] || return 1; ` +
		`return 0; }; `
}

// captureTargets narrows the scheduled-recording targets to what a packet
// capture may write to: no tmpfs/ramfs (tcpdump at line rate would burn host RAM
// and hand the OOM killer a production process), and no path we could not vouch
// for. The "/" mount is kept — the UI warns instead, since on some hosts it is
// the only roomy filesystem.
func (m *Manager) captureTargets(s *session) []RecTarget {
	src := m.recTargets(s)
	out := make([]RecTarget, 0, len(src))
	for _, t := range src {
		switch t.FSType {
		case "tmpfs", "ramfs":
			continue
		}
		if !validAbsPath(t.Path) { // I1: this path came back from the host
			continue
		}
		out = append(out, t)
	}
	return out
}

// defaultCaptureTarget prefers the roomiest non-root filesystem; it never
// defaults to stageDir, which may well be tmpfs.
func defaultCaptureTarget(ts []RecTarget) *RecTarget {
	var best *RecTarget
	for i := range ts {
		t := &ts[i]
		if best == nil {
			best = t
			continue
		}
		bestRoot, tRoot := best.Mount == "/", t.Mount == "/"
		if bestRoot != tRoot {
			if bestRoot {
				best = t
			}
			continue
		}
		if t.FreeBytes > best.FreeBytes {
			best = t
		}
	}
	return best
}

// ---- interface + capability probe --------------------------------------

// nicProbeScript emits one "N|" row per interface straight out of sysfs, plus the
// primary IPv4 from `ip`. Every value is re-validated by parseNICLine.
const nicProbeScript = `set -u
for p in /sys/class/net/*; do
  [ -d "$p" ] || continue
  n=${p##*/}
  fl=$(cat "$p/flags" 2>/dev/null || echo 0x0)
  op=$(cat "$p/operstate" 2>/dev/null || echo unknown)
  mac=$(cat "$p/address" 2>/dev/null || echo)
  mtu=$(cat "$p/mtu" 2>/dev/null || echo 0)
  sp=$(cat "$p/speed" 2>/dev/null || echo -1)
  vir=0; case "$(readlink -f "$p" 2>/dev/null)" in */virtual/*) vir=1;; esac
  ms=; [ -e "$p/master" ] && ms=$(basename "$(readlink -f "$p/master" 2>/dev/null)" 2>/dev/null)
  im=0; { [ -d "$p/bonding" ] || [ -d "$p/bridge" ]; } && im=1
  ip4=$(ip -4 -o addr show dev "$n" 2>/dev/null | awk 'NR==1{print $4}')
  printf 'N|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s\n' "$n" "$op" "$fl" "$mac" "$mtu" "$sp" "$vir" "$ms" "$im" "$ip4"
done`

// captureNICs lists the host's interfaces, newest kernel view each call, with a
// synthetic "any" entry first.
func (m *Manager) captureNICs(s *session) []NIC {
	out, _ := m.plainRun(s, nicProbeScript)
	nics := []NIC{{Name: "any", State: "up", Up: true, SpeedMb: -1}}
	for _, line := range strings.Split(out, "\n") {
		if n, ok := parseNICLine(strings.TrimRight(strings.TrimSpace(line), "\r")); ok {
			nics = append(nics, n)
		}
	}
	// Physical, up interfaces first; loopback and virtual last.
	rest := nics[1:]
	sort.SliceStable(rest, func(i, j int) bool {
		a, b := rest[i], rest[j]
		ai := (map[bool]int{true: 0, false: 1})[a.Up && !a.Virtual && !a.Loopback]
		bi := (map[bool]int{true: 0, false: 1})[b.Up && !b.Virtual && !b.Loopback]
		if ai != bi {
			return ai < bi
		}
		return a.Name < b.Name
	})
	return nics
}

// tcpdumpProbeScript resolves tcpdump the same way the wrapper does, so what the
// modal reports is what the capture will actually run.
const tcpdumpProbeScript = `set -u
tb=$(command -v tcpdump 2>/dev/null)
[ -n "$tb" ] || for p in /usr/sbin/tcpdump /sbin/tcpdump /usr/bin/tcpdump; do
  [ -x "$p" ] && { tb=$p; break; }
done
[ -n "$tb" ] || { echo "TD||"; exit 0; }
v=$("$tb" --version 2>&1 | head -1 | tr -d '|')
echo "TD|$tb|$v"`

// CaptureEnv is the modal's pre-flight for one host: what it can do, where a
// capture may go, and what is already sitting there.
func (m *Manager) CaptureEnv(hostID string) (CapEnv, error) {
	s := m.get(hostID)
	if s == nil {
		return CapEnv{}, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	env := CapEnv{
		Elevated: s.elevated, MaxSec: MaxCaptureSeconds, MaxMB: MaxCaptureMB,
		MaxPackets: MaxCapturePackets, Owner: s.user, OwnerUID: s.uid,
	}
	if out, _ := m.plainRun(s, tcpdumpProbeScript); out != "" {
		for _, line := range strings.Split(out, "\n") {
			if !strings.HasPrefix(line, "TD|") {
				continue
			}
			p := strings.SplitN(strings.TrimSpace(line[3:]), "|", 2)
			if len(p) == 2 && validAbsPath(p[0]) {
				env.Tcpdump = true
				env.TcpdumpPath = p[0]
				env.Version = strings.TrimSpace(p[1])
			}
		}
	}
	env.NICs = m.captureNICs(s)
	env.Targets = m.captureTargets(s)
	if lite, err := m.listCapturesLite(s); err == nil {
		for _, l := range lite {
			env.ExistingCount++
			env.ExistingBytes += l.size
			if st, _ := capStatusFrom(l.alive, l.haveDone, l.haveStop, l.reason); st == "running" || st == "stopping" {
				env.Running++
			}
		}
	}
	return env, nil
}

// ---- start -------------------------------------------------------------

// StartCapture launches one detached capture on the host.
//
// The order below is fixed and each step exists because of a specific failure:
// the concurrency check keeps two captures from silently sharing one disk budget;
// the target allowlist stops a fabricated path from reaching a root shell; the
// size gate refuses a capture that cannot fit; and the start verification exists
// because a detached launch is successful BY DEFINITION — without it a typo in
// the interface name is reported thirty minutes later as "the server rebooted".
func (m *Manager) StartCapture(hostID string, req CapRequest, hostName, startedBy string) (CapMeta, error) {
	s := m.get(hostID)
	if s == nil {
		return CapMeta{}, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	if !s.elevated {
		return CapMeta{}, fmt.Errorf("패킷 캡쳐에는 root 또는 sudo 권한이 필요합니다")
	}
	// One capture per host, enforced here — the frontend gate is UX only.
	if lite, err := m.listCapturesLite(s); err == nil {
		for _, l := range lite {
			if st, _ := capStatusFrom(l.alive, l.haveDone, l.haveStop, l.reason); st == "running" || st == "stopping" {
				return CapMeta{}, fmt.Errorf("이미 이 서버에서 캡쳐가 실행 중입니다. 먼저 중지하세요")
			}
		}
	}

	req = clampCapRequest(req)
	iface := req.Iface
	if iface == "" {
		iface = "any" // substituted before any shell assembly, never after
	}
	if !validIfName(iface) {
		return CapMeta{}, fmt.Errorf("사용할 수 없는 인터페이스 이름입니다: %q", req.Iface)
	}
	spec, err := parsePortSpec(req.PortSpec)
	if err != nil {
		return CapMeta{}, err
	}
	hosts, err := parseHostSpec(req.HostSpec)
	if err != nil {
		return CapMeta{}, err
	}
	if spec.All && hosts.All && !req.NoFilter {
		return CapMeta{}, fmt.Errorf("호스트도 포트도 지정하지 않았습니다. 전체 트래픽을 캡쳐하려면 '필터 없이 진행'을 켜세요")
	}
	words, filter, err := captureFilter(hosts, spec)
	if err != nil {
		return CapMeta{}, err
	}

	targets := m.captureTargets(s)
	if len(targets) == 0 {
		return CapMeta{}, fmt.Errorf("캡쳐를 저장할 디스크 기반 위치를 찾지 못했습니다 (tmpfs/ramfs는 제외됩니다)")
	}
	var chosen *RecTarget
	if req.TargetDir == "" {
		chosen = defaultCaptureTarget(targets)
	} else {
		for i := range targets {
			if targets[i].Path == req.TargetDir { // exact match against what we offered
				chosen = &targets[i]
				break
			}
		}
	}
	if chosen == nil {
		return CapMeta{}, fmt.Errorf("허용되지 않은 저장 위치입니다: %s", req.TargetDir)
	}
	maxBytes := req.MaxMB * 1024 * 1024
	minFree := int64(req.MinFreeMB) * 1024 * 1024
	if chosen.FreeBytes > 0 && maxBytes > chosen.FreeBytes-minFree {
		return CapMeta{}, fmt.Errorf("선택한 위치의 여유(%s)로는 최대 용량 %s와 최소 여유 %s를 함께 담을 수 없습니다",
			humanBytes(chosen.FreeBytes), humanBytes(maxBytes), humanBytes(minFree))
	}

	dir := chosen.Path + "/" + pcapDirName
	if err := m.ensureUserDirStrict(s, dir, "0700"); err != nil {
		return CapMeta{}, err
	}

	id := newCapID()
	file := dir + "/" + id + ".pcap"
	if !validAbsPath(file) {
		return CapMeta{}, fmt.Errorf("저장 경로를 사용할 수 없습니다: %s", file)
	}
	now := time.Now()
	meta := CapMeta{
		ID: id, HostID: hostID, HostName: hostName, File: file,
		Iface: iface, Filter: filter,
		PortSpec: spec.canonical(), PortsRaw: spec.Raw,
		HostSpec: hosts.canonical(), HostsRaw: hosts.Raw,
		SnapLen: req.SnapLen, MaxSec: req.MaxSec, MinFreeMB: req.MinFreeMB,
		MaxPackets: req.MaxPackets, MaxBytes: maxBytes, Promisc: req.Promisc,
		StartT:      now.UnixMilli(),
		PlannedEndT: now.Add(time.Duration(req.MaxSec) * time.Second).UnixMilli(),
		Owner:       s.user, OwnerUID: s.uid, StartedBy: startedBy,
		Captured: -1, Received: -1, DroppedKern: -1, DroppedIf: -1,
	}
	metaJSON, _ := json.Marshal(meta)
	if err := uploadBytes(s.client, metaJSON, dir+"/"+id+".meta.json", false); err != nil {
		return meta, fmt.Errorf("캡쳐 정보 업로드 실패: %w", err)
	}
	if err := m.launchCapture(s, meta, iface, words); err != nil {
		_ = m.deleteCaptureFiles(s, dir, id)
		return meta, err
	}
	if err := m.verifyCaptureStart(s, file); err != nil {
		// Clean up the debris so a failed start never leaves a ghost row.
		_, _ = m.plainRun(s, `set -u; : > `+shellQuote(file+".stop")+` 2>/dev/null; sleep 1`)
		_ = m.deleteCaptureFiles(s, dir, id)
		return meta, err
	}
	meta.Status = "running"
	return meta, nil
}

// launchCapture unpacks the wrapper as root under /run and starts it detached.
func (m *Manager) launchCapture(s *session, meta CapMeta, iface string, bpfWords []string) error {
	if s.uid < 0 {
		return fmt.Errorf("호스트 사용자 uid를 알 수 없습니다")
	}
	args := []string{
		"-o", meta.File,
		"-i", iface,
		"-m", strconv.Itoa(meta.MaxSec),
		"-b", strconv.FormatInt(meta.MaxBytes, 10),
		"-d", strconv.Itoa(meta.MinFreeMB),
		"-n", strconv.Itoa(s.uid),
	}
	// -Z hands the savefile to the login user before it is opened. A login name we
	// cannot vouch for is simply omitted: the wrapper still chowns by uid.
	if validUnixUser(s.user) {
		args = append(args, "-N", s.user)
	}
	if meta.SnapLen > 0 {
		args = append(args, "-s", strconv.Itoa(meta.SnapLen))
	}
	if meta.MaxPackets > 0 {
		args = append(args, "-c", strconv.Itoa(meta.MaxPackets))
	}
	if meta.Promisc {
		args = append(args, "-p")
	}
	// -U (flush per packet) only when a filter narrows the capture. It controls
	// libpcap's few-KB savefile buffer, not the 4MB kernel ring, so on an
	// unfiltered high-rate capture it buys nothing and costs kernel drops; on a
	// filtered (slow) one it keeps "download what we have so far" fresh.
	if len(bpfWords) > 0 {
		args = append(args, "-f")
	}

	// LF-normalised: a CR would break the shebang and every line of the wrapper.
	script := bytes.ReplaceAll(pcapdScript, []byte("\r\n"), []byte("\n"))
	b64 := base64.StdEncoding.EncodeToString(script)

	cmd := `set -u; ` +
		`d=$(mktemp -d /run/rtm-pcapd.XXXXXX) || { echo RTM_NOTMP; exit 4; }; ` +
		`chmod 700 "$d"; ` +
		`printf %s ` + shellQuote(b64) + ` | base64 -d > "$d/rtm-pcapd" || { rm -rf "$d"; echo RTM_NOWRITE; exit 4; }; ` +
		`chmod 700 "$d/rtm-pcapd"; ` +
		// --probe returns immediately: it catches a noexec /run BEFORE we launch.
		`"$d/rtm-pcapd" --probe >/dev/null 2>&1 || { rm -rf "$d"; echo RTM_NOEXEC; exit 4; }; ` +
		// The braces matter: `A && B & echo X` would background the whole list and
		// let echo report a success that never happened.
		`{ RTM_SELFDIR="$d" setsid "$d/rtm-pcapd" ` + shArgs(args...) + ` -- ` + shArgs(bpfWords...) +
		` >/dev/null 2>&1 </dev/null & } && echo RTM_LAUNCHED`

	out, err := m.sudoRun(s, cmd)
	switch {
	case strings.Contains(out, "RTM_LAUNCHED"):
		return nil
	case strings.Contains(out, "RTM_NOEXEC"):
		return fmt.Errorf("이 호스트의 /run 이 noexec 이라 캡쳐 도우미를 실행할 수 없습니다")
	case strings.Contains(out, "RTM_NOTMP"), strings.Contains(out, "RTM_NOWRITE"):
		return fmt.Errorf("캡쳐 도우미를 준비할 수 없습니다: %s", tailLines(out, 2))
	}
	detail := tailLines(out, 2)
	if detail == "" && err != nil {
		detail = err.Error()
	}
	return fmt.Errorf("캡쳐 시작 실패: %s", detail)
}

// verifyCaptureStart waits (up to ~6s, one round trip) for the wrapper to prove
// it really started. tcpdump opens the device and compiles the BPF before it
// opens the savefile, so ".started" means both succeeded.
func (m *Manager) verifyCaptureStart(s *session, file string) error {
	q := shellQuote(file)
	script := `set -u; f=` + q + `; i=0; ` +
		`while [ $i -lt 24 ]; do ` +
		`[ -e "$f.done" ] && { echo "FAIL:$(tr -d "\n" < "$f.done" 2>/dev/null)"; tail -c 512 "$f.log" 2>/dev/null; exit 0; }; ` +
		`[ -e "$f.started" ] && { echo OK; exit 0; }; ` +
		`sleep 0.25; i=$((i+1)); done; ` +
		`echo TIMEOUT; tail -c 512 "$f.log" 2>/dev/null`
	out, _ := m.plainRun(s, script)

	verdict := ""
	for _, l := range strings.Split(out, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			verdict = t
			break
		}
	}
	c := parseCapLog(out)
	switch {
	case verdict == "OK":
		return nil
	case strings.HasPrefix(verdict, "FAIL:"):
		detail := c.Err
		if detail == "" {
			detail = capReasonText(sanitizeReason(strings.TrimPrefix(verdict, "FAIL:")))
		}
		return fmt.Errorf("캡쳐 시작 실패: %s", detail)
	case verdict == "TIMEOUT":
		if c.Err != "" {
			return fmt.Errorf("캡쳐 시작 실패: %s", c.Err)
		}
		return fmt.Errorf("캡쳐 시작을 6초 내에 확인하지 못했습니다 (인터페이스 이름과 포트 필터를 확인하세요)")
	}
	return fmt.Errorf("캡쳐 시작을 확인하지 못했습니다: %s", tailLines(out, 2))
}

// ---- list --------------------------------------------------------------

// capListScript reports every capture found under any candidate base.
//
// The liveness test separates "no such /proc entry" (really dead) from "entry
// exists but is unreadable" (hidepid). Collapsing those two would freeze a
// reboot-killed capture as "running" forever — and the next Stop would then write
// .done=signal, promoting a truncated file to "cleanly stopped".
func capListScript(s *session, lite bool) string {
	body := `set -u
` + trustedDirSh(s) + `
hp=0; [ -r /proc/1/cmdline ] || hp=1
for b in ` + pcapBasesSh(s) + `; do d="$b/` + pcapDirName + `"; rtm_trusted "$d" || continue
 for mf in "$d"/*.meta.json; do [ -e "$mf" ] || continue
  id=$(basename "$mf" .meta.json); f="$d/$id.pcap"
  sz=$(stat -c%s "$f" 2>/dev/null || echo 0)
  run=0
  for p in $(cat "$f.pid" "$f.tpid" 2>/dev/null | head -c 256); do
    [ "$run" = 1 ] && break
    case "$p" in ''|*[!0-9]*) continue;; esac
    if [ -d "/proc/$p" ]; then
      if [ -r "/proc/$p/cmdline" ]; then
        c=$(tr '\0' ' ' < "/proc/$p/cmdline" 2>/dev/null)
        case "$c" in *"$f"*) run=1;; esac
      else run=2; fi
    elif [ "$hp" = 1 ]; then [ "$run" = 1 ] || run=2; fi
  done
  st=0; [ -e "$f.stop" ] && st=1
  dn=; [ -e "$f.done" ] && dn=$(head -c 256 "$f.done" 2>/dev/null | base64 -w0)
`
	if lite {
		return body + `  echo "L|$id|$sz|$run|$st|$dn"
 done
done`
	}
	return body + `  mt=$(stat -c%Y "$f" 2>/dev/null || echo 0)
  lg=; [ -e "$f.log" ] && lg=$(tail -c 2048 "$f.log" 2>/dev/null | base64 -w0)
  echo "STAT|$id|$d|$sz|$run|$mt|$st|$dn|$lg"
  echo "META|$(head -c 65536 "$mf" | base64 -w0)"
 done
done`
}

// listCapturesLite is the watcher's poll: scalars only, no meta, no log tail.
func (m *Manager) listCapturesLite(s *session) (map[string]capLite, error) {
	out, err := m.plainRun(s, capListScript(s, true))
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, err
	}
	res := map[string]capLite{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(strings.TrimSpace(line), "\r")
		if !strings.HasPrefix(line, "L|") {
			continue
		}
		p := strings.SplitN(line[2:], "|", 5)
		if len(p) < 4 {
			continue
		}
		id := p[0]
		if !validCapID(id) { // a planted filename never becomes an id we act on
			continue
		}
		l := capLite{}
		l.size, _ = strconv.ParseInt(p[1], 10, 64)
		l.alive, _ = strconv.Atoi(p[2])
		l.haveStop = p[3] == "1"
		if len(p) == 5 && strings.TrimSpace(p[4]) != "" {
			if db, e := base64.StdEncoding.DecodeString(strings.TrimSpace(p[4])); e == nil {
				l.haveDone = true
				l.reason = sanitizeReason(string(db))
			}
		}
		res[id] = l
	}
	return res, nil
}

// ListCaptures returns every capture on the host with live status, size and
// counters. It runs unprivileged: the files belong to the login user, so no sudo
// round trip (and no /var/log/secure noise) is needed to read them.
func (m *Manager) ListCaptures(hostID string) ([]CapMeta, error) {
	s := m.get(hostID)
	if s == nil {
		return nil, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	out, err := m.plainRun(s, capListScript(s, false))
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, err
	}
	var list []CapMeta
	cur := -1 // index in list that a following META| line belongs to
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "STAT|"):
			cur = -1
			// STAT|id|dir|size|alive|mtime|stop|doneB64|logB64
			p := strings.SplitN(line[5:], "|", 8)
			if len(p) < 7 {
				continue
			}
			id, dir := p[0], p[1]
			// The id's single source of truth is the remote basename, and the
			// directory must be one we recognise — the meta JSON's own "id" and
			// "file" are never read back.
			if !validCapID(id) || !validAbsPath(dir) || !strings.HasSuffix(dir, "/"+pcapDirName) {
				continue
			}
			cm := CapMeta{
				ID: id, HostID: hostID, File: dir + "/" + id + ".pcap",
				Captured: -1, Received: -1, DroppedKern: -1, DroppedIf: -1,
			}
			cm.SizeBytes, _ = strconv.ParseInt(p[2], 10, 64)
			alive, _ := strconv.Atoi(p[3])
			mt, _ := strconv.ParseInt(p[4], 10, 64)
			cm.LastT = mt * 1000
			haveStop := p[5] == "1"
			haveDone := false
			if strings.TrimSpace(p[6]) != "" {
				if db, e := base64.StdEncoding.DecodeString(strings.TrimSpace(p[6])); e == nil {
					haveDone = true
					cm.DoneReason = sanitizeReason(string(db))
				}
			}
			if len(p) == 8 && strings.TrimSpace(p[7]) != "" {
				if lb, e := base64.StdEncoding.DecodeString(strings.TrimSpace(p[7])); e == nil {
					c := parseCapLog(string(lb))
					cm.Captured, cm.Received = c.Captured, c.Received
					cm.DroppedKern, cm.DroppedIf = c.DroppedKern, c.DroppedIf
					cm.LinkType, cm.Err = c.LinkType, c.Err
				}
			}
			cm.Status, cm.Uncertain = capStatusFrom(alive, haveDone, haveStop, cm.DoneReason)
			list = append(list, cm)
			cur = len(list) - 1
		case strings.HasPrefix(line, "META|"):
			if cur < 0 {
				continue
			}
			raw, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(line[5:]))
			if derr != nil {
				continue
			}
			var hm CapMeta
			if json.Unmarshal(raw, &hm) != nil {
				continue
			}
			// Copy ONLY the fields the host was allowed to record at start time.
			// Status/counters/LocalPath stay ours (a host must not be able to
			// claim a local path we would then open).
			c := &list[cur]
			c.HostName, c.Iface = hm.HostName, hm.Iface
			c.Filter, c.PortSpec, c.PortsRaw = hm.Filter, hm.PortSpec, hm.PortsRaw
			c.HostSpec, c.HostsRaw = hm.HostSpec, hm.HostsRaw
			c.SnapLen, c.MaxSec, c.MinFreeMB = hm.SnapLen, hm.MaxSec, hm.MinFreeMB
			c.MaxPackets, c.MaxBytes, c.Promisc = hm.MaxPackets, hm.MaxBytes, hm.Promisc
			c.StartT, c.PlannedEndT = hm.StartT, hm.PlannedEndT
			c.Owner, c.OwnerUID, c.StartedBy = hm.Owner, hm.OwnerUID, hm.StartedBy
			if !validIfName(c.Iface) {
				c.Iface = ""
			}
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].StartT > list[j].StartT })
	return list, nil
}

// resolveCapFile locates a capture by id across the candidate bases, returning
// its directory and .pcap path. It resolves on the META file, not the .pcap: a
// capture whose tcpdump died before creating the file must still be listable,
// stoppable and deletable, or the row becomes a permanent ghost.
func (m *Manager) resolveCapFile(s *session, id string) (dir, file string, err error) {
	if !validCapID(id) {
		return "", "", fmt.Errorf("잘못된 캡쳐 ID입니다: %q", id)
	}
	// rtm_trusted is what stops a planted directory in a 1777 base from being
	// resolved and then handed to a root redirect (see trustedDirSh).
	script := `set -u; ` + trustedDirSh(s) + `for b in ` + pcapBasesSh(s) + `; do d="$b/` + pcapDirName + `"; ` +
		`rtm_trusted "$d" || continue; ` +
		`[ -e "$d/` + id + `.meta.json" ] && { echo "$d"; exit 0; }; done`
	out, _ := m.plainRun(s, script)
	for _, line := range strings.Split(out, "\n") {
		d := strings.TrimRight(strings.TrimSpace(line), "\r")
		if d == "" {
			continue
		}
		if validAbsPath(d) && strings.HasSuffix(d, "/"+pcapDirName) {
			return d, d + "/" + id + ".pcap", nil
		}
	}
	return "", "", fmt.Errorf("캡쳐를 찾지 못했습니다: %s", id)
}

// ---- stop / delete -----------------------------------------------------

// StopCapture asks a capture to stop and returns as soon as it can say something
// useful. It sends NO signal (I5): it drops a ".stop" sentinel that the wrapper
// notices within half a second and then finalises the savefile itself.
//
// If the wrapper has not finished within 3s the call returns "stopping" rather
// than blocking — the session watcher reports the real end, so a user stop and a
// self-stop converge on exactly one code path (and the modal never freezes).
func (m *Manager) StopCapture(hostID, id string) (CapMeta, error) {
	s := m.get(hostID)
	if s == nil {
		return CapMeta{}, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	_, file, err := m.resolveCapFile(s, id)
	if err != nil {
		return CapMeta{}, err
	}
	script := `set -u; f=` + shellQuote(file) + `; ` +
		`: > "$f.stop" 2>/dev/null || { echo STOP_NOWRITE; exit 9; }; ` +
		`i=0; while [ $i -lt 12 ] && [ ! -e "$f.done" ]; do sleep 0.25; i=$((i+1)); done; ` +
		`[ -e "$f.done" ] && echo "STOP_OK:$(tr -d "\n" < "$f.done")" || echo STOP_PENDING`
	out, _ := m.plainRun(s, script)
	if strings.Contains(out, "STOP_NOWRITE") {
		return CapMeta{}, fmt.Errorf("중지 신호를 쓸 수 없습니다 (권한 또는 경로 확인): %s", file)
	}
	metas, lerr := m.ListCaptures(hostID)
	if lerr == nil {
		for _, cm := range metas {
			if cm.ID == id {
				return cm, nil
			}
		}
	}
	// The listing failed but the sentinel landed; report the coarse outcome.
	cm := CapMeta{ID: id, HostID: hostID, File: file, Status: "stopping"}
	if strings.Contains(out, "STOP_OK") {
		cm.Status = "stopped"
	}
	return cm, nil
}

// StopCaptureForce escalates to signals, and only ever to PIDs the wrapper itself
// recorded — confirmed by /proc/<pid>/comm against a three-name allowlist AND by
// the capture path appearing in that process's cmdline. It never scans all of
// /proc (another session's stop script contains both the literal "tcpdump" and
// the capture path, so a cmdline sweep would make two operators kill each other).
//
// When nothing was signalled it deliberately does NOT write .done: a capture that
// was already dead stays honestly "interrupted" (I6).
func (m *Manager) StopCaptureForce(hostID, id string) (CapMeta, error) {
	s := m.get(hostID)
	if s == nil {
		return CapMeta{}, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	if !s.elevated {
		return CapMeta{}, fmt.Errorf("강제 중지에는 root 또는 sudo 권한이 필요합니다")
	}
	_, file, err := m.resolveCapFile(s, id)
	if err != nil {
		return CapMeta{}, err
	}
	q := shellQuote(file)
	// This script runs as ROOT and redirects into two paths, so it carries the same
	// protections pcapd.sh does: noclobber (so `>` is O_EXCL and cannot follow a
	// symlink) and an explicit -L refusal on both targets. The directory is already
	// vetted by resolveCapFile/rtm_trusted; this is the second line of defence, and
	// the .lock file is the one sidecar the wrapper's pre-emption sweep never claims.
	// The lock is opened with >> because noclobber would reject a legitimate,
	// pre-existing lock file.
	script := `set -u; set -C; f=` + q + `; ` +
		`[ -L "$f.lock" ] && { echo LOCK_SYMLINK; exit 8; }; ` +
		`[ -L "$f.done" ] && { echo DONE_SYMLINK; exit 8; }; ` +
		`exec 9>>"$f.lock" 2>/dev/null || { echo LOCKFAIL; exit 8; }; ` +
		`flock -n 9 2>/dev/null || { echo BUSY_LOCK; exit 0; }; ` +
		`kills=""; ` +
		`for p in $(cat "$f.pid" "$f.tpid" 2>/dev/null); do ` +
		`case "$p" in ''|*[!0-9]*) continue;; esac; ` +
		`[ -r "/proc/$p/comm" ] || continue; ` +
		`read -r k < "/proc/$p/comm"; ` +
		`case "$k" in rtm-pcapd|timeout|tcpdump) ;; *) continue;; esac; ` +
		`c=$(tr '\0' ' ' < "/proc/$p/cmdline" 2>/dev/null); ` +
		`case "$c" in *"$f"*) ;; *) continue;; esac; ` +
		`kill -TERM "$p" 2>/dev/null && kills="$kills $p"; done; ` +
		`i=0; while [ $i -lt 25 ]; do a=0; for p in $kills; do kill -0 "$p" 2>/dev/null && a=1; done; ` +
		`[ "$a" = 0 ] && break; sleep 0.2; i=$((i+1)); done; ` +
		`for p in $kills; do kill -0 "$p" 2>/dev/null && kill -KILL "$p" 2>/dev/null; done; ` +
		`[ -n "$kills" ] || { echo "KILLED:"; exit 0; }; ` +
		`[ -e "$f.done" ] || printf 'signal-forced\n' > "$f.done"; ` +
		`chown -h ` + strconv.Itoa(s.uid) + ` "$f" "$f.log" "$f.done" 2>/dev/null; ` +
		`echo "KILLED:$kills"`
	out, _ := m.sudoRun(s, script)
	if strings.Contains(out, "BUSY_LOCK") {
		return CapMeta{}, fmt.Errorf("다른 곳에서 중지 처리 중입니다. 잠시 후 다시 시도하세요")
	}
	if strings.Contains(out, "LOCK_SYMLINK") || strings.Contains(out, "DONE_SYMLINK") {
		return CapMeta{}, fmt.Errorf("캡쳐 보조 파일이 심볼릭 링크입니다(거부): %s — 서버에서 확인하세요", file)
	}
	if strings.Contains(out, "LOCKFAIL") {
		return CapMeta{}, fmt.Errorf("강제 중지 준비 실패: %s", tailLines(out, 2))
	}
	metas, lerr := m.ListCaptures(hostID)
	if lerr == nil {
		for _, cm := range metas {
			if cm.ID == id {
				return cm, nil
			}
		}
	}
	return CapMeta{ID: id, HostID: hostID, File: file, Status: "stopped"}, nil
}

// DeleteCapture removes a capture and its sidecars from the host.
//
// A running capture is refused: `rm` on an open savefile unlinks the inode while
// tcpdump keeps writing to it, so the space never comes back in df, nothing shows
// in du, and the operator has no way to diagnose it.
func (m *Manager) DeleteCapture(hostID, id string) error {
	s := m.get(hostID)
	if s == nil {
		return fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	dir, _, err := m.resolveCapFile(s, id)
	if err != nil {
		return err
	}
	if lite, lerr := m.listCapturesLite(s); lerr == nil {
		if l, ok := lite[id]; ok {
			st, _ := capStatusFrom(l.alive, l.haveDone, l.haveStop, l.reason)
			switch st {
			case "running", "stopping":
				return fmt.Errorf("캡쳐가 아직 실행 중입니다. 중지 후 다시 시도하세요")
			case "orphan":
				// "orphan" is a snapshot the dying wrapper took microseconds after it
				// SIGKILLed tcpdump, and nothing ever rewrites .done. Taking it at face
				// value forever made the row permanently undeletable — including after
				// the operator did exactly what the message told them to do and cleaned
				// the process up. Re-derive it instead of trusting the marker.
				held, herr := m.captureStillHeld(s, dir+"/"+id+".pcap")
				if herr != nil || held {
					return fmt.Errorf("tcpdump 프로세스가 파일을 아직 붙들고 있습니다 (고아 상태). " +
						"서버에서 해당 tcpdump를 정리한 뒤 다시 시도하세요")
				}
			}
		}
	}
	// Second, independent guard that does not trust the status at all: no .done
	// marker AND a file that was written to seconds ago means something is still
	// writing it. `rm` then unlinks an inode tcpdump keeps filling — the space
	// never returns in df, nothing shows in du, and the operator has no way to
	// diagnose it. A status bug must not be able to cause that. (We cannot check
	// /proc/<pid>/fd here: tcpdump's -Z transition clears its dumpable flag, so
	// those links are root-only, and this path is deliberately sudo-free.)
	if err := m.refuseIfBeingWritten(s, dir+"/"+id+".pcap"); err != nil {
		return err
	}
	return m.deleteCaptureFiles(s, dir, id)
}

// captureStillHeld reports whether any tcpdump currently has this capture's path
// in its command line — the same positive test the wrapper's finish() uses, and
// the same one that only ever reports, never signals. It runs unprivileged:
// tcpdump's -Z transition makes /proc/<pid>/fd root-only, but cmdline stays
// world-readable, which is exactly what the whole liveness model already relies on.
func (m *Manager) captureStillHeld(s *session, file string) (bool, error) {
	if !validAbsPath(file) {
		return true, fmt.Errorf("경로를 확인할 수 없습니다: %s", file)
	}
	out, err := m.plainRun(s, `set -u; f=`+shellQuote(file)+`; held=0; `+
		`for e in /proc/[0-9]*; do [ "$held" = 1 ] && break; `+
		`[ -r "$e/comm" ] || continue; `+
		`read -r k < "$e/comm" 2>/dev/null || continue; `+
		`[ "$k" = tcpdump ] || continue; `+
		`c=$(tr '\0' ' ' < "$e/cmdline" 2>/dev/null) || continue; `+
		`case "$c" in *"$f"*) held=1;; esac; done; echo "HELD:$held"`)
	switch {
	case strings.Contains(out, "HELD:0"):
		return false, nil
	case strings.Contains(out, "HELD:1"):
		return true, nil
	}
	// Could not tell — assume it is held. Refusing a delete is recoverable; a
	// wrongly unlinked, still-open savefile is not.
	if err == nil {
		err = fmt.Errorf("%s", tailLines(out, 2))
	}
	return true, err
}

// ForgetCapture removes a capture from the list WITHOUT touching its .pcap or
// .log — the escape hatch for a row whose real state we cannot determine.
//
// It exists because of hidepid: when /proc is masked, the login user can never
// read a root pid, so a capture killed by a reboot or the OOM killer reports
// alive=2 ("exists but unverifiable") indefinitely. It then reads as running
// forever, delete refuses it, force-stop has nothing to signal, and — because
// StartCapture allows one capture per host — packet capture on that host is dead
// with no in-app remedy. The honest way out is to stop tracking the row while
// leaving the capture file alone and saying so, rather than to guess that it
// stopped (which would fabricate state) or to delete a file that might still be
// open (which would leak the disk space invisibly).
func (m *Manager) ForgetCapture(hostID, id string) error {
	s := m.get(hostID)
	if s == nil {
		return fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	dir, file, err := m.resolveCapFile(s, id)
	if err != nil {
		return err
	}
	lite, lerr := m.listCapturesLite(s)
	if lerr != nil {
		return fmt.Errorf("캡쳐 상태를 확인할 수 없습니다: %w", lerr)
	}
	l, ok := lite[id]
	if !ok {
		return fmt.Errorf("캡쳐를 찾지 못했습니다: %s", id)
	}
	st, uncertain := capStatusFrom(l.alive, l.haveDone, l.haveStop, l.reason)
	// Only for the two states that genuinely have no other exit. A knowable
	// capture must go through stop + delete, or this becomes a way to litter the
	// host with untracked pcap files.
	if !uncertain && st != capReasonOrphan {
		return fmt.Errorf("이 캡쳐는 상태를 확인할 수 있습니다 (%s). 중지 후 삭제를 사용하세요", capReasonText(l.reason))
	}
	if !validCapID(id) || !validAbsPath(dir) || !strings.HasSuffix(dir, "/"+pcapDirName) {
		return fmt.Errorf("대상을 확인할 수 없습니다: %s/%s", dir, id)
	}
	base := dir + "/" + id
	// The .pcap, .log and .done stay: something may still be writing them, and the
	// operator needs to be able to find the file over SSH.
	drop := []string{
		base + ".meta.json", base + ".pcap.pid", base + ".pcap.tpid",
		base + ".pcap.stop", base + ".pcap.started", base + ".pcap.lock",
	}
	for _, f := range drop {
		if !validAbsPath(f) {
			return fmt.Errorf("대상 경로가 올바르지 않습니다: %s", f)
		}
	}
	out, rerr := m.plainRun(s, `set -u; rm -f -- `+shArgs(drop...)+`; echo FORGET_OK`)
	if !strings.Contains(out, "FORGET_OK") {
		if rerr == nil {
			rerr = fmt.Errorf("%s", tailLines(out, 2))
		}
		return fmt.Errorf("목록에서 제외 실패: %v", rerr)
	}
	m.capture(CaptureEvent{HostID: hostID, CapID: id, State: "deleted",
		Msg: "목록에서 제외했습니다 — 파일은 " + file + " 에 남아 있습니다"})
	return nil
}

// deleteFreshSeconds is how recently a .done-less capture must have been written
// to for deletion to be refused.
const deleteFreshSeconds = 15

// refuseIfBeingWritten reports an error when the capture has no completion marker
// and its file was modified within the last few seconds.
func (m *Manager) refuseIfBeingWritten(s *session, file string) error {
	if !validAbsPath(file) {
		return fmt.Errorf("삭제 대상 경로가 올바르지 않습니다: %s", file)
	}
	q := shellQuote(file)
	out, _ := m.plainRun(s, `set -u; f=`+q+`; `+
		`[ -e "$f.done" ] && { echo SAFE_DONE; exit 0; }; `+
		`[ -e "$f" ] || { echo SAFE_NOFILE; exit 0; }; `+
		`n=$(date +%s); mt=$(stat -c%Y "$f" 2>/dev/null || echo 0); `+
		`a=$((n - mt)); [ "$a" -lt `+strconv.Itoa(deleteFreshSeconds)+` ] && { echo "FRESH:$a"; exit 0; }; `+
		`echo "SAFE_STALE:$a"`)
	if strings.Contains(out, "FRESH:") {
		return fmt.Errorf("이 캡쳐는 방금까지 기록되고 있었고 종료 표시(.done)가 없습니다. " +
			"아직 tcpdump가 파일을 쓰고 있을 수 있어 삭제를 거부했습니다 — 중지한 뒤 다시 시도하세요")
	}
	return nil
}

// deleteCaptureFiles removes exactly the files this capture owns. Each path is
// named explicitly and re-checked: no `rm -rf` (a pcap is not a directory) and no
// globbing, so a surprising id can never widen the blast radius.
func (m *Manager) deleteCaptureFiles(s *session, dir, id string) error {
	if !validCapID(id) || !validAbsPath(dir) || !strings.HasSuffix(dir, "/"+pcapDirName) {
		return fmt.Errorf("삭제 대상을 확인할 수 없습니다: %s/%s", dir, id)
	}
	base := dir + "/" + id
	files := []string{
		base + ".pcap", base + ".pcap.log", base + ".pcap.done", base + ".pcap.pid",
		base + ".pcap.tpid", base + ".pcap.stop", base + ".pcap.started",
		base + ".pcap.lock", base + ".meta.json",
	}
	for _, f := range files {
		if !validAbsPath(f) {
			return fmt.Errorf("삭제 대상 경로가 올바르지 않습니다: %s", f)
		}
	}
	out, err := m.plainRun(s, `set -u; rm -f -- `+shArgs(files...)+`; echo DEL_OK`)
	if !strings.Contains(out, "DEL_OK") {
		if err == nil {
			err = fmt.Errorf("%s", tailLines(out, 2))
		}
		return fmt.Errorf("삭제 실패: %v", err)
	}
	return nil
}

// ---- session watcher ---------------------------------------------------

// CaptureEvent is what the UI needs to keep its toolbar, badge and list honest
// without polling from the renderer.
type CaptureEvent struct {
	HostID string `json:"hostId"`
	CapID  string `json:"id"`
	// State is the lifecycle step: snapshot / started / stopping / stopped /
	// deleted / failed.
	State string `json:"state"`
	// Status is the fine-grained status from capStatusFrom (deadline, maxsize,
	// low-disk, interrupted, orphan, ...) so a self-stop can be reported for what
	// it actually was.
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Msg     string `json:"msg"`
	Running int    `json:"running"`
	Total   int    `json:"total"`
	// IDs is every capture id currently on the host. The watcher already has them
	// from the cheap listing, so carrying them here lets the app count how many
	// are not downloaded yet without a second round trip.
	IDs []string `json:"ids"`
}

type CaptureFunc func(ev CaptureEvent)

func (m *Manager) capture(ev CaptureEvent) {
	if m.onCapture != nil {
		m.onCapture(ev)
	}
}

// watchCaptures polls the cheap listing and emits an event ONLY on a transition.
// Without a producer here there would be no self-stop notification, no recovery
// after an app restart, and a toolbar stuck on "capturing" forever.
//
// When a host has no captures at all it backs off to one poll per minute: this
// tool runs against boxes that are already in trouble, and a diagnostic must not
// fork a shell every ten seconds for nothing. A capture started from another
// client is still discovered within that minute.
func (m *Manager) watchCaptures(hostID string, s *session) {
	s.capMu.Lock()
	tick := s.capTick
	s.capTick++
	idle := s.capSeeded && len(s.capSnap) == 0
	s.capMu.Unlock()
	if idle && tick%6 != 0 {
		return
	}

	lite, err := m.listCapturesLite(s)
	if err != nil {
		return
	}
	s.capMu.Lock()
	prev, seeded := s.capSnap, s.capSeeded
	s.capSnap = lite
	s.capSeeded = true
	s.capMu.Unlock()

	running := 0
	statusOf := func(l capLite) string {
		st, _ := capStatusFrom(l.alive, l.haveDone, l.haveStop, l.reason)
		return st
	}
	ids := make([]string, 0, len(lite))
	for id, l := range lite {
		ids = append(ids, id)
		if st := statusOf(l); st == "running" || st == "stopping" {
			running++
		}
	}
	sort.Strings(ids)
	if !seeded {
		// First look on this session: hand the UI the current picture so a restart
		// or reconnect restores the toolbar state.
		m.capture(CaptureEvent{HostID: hostID, State: "snapshot", Running: running, Total: len(lite), IDs: ids})
		return
	}
	for id, l := range lite {
		st := statusOf(l)
		was := ""
		if p, ok := prev[id]; ok {
			was = statusOf(p)
		}
		if was == st {
			continue
		}
		state := "stopped"
		switch st {
		case "running":
			state = "started"
		case "stopping":
			state = "stopping"
		case "error":
			state = "failed"
		}
		m.capture(CaptureEvent{
			HostID: hostID, CapID: id, State: state, Status: st,
			Reason: l.reason, Msg: capReasonText(l.reason),
			Running: running, Total: len(lite), IDs: ids,
		})
	}
	for id := range prev {
		if _, ok := lite[id]; !ok {
			m.capture(CaptureEvent{
				HostID: hostID, CapID: id, State: "deleted",
				Running: running, Total: len(lite), IDs: ids,
			})
		}
	}
}

// ---- download ----------------------------------------------------------

// dlStallSeconds cancels a download that has made no progress for this long. A
// half-open TCP connection never returns an error from Read, so checking
// ctx.Err() between reads is unreachable — the watchdog is the only way out.
const dlStallSeconds = 60

// DownloadCaptureTo streams a capture to localPath and returns the bytes written.
//
// The existing transfers in this package are base64 → CombinedOutput → string,
// which cannot be used for a multi-GB pcap (I8). This one streams: gzip on the
// wire, a temp file next to the destination, and an atomic rename at the end — so
// the path the user chose never holds a half file.
func (m *Manager) DownloadCaptureTo(ctx context.Context, hostID, id, localPath string,
	progress func(copied, total int64)) (int64, error) {
	s := m.get(hostID)
	if s == nil {
		return 0, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	if localPath == "" {
		return 0, fmt.Errorf("저장할 파일 경로가 비어 있습니다")
	}
	_, file, err := m.resolveCapFile(s, id)
	if err != nil {
		return 0, err
	}
	info, ierr := m.statRemoteFile(s, file)
	if ierr != nil || info.Size < 0 {
		return 0, fmt.Errorf("캡쳐 파일을 읽을 수 없습니다: %s", file)
	}
	if info.Size == 0 {
		return 0, fmt.Errorf("캡쳐 파일이 비어 있습니다 (아직 패킷이 없거나 시작에 실패했습니다)")
	}
	// gzip on the wire earns its CPU here: a pcap is mostly repetitive headers, and
	// its trailer then verifies the transfer end to end for free.
	return m.streamRemoteFile(ctx, s, file, localPath, info, info.HaveGzip, progress)
}

// CaptureSHA256 hashes a capture on the host, on demand only. The routine
// integrity check is gzip's trailer; re-reading gigabytes just to confirm it
// would tax the very server being diagnosed.
func (m *Manager) CaptureSHA256(hostID, id string) (string, error) {
	s := m.get(hostID)
	if s == nil {
		return "", fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	_, file, err := m.resolveCapFile(s, id)
	if err != nil {
		return "", err
	}
	out, _ := m.plainRun(s, `set -u; nice -n 19 sha256sum -- `+shellQuote(file)+` 2>/dev/null | awk '{print $1}'`)
	sum := strings.TrimSpace(tailLines(out, 1))
	if len(sum) != 64 {
		return "", fmt.Errorf("sha256 계산 실패: %s", tailLines(out, 2))
	}
	return sum, nil
}

// progressWriter counts bytes for the UI and records the last time any arrived
// (for the stall watchdog). Updates are throttled to 250ms or 8MB so a fast
// transfer cannot flood the event bridge.
type progressWriter struct {
	total     int64
	copied    int64
	last      time.Time
	lastBytes int64
	lastAt    atomic.Int64 // unix nanos of the last byte received
	fn        func(copied, total int64)
}

func (w *progressWriter) mark() { w.lastAt.Store(time.Now().UnixNano()) }

func (w *progressWriter) Write(p []byte) (int, error) {
	w.copied += int64(len(p))
	w.mark()
	if w.fn != nil && (time.Since(w.last) >= 250*time.Millisecond || w.copied-w.lastBytes >= 8<<20) {
		w.last = time.Now()
		w.lastBytes = w.copied
		w.fn(w.copied, w.total)
	}
	return len(p), nil
}

// ---- helpers -----------------------------------------------------------

// sanitizeReason keeps only the wrapper's own vocabulary shape from a .done file,
// so a surprising marker can never be echoed into a message or a script.
func sanitizeReason(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 24 {
		s = s[:24]
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r == '-') {
			return ""
		}
	}
	return s
}

// capReasonText renders a stop reason for the operator.
func capReasonText(reason string) string {
	switch reason {
	case capReasonDone:
		return "정상 종료"
	case capReasonSignal:
		return "사용자 중지"
	case capReasonSignalForce:
		return "사용자 중지 (강제 종료 — 끝부분 잘림 가능)"
	case capReasonDeadline:
		return "최대 시간 도달"
	case capReasonMaxSize:
		return "최대 용량 도달"
	case capReasonMaxPackets:
		return "최대 패킷 수 도달"
	case capReasonLowDisk:
		return "디스크 여유 부족으로 중지"
	case capReasonOrphan:
		return "고아 프로세스 잔존 — 수동 확인 필요"
	case capReasonNoTcpdump:
		return "이 호스트에 tcpdump가 없습니다"
	case capReasonError:
		return "실행 실패"
	case "":
		return "알 수 없는 이유"
	}
	return reason
}

// humanBytes formats a byte count for a message (not for the UI, which has its
// own formatter).
func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return strconv.FormatInt(n, 10) + " B"
	}
	v := float64(n)
	units := []string{"KB", "MB", "GB", "TB"}
	i := -1
	for v >= u && i < len(units)-1 {
		v /= u
		i++
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " " + units[i]
}

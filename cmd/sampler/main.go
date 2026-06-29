// Command sampler is a tiny, dependency-free agent that runs on the remote
// RHEL host. Every <interval> seconds it reads /proc and emits ONE coherent
// NDJSON frame describing every process: CPU%, MEM%, disk I/O, the owning
// systemd unit (service name), PID and command.
//
// It is cross-compiled for linux/amd64, embedded into the Wails binary, then
// uploaded to /tmp and executed over a single streaming SSH session. Keeping it
// stdlib-only makes the binary small and static so it runs on any RHEL8/9 box.
//
// Build:  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" \
//             -o internal/agent/sampler-linux-amd64 ./cmd/sampler
//
//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// frame is one second-aligned snapshot of the whole machine.
type frame struct {
	T        int64   `json:"t"`        // unix millis
	NCPU     int     `json:"ncpu"`     // logical CPU count
	MemTotal int64   `json:"memTotal"` // KiB
	MemUsed  int64   `json:"memUsed"`  // KiB (total - available)
	CPU      float64 `json:"cpu"`      // overall busy %, 100 = all cores
	Mem      float64 `json:"mem"`      // overall memory %
	Procs    []proc  `json:"procs"`
}

// proc is one process row. Rates are over the sampling interval.
type proc struct {
	PID     int     `json:"pid"`
	PPID    int     `json:"ppid"`
	Name    string  `json:"name"`    // friendly command name
	User    string  `json:"user"`    // owner login or uid
	Service string  `json:"service"` // systemd unit from cgroup, or "-"
	State   string  `json:"state"`   // R/S/D/Z...
	CPU     float64 `json:"cpu"`     // % of whole machine (100 = all cores)
	MemPct  float64 `json:"memPct"`  // RSS / MemTotal %
	RSSKiB  int64   `json:"rssKiB"`  // resident set size
	DiskR   int64   `json:"diskR"`   // bytes/s read (-1 if /proc/pid/io denied)
	DiskW   int64   `json:"diskW"`   // bytes/s written (-1 if denied)
	Threads int     `json:"threads"`
}

// prevProc holds last-tick counters needed to compute deltas.
type prevProc struct {
	cpuJiffies uint64
	readBytes  int64
	writeBytes int64
}

func main() {
	interval := 1 * time.Second
	if len(os.Args) > 1 {
		if n, err := strconv.Atoi(os.Args[1]); err == nil && n > 0 {
			interval = time.Duration(n) * time.Second
		}
	}

	ncpu := countCPUs()
	pageSize := int64(os.Getpagesize())
	users := loadUsers()
	clkTck := int64(100) // USER_HZ; 100 on every mainstream Linux

	prev := map[int]prevProc{}
	var prevTotal uint64

	out := bufio.NewWriter(os.Stdout)

	// Prime deltas: take a baseline, sleep, then emit on every subsequent tick.
	prevTotal = readTotalJiffies()
	primeProcs(prev)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for now := range ticker.C {
		curTotal := readTotalJiffies()
		totalDelta := float64(curTotal - prevTotal)
		if totalDelta <= 0 {
			totalDelta = 1
		}
		secs := interval.Seconds()

		memTotal, memAvail := readMem()
		f := frame{
			T:        now.UnixMilli(),
			NCPU:     ncpu,
			MemTotal: memTotal,
			MemUsed:  memTotal - memAvail,
		}
		if memTotal > 0 {
			f.Mem = float64(memTotal-memAvail) / float64(memTotal) * 100
		}

		newPrev := make(map[int]prevProc, len(prev))
		var cpuSum float64
		for _, pid := range pids() {
			st, ok := readStat(pid)
			if !ok {
				continue
			}
			pp := prev[pid]
			cpuDelta := float64(st.cpuJiffies - pp.cpuJiffies)
			if st.cpuJiffies < pp.cpuJiffies {
				cpuDelta = 0 // pid reused
			}
			cpuPct := cpuDelta / totalDelta * 100 // 100 == all cores busy
			cpuSum += cpuPct

			rkB := st.rssPages * pageSize / 1024
			memPct := 0.0
			if memTotal > 0 {
				memPct = float64(rkB) / float64(memTotal) * 100
			}

			rd, wr := readIO(pid)
			var dR, dW int64 = -1, -1
			if rd >= 0 {
				dR = int64(float64(rd-pp.readBytes) / secs)
				dW = int64(float64(wr-pp.writeBytes) / secs)
				if dR < 0 || rd < pp.readBytes {
					dR = 0
				}
				if dW < 0 || wr < pp.writeBytes {
					dW = 0
				}
			}

			f.Procs = append(f.Procs, proc{
				PID:     pid,
				PPID:    st.ppid,
				Name:    procName(pid, st.comm),
				User:    users.name(uidOf(pid)),
				Service: serviceOf(pid),
				State:   st.state,
				CPU:     round2(cpuPct),
				MemPct:  round2(memPct),
				RSSKiB:  rkB,
				DiskR:   dR,
				DiskW:   dW,
				Threads: st.threads,
			})
			newPrev[pid] = prevProc{cpuJiffies: st.cpuJiffies, readBytes: rd, writeBytes: wr}
		}
		f.CPU = round2(cpuSum)
		prev = newPrev
		prevTotal = curTotal
		_ = clkTck

		writeJSON(out, f)
		out.Flush()
	}
}

// ---- /proc parsing helpers ----

type statInfo struct {
	comm       string
	state      string
	ppid       int
	cpuJiffies uint64
	rssPages   int64
	threads    int
}

func readStat(pid int) (statInfo, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return statInfo{}, false
	}
	s := string(b)
	// comm is wrapped in parens and may contain spaces/parens -> split on last ')'
	rp := strings.LastIndexByte(s, ')')
	lp := strings.IndexByte(s, '(')
	if rp < 0 || lp < 0 || rp < lp {
		return statInfo{}, false
	}
	comm := s[lp+1 : rp]
	fields := strings.Fields(s[rp+2:]) // fields[0] == state (man proc field 3)
	if len(fields) < 22 {
		return statInfo{}, false
	}
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	ppid, _ := strconv.Atoi(fields[1])
	threads, _ := strconv.Atoi(fields[17])
	rss, _ := strconv.ParseInt(fields[21], 10, 64)
	return statInfo{
		comm:       comm,
		state:      fields[0],
		ppid:       ppid,
		cpuJiffies: utime + stime,
		rssPages:   rss,
		threads:    threads,
	}, true
}

// readIO returns cumulative read_bytes/write_bytes from /proc/pid/io.
// Returns (-1,-1) when the file is unreadable (no permission for other users).
func readIO(pid int) (int64, int64) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/io", pid))
	if err != nil {
		return -1, -1
	}
	var rd, wr int64 = -1, -1
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "read_bytes:"):
			rd, _ = strconv.ParseInt(strings.TrimSpace(line[len("read_bytes:"):]), 10, 64)
		case strings.HasPrefix(line, "write_bytes:"):
			wr, _ = strconv.ParseInt(strings.TrimSpace(line[len("write_bytes:"):]), 10, 64)
		}
	}
	if rd < 0 {
		return -1, -1
	}
	return rd, wr
}

// serviceOf maps a PID to its systemd unit by reading /proc/pid/cgroup.
// Handles cgroup v2 ("0::/system.slice/foo.service") and v1 paths.
func serviceOf(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "-"
	}
	for _, line := range strings.Split(string(b), "\n") {
		// v2: 0::/path   v1: 12:pids:/path
		idx := strings.LastIndexByte(line, ':')
		if idx < 0 {
			continue
		}
		path := line[idx+1:]
		for _, seg := range strings.Split(path, "/") {
			if strings.HasSuffix(seg, ".service") || strings.HasSuffix(seg, ".scope") {
				return seg
			}
		}
	}
	return "-"
}

// procName prefers the cmdline basename (nicer than the 15-char comm), and
// falls back to comm for kernel threads with an empty cmdline.
func procName(pid int, comm string) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err == nil && len(b) > 0 {
		arg0 := b
		if i := indexByte(b, 0); i >= 0 {
			arg0 = b[:i]
		}
		s := strings.TrimSpace(string(arg0))
		if s != "" {
			return filepath.Base(s)
		}
	}
	return "[" + comm + "]"
}

func uidOf(pid int) uint32 {
	var st syscall.Stat_t
	if err := syscall.Stat(fmt.Sprintf("/proc/%d", pid), &st); err != nil {
		return 0
	}
	return st.Uid
}

func pids() []int {
	d, err := os.Open("/proc")
	if err != nil {
		return nil
	}
	defer d.Close()
	names, _ := d.Readdirnames(-1)
	out := make([]int, 0, len(names))
	for _, n := range names {
		if pid, err := strconv.Atoi(n); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

func readTotalJiffies() uint64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	line := b
	if i := indexByte(b, '\n'); i >= 0 {
		line = b[:i]
	}
	var total uint64
	for _, f := range strings.Fields(string(line))[1:] { // skip "cpu"
		v, _ := strconv.ParseUint(f, 10, 64)
		total += v
	}
	return total
}

func countCPUs() int {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 1
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "cpu") && len(line) > 3 && line[3] >= '0' && line[3] <= '9' {
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return n
}

// readMem returns MemTotal and MemAvailable in KiB.
func readMem() (total, avail int64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		}
	}
	return total, avail
}

func primeProcs(prev map[int]prevProc) {
	for _, pid := range pids() {
		st, ok := readStat(pid)
		if !ok {
			continue
		}
		rd, wr := readIO(pid)
		prev[pid] = prevProc{cpuJiffies: st.cpuJiffies, readBytes: rd, writeBytes: wr}
	}
}

// ---- uid -> name ----

type userTable map[uint32]string

func (u userTable) name(uid uint32) string {
	if n, ok := u[uid]; ok {
		return n
	}
	return strconv.FormatUint(uint64(uid), 10)
}

func loadUsers() userTable {
	t := userTable{}
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return t
	}
	for _, line := range strings.Split(string(b), "\n") {
		cols := strings.Split(line, ":")
		if len(cols) < 3 {
			continue
		}
		if uid, err := strconv.ParseUint(cols[2], 10, 32); err == nil {
			t[uint32(uid)] = cols[0]
		}
	}
	return t
}

// ---- small utils (avoid importing bytes/encoding for a leaner binary) ----

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

// writeJSON hand-rolls the frame encoding so the agent has zero imports beyond
// stdlib basics and stays tiny. Order matches the frame/proc structs above.
func writeJSON(w *bufio.Writer, f frame) {
	fmt.Fprintf(w, `{"t":%d,"ncpu":%d,"memTotal":%d,"memUsed":%d,"cpu":%s,"mem":%s,"procs":[`,
		f.T, f.NCPU, f.MemTotal, f.MemUsed, ftoa(f.CPU), ftoa(f.Mem))
	for i, p := range f.Procs {
		if i > 0 {
			w.WriteByte(',')
		}
		fmt.Fprintf(w, `{"pid":%d,"ppid":%d,"name":%s,"user":%s,"service":%s,"state":%s,"cpu":%s,"memPct":%s,"rssKiB":%d,"diskR":%d,"diskW":%d,"threads":%d}`,
			p.PID, p.PPID, jstr(p.Name), jstr(p.User), jstr(p.Service), jstr(p.State),
			ftoa(p.CPU), ftoa(p.MemPct), p.RSSKiB, p.DiskR, p.DiskW, p.Threads)
	}
	w.WriteString("]}\n")
}

func ftoa(f float64) string {
	return strconv.FormatFloat(f, 'f', 2, 64)
}

// jstr quotes and escapes a string for JSON (handles the few bytes that matter
// in process names: quote, backslash, control chars).
func jstr(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString("\\n")
		case '\t':
			b.WriteString("\\t")
		case '\r':
			b.WriteString("\\r")
		default:
			if c < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

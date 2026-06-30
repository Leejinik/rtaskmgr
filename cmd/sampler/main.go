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
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// frame is one second-aligned snapshot of the whole machine.
type frame struct {
	T         int64      `json:"t"`         // unix millis
	NCPU      int        `json:"ncpu"`      // logical CPU count
	MemTotal  int64      `json:"memTotal"`  // KiB
	MemUsed   int64      `json:"memUsed"`   // KiB (total - available)
	CPU       float64    `json:"cpu"`       // overall busy %, 100 = all cores
	Mem       float64    `json:"mem"`       // overall memory %
	SwapTotal int64      `json:"swapTotal"` // KiB
	SwapUsed  int64      `json:"swapUsed"`  // KiB (total - free)
	NetRx     int64      `json:"netRx"`     // bytes/s received (sum of physical NICs)
	NetTx     int64      `json:"netTx"`     // bytes/s sent
	NetSpeed  int64      `json:"netSpeed"`  // sum NIC link speed, Mbit/s (0 = unknown)
	Nets      []netStat  `json:"nets"`      // per-interface throughput
	Disks     []diskStat `json:"disks"`     // per-mounted-filesystem usage + I/O
	Procs     []proc     `json:"procs"`
}

// netStat is one network interface's throughput over the interval.
type netStat struct {
	Name  string `json:"name"`
	RxBps int64  `json:"rxBps"`
	TxBps int64  `json:"txBps"`
	Speed int64  `json:"speed"` // link speed Mbit/s (0 = unknown)
}

// diskStat is one mounted real filesystem: space usage + I/O over the interval.
type diskStat struct {
	Mount  string  `json:"mount"`  // mountpoint (e.g. /, /home, /data)
	Dev    string  `json:"dev"`    // backing device (e.g. /dev/sda1)
	FSType string  `json:"fsType"` // ext4 / xfs / ...
	Total  int64   `json:"total"`  // bytes
	Free   int64   `json:"free"`   // bytes available
	Used   int64   `json:"used"`   // bytes
	RBps   int64   `json:"rBps"`   // bytes/s read
	WBps   int64   `json:"wBps"`   // bytes/s written
	Busy   float64 `json:"busy"`   // % of interval the device was doing I/O (0..100)
	Kind   string  `json:"kind"`   // "SSD" / "HDD" / "" (unknown)
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
	// Flags. Backwards compatible: a bare positional integer (old "sampler 1")
	// is still accepted as the interval.
	var (
		intervalSec = flag.Int("i", 1, "sampling interval seconds")
		maxSec      = flag.Int("max", 0, "hard stop after N seconds (0 = unlimited); safety cap for scheduled recordings")
		outPath     = flag.String("o", "", "write NDJSON to this file (.gz = gzip) instead of stdout")
		minFreeMB   = flag.Int("minfree", 512, "stop when the output filesystem free space drops below this many MB")
	)
	flag.Parse()
	if rest := flag.Args(); len(rest) > 0 {
		if n, err := strconv.Atoi(rest[0]); err == nil && n > 0 {
			*intervalSec = n
		}
	}
	if *intervalSec <= 0 {
		*intervalSec = 1
	}
	interval := time.Duration(*intervalSec) * time.Second

	// Output target: stdout (live streaming) or a file (scheduled recording),
	// optionally gzip-compressed. File mode also enforces disk-safety guards.
	var (
		out      *bufio.Writer
		closers  []io.Closer
		outDir   string
		fileMode = *outPath != ""
		gz       *gzip.Writer
	)
	if fileMode {
		outDir = filepath.Dir(*outPath)
		f, err := os.Create(*outPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "open output:", err)
			os.Exit(1)
		}
		var w io.Writer = f
		if strings.HasSuffix(*outPath, ".gz") {
			gz = gzip.NewWriter(f)
			w = gz
			closers = append(closers, gz)
		}
		closers = append(closers, f)
		out = bufio.NewWriter(w)
	} else {
		out = bufio.NewWriter(os.Stdout)
	}
	stop := func(reason string) {
		out.Flush()
		// Close in append order: the gzip writer first (so it flushes its
		// trailer into the file) and only then the underlying file. Closing the
		// file first would truncate the gzip stream ("unexpected end of file").
		for _, c := range closers {
			c.Close()
		}
		if fileMode {
			// Marker the host-side lister/Go reads to know recording finished.
			_ = os.WriteFile(*outPath+".done", []byte(reason+"\n"), 0o600)
		}
	}

	ncpu := countCPUs()
	pageSize := int64(os.Getpagesize())
	users := loadUsers()
	clkTck := int64(100) // USER_HZ; 100 on every mainstream Linux

	prev := map[int]prevProc{}
	var prevTotal uint64

	var deadline time.Time
	if *maxSec > 0 {
		deadline = time.Now().Add(time.Duration(*maxSec) * time.Second)
	}
	minFreeBytes := uint64(*minFreeMB) * 1024 * 1024
	frameNo := 0

	// On SIGTERM/SIGINT (manual stop) finalize the file cleanly so the gzip
	// trailer is written — otherwise the capture would be unreadable.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)

	// Prime deltas: take a baseline now, then emit the FIRST frame after a brief
	// warm-up (≤1s) so the UI shows current values immediately on connect —
	// instead of waiting a full interval (which looks like "not connecting" when
	// the interval is 20s/1m). Subsequent frames follow the configured interval.
	prevTotal = readTotalJiffies()
	primeProcs(prev)
	prevNet := map[string][2]int64{} // iface -> {rx, tx} cumulative
	for _, ni := range readNetIfaces() {
		prevNet[ni.name] = [2]int64{ni.rx, ni.tx}
	}
	prevDisk := readDiskstats()
	prevT := time.Now()
	warmup := time.Second
	if interval < warmup {
		warmup = interval
	}
	timer := time.NewTimer(warmup)
	defer timer.Stop()
	for {
		var now time.Time
		select {
		case <-sigc:
			stop("signal")
			return
		case now = <-timer.C:
		}
		timer.Reset(interval) // next frame at the full interval
		// Safety: hard time cap (scheduled recordings) and disk free-space guard.
		if !deadline.IsZero() && now.After(deadline) {
			stop("deadline")
			return
		}
		if fileMode {
			frameNo++
			if frameNo%10 == 0 && freeBytes(outDir) < minFreeBytes {
				stop("low-disk")
				return
			}
		}
		curTotal := readTotalJiffies()
		totalDelta := float64(curTotal - prevTotal)
		if totalDelta <= 0 {
			totalDelta = 1
		}
		// Use the ACTUAL elapsed time as the rate denominator so the short first
		// frame (and any timer jitter) yields accurate bytes/s. CPU% is derived
		// from the jiffie delta ratio, so it is window-length independent.
		secs := now.Sub(prevT).Seconds()
		if secs <= 0 {
			secs = interval.Seconds()
		}
		prevT = now

		memTotal, memAvail, swapTotal, swapFree := readMem()
		f := frame{
			T:         now.UnixMilli(),
			NCPU:      ncpu,
			MemTotal:  memTotal,
			MemUsed:   memTotal - memAvail,
			SwapTotal: swapTotal,
			SwapUsed:  swapTotal - swapFree,
		}
		if memTotal > 0 {
			f.Mem = float64(memTotal-memAvail) / float64(memTotal) * 100
		}

		// Per-interface + total network throughput (bytes/s) over the window.
		for _, ni := range readNetIfaces() {
			p := prevNet[ni.name]
			rxBps := perSec(ni.rx-p[0], secs)
			txBps := perSec(ni.tx-p[1], secs)
			f.Nets = append(f.Nets, netStat{Name: ni.name, RxBps: rxBps, TxBps: txBps, Speed: ni.speedMbit})
			f.NetRx += rxBps
			f.NetTx += txBps
			f.NetSpeed += ni.speedMbit
			prevNet[ni.name] = [2]int64{ni.rx, ni.tx}
		}

		// Per-filesystem usage (statfs) + I/O (diskstats delta).
		curDisk := readDiskstats()
		for _, mi := range readMounts() {
			total, free, used := statfsUsage(mi.mount)
			ds := diskStat{
				Mount: mi.mount, Dev: mi.dev, FSType: mi.fsType,
				Total: total, Free: free, Used: used,
				Kind: diskKind(mi.diskKey),
			}
			if cur, ok := curDisk[mi.diskKey]; ok {
				if prev, ok2 := prevDisk[mi.diskKey]; ok2 {
					ds.RBps = perSec((cur.readSectors-prev.readSectors)*512, secs)
					ds.WBps = perSec((cur.writeSectors-prev.writeSectors)*512, secs)
					busy := float64(cur.ioMs-prev.ioMs) / (secs * 1000) * 100
					if busy < 0 {
						busy = 0
					}
					if busy > 100 {
						busy = 100
					}
					ds.Busy = round2(busy)
				}
			}
			f.Disks = append(f.Disks, ds)
		}
		prevDisk = curDisk

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
		if gz != nil && frameNo%10 == 0 {
			gz.Flush() // periodic sync point so a crash loses at most ~10s
		}
	}
}

// freeBytes returns the available bytes on the filesystem holding dir.
func freeBytes(dir string) uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return ^uint64(0) // unknown → don't trip the guard
	}
	return st.Bavail * uint64(st.Bsize)
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

// readMem returns MemTotal, MemAvailable, SwapTotal, SwapFree — all in KiB.
func readMem() (total, avail, swapTotal, swapFree int64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, 0
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
		case "SwapTotal:":
			swapTotal = v
		case "SwapFree:":
			swapFree = v
		}
	}
	return total, avail, swapTotal, swapFree
}

// ---- network totals ----

// virtualIface reports whether an interface name is a virtual/software device we
// should exclude from system-wide network throughput (keep physical NICs/bonds).
func virtualIface(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range []string{"veth", "docker", "br-", "virbr", "vnet", "tap", "tun", "cni", "flannel", "cali", "kube", "cni0", "dummy"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

type ifaceCum struct {
	name        string
	rx, tx      int64 // cumulative bytes
	speedMbit   int64
}

// readNetIfaces returns cumulative rx/tx bytes and link speed for each physical
// interface (virtual/software devices excluded).
func readNetIfaces() []ifaceCum {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil
	}
	var out []ifaceCum
	for _, line := range strings.Split(string(b), "\n") {
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		if virtualIface(name) {
			continue
		}
		// Skip administratively-down NICs (reduces noise from unused ports).
		if st, e := os.ReadFile("/sys/class/net/" + name + "/operstate"); e == nil {
			if strings.TrimSpace(string(st)) == "down" {
				continue
			}
		}
		f := strings.Fields(line[i+1:])
		if len(f) < 16 {
			continue
		}
		r, _ := strconv.ParseInt(f[0], 10, 64) // rx bytes
		t, _ := strconv.ParseInt(f[8], 10, 64) // tx bytes
		ic := ifaceCum{name: name, rx: r, tx: t}
		if sb, e := os.ReadFile("/sys/class/net/" + name + "/speed"); e == nil {
			if s, _ := strconv.ParseInt(strings.TrimSpace(string(sb)), 10, 64); s > 0 {
				ic.speedMbit = s
			}
		}
		out = append(out, ic)
	}
	return out
}

// ---- per-filesystem disk usage + I/O ----

// realFS is the set of on-disk filesystem types we report (pseudo/network fs are
// skipped — they have no df-meaningful size or /proc/diskstats I/O).
var realFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"f2fs": true, "jfs": true, "reiserfs": true, "vfat": true, "exfat": true,
	"ntfs": true, "ntfs3": true,
}

type mountInfo struct {
	mount  string
	dev    string // raw device from /proc/mounts
	diskKey string // /proc/diskstats device name (resolved, e.g. sda1, dm-3)
	fsType string
}

// readMounts lists mounted real filesystems, resolving each device to its
// /proc/diskstats key (handles /dev/mapper symlinks → dm-N).
func readMounts() []mountInfo {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []mountInfo
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.HasPrefix(f[0], "/dev/") || !realFS[f[2]] {
			continue
		}
		mp := unescapeMount(f[1])
		if seen[mp] {
			continue
		}
		seen[mp] = true
		key := f[0]
		if resolved, e := filepath.EvalSymlinks(f[0]); e == nil {
			key = resolved
		}
		out = append(out, mountInfo{mount: mp, dev: f[0], diskKey: filepath.Base(key), fsType: f[2]})
	}
	return out
}

// unescapeMount decodes the octal escapes (\040 space etc.) used in /proc/mounts.
func unescapeMount(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseInt(s[i+1:i+4], 8, 16); err == nil {
				sb.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

type diskCounters struct {
	readSectors  int64
	writeSectors int64
	ioMs         int64 // milliseconds spent doing I/O (field 13)
}

// readDiskstats returns cumulative counters keyed by device name.
func readDiskstats() map[string]diskCounters {
	b, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return nil
	}
	out := map[string]diskCounters{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 14 {
			continue
		}
		name := f[2]
		rd, _ := strconv.ParseInt(f[5], 10, 64)  // sectors read
		wr, _ := strconv.ParseInt(f[9], 10, 64)  // sectors written
		io, _ := strconv.ParseInt(f[12], 10, 64) // ms doing I/O
		out[name] = diskCounters{readSectors: rd, writeSectors: wr, ioMs: io}
	}
	return out
}

// perSec converts a counter delta over `secs` seconds into a per-second rate,
// clamping negatives (counter reset / device removed) to 0.
func perSec(delta int64, secs float64) int64 {
	if delta <= 0 || secs <= 0 {
		return 0
	}
	return int64(float64(delta) / secs)
}

// diskKind classifies a block device as "SSD" / "HDD" via the kernel's
// rotational flag, or "" when it can't be determined.
func diskKind(diskKey string) string {
	switch rotationalOf(diskKey) {
	case 1:
		return "HDD"
	case 0:
		return "SSD"
	}
	return ""
}

// rotationalOf returns 1 (rotational/HDD), 0 (non-rotational/SSD), or -1.
func rotationalOf(key string) int {
	// Whole device (sda, nvme0n1, dm-3) — its own rotational flag.
	if v, ok := readRot("/sys/block/" + key + "/queue/rotational"); ok {
		// device-mapper rotational can be misleading; prefer an underlying slave.
		if strings.HasPrefix(key, "dm-") {
			if slaves, e := os.ReadDir("/sys/block/" + key + "/slaves"); e == nil && len(slaves) > 0 {
				if p := parentDisk(slaves[0].Name()); p != "" {
					if sv, ok2 := readRot("/sys/block/" + p + "/queue/rotational"); ok2 {
						return sv
					}
				}
			}
		}
		return v
	}
	// Partition (sda1, nvme0n1p1) — the flag lives on the parent disk.
	if p := parentDisk(key); p != "" {
		if v, ok := readRot("/sys/block/" + p + "/queue/rotational"); ok {
			return v
		}
	}
	return -1
}

func readRot(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	switch strings.TrimSpace(string(b)) {
	case "1":
		return 1, true
	case "0":
		return 0, true
	}
	return 0, false
}

// parentDisk strips a partition suffix to the whole-disk name (sda1→sda,
// nvme0n1p1→nvme0n1). Returns "" when the name has no trailing partition number.
func parentDisk(part string) string {
	i := len(part)
	for i > 0 && part[i-1] >= '0' && part[i-1] <= '9' {
		i--
	}
	if i == len(part) {
		return "" // no trailing digits → not a partition
	}
	// nvme0n1p1 / mmcblk0p1: drop the 'p' separator too.
	if i > 1 && part[i-1] == 'p' && part[i-2] >= '0' && part[i-2] <= '9' {
		i--
	}
	return part[:i]
}

// statfsUsage returns total/free/used bytes for a mountpoint.
func statfsUsage(mount string) (total, free, used int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(mount, &st); err != nil {
		return 0, 0, 0
	}
	bs := int64(st.Bsize)
	total = int64(st.Blocks) * bs
	free = int64(st.Bavail) * bs
	used = total - int64(st.Bfree)*bs
	return total, free, used
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
	fmt.Fprintf(w, `{"t":%d,"ncpu":%d,"memTotal":%d,"memUsed":%d,"cpu":%s,"mem":%s,"swapTotal":%d,"swapUsed":%d,"netRx":%d,"netTx":%d,"netSpeed":%d,"nets":[`,
		f.T, f.NCPU, f.MemTotal, f.MemUsed, ftoa(f.CPU), ftoa(f.Mem),
		f.SwapTotal, f.SwapUsed, f.NetRx, f.NetTx, f.NetSpeed)
	for i, ni := range f.Nets {
		if i > 0 {
			w.WriteByte(',')
		}
		fmt.Fprintf(w, `{"name":%s,"rxBps":%d,"txBps":%d,"speed":%d}`,
			jstr(ni.Name), ni.RxBps, ni.TxBps, ni.Speed)
	}
	w.WriteString(`],"disks":[`)
	for i, d := range f.Disks {
		if i > 0 {
			w.WriteByte(',')
		}
		fmt.Fprintf(w, `{"mount":%s,"dev":%s,"fsType":%s,"total":%d,"free":%d,"used":%d,"rBps":%d,"wBps":%d,"busy":%s,"kind":%s}`,
			jstr(d.Mount), jstr(d.Dev), jstr(d.FSType), d.Total, d.Free, d.Used, d.RBps, d.WBps, ftoa(d.Busy), jstr(d.Kind))
	}
	w.WriteString(`],"procs":[`)
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

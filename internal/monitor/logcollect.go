// Bulk log collection across a cluster (OQT-323 "두 번째: 로그 파일 복제 기능").
//
// The flow, per server, is: copy the selected logs into a staging directory →
// archive it → download → verify → delete the staging copies and the archive.
// The ORIGINAL logs are never touched, which is what makes the destructive half
// safe: an active log that a service holds open must not be `rm`ed (the service
// keeps writing to the unlinked inode and the space never returns to df) and must
// not be truncated either (the lines written between the copy and the truncate
// would be lost). Copying and then deleting only our own copies avoids both.
//
// The archive the operator ends up with is ONE file laid out by server and then by
// category, so what they picked in the tree is what they find on disk:
//
//	rtaskmgr-logs-<ts>.tar.gz
//	 ├ <server>/_MANIFEST.txt
//	 ├ <server>/modules/lizcollector/collector.log
//	 ├ <server>/middleware/kafka/server.log
//	 ├ <server>/system/messages-20260705.from-20260630
//	 └ <server>/journal/journal-20260630_20260730.log
//
// Privilege model: the survey and the copy run as root through the session's
// existing sudoRun path (the same one used to install nethogs, kill a process and
// start a capture), because /var/log/messages and /var/log/audit are 0600 root:root
// and the login user cannot read them at all. "sudo su - root" would want a TTY on a
// non-tty exec channel; sudoRun achieves the same thing and keeps the password on
// session stdin instead of a command line.
//
// The consequence has to be handled deliberately: files root copies are root-owned,
// so the staging directory is chown'd to the login user immediately after the copy.
// Otherwise the archive, download and cleanup steps would all need sudo too — and
// after a password rotation the operator could not delete their own collection. This
// is the same mistake that made a live packet capture report itself as "interrupted"
// (root-owned .pid sidecars the unprivileged lister could not read).
//
// This file holds the pure, testable half: the catalog, path assembly, naming and
// the arithmetic the tree shows. The session plumbing lives alongside it.
package monitor

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Archive category directory names. Lowercase on purpose: an operator unpacking on
// Windows would hit a collision if these ever differed only by case, and lowercase
// is the convention inside archives.
const (
	LogCatModules    = "modules"    // liz application modules
	LogCatMiddleware = "middleware" // kafka, zookeeper, redis, mariadb, clickhouse …
	LogCatSystem     = "system"     // /var/log, dmesg, sysctl
	LogCatJournal    = "journal"    // journald export
)

// logStageDirName is the per-collection staging directory created on each host.
const logStageDirName = ".rtaskmgr-logs"

// LogModuleDef is one row of the collection tree: a named group of log files, or a
// command whose output is collected as a file.
//
// Name is used for BOTH the tree label and the archive directory, so the two can
// never drift apart.
type LogModuleDef struct {
	Name string `json:"name"`
	// Paths are directories to scan and/or individual files to take as-is.
	Paths []string `json:"paths,omitempty"`
	// Match filters filenames inside scanned directories (shell glob, "" = all).
	Match string `json:"match,omitempty"`
	// Recursive scans subdirectories. Off by default: log directories are usually
	// flat, and recursing into a nested one (lift's packet/<yyyy-MM>/) would swallow
	// a module the operator wants to toggle separately.
	Recursive bool `json:"recursive,omitempty"`
	// Exclude drops paths whose basename OR whose relative path matches one of these
	// globs. It matters most for /var/log, which as root contains a great deal that
	// is not a log (see defaultLogCatalog).
	Exclude []string `json:"exclude,omitempty"`
	// NeedsRoot marks a module whose files the login user cannot read, so the tree
	// can say so instead of showing "권한 없음" as if the file were unusable.
	NeedsRoot bool `json:"needsRoot,omitempty"`
	// Cmd makes this module a command rather than files; its stdout is stored as
	// OutFile. dmesg and sysctl are not files, and neither is journald.
	Cmd     string `json:"cmd,omitempty"`
	OutFile string `json:"outFile,omitempty"`
}

// IsCmd reports whether this module collects command output instead of files.
func (d LogModuleDef) IsCmd() bool { return d.Cmd != "" }

// LogCategoryDef groups modules under one archive directory.
type LogCategoryDef struct {
	Key   string `json:"key"`   // archive directory name (LogCat*)
	Label string `json:"label"` // tree label
	// DiscoverGlobs are shell globs whose MATCHED DIRECTORY becomes a module, named
	// after the directory that contains it. The liz modules come and go with
	// deployments, so enumerating them beats hard-coding a list that goes stale.
	DiscoverGlobs []string       `json:"discoverGlobs,omitempty"`
	Modules       []LogModuleDef `json:"modules,omitempty"`
}

// LogCatalog is the editable definition of what can be collected.
type LogCatalog struct {
	Version    int              `json:"version"`
	Categories []LogCategoryDef `json:"categories"`
}

// defaultLogCatalog is the shipped catalog. The middleware paths and the three-way
// split come from OQT-323; the liz module paths and rotation naming were read out
// of the liz source (log4j2.xml: FILE_LOG_DIR_PATH and filePattern).
func defaultLogCatalog() LogCatalog {
	return LogCatalog{
		Version: 1,
		Categories: []LogCategoryDef{
			{
				Key:   LogCatModules,
				Label: "liz 모듈",
				// The ticket's rule is "/usr/local/liz/liz* 로 시작하는 모든 디렉토리의
				// log, logs". Both spellings exist in the field, so glob both.
				DiscoverGlobs: []string{
					"/usr/local/liz/liz*/logs",
					"/usr/local/liz/liz*/log",
				},
				Modules: []LogModuleDef{
					// lift does not match the liz* rule but is a liz module that logs.
					{Name: "lift", Paths: []string{"/usr/local/liz/lift/logs"}},
					// lift's packet logs nest by month and accumulate .gz for a long
					// time — by far the biggest thing here, so it is its own row to be
					// switched off independently.
					{Name: "lift-packet", Paths: []string{"/usr/local/liz/lift/logs/packet"}, Recursive: true},
				},
			},
			{
				Key:   LogCatMiddleware,
				Label: "미들웨어",
				Modules: []LogModuleDef{
					// /var/log/messages is mode 0600 root:root on RHEL — the login user
					// cannot read it at all, so this module is root-only by nature.
					{Name: "keepalived", Paths: []string{"/var/log/messages"}, NeedsRoot: true},
					// Verified on a live host: the rotated files are root-only
					// (-rw------- root:root), so this cannot be surveyed unprivileged.
					{Name: "zookeeper", Paths: []string{"/data/zookeeper-log"}, NeedsRoot: true},
					{Name: "kafka", Paths: []string{"/data/kafka-log"}},
					// redis and redis-sentinel share a directory, and the sentinel's file is
					// "redis-sentinel.log" — so a "redis*" match swallows BOTH and a
					// "sentinel*" match finds nothing. The patterns have to be disjoint on
					// the real filenames, which is what TestRedisSentinelMatchesAreDisjoint
					// pins.
					{Name: "redis", Paths: []string{"/usr/local/liz/redis/logs"}, Match: "redis.log*"},
					{Name: "redis-sentinel", Paths: []string{"/usr/local/liz/redis/logs"}, Match: "redis-sentinel*"},
					// /data/mariadb-log is drwxr-x--- mysql:mysql and mariadb.err is
					// -rw-rw---- mysql:mysql — unreadable by the login user.
					{Name: "mariadb", Paths: []string{"/data/mariadb-log"}, NeedsRoot: true},
					{Name: "clickhouse-server", Paths: []string{"/data/clickhouse/logs"}},
				},
			},
			{
				Key:   LogCatSystem,
				Label: "시스템",
				Modules: []LogModuleDef{
					// Read as root, /var/log holds a lot that is not a log, and taking it
					// all would quietly turn a 300MB collection into a multi-GB one:
					//   journal/  the binary journald database — its own category here,
					//             and routinely gigabytes
					//   audit/    root-only and frequently the largest thing on the box
					//   sa/       sysstat binary accounting
					//   lastlog wtmp btmp tallylog  binary login records (and a
					//             privacy question we have no reason to answer)
					// Excluding them is not hiding anything: journald is collected
					// properly by its own category, and the rest are not text logs.
					{
						Name: "var-log", Paths: []string{"/var/log"}, Recursive: true, NeedsRoot: true,
						Exclude: []string{
							"journal", "journal/*", "audit", "audit/*", "sa", "sa/*",
							"lastlog", "wtmp", "btmp", "tallylog", "*.journal", "*.journal~",
						},
					},
					{Name: "dmesg", Cmd: "dmesg -T", OutFile: "dmesg.txt"},
					{Name: "sysctl", Cmd: "sysctl -a", OutFile: "sysctl.txt"},
				},
			},
			{
				Key:   LogCatJournal,
				Label: "journald",
				Modules: []LogModuleDef{
					// The concrete journalctl invocation is built from the date range and
					// the unit selection at collection time.
					{Name: "journal", Cmd: "journalctl", OutFile: "journal.log"},
				},
			},
		},
	}
}

// LogEntry is one collected item, as shown in the tree and recorded in the manifest.
type LogEntry struct {
	HostID   string `json:"hostId"`
	Category string `json:"category"` // LogCat*
	Module   string `json:"module"`
	// Source is the absolute path on the host, or the command for a Cmd module.
	Source string `json:"source"`
	// Rel is the path inside the module's archive directory (keeps a module's own
	// nesting, e.g. "packet/2026-07/onion-2026-07-28_1.log.gz").
	Rel       string `json:"rel"`
	SizeBytes int64  `json:"sizeBytes"`
	MtimeMs   int64  `json:"mtimeMs"`
	// Cut is set when only part of the file was taken (a date range that starts
	// inside it). The archived name carries the same marker so a partial file can
	// never be mistaken for a complete one.
	Cut     bool   `json:"cut"`
	CutFrom string `json:"cutFrom,omitempty"` // "20260630"
	// Coverage is what the file actually spans, once known.
	FirstMs int64  `json:"firstMs,omitempty"`
	LastMs  int64  `json:"lastMs,omitempty"`
	Skipped string `json:"skipped,omitempty"` // why this entry was not collected
	IsCmd   bool   `json:"isCmd,omitempty"`
	// DupOf names the archive path this entry was folded into when two categories
	// claimed the same file.
	DupOf string `json:"dupOf,omitempty"`
}

// ServerIdent is what a server is called, for naming its archive directory.
type ServerIdent struct {
	HostID      string
	Hostname    string // from `hostname` on the host
	DisplayName string // the name in this app's host list
	Addr        string
}

var (
	collectIDRe = regexp.MustCompile(`^log-[0-9]{10,17}-[0-9a-f]{8}$`)
	segBadRe    = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// maxSegLen keeps an archive path segment to something every filesystem and every
// unpacker accepts.
const maxSegLen = 64

func validCollectID(id string) bool {
	return len(id) <= 40 && collectIDRe.MatchString(id)
}

func newCollectID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		n := time.Now().UnixNano()
		b = [4]byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
	}
	return fmt.Sprintf("log-%d-%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}

// sanitizeSegment makes one archive path segment safe: only plain characters, no
// "." / ".." and never empty-after-trimming. Host-supplied values (hostnames,
// filenames) end up here, so this is the only thing standing between a hostile
// name and a path that escapes the archive root.
func sanitizeSegment(s string) string {
	s = segBadRe.ReplaceAllString(strings.TrimSpace(s), "_")
	s = strings.Trim(s, "._-")
	if len(s) > maxSegLen {
		s = s[:maxSegLen]
		s = strings.Trim(s, "._-")
	}
	if s == "" || s == "." || s == ".." {
		return ""
	}
	return s
}

// serverDirNames assigns each host a unique archive directory name.
//
// The hostname is preferred (the ticket asks for hostname separation, and it is
// what the receiving developer recognises), but hostnames are NOT unique in
// practice — two boxes in one data centre both called localhost.localdomain would
// otherwise silently merge their logs into one directory. When names collide,
// EVERY colliding host gets the address appended, not just the later ones: leaving
// one of them with the bare name makes it arbitrary which server that is.
func serverDirNames(items []ServerIdent) map[string]string {
	base := make(map[string]string, len(items))
	count := map[string]int{}
	for _, it := range items {
		b := sanitizeSegment(it.Hostname)
		if b == "" {
			b = sanitizeSegment(it.DisplayName)
		}
		if b == "" {
			b = sanitizeSegment(it.Addr)
		}
		if b == "" {
			b = "host"
		}
		base[it.HostID] = b
		count[b]++
	}
	out := make(map[string]string, len(items))
	used := map[string]bool{}
	for _, it := range items {
		name := base[it.HostID]
		if count[name] > 1 {
			if a := sanitizeSegment(it.Addr); a != "" {
				name = name + "_" + a
			}
		}
		// Last resort: still colliding (same hostname and same address) — fall back
		// to the host id so nothing is ever silently overwritten.
		if used[name] {
			if s := sanitizeSegment(it.HostID); s != "" {
				name = name + "_" + s
			}
		}
		used[name] = true
		out[it.HostID] = name
	}
	return out
}

// archivePathFor assembles "<server>/<category>/<module>/<rel>", sanitising every
// segment. It refuses anything that would escape the server directory.
func archivePathFor(serverDir, category, module, rel string, cut bool, cutFrom string) (string, error) {
	sd := sanitizeSegment(serverDir)
	cat := sanitizeSegment(category)
	if sd == "" || cat == "" {
		return "", fmt.Errorf("아카이브 경로를 만들 수 없습니다 (server=%q category=%q)", serverDir, category)
	}
	parts := []string{sd, cat}
	if m := sanitizeSegment(module); m != "" {
		parts = append(parts, m)
	}
	rel = strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "/")
	var relParts []string
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" {
			continue
		}
		s := sanitizeSegment(seg)
		if s == "" {
			return "", fmt.Errorf("아카이브 경로에 쓸 수 없는 이름입니다: %q", seg)
		}
		relParts = append(relParts, s)
	}
	if len(relParts) == 0 {
		return "", fmt.Errorf("아카이브 경로에 파일 이름이 없습니다")
	}
	// The cut marker goes on the LAST segment, so a partial file is obvious wherever
	// it is seen — in the archive, after extraction, and in a mail attachment.
	if cut {
		f := sanitizeSegment(cutFrom)
		if f == "" {
			return "", fmt.Errorf("잘린 파일의 기준 날짜가 없습니다")
		}
		relParts[len(relParts)-1] += ".from-" + f
	}
	full := path.Join(append(parts, relParts...)...)
	// path.Join already cleans, but assert the result really is inside the server
	// directory rather than trusting that.
	if !strings.HasPrefix(full, sd+"/") || strings.Contains(full, "../") {
		return "", fmt.Errorf("아카이브 경로가 서버 디렉터리를 벗어납니다: %q", full)
	}
	return full, nil
}

// categoryRank orders the categories for deterministic output.
func categoryRank(key string) int {
	switch key {
	case LogCatModules:
		return 0
	case LogCatMiddleware:
		return 1
	case LogCatSystem:
		return 2
	case LogCatJournal:
		return 3
	}
	return 9
}

// dedupeEntries folds entries that name the same file on the same host into one.
//
// /var/log/messages is claimed by both middleware(keepalived) and system(/var/log),
// and copying it twice would double its size in the archive and confuse whoever
// reads it. The more SPECIFIC claim wins — a module that names the exact file beats
// one that scans a directory — so a keepalived investigation finds it under
// middleware/keepalived where it is looked for. The loser is kept as a record with
// DupOf pointing at the survivor, so the manifest can explain where the file went
// instead of it appearing to have been dropped.
func dedupeEntries(entries []LogEntry, specific map[string]bool) []LogEntry {
	type key struct{ host, src string }
	best := map[key]int{}
	out := make([]LogEntry, len(entries))
	copy(out, entries)

	score := func(e LogEntry) int {
		s := 0
		if specific[e.Category+"/"+e.Module] {
			s += 10
		}
		// Lower category rank breaks ties deterministically.
		return s*10 - categoryRank(e.Category)
	}
	for i := range out {
		if out[i].Skipped != "" || out[i].IsCmd {
			continue
		}
		k := key{out[i].HostID, out[i].Source}
		j, seen := best[k]
		if !seen {
			best[k] = i
			continue
		}
		if score(out[i]) > score(out[j]) {
			best[k] = i
			out[j].DupOf = out[i].Category + "/" + out[i].Module
		} else {
			out[i].DupOf = out[j].Category + "/" + out[j].Module
		}
	}
	return out
}

// compressedExts are already-compressed files: they will not shrink again, so the
// archive estimate must not pretend they will.
var compressedExts = map[string]bool{
	".gz": true, ".gzip": true, ".zip": true, ".xz": true, ".bz2": true,
	".zst": true, ".lz4": true, ".7z": true,
}

// textCompressRatio is a deliberately conservative guess for plain log text. Under-
// promising is the safe direction: the number gates whether a copy is allowed to
// start, and an optimistic ratio would let a collection begin that cannot fit.
const textCompressRatio = 0.35

// estimateArchiveBytes projects the archive size of a selection.
func estimateArchiveBytes(entries []LogEntry) int64 {
	var total float64
	for _, e := range entries {
		if e.Skipped != "" || e.DupOf != "" {
			continue
		}
		if compressedExts[strings.ToLower(path.Ext(e.Rel))] {
			total += float64(e.SizeBytes)
			continue
		}
		total += float64(e.SizeBytes) * textCompressRatio
	}
	return int64(total)
}

// selectedBytes is the raw size of what would be copied — the number the disk
// pre-flight compares against free space, because cp writes it in full before
// anything is compressed.
func selectedBytes(entries []LogEntry) (files int, bytes int64) {
	for _, e := range entries {
		if e.Skipped != "" || e.DupOf != "" {
			continue
		}
		files++
		bytes += e.SizeBytes
	}
	return files, bytes
}

// logStageRoot is where one collection stages its copies on a host.
func logStageRoot(target, collectID string) string {
	return strings.TrimRight(target, "/") + "/" + logStageDirName + "/" + collectID
}

func logArchivePath(target, collectID string) string {
	return strings.TrimRight(target, "/") + "/" + logStageDirName + "/" + collectID + ".tar.gz"
}

// validLogStagePath is the guard the cleanup path runs before removing anything: the
// path must be one we could have created for this collection and nothing else. The
// cleanup function reconstructs paths from the collect id rather than accepting
// them, so an original log path can never reach a delete.
func validLogStagePath(p, collectID string) bool {
	if !validCollectID(collectID) || !validAbsPath(p) {
		return false
	}
	suffix := "/" + logStageDirName + "/" + collectID
	return strings.HasSuffix(p, suffix) || strings.HasSuffix(p, suffix+".tar.gz")
}

// renderManifest writes the per-server _MANIFEST.txt.
//
// The archive path no longer encodes the original location, so this is the only
// record of where each file came from — without it, "modules/lizcollector/
// collector.log" is untraceable. It also fixes the ordering trap: the active log
// (/var/log/messages) sorts BEFORE its rotated siblings (messages-20260705…), so a
// receiver running `cat messages*` would splice the newest chunk in first. The
// chronological list and the ready-made cat line are here for that reason.
func renderManifest(server ServerIdent, dirName string, entries []LogEntry, from, to int64, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "rtaskmgr 로그 수집 매니페스트\n")
	fmt.Fprintf(&b, "서버        : %s (%s)\n", server.Hostname, server.Addr)
	if server.DisplayName != "" && server.DisplayName != server.Hostname {
		fmt.Fprintf(&b, "표시 이름   : %s\n", server.DisplayName)
	}
	fmt.Fprintf(&b, "아카이브 위치: %s/\n", dirName)
	fmt.Fprintf(&b, "수집 시각   : %s\n", now.Format("2006-01-02 15:04:05"))
	if from > 0 || to > 0 {
		fmt.Fprintf(&b, "요청 기간   : %s ~ %s\n", msDate(from), msDate(to))
	} else {
		fmt.Fprintf(&b, "요청 기간   : 전체\n")
	}

	live := make([]LogEntry, 0, len(entries))
	for _, e := range entries {
		if e.Skipped == "" && e.DupOf == "" {
			live = append(live, e)
		}
	}
	files, bytes := selectedBytes(entries)
	fmt.Fprintf(&b, "수집 파일   : %d개 / %s\n\n", files, humanBytes(bytes))

	sort.SliceStable(live, func(i, j int) bool {
		if a, c := categoryRank(live[i].Category), categoryRank(live[j].Category); a != c {
			return a < c
		}
		if live[i].Module != live[j].Module {
			return live[i].Module < live[j].Module
		}
		// Oldest first: this is the order the files must be concatenated in.
		if live[i].FirstMs != live[j].FirstMs {
			return live[i].FirstMs < live[j].FirstMs
		}
		return live[i].Rel < live[j].Rel
	})

	fmt.Fprintf(&b, "%-11s %-16s %-46s %-52s %10s %s\n",
		"분류", "모듈", "아카이브 경로", "원본", "크기", "비고")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 150))
	for _, e := range live {
		ap, err := archivePathFor(dirName, e.Category, e.Module, e.Rel, e.Cut, e.CutFrom)
		if err != nil {
			ap = "(경로 오류)"
		}
		note := "온전"
		if e.Cut {
			note = fmt.Sprintf("%s 이전을 잘라냄", e.CutFrom)
		}
		if e.IsCmd {
			note = "명령 출력"
		}
		if e.FirstMs > 0 || e.LastMs > 0 {
			note += fmt.Sprintf(" · %s~%s", msDate(e.FirstMs), msDate(e.LastMs))
		}
		fmt.Fprintf(&b, "%-11s %-16s %-46s %-52s %10s %s\n",
			e.Category, e.Module, strings.TrimPrefix(ap, dirName+"/"), e.Source, humanBytes(e.SizeBytes), note)
	}

	// Everything that was NOT collected, and why. Silence here would read as "that
	// log does not exist on this server".
	var dropped []LogEntry
	for _, e := range entries {
		if e.Skipped != "" || e.DupOf != "" {
			dropped = append(dropped, e)
		}
	}
	if len(dropped) > 0 {
		fmt.Fprintf(&b, "\n수집되지 않은 항목\n%s\n", strings.Repeat("-", 60))
		for _, e := range dropped {
			why := e.Skipped
			if why == "" {
				why = "중복 — " + e.DupOf + " 에 포함됨"
			}
			fmt.Fprintf(&b, "%-11s %-16s %-52s %s\n", e.Category, e.Module, e.Source, why)
		}
	}

	// Concatenation order per module, for the rotated-file sets.
	groups := map[string][]LogEntry{}
	for _, e := range live {
		if e.IsCmd {
			continue
		}
		groups[e.Category+"/"+e.Module] = append(groups[e.Category+"/"+e.Module], e)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		if len(groups[k]) > 1 {
			keys = append(keys, k)
		}
	}
	if len(keys) > 0 {
		sort.Strings(keys)
		fmt.Fprintf(&b, "\n시간순 이어붙이기 (파일명 정렬은 활성 로그가 앞으로 와서 시간 역순이 됩니다)\n%s\n",
			strings.Repeat("-", 60))
		for _, k := range keys {
			names := make([]string, 0, len(groups[k]))
			for _, e := range groups[k] {
				ap, err := archivePathFor(dirName, e.Category, e.Module, e.Rel, e.Cut, e.CutFrom)
				if err != nil {
					continue
				}
				names = append(names, path.Base(ap))
			}
			fmt.Fprintf(&b, "%s:\n  cd %s && cat %s > all.log\n", k, k, strings.Join(names, " "))
		}
	}
	return b.String()
}

func msDate(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).Format("2006-01-02")
}

// compactDate renders a date for the ".from-YYYYMMDD" marker.
func compactDate(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).Format("20060102")
}

// excludedBy reports whether a path relative to a module's log directory is
// excluded. Both the basename and the whole relative path are tested, so a rule can
// name a file ("wtmp") or a subtree ("journal/*").
func excludedBy(def LogModuleDef, rel string) bool {
	rel = strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "/")
	if rel == "" {
		return false
	}
	base := path.Base(rel)
	for _, pat := range def.Exclude {
		if pat == "" {
			continue
		}
		if ok, _ := path.Match(pat, base); ok {
			return true
		}
		if ok, _ := path.Match(pat, rel); ok {
			return true
		}
		// A directory rule ("journal") must also drop everything beneath it.
		if rel == pat || strings.HasPrefix(rel, pat+"/") {
			return true
		}
	}
	return false
}

// moduleNameFromLogDir turns a discovered log directory into the module name: the
// directory that CONTAINS the log dir, since "/usr/local/liz/lizcollector/logs"
// belongs to lizcollector, not to "logs".
func moduleNameFromLogDir(dir string) string {
	dir = strings.TrimRight(strings.ReplaceAll(dir, "\\", "/"), "/")
	base := path.Base(dir)
	if base == "log" || base == "logs" {
		parent := path.Base(path.Dir(dir))
		if parent != "" && parent != "." && parent != "/" {
			return sanitizeSegment(parent)
		}
	}
	return sanitizeSegment(base)
}

// clampLogRange normalises the requested window. A zero start means "everything";
// recentDays is a convenience the UI offers instead of typing dates.
func clampLogRange(fromMs, toMs int64, recentDays int, now time.Time) (int64, int64) {
	if recentDays > 0 {
		start := now.AddDate(0, 0, -recentDays)
		y, m, d := start.Date()
		fromMs = time.Date(y, m, d, 0, 0, 0, 0, now.Location()).UnixMilli()
		toMs = 0
	}
	if fromMs < 0 {
		fromMs = 0
	}
	if toMs < 0 {
		toMs = 0
	}
	if fromMs > 0 && toMs > 0 && toMs < fromMs {
		fromMs, toMs = toMs, fromMs
	}
	return fromMs, toMs
}

// logSizeCapBytes is a runaway guard, not the real gate. The real gate is the tree:
// it shows per-module sizes and the free space on each server, because a kafka or
// clickhouse log legitimately running to tens of GB is normal and only the operator
// can say which of those they actually want.
const logSizeCapBytes = 20 << 30

func overSizeCap(bytes int64) bool { return bytes > logSizeCapBytes }

// journalCmd builds the journalctl invocation for the requested window and units.
// Unit names are validated with the same rule the service actions use, so a unit
// list can never carry shell syntax into the command.
func journalCmd(fromMs, toMs int64, units []string, maxBytes int64) (string, error) {
	var sb strings.Builder
	sb.WriteString("journalctl --no-pager -o short-iso")
	if fromMs > 0 {
		sb.WriteString(" --since " + shellQuote(time.UnixMilli(fromMs).Format("2006-01-02 15:04:05")))
	}
	if toMs > 0 {
		sb.WriteString(" --until " + shellQuote(time.UnixMilli(toMs).Format("2006-01-02 15:04:05")))
	}
	for _, u := range units {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !validUnit(u) {
			return "", fmt.Errorf("유닛 이름을 사용할 수 없습니다: %q", u)
		}
		sb.WriteString(" -u " + shellQuote(u))
	}
	if maxBytes > 0 {
		// Bound the output: a journal with no unit filter easily runs to gigabytes,
		// and the whole point of the size gate is that nothing unbounded is copied.
		sb.WriteString(" | head -c " + strconv.FormatInt(maxBytes, 10))
	}
	return sb.String(), nil
}

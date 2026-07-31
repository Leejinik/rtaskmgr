// The session half of log collection: enumerate → gate → copy → archive →
// download → verify → delete.
//
// The ordering is not arbitrary. The staging copies are deleted the moment the
// archive exists and passes `gzip -t`, BEFORE the download starts, so the peak disk
// cost on the host is the copies plus the archive rather than the copies plus the
// archive plus a download that may take twenty minutes. The archive itself is only
// deleted after the downloaded file has been opened locally and matched against the
// manifest — an interrupted download therefore leaves something to retry from, and a
// mismatch leaves the evidence on the server instead of destroying it.
//
// Two rules run through everything here:
//
//   - Nothing that could delete an ORIGINAL log exists. The cleanup paths are
//     reconstructed from the collection id, never taken from the manifest, and are
//     checked three times over (Go's validLogStagePath, the host's rtm_trusted, and a
//     literal `*/.rtaskmgr-logs/log-*` shell pattern) before an rm runs.
//   - Filenames from the host are never interpolated into a shell command. The copy
//     script reads its work list as NUL-separated fields from an uploaded file, so a
//     log called "새 파일.log" or one containing a newline is handled by the same code
//     path as any other, with nothing to quote and nothing to escape.
package monitor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// logMinFreeBytes is the headroom the collection refuses to eat into. Filling a
	// production /data to zero is a worse outage than the one being investigated.
	logMinFreeBytes = 2 << 30
	// logCopyOverhead covers per-file block rounding — 340 small files do not occupy
	// sum(size) bytes on a 4K filesystem — and leaves a little slack for the active
	// logs that keep growing between the survey and the copy.
	logCopyOverhead = 1.05
	// logJournalDefaultMax bounds a journald export. With no unit filter a journal
	// runs to gigabytes, and an unbounded export would walk straight past the disk
	// gate that everything else respects.
	logJournalDefaultMax = 512 << 20
	// logCtlDir holds the awk program and the work list inside the staging root. It
	// is excluded from the archive and disappears with the staging directory.
	logCtlDir = "_rtm"
	// logNoteBytes is how much of a failing command's stderr is carried back. Enough
	// to identify the failure, small enough that 300 failures cannot flood the reply.
	logNoteBytes = 400
)

// LogPick is one row the operator ticked in the collection tree.
type LogPick struct {
	Category string `json:"category"`
	Module   string `json:"module"`
	Dir      string `json:"dir"` // as surveyed; ignored for command modules
}

// LogRequest is one host's collection request.
type LogRequest struct {
	Picks      []LogPick `json:"picks"`
	FromMs     int64     `json:"fromMs"`
	ToMs       int64     `json:"toMs"`
	RecentDays int       `json:"recentDays"`
	// Target is the filesystem to stage on. Empty picks the roomiest candidate.
	Target string `json:"target"`
	// ServerDir overrides the archive's top-level directory name. The cluster view
	// assigns these for all hosts at once so two boxes with the same hostname cannot
	// merge into one directory; a single-host call derives it from the hostname.
	ServerDir string `json:"serverDir"`
	// HostName and Addr are this app's label for the host and its address. They come
	// from the caller for the same reason StartCapture takes a host name: the session
	// knows how to reach a host, not what the operator calls it.
	HostName        string   `json:"hostName"`
	Addr            string   `json:"addr"`
	TZ              string   `json:"tz"`
	JournalUnits    []string `json:"journalUnits"`
	JournalMaxBytes int64    `json:"journalMaxBytes"`
}

// LogPlan is the pre-flight answer: what would be collected, how big it is, and
// whether it fits.
type LogPlan struct {
	HostID     string      `json:"hostId"`
	Hostname   string      `json:"hostname"`
	ServerDir  string      `json:"serverDir"`
	Entries    []LogEntry  `json:"entries"`
	Files      int         `json:"files"`
	TotalBytes int64       `json:"totalBytes"`
	EstArchive int64       `json:"estArchive"`
	NeedBytes  int64       `json:"needBytes"`
	FreeBytes  int64       `json:"freeBytes"`
	Target     string      `json:"target"`
	Targets    []RecTarget `json:"targets"`
	OK         bool        `json:"ok"`
	Reason     string      `json:"reason"`
	Elevated   bool        `json:"elevated"`
	FromMs     int64       `json:"fromMs"`
	ToMs       int64       `json:"toMs"`
}

// LogJob is one collection, from the copy to the cleanup.
type LogJob struct {
	ID           string     `json:"id"`
	HostID       string     `json:"hostId"`
	ServerDir    string     `json:"serverDir"`
	StageRoot    string     `json:"stageRoot"`
	Archive      string     `json:"archive"`
	Target       string     `json:"target"`
	Files        int        `json:"files"`
	TotalBytes   int64      `json:"totalBytes"`
	ArchiveBytes int64      `json:"archiveBytes"`
	Stage string `json:"stage"`
	// Leftover names something this job left on the host that the operator should be
	// offered a way to remove. It is set explicitly rather than inferred, because a
	// leftover nobody is told about is how this feature would quietly fill a data
	// partition. Cleaning up by collection id removes the staging tree AND the
	// archive, so either path is a sufficient handle.
	Leftover string `json:"leftover,omitempty"`
	Err      string `json:"err,omitempty"`
	StartMs      int64      `json:"startMs"`
	EndMs        int64      `json:"endMs"`
	Entries      []LogEntry `json:"entries,omitempty"`
}

// Collection stages, reported as progress.
const (
	LogStageGate     = "gate"
	LogStageCopy     = "copy"
	LogStageArchive  = "archive"
	LogStageDownload = "download"
	LogStageVerify   = "verify"
	LogStageCleanup  = "cleanup"
	LogStageDone     = "done"
)

// ---- selection ----------------------------------------------------------

// logPickDef is a ticked row married to its catalog definition.
type logPickDef struct {
	Cat, Mod, Dir string
	Def           LogModuleDef
}

// resolvePicks matches what the operator ticked against the catalog and refuses
// anything the catalog does not describe.
//
// The directory arrives from the client, which got it from a survey of the host, so
// it is not trusted input: it must be one of the module's own paths or something a
// discovery glob for that category matches. Without this a stale or tampered
// selection could point the collector at any readable directory on the box —
// harmless for reading, but it would end up inside an archive that claims to be the
// module's logs.
func resolvePicks(cat LogCatalog, picks []LogPick) (out []logPickDef, rejected []string) {
	byCat := map[string]LogCategoryDef{}
	for _, c := range cat.Categories {
		byCat[c.Key] = c
	}
	seen := map[string]bool{}
	for _, p := range picks {
		key := p.Category + "/" + p.Module
		if seen[key] {
			continue
		}
		c, ok := byCat[p.Category]
		if !ok {
			rejected = append(rejected, key+": 알 수 없는 분류")
			continue
		}
		var def LogModuleDef
		found := false
		for _, d := range c.Modules {
			if d.Name == p.Module {
				def, found = d, true
				break
			}
		}
		if !found {
			// A module discovered by a glob has no definition; it is named after the
			// directory that was found, and carries no filter of its own.
			def = LogModuleDef{Name: p.Module}
		}
		if def.IsCmd() {
			seen[key] = true
			out = append(out, logPickDef{Cat: p.Category, Mod: p.Module, Def: def})
			continue
		}
		if !validAbsPath(p.Dir) {
			rejected = append(rejected, key+": 사용할 수 없는 경로")
			continue
		}
		if !pickAllowed(c, def, p.Dir) {
			rejected = append(rejected, key+": 카탈로그에 없는 경로 — "+p.Dir)
			continue
		}
		seen[key] = true
		out = append(out, logPickDef{Cat: p.Category, Mod: p.Module, Dir: p.Dir, Def: def})
	}
	return out, rejected
}

// pickAllowed reports whether dir is a place this category may collect from.
func pickAllowed(c LogCategoryDef, def LogModuleDef, dir string) bool {
	for _, p := range def.Paths {
		if p == dir {
			return true
		}
	}
	for _, g := range c.DiscoverGlobs {
		if ok, _ := path.Match(g, dir); ok {
			return true
		}
	}
	return false
}

// ---- enumeration --------------------------------------------------------

// logFileRow is one file the host reported.
type logFileRow struct {
	Cat, Mod, Dir, Rel string
	Size               int64
	MtimeMs            int64
	// IsFile records that the catalog path WAS the file (find's %P came back empty),
	// which is what makes keepalived a more specific claim on /var/log/messages than
	// the /var/log scan.
	IsFile bool
}

// logEnumMiss is a module that could not be listed.
type logEnumMiss struct{ Cat, Mod, Why string }

// logEnumScript lists the actual files behind the selection.
//
// Records are NUL-separated, not newline-separated, because a log file may legally
// be called "report\nfinal.log" and a line-oriented protocol would split it into two
// half-records — one of which would parse as a plausible file. find's own -printf
// emits the NULs, so no per-file fork is needed for 6489 kafka files.
func logEnumScript(picks []logPickDef, fromMs int64) string {
	var sb strings.Builder
	sb.WriteString("set -u\nexport LC_ALL=C\n")
	// The host's UTC offset comes back with its name. Rotated filenames carry dates
	// the HOST wrote ("messages-20260705"), and reading them in the client's zone
	// shifts a file's believed coverage by hours — in the direction that makes
	// classifyFile skip a file that actually holds the incident.
	sb.WriteString("printf 'HOST|%s|%s\\0' \"$(hostname 2>/dev/null | base64 -w0)\" \"$(date +%z 2>/dev/null)\"\n")
	sb.WriteString(`rtm_enum(){
  d=$(readlink -f "$RTM_DIR" 2>/dev/null || printf %s "$RTM_DIR")
  if [ ! -e "$d" ]; then printf 'ERR|%s|%s|missing\0' "$RTM_CAT" "$RTM_MOD"; return; fi
  if [ ! -r "$d" ]; then printf 'ERR|%s|%s|denied\0' "$RTM_CAT" "$RTM_MOD"; return; fi
  printf 'MOD|%s|%s|%s\0' "$RTM_CAT" "$RTM_MOD" "$(printf %s "$d" | base64 -w0)"
  eval "find \"\$d\" $RTM_FIND -printf '%s|%T@|%P\0'" 2>/dev/null
}
`)
	for _, p := range picks {
		if p.Def.IsCmd() {
			continue
		}
		expr := logFindExpr(p.Def, fromMs)
		sb.WriteString("RTM_CAT=" + shellQuote(p.Cat) + "; RTM_MOD=" + shellQuote(p.Mod) +
			"; RTM_DIR=" + shellQuote(p.Dir) + "; RTM_FIND=" + shellQuote(expr) + "; rtm_enum\n")
	}
	return sb.String()
}

// logEnumResult is what one host's enumeration produced.
type logEnumResult struct {
	Hostname string
	// Offset is the host's UTC offset in seconds, and Loc the location built from it.
	// A host that did not answer leaves Loc nil and the caller falls back to the
	// client's zone.
	Loc    *time.Location
	Rows   []logFileRow
	Misses []logEnumMiss
	// Listed marks "<cat>/<mod>" for every module the host actually reached, so a
	// module that returned no files can be told apart from one that never ran.
	Listed map[string]bool
	// Records counts every well-formed record. Zero means the enumeration never ran
	// — a rejected sudo password, a dropped session — which must never be mistaken
	// for "this host has no matching logs".
	Records int
}

// parseLogEnum turns the enumeration into rows. Everything it reads came back from
// the host, so a malformed record is dropped rather than repaired.
func parseLogEnum(out string) logEnumResult {
	res := logEnumResult{Listed: map[string]bool{}}
	dec := func(s string) string {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
		if err != nil {
			return ""
		}
		return string(b)
	}
	var curCat, curMod, curDir string
	for _, rec := range strings.Split(out, "\x00") {
		rec = strings.TrimLeft(rec, "\r\n")
		if rec == "" {
			continue
		}
		switch {
		case strings.HasPrefix(rec, "HOST|"):
			p := strings.SplitN(rec[5:], "|", 2)
			res.Hostname = strings.TrimSpace(dec(p[0]))
			if len(p) == 2 {
				res.Loc = zoneFromOffset(strings.TrimSpace(p[1]))
			}
			res.Records++
		case strings.HasPrefix(rec, "MOD|"):
			p := strings.SplitN(rec[4:], "|", 3)
			if len(p) != 3 {
				continue
			}
			res.Records++
			curCat, curMod, curDir = p[0], p[1], dec(p[2])
			res.Listed[curCat+"/"+curMod] = true
			if !validAbsPath(curDir) {
				// I1: this path came back from the host. Blanking it drops every file
				// record that follows, so the module has to be RECORDED as dropped —
				// otherwise it vanishes from the plan, from the manifest and from the
				// "수집되지 않은 항목" section, and the collection still verifies clean.
				res.Misses = append(res.Misses, logEnumMiss{Cat: curCat, Mod: curMod, Why: "unusable"})
				curDir = ""
			}
		case strings.HasPrefix(rec, "ERR|"):
			p := strings.SplitN(rec[4:], "|", 3)
			if len(p) != 3 {
				continue
			}
			res.Records++
			res.Misses = append(res.Misses, logEnumMiss{Cat: p[0], Mod: p[1], Why: p[2]})
		default:
			if curDir == "" {
				continue
			}
			p := strings.SplitN(rec, "|", 3)
			if len(p) != 3 {
				continue
			}
			size, err := strconv.ParseInt(p[0], 10, 64)
			if err != nil || size < 0 {
				continue
			}
			mt, err := strconv.ParseFloat(p[1], 64)
			if err != nil || mt < 0 {
				continue
			}
			res.Records++
			row := logFileRow{
				Cat: curCat, Mod: curMod, Dir: curDir, Rel: p[2],
				Size: size, MtimeMs: int64(mt * 1000),
			}
			if row.Rel == "" {
				// The catalog path was the file itself (/var/log/messages).
				row.Rel = path.Base(curDir)
				row.IsFile = true
			}
			res.Rows = append(res.Rows, row)
		}
	}
	return res
}

// zoneFromOffset turns `date +%z` ("+0900", "-0500") into a location. Rotated
// filenames are dates the HOST wrote, so they have to be read in the host's zone.
func zoneFromOffset(z string) *time.Location {
	if len(z) != 5 || (z[0] != '+' && z[0] != '-') {
		return nil
	}
	h, err1 := strconv.Atoi(z[1:3])
	m, err2 := strconv.Atoi(z[3:5])
	if err1 != nil || err2 != nil || h > 14 || m > 59 {
		return nil
	}
	secs := h*3600 + m*60
	if z[0] == '-' {
		secs = -secs
	}
	return time.FixedZone("host"+z, secs)
}

// sourcePath is the absolute path of a row on the host.
func (r logFileRow) sourcePath() string {
	if r.IsFile {
		return r.Dir
	}
	return strings.TrimRight(r.Dir, "/") + "/" + r.Rel
}

// ---- T1: what has to be read at all -------------------------------------

// prevRotationFor works out the start bound of each logrotate dateext file.
//
// "messages-20260705" is named for the day it was ROTATED, so its content starts at
// the PREVIOUS rotation — which is only knowable by looking at its siblings. Getting
// this backwards is not a cosmetic error: it would make the file look like it holds
// 07-05 only, and a request for 06-30~07-04 would skip the very file that holds
// those days.
func prevRotationFor(rows []logFileRow, loc *time.Location) []int64 {
	type sk struct{ cat, mod, series string }
	idxBySeries := map[sk][]int{}
	dayOf := make([]int64, len(rows))
	for i, r := range rows {
		series, day, ok := dateExtSeries(path.Base(r.Rel), loc)
		if !ok {
			continue
		}
		dayOf[i] = day
		k := sk{r.Cat, r.Mod, series}
		idxBySeries[k] = append(idxBySeries[k], i)
	}
	prev := make([]int64, len(rows))
	for _, idxs := range idxBySeries {
		sort.Slice(idxs, func(a, b int) bool { return dayOf[idxs[a]] < dayOf[idxs[b]] })
		for n, i := range idxs {
			if n == 0 {
				continue // no earlier sibling: the start stays unknown, so it straddles
			}
			// MIDNIGHT of the previous rotation day, i.e. the EARLIEST instant this file
			// could begin at — never the latest.
			//
			// logrotate.timer on RHEL 8/9 fires around 00:00 with an hour of slack, so
			// "messages-20260705" really begins somewhere in the small hours of 07-04.
			// Taking the END of that day instead (the obvious-looking +24h-1) puts the
			// believed start almost a full day too late, and classifyFile then answers
			// LogT1Skip for any window ending before it — so a request for "up to noon on
			// 07-04" skips the one file that holds 07-04. Worse, for daily rotation it
			// yields FirstMs > LastMs, a coverage window that cannot exist. An estimate
			// that is too early only makes the file straddle, which is the safe answer.
			prev[i] = dayOf[idxs[n-1]]
		}
	}
	return prev
}

// dateExtSeries splits "messages-20260705.gz" into ("messages", 2026-07-05). It
// returns false for the other rotation conventions, which carry their own meaning
// and must not be folded into this one.
func dateExtSeries(base string, loc *time.Location) (series string, dayMs int64, ok bool) {
	if hourlyRe.MatchString(base) || log4jDateRe.MatchString(base) || midDateRe.MatchString(base) {
		return "", 0, false
	}
	m := dateExtRe.FindStringSubmatch(base)
	if m == nil {
		return "", 0, false
	}
	d := time.Date(atoi(m[1]), time.Month(atoi(m[2])), atoi(m[3]), 0, 0, 0, 0, loc)
	return base[:len(base)-len(m[0])], d.UnixMilli(), true
}

// planEntries applies the T1 decision to every row.
//
// Files that fall entirely outside the window are NOT listed one by one: a kafka
// directory holds thousands of them and a manifest that names each would bury the
// handful of things the operator has to read. They are collapsed into one line per
// module, which keeps the manifest honest ("214 files were left out, here is why")
// without making it useless.
func planEntries(hostID string, rows []logFileRow, misses []logEnumMiss,
	fromMs, toMs int64, loc *time.Location) []LogEntry {

	prev := prevRotationFor(rows, loc)
	type skipAgg struct {
		n     int
		bytes int64
		dir   string
	}
	skipped := map[string]*skipAgg{}
	var order []string

	entries := make([]LogEntry, 0, len(rows))
	for i, r := range rows {
		cov := filenameCoverage(r.Rel, r.MtimeMs, prev[i], loc)
		act := classifyFile(cov, fromMs, toMs)

		note := ""
		if act == LogT1Filter && compressedExts[strings.ToLower(path.Ext(r.Rel))] {
			// Reading a .gz back through zcat, filtering and recompressing costs CPU on
			// a host that is already unwell, to save part of a file that is already
			// small. Over-inclusion is the standing bias, so take it whole and say so.
			act = LogT1Whole
			note = "압축 파일이라 전체를 담았습니다"
		}
		if act == LogT1Skip {
			k := r.Cat + "/" + r.Mod
			a := skipped[k]
			if a == nil {
				a = &skipAgg{dir: r.Dir}
				skipped[k] = a
				order = append(order, k)
			}
			a.n++
			a.bytes += r.Size
			continue
		}
		e := LogEntry{
			HostID: hostID, Category: r.Cat, Module: r.Mod,
			Source: r.sourcePath(), Rel: r.Rel,
			SizeBytes: r.Size, MtimeMs: r.MtimeMs,
			FirstMs: cov.FirstMs, LastMs: cov.LastMs,
			Action: act, Note: note,
		}
		if act == LogT1Filter {
			e.Cut = true
			// Only the boundary that actually cuts this file gets a marker. A file that
			// merely starts before the window is not "cut at the end" too.
			if fromMs > 0 && !(cov.FirstMs > 0 && cov.FirstMs >= fromMs) {
				e.CutFrom = compactDate(fromMs)
			}
			if toMs > 0 && !(cov.LastMs > 0 && cov.LastMs <= toMs) {
				e.CutTo = compactDate(toMs)
			}
			if e.CutFrom == "" && e.CutTo == "" {
				// Nothing to mark: treat it as a whole copy rather than inventing a label.
				e.Cut = false
				e.Action = LogT1Whole
			}
		}
		entries = append(entries, e)
	}

	for _, k := range order {
		a := skipped[k]
		i := strings.IndexByte(k, '/')
		entries = append(entries, LogEntry{
			HostID: hostID, Category: k[:i], Module: k[i+1:], Source: a.dir,
			Skipped: fmt.Sprintf("요청 기간 밖 %d개 (%s) 제외", a.n, humanBytes(a.bytes)),
		})
	}
	for _, ms := range misses {
		why := "읽을 수 없습니다"
		switch ms.Why {
		case "missing":
			why = "이 서버에 없습니다"
		case "denied":
			why = "권한이 없습니다 (sudo 로 다시 시도하세요)"
		case "unusable":
			why = "경로에 다룰 수 없는 문자가 있어 제외했습니다"
		case "notlisted":
			why = "열거하지 못했습니다 (수집에 포함되지 않았습니다)"
		case "empty":
			why = "이 기간에 해당하는 파일이 없습니다"
		}
		entries = append(entries, LogEntry{
			HostID: hostID, Category: ms.Cat, Module: ms.Mod, Skipped: why,
		})
	}
	return entries
}

// cmdEntries adds the modules that produce output instead of reading files.
func cmdEntries(hostID string, picks []logPickDef, fromMs, toMs int64,
	units []string, journalMax int64) []LogEntry {

	var out []LogEntry
	for _, p := range picks {
		if !p.Def.IsCmd() {
			continue
		}
		cmd, outFile := p.Def.Cmd, p.Def.OutFile
		if p.Cat == LogCatJournal {
			jc, err := journalCmd(fromMs, toMs, units)
			if err != nil {
				out = append(out, LogEntry{HostID: hostID, Category: p.Cat, Module: p.Mod,
					Skipped: err.Error()})
				continue
			}
			cmd = jc
		}
		if outFile == "" {
			outFile = sanitizeSegment(p.Mod) + ".txt"
		}
		out = append(out, LogEntry{
			HostID: hostID, Category: p.Cat, Module: p.Mod,
			Source: cmd, Rel: outFile, IsCmd: true, Action: LogT1Whole,
		})
	}
	return out
}

// ---- the work list ------------------------------------------------------

// logJobFields is how many NUL-separated fields make one job record.
const logJobFields = 6

// logJobRecords renders the work list the copy script consumes.
//
// Every field is NUL-terminated raw bytes: no quoting, no escaping, no encoding.
// That is the whole reason a log called "장애 기록\n.log" needs no special case
// anywhere in this feature — the only byte that cannot appear in a filename is the
// one used as the separator.
func logJobRecords(stageRoot string, entries []LogEntry, loc *time.Location) (jobs []byte, dirs []byte) {
	var jb, db bytes.Buffer
	seenDir := map[string]bool{}
	addDir := func(p string) {
		d := path.Dir(p)
		if d == "" || d == "." || seenDir[d] {
			return
		}
		seenDir[d] = true
		db.WriteString(d)
		db.WriteByte(0)
	}
	root := strings.TrimRight(stageRoot, "/")
	for i, e := range entries {
		if e.Skipped != "" || e.DupOf != "" || e.Arch == "" {
			continue
		}
		mode := LogT1Whole
		switch {
		case e.IsCmd:
			mode = "cmd"
		case e.Action == LogT1Filter:
			mode = LogT1Filter
		}
		dst := root + "/" + e.ArchWhole
		dstCut := root + "/" + e.Arch
		src, extra := e.Source, ""
		switch mode {
		case LogT1Filter:
			extra = strconv.Itoa(seedYearFor(e.Rel, e.MtimeMs, loc))
		case "cmd":
			// A command module has no source file; the command travels in `extra`.
			src, extra = "", e.Source
		}
		for _, f := range []string{strconv.Itoa(i), mode, src, dst, dstCut, extra} {
			jb.WriteString(f)
			jb.WriteByte(0)
		}
		addDir(dst)
		addDir(dstCut)
	}
	return jb.Bytes(), db.Bytes()
}

// ---- host scripts -------------------------------------------------------

// filterActionFor states, in Go, the rule the copy script applies to each filtered
// file. The script has to decide on the host — a round trip per boundary file would
// mean dozens of them on a link that is often the slow part — but a rule that lives
// only in an embedded awk snippet drifts. This is the twin the tests hold it against,
// and it must agree with filterVerdict, which supplies the wording for the same
// decision.
//
// Everything that is not a clean, well-understood filtered result becomes a whole
// copy. Only "the file is datable and none of it is in range" may produce nothing.
func filterActionFor(rc int, st LogFilterStats) string {
	switch {
	case !st.Found:
		return "whole"
	case rc == logFilterOutOfRange:
		return "none"
	case rc != logFilterEmitted:
		return "whole"
	case st.Lines > 0 && st.Emitted == 0:
		return "whole"
	case st.Lines > 0 && st.Dated*10 < st.Lines:
		return "whole"
	}
	return "filtered"
}

// logCopyScript copies (and where needed filters) every job into the staging tree.
//
// The filter's verdict is taken HERE rather than round-tripping each file to Go,
// because a boundary file per module means dozens of round trips on a link that may
// already be the slow part. The rule it applies is the same one filterVerdict states
// in Go, and TestCopyScriptVerdictMatchesGo pins the two together: anything that is
// not a clean, well-understood filtered result falls back to copying the file whole.
func logCopyScript(stageRoot string, fromMs, toMs int64, tz string, chownUID int, cmdCap int64) string {
	root := shellQuote(stageRoot)
	if cmdCap <= 0 {
		cmdCap = logJournalDefaultMax
	}
	var sb strings.Builder
	sb.WriteString("set -u\nexport LC_ALL=C\numask 077\n")
	sb.WriteString("ROOT=" + root + "\nCTL=\"$ROOT/" + logCtlDir + "\"\n")
	sb.WriteString("CAP=" + strconv.FormatInt(cmdCap, 10) + "\n")
	sb.WriteString("[ -d \"$CTL\" ] || { echo 'ERR|CTL'; exit 90; }\n")
	sb.WriteString(hostRangeKeys(fromMs, toMs, tz) + "\n")
	// -r so an empty list is a no-op rather than "mkdir: missing operand".
	sb.WriteString("xargs -0 -r mkdir -p -- < \"$CTL/dirs\" || { echo 'ERR|MKDIR'; exit 91; }\n")
	sb.WriteString("NI='nice -n 19'\ncommand -v ionice >/dev/null 2>&1 && NI=\"$NI ionice -c3\"\n")
	sb.WriteString(`exec 9<"$CTL/jobs"
while IFS= read -r -d '' -u 9 idx \
   && IFS= read -r -d '' -u 9 mode \
   && IFS= read -r -d '' -u 9 src \
   && IFS= read -r -d '' -u 9 dst \
   && IFS= read -r -d '' -u 9 dstcut \
   && IFS= read -r -d '' -u 9 extra; do
  case $mode in
  whole)
    $NI cp -p -- "$src" "$dst" 2>"$CTL/err"; rc=$?
    if [ "$rc" -eq 0 ]; then
      printf 'R|%s|whole|0|\n' "$idx"
    else
      # cp leaves the partial destination behind on a mid-transfer failure (EIO on a
      # failing disk, ENOSPC, a signal). Go marks this entry as not collected, so a
      # survivor would be archived while absent from the manifest and fail the whole
      # host's verification over one file out of three hundred.
      rm -f -- "$dst"
      printf 'R|%s|fail|%s|%s\n' "$idx" "$rc" "$(tail -c 400 "$CTL/err" 2>/dev/null | base64 -w0)"
    fi
    ;;
  filter)
    $NI awk -v S="$S" -v E="$E" -v Y="$extra" -v MAXPRE=5000 -f "$CTL/logfilter.awk" -- "$src" \
      > "$dstcut" 2>"$CTL/err"; rc=$?
    keep=$(awk -v RC="$rc" '
      /RTM_STAT/{ok=1; for(i=1;i<=NF;i++){n=split($i,a,"="); if(n==2) v[a[1]]=a[2]+0}}
      END{
        if(!ok){print "whole"; exit}
        if(RC==4){print "none"; exit}
        if(RC!=0){print "whole"; exit}
        if(v["lines"]>0 && v["emitted"]==0){print "whole"; exit}
        if(v["lines"]>0 && v["dated"]*10 < v["lines"]){print "whole"; exit}
        print "filtered"
      }' "$CTL/err" 2>/dev/null)
    case $keep in
    filtered) : ;;
    whole)
      rm -f -- "$dstcut"
      # The fallback copy can fail on its own, and its stderr must replace the
      # filter's — reporting "복사 실패: RTM_STAT lines=100 dated=90 …" instead of
      # "No space left on device" hides the actual cause.
      $NI cp -p -- "$src" "$dst" 2>"$CTL/err" || { keep=fail; rm -f -- "$dst"; }
      ;;
    *) rm -f -- "$dstcut" ;;
    esac
    note=$(tail -c 400 "$CTL/err" 2>/dev/null | base64 -w0)
    printf 'R|%s|%s|%s|%s\n' "$idx" "$keep" "$rc" "$note"
    ;;
  cmd)
    # The command's own exit status has to survive the size bound. Piping straight
    # into head would make the pipeline's status head's — always 0 — so a journalctl
    # that failed outright and a journal cut off at the cap both looked like a clean
    # success with an empty or truncated file. The status is written to a side file
    # instead; if head closes the pipe first the writer dies of SIGPIPE and the
    # recorded status is 141, which is how truncation is told apart from failure.
    rm -f -- "$CTL/crc"
    { bash -c "$extra" 2>"$CTL/err"; echo $? > "$CTL/crc"; } | head -c "$CAP" > "$dst"
    rc=$(cat "$CTL/crc" 2>/dev/null); rc=${rc:-141}
    sz=$(stat -c%s "$dst" 2>/dev/null || echo 0)
    keep=cmd; [ "$sz" -ge "$CAP" ] && keep=cmdcut
    printf 'R|%s|%s|%s|%s\n' "$idx" "$keep" "$rc" "$(tail -c 400 "$CTL/err" 2>/dev/null | base64 -w0)"
    ;;
  esac
done
exec 9<&-
rm -f -- "$CTL/err" "$CTL/crc"
`)
	if chownUID >= 0 {
		// Everything root just wrote is root-owned. Handing the staging tree back now
		// is what keeps the archive, the download and the cleanup unprivileged — the
		// alternative is an operator who cannot delete their own collection after a
		// password rotation.
		sb.WriteString("chown -R " + strconv.Itoa(chownUID) + " \"$ROOT\" 2>/dev/null || true\n")
	}
	sb.WriteString("echo 'DONE|copy'\n")
	return sb.String()
}

// logFoundScript lists what actually landed in the staging tree.
//
// This is the truth the manifest is built from, not the sizes taken during the
// survey: an active log grows between the two, a filtered file shrinks, and a copy
// can fail for one file out of three hundred. One find pass answers all of it
// without a stat per file.
func logFoundScript(stageRoot string) string {
	return "set -u\nexport LC_ALL=C\n" +
		"find " + shellQuote(stageRoot) + " -name " + shellQuote(logCtlDir) +
		" -prune -o -type f -printf 'F|%s|%P\\0' 2>/dev/null\n"
}

// parseLogFound maps archive-relative path → bytes on disk.
func parseLogFound(out string) map[string]int64 {
	found := map[string]int64{}
	for _, rec := range strings.Split(out, "\x00") {
		rec = strings.TrimLeft(rec, "\r\n")
		if !strings.HasPrefix(rec, "F|") {
			continue
		}
		p := strings.SplitN(rec[2:], "|", 2)
		if len(p) != 2 {
			continue
		}
		sz, err := strconv.ParseInt(p[0], 10, 64)
		if err != nil || p[1] == "" {
			continue
		}
		found[p[1]] = sz
	}
	return found
}

// logCopyResult is one 'R|' line from the copy script.
type logCopyResult struct {
	Idx  int
	Keep string // whole | filtered | none | fail | cmd
	RC   int
	Note string // decoded stderr tail
}

func parseLogCopyResults(out string) map[int]logCopyResult {
	res := map[int]logCopyResult{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "R|") {
			continue
		}
		p := strings.SplitN(line[2:], "|", 4)
		if len(p) != 4 {
			continue
		}
		idx, err := strconv.Atoi(p[0])
		if err != nil || idx < 0 {
			continue
		}
		rc, _ := strconv.Atoi(p[2])
		note := ""
		if b, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(p[3])); derr == nil {
			note = string(b)
		}
		res[idx] = logCopyResult{Idx: idx, Keep: p[1], RC: rc, Note: note}
	}
	return res
}

// logArchiveScript compresses the staging tree and then deletes it.
//
// Deleting here rather than after the download halves the peak disk cost: the copies
// and the archive are never both present while a multi-minute transfer runs. The
// archive is what the download and the verification work from, so nothing is lost.
//
// The rm is guarded three times over — Go checked the path before this script was
// built, rtm_trusted re-checks ownership and that it is not a symlink, and the case
// pattern refuses anything that is not one of our own collection directories.
func logArchiveScript(s *session, stageRoot, archive string) string {
	return "set -u\nexport LC_ALL=C\n" +
		"ROOT=" + shellQuote(stageRoot) + "\nARC=" + shellQuote(archive) + "\n" +
		trustedDirSh(s) + "\n" +
		"rtm_trusted \"$ROOT\" || { echo 'ERR|UNTRUSTED'; exit 3; }\n" +
		"case \"$ROOT\" in */" + logStageDirName + "/log-*) : ;; *) echo 'ERR|PATH'; exit 3;; esac\n" +
		"case \"$ARC\" in */" + logStageDirName + "/log-*.tar.gz) : ;; *) echo 'ERR|PATH'; exit 3;; esac\n" +
		"NI='nice -n 19'\ncommand -v ionice >/dev/null 2>&1 && NI=\"$NI ionice -c3\"\n" +
		// The second pattern is quoted so the shell cannot expand it against the
		// current directory before tar ever sees it.
		"$NI tar -C \"$ROOT\" --exclude=./" + logCtlDir + " \"--exclude=./" + logCtlDir + "/*\" " +
		"-czf \"$ARC\" . 2>\"$ROOT/" + logCtlDir + "/tarerr\"; trc=$?\n" +
		"terr=$(tail -c 400 \"$ROOT/" + logCtlDir + "/tarerr\" 2>/dev/null | base64 -w0)\n" +
		"if [ \"$trc\" -ge 2 ]; then echo \"ERR|TAR|$trc|$terr\"; exit 4; fi\n" +
		// gzip -t is the proof the archive is readable end to end. Without it a torn
		// write is only discovered after the staging copies have been deleted.
		"gzip -t -- \"$ARC\" 2>/dev/null || { echo \"ERR|GZIP|$terr\"; exit 5; }\n" +
		"sz=$(stat -c%s \"$ARC\" 2>/dev/null || echo -1)\n" +
		"rm -rf -- \"$ROOT\"\n" +
		"echo \"OK|$sz|$trc|$terr\"\n"
}

// logRemoveScript deletes one collection's leftovers: the staging directory, the
// archive, or both. It takes the collection id and rebuilds the paths itself — the
// manifest's original paths never reach a delete, which is what makes it impossible
// for this function to remove a real log.
func logRemoveScript(s *session, stageRoot, archive string) string {
	return "set -u\nexport LC_ALL=C\n" +
		"ROOT=" + shellQuote(stageRoot) + "\nARC=" + shellQuote(archive) + "\n" +
		trustedDirSh(s) + "\n" +
		"case \"$ROOT\" in */" + logStageDirName + "/log-*) : ;; *) echo 'ERR|PATH'; exit 3;; esac\n" +
		"case \"$ARC\" in */" + logStageDirName + "/log-*.tar.gz) : ;; *) echo 'ERR|PATH'; exit 3;; esac\n" +
		"if [ -e \"$ROOT\" ]; then rtm_trusted \"$ROOT\" && rm -rf -- \"$ROOT\" || echo 'ERR|UNTRUSTED'; fi\n" +
		"if [ -f \"$ARC\" ] && [ ! -L \"$ARC\" ]; then\n" +
		"  o=$(stat -c%u \"$ARC\" 2>/dev/null || echo -1)\n" +
		"  [ \"$o\" = " + strconv.Itoa(s.uid) + " ] && rm -f -- \"$ARC\" || echo 'ERR|OWNER'\n" +
		"fi\n" +
		"echo 'DONE|remove'\n"
}

// logOrphanScript finds collections a previous run left behind.
//
// These are the main way this feature could eat a server's disk: a download that
// died, an app that was closed mid-collection, a host that rebooted. They are
// invisible (a dotted directory on a data partition) unless something goes looking,
// so the UI shows them in their own section.
func logOrphanScript(s *session) string {
	return "set -u\nexport LC_ALL=C\n" + trustedDirSh(s) + "\n" +
		"for b in " + logBasesSh(s) + "; do\n" +
		"  d=\"$b/" + logStageDirName + "\"\n" +
		"  [ -d \"$d\" ] || continue\n" +
		"  rtm_trusted \"$d\" || continue\n" +
		"  for p in \"$d\"/log-*; do\n" +
		"    [ -e \"$p\" ] || continue\n" +
		"    case \"$p\" in *.tar.gz) k=A;; *) k=D;; esac\n" +
		"    sz=$(du -sb -- \"$p\" 2>/dev/null | awk 'NR==1{print $1}')\n" +
		"    mt=$(stat -c%Y -- \"$p\" 2>/dev/null || echo 0)\n" +
		"    printf 'O|%s|%s|%s|%s\\n' \"$k\" \"${sz:-0}\" \"$mt\" \"$p\"\n" +
		"  done\n" +
		"done\n"
}

// logBasesSh lists the filesystems a collection could have staged on.
//
// It deliberately does NOT use "$HOME" or "$(id -u)" the way the capture scan does.
// This script runs under sudo whenever the session is elevated, and sudo resets both
// to root's — so the operator's own leftovers under /home/liz would be invisible in
// exactly the case (an interrupted privileged copy) that produces the biggest ones.
// The login user's home is resolved by name instead.
func logBasesSh(s *session) string {
	b := " /var/tmp /dev/shm /tmp /data /home"
	if s.uid >= 0 {
		b = " \"/run/user/" + strconv.Itoa(s.uid) + "\"" + b
	}
	if validUnixUser(s.user) {
		b = " \"$(getent passwd " + shellQuote(s.user) + " 2>/dev/null | cut -d: -f6)\"" + b
	}
	if s.stageDir != "" && validAbsPath(s.stageDir) {
		b = shellQuote(s.stageDir) + b
	}
	return b
}

// LogLeftover is one collection still occupying space on a host.
type LogLeftover struct {
	HostID  string `json:"hostId"`
	ID      string `json:"id"`
	Path    string `json:"path"`
	Kind    string `json:"kind"` // "dir" | "archive"
	Bytes   int64  `json:"bytes"`
	MtimeMs int64  `json:"mtimeMs"`
}

func parseLogOrphans(hostID, out string) []LogLeftover {
	var res []LogLeftover
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "O|") {
			continue
		}
		p := strings.SplitN(line[2:], "|", 4)
		if len(p) != 4 {
			continue
		}
		full := p[3]
		if !validAbsPath(full) || seen[full] {
			continue
		}
		id := path.Base(full)
		kind := "dir"
		if p[0] == "A" {
			kind, id = "archive", strings.TrimSuffix(id, ".tar.gz")
		}
		if !validCollectID(id) {
			continue // I1: not one of ours, so it is not ours to offer for deletion
		}
		seen[full] = true
		sz, _ := strconv.ParseInt(p[1], 10, 64)
		mt, _ := strconv.ParseInt(p[2], 10, 64)
		res = append(res, LogLeftover{HostID: hostID, ID: id, Path: full, Kind: kind,
			Bytes: sz, MtimeMs: mt * 1000})
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].MtimeMs != res[j].MtimeMs {
			return res[i].MtimeMs > res[j].MtimeMs
		}
		return res[i].Path < res[j].Path
	})
	return res
}

// ---- the disk gate ------------------------------------------------------

// logGate is the pre-flight. cp writes the selection in full before anything is
// compressed, so the copies AND the archive have to fit at the same time, and a
// margin has to survive both. This is the largest new risk the feature carries, and
// the answer to failing it is to un-tick something in the tree — never to proceed.
func logGate(totalBytes, estArchive, freeBytes int64) (need int64, ok bool, reason string) {
	need = int64(float64(totalBytes)*logCopyOverhead) + estArchive + logMinFreeBytes
	switch {
	case overSizeCap(totalBytes):
		return need, false, fmt.Sprintf("선택한 용량이 상한을 넘습니다: %s (서버당 상한 %s) — 트리에서 큰 모듈을 제외하세요",
			humanBytes(totalBytes), humanBytes(logSizeCapBytes))
	case freeBytes <= 0:
		return need, false, "저장 위치의 여유 공간을 확인할 수 없습니다"
	case need > freeBytes:
		return need, false, fmt.Sprintf("여유 공간이 부족합니다: %s 필요(사본 %s + 아카이브 %s + 여유 %s), %s 사용 가능",
			humanBytes(need), humanBytes(totalBytes), humanBytes(estArchive),
			humanBytes(logMinFreeBytes), humanBytes(freeBytes))
	}
	return need, true, ""
}

// ---- Manager API --------------------------------------------------------

// LogPlanFor enumerates what the selection actually contains and answers the only
// question worth asking before a copy starts: does it fit, and what will be cut.
func (m *Manager) LogPlanFor(hostID string, req LogRequest, cat LogCatalog) (LogPlan, error) {
	s := m.get(hostID)
	if s == nil {
		return LogPlan{}, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	if len(cat.Categories) == 0 {
		cat = defaultLogCatalog()
	}
	picks, rejected := resolvePicks(cat, req.Picks)
	if len(picks) == 0 {
		return LogPlan{}, fmt.Errorf("수집할 항목이 없습니다%s", joinReasons(rejected))
	}
	fromMs, toMs := clampLogRange(req.FromMs, req.ToMs, req.RecentDays, time.Now())

	out := ""
	if s.elevated {
		// /var/log/messages and the mariadb/zookeeper directories are unreadable to
		// the login user, so an unprivileged enumeration would report whole modules as
		// empty and the operator would download an archive missing exactly them.
		out, _ = m.sudoRun(s, logEnumScript(picks, fromMs))
	} else {
		out, _ = m.plainRun(s, logEnumScript(picks, fromMs))
	}
	en := parseLogEnum(out)
	// Not one well-formed record means the script never ran — a rejected sudo
	// password (the session's credentials were latched at connect and may since have
	// been rotated), a dropped channel. That is NOT "this host has no matching logs":
	// reported as such, a selection that also contains a command module would sail
	// through the gate and hand back an archive holding only dmesg, with every ticked
	// file module absent from the manifest as well as from the archive.
	if en.Records == 0 {
		return LogPlan{HostID: hostID, Elevated: s.elevated},
			fmt.Errorf("수집 대상을 조사하지 못했습니다 (권한 또는 연결을 확인하세요): %s", tailLines(out, 2))
	}
	// Filenames carry dates the HOST wrote, so they are read in the host's zone.
	loc := en.Loc
	if loc == nil {
		loc = time.Local
	}
	// Any pick that produced neither files nor an explicit failure has to be said out
	// loud. Silence here is the worst outcome the feature has: a module the operator
	// ticked simply not being in the archive, with nothing anywhere to say so.
	misses := append([]logEnumMiss(nil), en.Misses...)
	misses = append(misses, unlistedPicks(picks, en)...)

	entries := planEntries(hostID, en.Rows, misses, fromMs, toMs, loc)
	entries = append(entries, cmdEntries(hostID, picks, fromMs, toMs, req.JournalUnits, req.JournalMaxBytes)...)
	entries = dedupeEntries(entries, specificModules(en.Rows))

	serverDir := sanitizeSegment(req.ServerDir)
	if serverDir == "" {
		serverDir = serverDirNames([]ServerIdent{{
			HostID: hostID, Hostname: en.Hostname, DisplayName: req.HostName, Addr: req.Addr,
		}})[hostID]
	}
	entries = assignArchivePaths(serverDir, entries)

	files, total := selectedBytes(entries)
	est := estimateArchiveBytes(entries)
	targets := m.captureTargets(s)
	target, free := chooseLogTarget(targets, req.Target)

	plan := LogPlan{
		HostID: hostID, Hostname: en.Hostname, ServerDir: serverDir, Entries: entries,
		Files: files, TotalBytes: total, EstArchive: est,
		Target: target, Targets: targets, FreeBytes: free,
		Elevated: s.elevated, FromMs: fromMs, ToMs: toMs,
	}
	if target == "" {
		plan.Reason = "수집물을 저장할 파티션을 찾지 못했습니다"
		return plan, nil
	}
	if files == 0 {
		plan.Reason = "조건에 맞는 로그 파일이 없습니다" + joinReasons(rejected)
		return plan, nil
	}
	plan.NeedBytes, plan.OK, plan.Reason = logGate(total, est, free)
	if plan.OK && len(rejected) > 0 {
		plan.Reason = "일부 항목을 제외했습니다" + joinReasons(rejected)
	}
	return plan, nil
}

// unlistedPicks names the file modules that came back with neither a single file nor
// an explicit failure.
//
// The host prints an ERR| record for "missing" and "denied", but a module can also
// produce nothing at all — its directory resolved to a path Go refuses, the output
// was truncated, the find never ran. Without this the module is absent from the
// entries AND from the manifest's "수집되지 않은 항목" section, so the operator gets a
// clean, verifying archive with a whole module missing and nothing anywhere saying
// so. A module that legitimately holds no files in the window is reported the same
// way, which is correct: "0개" is information too.
func unlistedPicks(picks []logPickDef, en logEnumResult) []logEnumMiss {
	withRows := map[string]bool{}
	for _, r := range en.Rows {
		withRows[r.Cat+"/"+r.Mod] = true
	}
	reported := map[string]bool{}
	for _, ms := range en.Misses {
		reported[ms.Cat+"/"+ms.Mod] = true
	}
	var out []logEnumMiss
	for _, p := range picks {
		k := p.Cat + "/" + p.Mod
		if p.Def.IsCmd() || withRows[k] || reported[k] {
			continue
		}
		why := "notlisted" // the host never reached this module
		if en.Listed[k] {
			why = "empty" // it was reached and simply holds nothing in the window
		}
		out = append(out, logEnumMiss{Cat: p.Cat, Mod: p.Mod, Why: why})
	}
	return out
}

// chooseLogTarget picks the staging filesystem and reports its free space.
func chooseLogTarget(targets []RecTarget, want string) (string, int64) {
	for _, t := range targets {
		if t.Path == want {
			return t.Path, t.FreeBytes
		}
	}
	if best := defaultCaptureTarget(targets); best != nil {
		return best.Path, best.FreeBytes
	}
	return "", 0
}

// specificModules marks the modules that name an exact file rather than scanning a
// directory, so dedupeEntries can decide which claim on /var/log/messages wins.
func specificModules(rows []logFileRow) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		if r.IsFile {
			out[r.Cat+"/"+r.Mod] = true
		}
	}
	return out
}

func joinReasons(rs []string) string {
	if len(rs) == 0 {
		return ""
	}
	return " (" + strings.Join(rs, ", ") + ")"
}

// StartLogCollect runs the host-side half: stage, copy, manifest, archive.
//
// It re-plans rather than trusting the plan the UI is showing. Time has passed since
// the preview — files rotate, a partition fills — and the disk gate is only
// meaningful if it is evaluated against the state the copy will actually meet.
func (m *Manager) StartLogCollect(hostID string, req LogRequest, cat LogCatalog,
	progress func(stage string, done, total int64)) (LogJob, error) {

	s := m.get(hostID)
	if s == nil {
		return LogJob{}, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	step := func(stage string, done, total int64) {
		if progress != nil {
			progress(stage, done, total)
		}
	}
	step(LogStageGate, 0, 0)
	plan, err := m.LogPlanFor(hostID, req, cat)
	if err != nil {
		return LogJob{}, err
	}
	if !plan.OK {
		reason := plan.Reason
		if reason == "" {
			reason = "수집을 시작할 수 없습니다"
		}
		return LogJob{}, fmt.Errorf("%s", reason)
	}

	id := newCollectID()
	stageRoot := logStageRoot(plan.Target, id)
	archive := logArchivePath(plan.Target, id)
	if !validLogStagePath(stageRoot, id) || !validLogStagePath(archive, id) {
		return LogJob{}, fmt.Errorf("내부 오류: 스테이징 경로를 만들 수 없습니다")
	}
	job := LogJob{
		ID: id, HostID: hostID, ServerDir: plan.ServerDir, StageRoot: stageRoot,
		Archive: archive, Target: plan.Target, Stage: LogStageCopy,
		StartMs: time.Now().UnixMilli(), Entries: plan.Entries,
	}

	// abandon takes the staging tree back for a failure that happened before the
	// archive existed. There is nothing to retry from at that point — a retry gets a
	// new collection id — so leaving gigabytes of copies on a production data
	// partition is pure loss, and the very disk headroom logGate exists to protect is
	// what they consume. If the removal itself fails the caller is TOLD, because a
	// leftover nobody knows about is how this feature would quietly fill a server.
	abandon := func(cause error) (LogJob, error) {
		if rerr := m.removeCollect(s, stageRoot, archive); rerr != nil {
			job.Leftover = stageRoot
			return job, fmt.Errorf("%w — 서버에 %s 가 남아 있습니다 (정리에 실패: %v)",
				cause, stageRoot, rerr)
		}
		return job, cause
	}

	base := path.Dir(stageRoot) // <target>/.rtaskmgr-logs
	for _, d := range []string{base, stageRoot, stageRoot + "/" + logCtlDir} {
		if err := m.ensureUserDirStrict(s, d, "0700"); err != nil {
			return job, err
		}
	}
	if err := uploadBytes(s.client, logFilterAwk, stageRoot+"/"+logCtlDir+"/logfilter.awk", false); err != nil {
		return abandon(fmt.Errorf("필터 프로그램을 올리지 못했습니다: %w", err))
	}
	jobs, dirs := logJobRecords(stageRoot, plan.Entries, time.Local)
	if len(jobs) == 0 {
		return abandon(fmt.Errorf("복사할 파일이 없습니다"))
	}
	// The manifest's own directory must exist even if every copy fails.
	dirs = append(dirs, []byte(stageRoot+"/"+plan.ServerDir)...)
	dirs = append(dirs, 0)
	if err := uploadBytes(s.client, jobs, stageRoot+"/"+logCtlDir+"/jobs", false); err != nil {
		return abandon(fmt.Errorf("작업 목록을 올리지 못했습니다: %w", err))
	}
	if err := uploadBytes(s.client, dirs, stageRoot+"/"+logCtlDir+"/dirs", false); err != nil {
		return abandon(fmt.Errorf("작업 목록을 올리지 못했습니다: %w", err))
	}

	step(LogStageCopy, 0, int64(plan.Files))
	chownUID := -1
	copyOut := ""
	if s.elevated {
		chownUID = s.uid
	}
	script := logCopyScript(stageRoot, plan.FromMs, plan.ToMs, req.TZ, chownUID, req.JournalMaxBytes)
	if s.elevated {
		copyOut, _ = m.sudoRun(s, script)
	} else {
		copyOut, _ = m.plainRun(s, script)
	}
	if !strings.Contains(copyOut, "DONE|copy") {
		// The copy died part-way — a dropped channel, a rotated sudo password, the app
		// restarting — with up to the whole selection already on disk.
		return abandon(fmt.Errorf("복사가 완료되지 않았습니다: %s", tailLines(copyOut, 3)))
	}
	entries := applyCopyResults(plan.Entries, parseLogCopyResults(copyOut))

	foundOut, _ := m.plainRun(s, logFoundScript(stageRoot))
	entries = applyFound(entries, parseLogFound(foundOut))
	job.Entries = entries
	job.Files, job.TotalBytes = selectedBytes(entries)
	if job.Files == 0 {
		return abandon(fmt.Errorf("수집된 파일이 없습니다: %s", tailLines(copyOut, 2)))
	}

	srv := ServerIdent{HostID: hostID, Hostname: plan.Hostname,
		DisplayName: req.HostName, Addr: req.Addr}
	manifest := renderManifest(srv, plan.ServerDir, entries, plan.FromMs, plan.ToMs, time.Now())
	if err := uploadBytes(s.client, []byte(manifest),
		stageRoot+"/"+plan.ServerDir+"/_MANIFEST.txt", false); err != nil {
		return abandon(fmt.Errorf("매니페스트를 올리지 못했습니다: %w", err))
	}

	step(LogStageArchive, 0, 0)
	job.Stage = LogStageArchive
	arcOut, _ := m.plainRun(s, logArchiveScript(s, stageRoot, archive))
	sz, aerr := parseArchiveResult(arcOut)
	if aerr != nil {
		// The archive script deletes the staging tree only AFTER gzip -t passes, so a
		// failure here leaves the copies and possibly a partial archive. They are kept
		// on purpose — a torn archive is worth looking at — but the operator has to be
		// handed something to clean up with.
		job.Leftover = stageRoot
		return job, aerr
	}
	job.ArchiveBytes = sz
	job.Stage = LogStageDownload
	step(LogStageArchive, sz, sz)
	return job, nil
}

// parseArchiveResult reads the archive script's verdict.
func parseArchiveResult(out string) (int64, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		switch {
		case strings.HasPrefix(line, "OK|"):
			p := strings.SplitN(line[3:], "|", 4)
			sz, err := strconv.ParseInt(p[0], 10, 64)
			if err != nil || sz <= 0 {
				return 0, fmt.Errorf("아카이브 크기를 확인할 수 없습니다")
			}
			return sz, nil
		case strings.HasPrefix(line, "ERR|UNTRUSTED"):
			return 0, fmt.Errorf("스테이징 디렉터리를 신뢰할 수 없어 중단했습니다 (소유자·권한 확인)")
		case strings.HasPrefix(line, "ERR|PATH"):
			return 0, fmt.Errorf("내부 오류: 스테이징 경로가 올바르지 않습니다")
		case strings.HasPrefix(line, "ERR|TAR"):
			return 0, fmt.Errorf("압축에 실패했습니다: %s", decodeTail(line))
		case strings.HasPrefix(line, "ERR|GZIP"):
			return 0, fmt.Errorf("압축 파일이 손상되었습니다 (gzip -t 실패): %s", decodeTail(line))
		}
	}
	return 0, fmt.Errorf("압축 결과를 확인할 수 없습니다: %s", tailLines(out, 2))
}

// decodeTail pulls the base64 stderr tail off an ERR line.
func decodeTail(line string) string {
	i := strings.LastIndexByte(line, '|')
	if i < 0 || i+1 >= len(line) {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(line[i+1:]))
	if err != nil {
		return ""
	}
	return tailLines(string(b), 2)
}

// applyCopyResults folds what the host did back into the entries.
//
// The host decided the ACTION (it is the only place that can see the file); the
// wording of the note is produced here by filterVerdict so the two halves of that
// rule cannot drift into disagreeing about what happened.
func applyCopyResults(entries []LogEntry, res map[int]logCopyResult) []LogEntry {
	out := make([]LogEntry, len(entries))
	copy(out, entries)
	for i := range out {
		r, ok := res[i]
		if !ok {
			continue
		}
		// The filter's stats — and therefore its wording — only exist for a file that
		// was actually run through the filter. Reading them off a plain copy would
		// stamp every ordinary file with "필터가 정상 종료하지 않았습니다".
		wasFiltered := out[i].Action == LogT1Filter
		var st LogFilterStats
		note := ""
		if wasFiltered {
			st = parseFilterStats(r.Note)
			_, note = filterVerdict(r.RC, st)
		}
		switch r.Keep {
		case "filtered":
			out[i].Action = LogTakeFiltered
			out[i].Note = joinNote(out[i].Note, note)
			if st.Found {
				out[i].FirstMs, out[i].LastMs = keyMs(st.FirstKey), keyMs(st.LastKey)
			}
		case "whole":
			// The filter was not trusted here, so the file was taken whole — and the
			// ".from-" marker must come off, because a marker on a complete file is a
			// lie the reader has no way to detect.
			out[i].Action = LogTakeWhole
			out[i].Cut, out[i].CutFrom, out[i].CutTo = false, "", ""
			out[i].Arch = out[i].ArchWhole
			out[i].Note = joinNote(out[i].Note, note)
		case "none":
			out[i].Skipped = note
			if out[i].Skipped == "" {
				out[i].Skipped = "요청 기간에 해당하는 내용이 없습니다"
			}
		case "cmd", "cmdcut":
			out[i].Action = LogTakeWhole
			if r.Keep == "cmdcut" {
				// The output hit the size bound. journalctl emits oldest-first, so what
				// was dropped is the NEWEST part — exactly what an incident is about — and
				// saying nothing would present a truncated export as a complete one.
				out[i].Note = joinNote(out[i].Note, "출력 상한에 걸려 뒷부분이 잘렸습니다")
			} else if r.RC != 0 {
				// Not "and the note is non-empty": a command that failed silently still
				// failed, and its status is the only signal there is.
				out[i].Note = joinNote(out[i].Note, "명령 종료 코드 "+strconv.Itoa(r.RC))
			}
			if t := tailLines(r.Note, 1); t != "" {
				out[i].Note = joinNote(out[i].Note, t)
			}
		case "fail":
			out[i].Skipped = "복사 실패"
			if t := tailLines(r.Note, 1); t != "" {
				out[i].Skipped += ": " + t
			}
		}
	}
	return out
}

// applyFound replaces the surveyed sizes with what is really in the staging tree and
// drops anything that did not arrive.
//
// An entry that the manifest lists but the archive does not contain is the one
// failure mode that must never pass silently: the operator would hand over an
// archive believing a log is in it.
func applyFound(entries []LogEntry, found map[string]int64) []LogEntry {
	out := make([]LogEntry, len(entries))
	copy(out, entries)
	for i := range out {
		if out[i].Skipped != "" || out[i].DupOf != "" || out[i].Arch == "" {
			continue
		}
		sz, ok := found[out[i].Arch]
		if !ok {
			out[i].Skipped = "복사되지 않았습니다"
			continue
		}
		if out[i].IsCmd && sz == 0 {
			// An empty command output is not a collection. journald with Storage=none,
			// a masked journald, an SELinux denial, `dmesg -T` under dmesg_restrict —
			// all produce a 0-byte file, and shipping it as "명령 출력" tells the operator
			// the journal was collected when it was not.
			out[i].Skipped = "명령이 아무것도 출력하지 않았습니다"
			if out[i].Note != "" {
				out[i].Skipped += " — " + out[i].Note
			}
			continue
		}
		out[i].SizeBytes = sz
	}
	return out
}

func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + " · " + b
}

// keyMs turns a YYYYMMDDHHMMSS filter key into millis.
func keyMs(k int64) int64 {
	if k <= 0 {
		return 0
	}
	y, mo, d := int(k/10000000000), int((k/100000000)%100), int((k/1000000)%100)
	h, mi, sec := int((k/10000)%100), int((k/100)%100), int(k%100)
	if mo < 1 || mo > 12 || d < 1 || d > 31 {
		return 0
	}
	return time.Date(y, time.Month(mo), d, h, mi, sec, 0, time.Local).UnixMilli()
}

// DownloadLogArchiveTo streams one host's archive to a local file.
func (m *Manager) DownloadLogArchiveTo(ctx context.Context, hostID, collectID, target, localPath string,
	progress func(copied, total int64)) (int64, error) {

	s := m.get(hostID)
	if s == nil {
		return 0, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	if localPath == "" {
		return 0, fmt.Errorf("저장할 파일 경로가 비어 있습니다")
	}
	if !validCollectID(collectID) || !validAbsPath(target) {
		return 0, fmt.Errorf("잘못된 수집 식별자입니다")
	}
	archive := logArchivePath(target, collectID)
	if !validLogStagePath(archive, collectID) {
		return 0, fmt.Errorf("잘못된 아카이브 경로입니다")
	}
	info, err := m.statRemoteFile(s, archive)
	if err != nil || info.Size < 0 {
		return 0, fmt.Errorf("아카이브를 읽을 수 없습니다: %s", archive)
	}
	if info.Size == 0 {
		return 0, fmt.Errorf("아카이브가 비어 있습니다: %s", archive)
	}
	// No gzip on the wire: the file already is one, and re-compressing would only
	// burn CPU on a host that is by definition having a bad day. Integrity is not
	// lost — VerifyLogArchive reads the whole archive back through gzip locally.
	return m.streamRemoteFile(ctx, s, archive, localPath, info, false, progress)
}

// VerifyLogArchive opens the downloaded archive and matches it against the manifest.
//
// This is the gate in front of every delete. Nothing on the host is removed until
// the bytes on this machine have been read back and shown to hold what was promised,
// so a truncated transfer or a quarantining security agent leaves the collection
// recoverable on the server instead of destroying it.
func VerifyLogArchive(localPath, serverDir string, entries []LogEntry) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("내려받은 파일을 열 수 없습니다: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("압축을 풀 수 없습니다 (전송이 손상되었을 수 있습니다): %w", err)
	}
	defer gz.Close()

	have := map[string]int64{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("아카이브를 읽는 중 오류: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		have[strings.TrimPrefix(path.Clean(h.Name), "./")] = h.Size
	}
	return matchArchive(have, serverDir, entries)
}

// matchArchive compares what the archive holds against what the manifest claims.
func matchArchive(have map[string]int64, serverDir string, entries []LogEntry) error {
	want := map[string]int64{}
	// known holds the places an entry could legitimately occupy even though it is not
	// expected — a file whose copy failed part-way and left a remnant. Failing the
	// whole host's verification (and so refusing to clean up 339 good files) because
	// of one leftover would be the wrong trade.
	known := map[string]bool{}
	for _, e := range entries {
		if e.Arch != "" {
			known[e.Arch] = true
		}
		if e.ArchWhole != "" {
			known[e.ArchWhole] = true
		}
		if e.Skipped != "" || e.DupOf != "" || e.Arch == "" {
			continue
		}
		want[e.Arch] = e.SizeBytes
	}
	want[serverDir+"/_MANIFEST.txt"] = -1 // present, size not pinned

	var missing, wrong, extra []string
	for p, sz := range want {
		h, ok := have[p]
		if !ok {
			missing = append(missing, p)
			continue
		}
		if sz >= 0 && h != sz {
			wrong = append(wrong, fmt.Sprintf("%s (%d ≠ %d)", p, h, sz))
		}
	}
	for p := range have {
		if _, ok := want[p]; !ok && !known[p] {
			extra = append(extra, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(wrong)
	sort.Strings(extra)
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("누락 %d개: %s", len(missing), firstFew(missing)))
	}
	if len(wrong) > 0 {
		parts = append(parts, fmt.Sprintf("크기 불일치 %d개: %s", len(wrong), firstFew(wrong)))
	}
	if len(extra) > 0 {
		parts = append(parts, fmt.Sprintf("목록에 없는 항목 %d개: %s", len(extra), firstFew(extra)))
	}
	if len(parts) > 0 {
		return fmt.Errorf("아카이브가 매니페스트와 다릅니다 — 서버 정리를 하지 않았습니다: %s",
			strings.Join(parts, " / "))
	}
	return nil
}

func firstFew(v []string) string {
	if len(v) > 3 {
		return strings.Join(v[:3], ", ") + " …"
	}
	return strings.Join(v, ", ")
}

// removeCollect deletes one collection's leftovers, with the same privilege the copy
// that created them had.
//
// This has to be elevated whenever the session is. The copy script runs under sudo
// (that is the only way /var/log/messages and the mariadb/zookeeper directories can
// be read) with umask 077, so every directory it creates under the staging root is
// root-owned 0700 — and it hands the tree back with `chown -R` only as its very LAST
// step, which by definition has not run on any path that failed. An unprivileged
// `rm -rf` cannot descend into those directories: it deletes nothing, exits non-zero,
// and the script then answers ERR|UNTRUSTED, so the operator is told the leftover
// looks suspicious when in fact it is simply out of reach. The gigabytes stay.
func (m *Manager) removeCollect(s *session, stageRoot, archive string) error {
	script := logRemoveScript(s, stageRoot, archive)
	out := ""
	if s.elevated {
		out, _ = m.sudoRun(s, script)
	} else {
		out, _ = m.plainRun(s, script)
	}
	switch {
	case strings.Contains(out, "ERR|PATH"):
		return fmt.Errorf("내부 오류: 정리 경로가 올바르지 않습니다")
	case strings.Contains(out, "ERR|UNTRUSTED"):
		return fmt.Errorf("정리 대상의 소유자·권한이 예상과 달라 삭제하지 않았습니다")
	case strings.Contains(out, "ERR|OWNER"):
		return fmt.Errorf("아카이브의 소유자가 달라 삭제하지 않았습니다")
	case !strings.Contains(out, "DONE|remove"):
		return fmt.Errorf("정리에 실패했습니다: %s", tailLines(out, 2))
	}
	return nil
}

// CleanupLogCollect removes one collection's leftovers from the host.
//
// It takes only the id and the staging filesystem: the paths are rebuilt here, so no
// value that came from the host — or from the manifest — can steer a delete.
func (m *Manager) CleanupLogCollect(hostID, collectID, target string) error {
	s := m.get(hostID)
	if s == nil {
		return fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	if !validCollectID(collectID) || !validAbsPath(target) {
		return fmt.Errorf("잘못된 수집 식별자입니다")
	}
	stageRoot := logStageRoot(target, collectID)
	archive := logArchivePath(target, collectID)
	if !validLogStagePath(stageRoot, collectID) || !validLogStagePath(archive, collectID) {
		return fmt.Errorf("내부 오류: 정리 경로가 올바르지 않습니다")
	}
	return m.removeCollect(s, stageRoot, archive)
}

// ListLogCollects reports collections still occupying space on a host. It runs with
// the session's privilege for the same reason the removal does: an interrupted
// elevated copy leaves root-owned subdirectories that an unprivileged `du` cannot
// walk, so the leftover would be listed at a fraction of its real size.
func (m *Manager) ListLogCollects(hostID string) ([]LogLeftover, error) {
	s := m.get(hostID)
	if s == nil {
		return nil, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	out := ""
	if s.elevated {
		out, _ = m.sudoRun(s, logOrphanScript(s))
	} else {
		out, _ = m.plainRun(s, logOrphanScript(s))
	}
	return parseLogOrphans(hostID, out), nil
}

package monitor

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ms(t time.Time) int64 { return t.UnixMilli() }

func day(y int, mo time.Month, d int) time.Time {
	return time.Date(y, mo, d, 0, 0, 0, 0, time.Local)
}

// nulRec builds the host's NUL-separated enumeration output.
func nulRec(parts ...string) string { return strings.Join(parts, "\x00") + "\x00" }

// ---- enumeration --------------------------------------------------------

// A log file may legally contain a space, a Korean name, a pipe or an actual
// newline. The whole reason the protocol is NUL-separated is that a line-oriented
// one splits "장애\n기록.log" into two half-records, the second of which parses as a
// plausible file — so the operator ends up with a manifest naming a file that does
// not exist and missing the one that does.
func TestParseLogEnumSurvivesHostileFilenames(t *testing.T) {
	dirB64 := "L2RhdGEva2Fma2EtbG9n" // /data/kafka-log
	out := nulRec(
		"HOST|dHJ1bmst", // "trunk-"
		"MOD|middleware|kafka|"+dirB64,
		"120|1751000000.5|server.log",
		"64|1751000001.0|장애 기록.log",
		"32|1751000002.0|weird\nname.log",
		"16|1751000003.0|pipe|in|name.log",
		"8|1751000004.0|trailing space .log",
	)
	en := parseLogEnum(out)
	hostname, rows, misses := en.Hostname, en.Rows, en.Misses
	if hostname != "trunk-" {
		t.Errorf("hostname = %q", hostname)
	}
	if len(misses) != 0 {
		t.Errorf("unexpected misses: %v", misses)
	}
	want := []struct {
		rel  string
		size int64
	}{
		{"server.log", 120},
		{"장애 기록.log", 64},
		{"weird\nname.log", 32},
		{"pipe|in|name.log", 16},
		{"trailing space .log", 8},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i].Rel != w.rel || rows[i].Size != w.size {
			t.Errorf("row %d = %q/%d, want %q/%d", i, rows[i].Rel, rows[i].Size, w.rel, w.size)
		}
		if rows[i].Dir != "/data/kafka-log" {
			t.Errorf("row %d dir = %q", i, rows[i].Dir)
		}
		if rows[i].sourcePath() != "/data/kafka-log/"+w.rel {
			t.Errorf("row %d source = %q", i, rows[i].sourcePath())
		}
	}
}

// find prints an empty %P when the catalog named the file itself, which is what
// makes keepalived a more specific claim on /var/log/messages than the /var/log scan.
func TestParseLogEnumMarksSingleFileModules(t *testing.T) {
	out := nulRec(
		"MOD|middleware|keepalived|L3Zhci9sb2cvbWVzc2FnZXM=", // /var/log/messages
		"4096|1751000000.0|",
	)
	rows := parseLogEnum(out).Rows
	if len(rows) != 1 || !rows[0].IsFile {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Rel != "messages" || rows[0].sourcePath() != "/var/log/messages" {
		t.Errorf("rel=%q source=%q", rows[0].Rel, rows[0].sourcePath())
	}
	if !specificModules(rows)["middleware/keepalived"] {
		t.Error("a module that names an exact file must count as the specific claim")
	}
}

func TestParseLogEnumRejectsGarbage(t *testing.T) {
	out := nulRec(
		"MOD|system|var-log|L3Zhci9sb2c=", // /var/log
		"notasize|1751000000.0|x.log",
		"-5|1751000000.0|neg.log",
		"10|notatime|bad.log",
		"10|1751000000.0", // too few fields
		"64|1751000000.0|good.log",
	)
	rows := parseLogEnum(out).Rows
	if len(rows) != 1 || rows[0].Rel != "good.log" {
		t.Fatalf("malformed records were not dropped: %+v", rows)
	}
	// A directory the host could not resolve must not silently adopt the previous
	// module's directory — that would file another module's logs under this name.
	bad := nulRec("MOD|system|x|"+b64("/var/log; rm -rf /"), "64|1751000000.0|a.log")
	enBad := parseLogEnum(bad)
	rows2 := enBad.Rows
	if len(rows2) != 0 {
		t.Errorf("rows adopted after an invalid directory: %+v", rows2)
	}
}

func b64(s string) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out []byte
	for i := 0; i < len(s); i += 3 {
		var n uint32
		k := 0
		for j := 0; j < 3; j++ {
			n <<= 8
			if i+j < len(s) {
				n |= uint32(s[i+j])
				k++
			}
		}
		for j := 0; j < 4; j++ {
			if j <= k {
				out = append(out, tbl[(n>>uint(18-6*j))&0x3f])
			} else {
				out = append(out, '=')
			}
		}
	}
	return string(out)
}

// ---- T1 ------------------------------------------------------------------

// "messages-20260705" is named for the day it was ROTATED, so it holds the days
// BEFORE that — which only its siblings can say. Read the other way round, a request
// for 06-30~07-04 skips the one file that actually holds those days.
func TestPrevRotationForDateExt(t *testing.T) {
	rows := []logFileRow{
		{Cat: "system", Mod: "var-log", Rel: "messages-20260712"},
		{Cat: "system", Mod: "var-log", Rel: "messages-20260705"},
		{Cat: "system", Mod: "var-log", Rel: "messages-20260628"},
		{Cat: "system", Mod: "var-log", Rel: "messages"},                         // active, no date
		{Cat: "system", Mod: "var-log", Rel: "secure-20260705"},                  // different series
		{Cat: "modules", Mod: "lizcollector", Rel: "collector.log.2026-07-05_1"}, // log4j2, not dateext
	}
	prev := prevRotationFor(rows, time.Local)
	if prev[2] != 0 {
		t.Error("the oldest file in a series has no predecessor, so its start must stay unknown")
	}
	if prev[1] == 0 || prev[1] >= ms(day(2026, 7, 5)) {
		t.Errorf("messages-20260705 must start after 06-28's rotation, got %d", prev[1])
	}
	if prev[0] == 0 || prev[0] >= ms(day(2026, 7, 12)) {
		t.Errorf("messages-20260712 must start after 07-05's rotation, got %d", prev[0])
	}
	if prev[3] != 0 || prev[4] != 0 || prev[5] != 0 {
		t.Error("only dateext files in the same series may take a predecessor")
	}
	// A log4j2 name means the opposite thing and must never be folded into a series.
	if _, _, ok := dateExtSeries("collector.log.2026-07-05_1", time.Local); ok {
		t.Error("log4j2 rotation must not be read as logrotate dateext")
	}
	if _, _, ok := dateExtSeries("zookeeper-2026-07-20.log", time.Local); ok {
		t.Error("zookeeper's mid-name date must not be read as logrotate dateext")
	}
}

func TestPlanEntriesClassifies(t *testing.T) {
	from := ms(day(2026, 7, 10))
	rows := []logFileRow{
		// Entirely inside the window.
		{Cat: "modules", Mod: "lizcollector", Dir: "/l/logs", Rel: "collector.log.2026-07-12_1",
			Size: 100, MtimeMs: ms(day(2026, 7, 13))},
		// The never-rotated active log: unknown start, so it straddles and is filtered.
		{Cat: "modules", Mod: "lizcollector", Dir: "/l/logs", Rel: "collector.log",
			Size: 200, MtimeMs: ms(day(2026, 7, 20))},
		// Last written before the window: not one byte of it is read.
		{Cat: "modules", Mod: "lizcollector", Dir: "/l/logs", Rel: "collector.log.2026-07-01_1",
			Size: 300, MtimeMs: ms(day(2026, 7, 2))},
		// A straddling .gz: recompressing to trim it is not worth the CPU, so it is
		// taken whole and the manifest says why.
		{Cat: "modules", Mod: "lizcollector", Dir: "/l/logs", Rel: "collector.log.2026-07-09_1.gz",
			Size: 400, MtimeMs: ms(day(2026, 7, 11))},
	}
	got := planEntries("h1", rows, nil, from, 0, time.Local)

	byRel := map[string]LogEntry{}
	for _, e := range got {
		byRel[e.Rel] = e
	}
	if e := byRel["collector.log.2026-07-12_1"]; e.Action != LogT1Whole || e.Cut {
		t.Errorf("a file inside the window must be copied whole: %+v", e)
	}
	if e := byRel["collector.log"]; e.Action != LogT1Filter || !e.Cut || e.CutFrom != "20260710" {
		t.Errorf("the active log must be filtered and marked: %+v", e)
	}
	if e := byRel["collector.log"]; e.CutTo != "" {
		t.Error("a window with no end must not claim the tail was cut")
	}
	if _, ok := byRel["collector.log.2026-07-01_1"]; ok {
		t.Error("a file entirely before the window must not appear as an entry")
	}
	if e := byRel["collector.log.2026-07-09_1.gz"]; e.Action != LogT1Whole || e.Cut || e.Note == "" {
		t.Errorf("a straddling .gz must be taken whole with a note: %+v", e)
	}
	// The skipped file is reported once per module, not once per file.
	var summary *LogEntry
	for i := range got {
		if got[i].Skipped != "" {
			summary = &got[i]
		}
	}
	if summary == nil || !strings.Contains(summary.Skipped, "1개") {
		t.Fatalf("out-of-range files must be summarised per module: %+v", got)
	}
}

// Thousands of rotated kafka files outside the window must not each become a
// manifest line — a manifest nobody can read is a manifest nobody checks.
func TestPlanEntriesCollapsesOutOfRange(t *testing.T) {
	var rows []logFileRow
	for i := 0; i < 500; i++ {
		rows = append(rows, logFileRow{
			Cat: "middleware", Mod: "kafka", Dir: "/data/kafka-log",
			Rel:  fmt.Sprintf("server.log.2026-01-%02d-%02d", 1+i%28, i%24),
			Size: 1000, MtimeMs: ms(day(2026, 1, 2)),
		})
	}
	got := planEntries("h1", rows, nil, ms(day(2026, 7, 1)), 0, time.Local)
	if len(got) != 1 {
		t.Fatalf("500 out-of-range files produced %d entries, want 1 summary", len(got))
	}
	if !strings.Contains(got[0].Skipped, "500개") {
		t.Errorf("summary must say how many were left out: %q", got[0].Skipped)
	}
}

func TestPlanEntriesMarksBothBounds(t *testing.T) {
	from, to := ms(day(2026, 7, 10)), ms(day(2026, 7, 12))
	rows := []logFileRow{{Cat: "system", Mod: "var-log", Dir: "/var/log", Rel: "messages",
		Size: 10, MtimeMs: ms(day(2026, 7, 20))}}
	got := planEntries("h1", rows, nil, from, to, time.Local)
	if len(got) != 1 {
		t.Fatalf("entries = %+v", got)
	}
	e := got[0]
	if !e.Cut || e.CutFrom != "20260710" || e.CutTo != "20260712" {
		t.Errorf("both bounds cut this file, both must be marked: %+v", e)
	}
	if e.cutMark() != "from-20260710-to-20260712" {
		t.Errorf("cutMark = %q", e.cutMark())
	}
}

func TestPlanEntriesReportsMisses(t *testing.T) {
	got := planEntries("h1", nil, []logEnumMiss{
		{Cat: "middleware", Mod: "mariadb", Why: "denied"},
		{Cat: "middleware", Mod: "clickhouse-server", Why: "missing"},
	}, 0, 0, time.Local)
	if len(got) != 2 {
		t.Fatalf("entries = %+v", got)
	}
	// "권한 없음" and "없음" are different problems with different fixes, and silence
	// on either reads as "that log does not exist on this server".
	if !strings.Contains(got[0].Skipped, "sudo") {
		t.Errorf("a denied module must point at the fix: %q", got[0].Skipped)
	}
	if !strings.Contains(got[1].Skipped, "없습니다") {
		t.Errorf("a missing module must say so: %q", got[1].Skipped)
	}
}

// ---- archive paths -------------------------------------------------------

// sanitizeSegment maps everything outside [A-Za-z0-9._-] to "_", so two files whose
// names differ only in non-ASCII characters land on the same archive path. Copying
// one over the other would hand the operator an archive that silently lost a log the
// manifest swears is in it.
func TestAssignArchivePathsBreaksCollisions(t *testing.T) {
	entries := []LogEntry{
		{Category: LogCatSystem, Module: "var-log", Rel: "장애.log", SizeBytes: 1},
		{Category: LogCatSystem, Module: "var-log", Rel: "수집.log", SizeBytes: 2},
		{Category: LogCatSystem, Module: "var-log", Rel: "a b.log", SizeBytes: 3},
		{Category: LogCatSystem, Module: "var-log", Rel: "a_b.log", SizeBytes: 4},
	}
	got := assignArchivePaths("srv", entries)
	seen := map[string]bool{}
	for _, e := range got {
		if e.Arch == "" {
			t.Fatalf("no archive path assigned: %+v", e)
		}
		if seen[e.Arch] {
			t.Errorf("two entries share the archive path %q", e.Arch)
		}
		seen[e.Arch] = true
	}
}

// A file the range will trim has two possible names, and the host only chooses
// between them once it has read the file. Both must be reserved up front, or a late
// "take it whole" can land on top of another entry's path.
func TestAssignArchivePathsReservesBothNames(t *testing.T) {
	entries := []LogEntry{
		{Category: LogCatSystem, Module: "var-log", Rel: "messages", Cut: true, CutFrom: "20260630"},
		{Category: LogCatSystem, Module: "var-log", Rel: "messages"},
	}
	got := assignArchivePaths("srv", entries)
	if got[0].Arch != "srv/system/var-log/messages.from-20260630" {
		t.Errorf("cut name = %q", got[0].Arch)
	}
	if got[0].ArchWhole == got[1].Arch {
		t.Errorf("the fallback name %q collides with another entry", got[0].ArchWhole)
	}
	if got[1].Arch != got[1].ArchWhole {
		t.Error("an untrimmed entry must have one name")
	}
}

// ---- the work list -------------------------------------------------------

func TestLogJobRecordsAreNULSeparated(t *testing.T) {
	entries := []LogEntry{
		{Category: LogCatSystem, Module: "var-log", Rel: "messages", Source: "/var/log/messages",
			Action: LogT1Filter, Cut: true, CutFrom: "20260630", MtimeMs: ms(day(2026, 7, 1))},
		{Category: LogCatSystem, Module: "var-log", Rel: "weird\nname.log",
			Source: "/var/log/weird\nname.log", Action: LogT1Whole},
		{Category: LogCatSystem, Module: "dmesg", Rel: "dmesg.txt", Source: "dmesg -T",
			IsCmd: true, Action: LogT1Whole},
		{Category: LogCatSystem, Module: "var-log", Rel: "gone.log", Skipped: "복사 실패"},
	}
	entries = assignArchivePaths("srv", entries)
	jobs, dirs := logJobRecords("/data/.rtaskmgr-logs/log-1-aabbccdd", entries, time.Local)

	fields := strings.Split(strings.TrimSuffix(string(jobs), "\x00"), "\x00")
	if len(fields)%logJobFields != 0 {
		t.Fatalf("job records must be a multiple of %d fields, got %d", logJobFields, len(fields))
	}
	if n := len(fields) / logJobFields; n != 3 {
		t.Fatalf("got %d jobs, want 3 (the skipped entry must not be copied)", n)
	}
	// The newline lives inside a field and is not a record separator.
	if fields[logJobFields+2] != "/var/log/weird\nname.log" {
		t.Errorf("source field mangled: %q", fields[logJobFields+2])
	}
	if fields[1] != LogT1Filter || fields[logJobFields+1] != LogT1Whole || fields[2*logJobFields+1] != "cmd" {
		t.Errorf("modes = %q %q %q", fields[1], fields[logJobFields+1], fields[2*logJobFields+1])
	}
	// A command has no source file, and its command must survive in `extra`.
	if fields[2*logJobFields+2] != "" {
		t.Errorf("a command module must have no source: %q", fields[2*logJobFields+2])
	}
	if fields[2*logJobFields+5] != "dmesg -T" {
		t.Errorf("command lost: %q", fields[2*logJobFields+5])
	}
	// The filtered job carries a seed year, and both destinations.
	if fields[5] != "2026" {
		t.Errorf("seed year = %q", fields[5])
	}
	if !strings.HasSuffix(fields[4], ".from-20260630") || strings.HasSuffix(fields[3], ".from-20260630") {
		t.Errorf("cut/whole destinations wrong: %q / %q", fields[4], fields[3])
	}
	// Every destination directory has to be created before the copy runs.
	dirList := strings.Split(strings.TrimSuffix(string(dirs), "\x00"), "\x00")
	for _, d := range dirList {
		if !strings.HasPrefix(d, "/data/.rtaskmgr-logs/log-1-aabbccdd/") {
			t.Errorf("dir outside the staging root: %q", d)
		}
	}
	if len(dirList) == 0 {
		t.Error("no directories to create")
	}
}

// ---- the copy script -----------------------------------------------------

// The host decides what to do with a filtered file, and Go writes the sentence that
// explains it. If the two disagree the operator is told one thing and given another.
func TestCopyScriptVerdictMatchesGo(t *testing.T) {
	cases := []struct {
		rc int
		st LogFilterStats
	}{
		{0, LogFilterStats{Found: true, Lines: 100, Dated: 90, Emitted: 40}},
		{0, LogFilterStats{Found: true, Lines: 100, Dated: 90, Emitted: 0}},
		{0, LogFilterStats{Found: true, Lines: 100, Dated: 9, Emitted: 5}},
		{0, LogFilterStats{Found: true, Lines: 100, Dated: 10, Emitted: 5}},
		{0, LogFilterStats{Found: true, Lines: 0, Dated: 0, Emitted: 0}},
		{0, LogFilterStats{Found: true, Lines: 100, Dated: 90, Emitted: 40, PreTrunc: 12}},
		{3, LogFilterStats{Found: true, Lines: 100, Dated: 0}},
		{4, LogFilterStats{Found: true, Lines: 100, Dated: 100, Emitted: 0, FirstKey: 20260101000000}},
		{1, LogFilterStats{Found: true, Lines: 100, Dated: 90}},
		{0, LogFilterStats{}}, // no stats line at all
		{4, LogFilterStats{}},
	}
	for _, c := range cases {
		verdict, _ := filterVerdict(c.rc, c.st)
		action := filterActionFor(c.rc, c.st)
		want := map[string]string{
			LogTakeFiltered: "filtered",
			LogTakeUncut:    "nocut", // ran, dropped nothing → must not be marked as cut
			LogTakeNone:     "none",
			LogTakeWhole:    "whole",
			LogTakeFailed:   "whole", // uncertainty always resolves to taking everything
		}[verdict]
		if action != want {
			t.Errorf("rc=%d %+v: script would %q, Go says %q (%s)", c.rc, c.st, action, want, verdict)
		}
	}
	// And the rule really is the one the script carries.
	sh := logCopyScript("/data/.rtaskmgr-logs/log-1-aabbccdd", 0, 0, "", -1, 0)
	for _, want := range []string{
		`if(RC==4){print "none"; exit}`,
		`if(RC!=0){print "whole"; exit}`,
		`if(v["lines"]>0 && v["emitted"]==0){print "whole"; exit}`,
		`if(v["lines"]>0 && v["dated"]*10 < v["lines"]){print "whole"; exit}`,
		`if(v["pretrunc"]==0 && v["emitted"]==v["lines"]){print "nocut"; exit}`,
		`if(!ok){print "whole"; exit}`,
	} {
		if !strings.Contains(sh, want) {
			t.Errorf("the copy script no longer applies the rule %q", want)
		}
	}
}

func TestCopyScriptInvariants(t *testing.T) {
	sh := logCopyScript("/data/.rtaskmgr-logs/log-1-aabbccdd", 1000, 2000, "Asia/Seoul", 1001, 0)
	for _, want := range []string{
		"set -u",
		// NUL-delimited reads are what make a filename with a newline ordinary.
		`read -r -d '' -u 9`,
		// The work list is read from a file; paths are never interpolated into the script.
		`exec 9<"$CTL/jobs"`,
		// A collection must not starve the box it is diagnosing.
		"nice -n 19",
		// Root-written copies must be handed back, or the operator cannot clean up
		// their own collection after a password rotation.
		"chown -R 1001",
		// Boundaries are computed on the host, with an explicit TZ.
		"export TZ=",
		"+%Y%m%d%H%M%S",
	} {
		if !strings.Contains(sh, want) {
			t.Errorf("the copy script must contain %q", want)
		}
	}
	// A host-supplied path must never be eval'd, and the script must never rm
	// anything but its own staging destinations.
	if strings.Contains(sh, `eval "$src"`) || strings.Contains(sh, "eval $src") {
		t.Error("a source path must never be evaluated")
	}
	for _, banned := range []string{"rm -rf", "rm -r "} {
		if strings.Contains(sh, banned) {
			t.Errorf("the copy script must not contain %q — it only ever writes", banned)
		}
	}
	// Without a uid there is no chown: an unprivileged copy already owns its files,
	// and a chown to nobody-knows-who would be a way to hand them away.
	if strings.Contains(logCopyScript("/data/.rtaskmgr-logs/log-1-aabbccdd", 0, 0, "", -1, 0), "chown") {
		t.Error("an unprivileged copy must not chown")
	}
}

// ---- the archive and cleanup scripts -------------------------------------

func TestArchiveScriptGuardsTheDelete(t *testing.T) {
	s := &session{uid: 1001}
	sh := logArchiveScript(s, "/data/.rtaskmgr-logs/log-1-aabbccdd", "/data/.rtaskmgr-logs/log-1-aabbccdd.tar.gz")
	for _, want := range []string{
		"rtm_trusted \"$ROOT\"",
		"*/.rtaskmgr-logs/log-*",
		"gzip -t",
		"nice -n 19",
	} {
		if !strings.Contains(sh, want) {
			t.Errorf("the archive script must contain %q", want)
		}
	}
	// The order is the whole safety argument: the copies may only be deleted after
	// the archive that replaces them has been proven readable.
	iGzip, iRm := strings.Index(sh, "gzip -t"), strings.Index(sh, "rm -rf")
	if iGzip < 0 || iRm < 0 || iGzip > iRm {
		t.Error("gzip -t must pass before the staging copies are removed")
	}
	iTrust := strings.Index(sh, "rtm_trusted \"$ROOT\"")
	if iTrust > iRm {
		t.Error("the trust check must come before the rm")
	}
	// The control directory holds the awk program and the work list; shipping them
	// inside the archive would be noise in every collection.
	if !strings.Contains(sh, "--exclude=./"+logCtlDir) {
		t.Error("the control directory must not be archived")
	}
}

// The cleanup path must be incapable of removing an original log. It is handed only
// a collection id and a staging base; every path it acts on is rebuilt here.
func TestCleanupRefusesAnythingButOurOwn(t *testing.T) {
	const id = "log-1751000000000-aabbccdd"
	bad := []string{
		"/", "/var/log", "/var/log/messages", "/data", "/data/kafka-log",
		"/usr/local/liz", "/usr/local/liz/lizcollector/logs", "/home/liz",
		"/data/.rtaskmgr-logs", "/data/.rtaskmgr-logs/log-1751000000000-aabbccdd/../..",
		"/data/.rtaskmgr-logs/other", "/data/.rtaskmgr-pcap/log-1751000000000-aabbccdd",
		"/data/.rtaskmgr-logs/log-1751000000000-aabbccdd/x", "", "relative/path",
		"/data/.rtaskmgr-logs/log-XXXX", "/etc", "/etc/passwd", "/data/../etc",
	}
	for _, p := range bad {
		if validLogStagePath(p, id) {
			t.Errorf("validLogStagePath accepted %q", p)
		}
	}
	good := []string{
		"/data/.rtaskmgr-logs/log-1751000000000-aabbccdd",
		"/data/.rtaskmgr-logs/log-1751000000000-aabbccdd.tar.gz",
		"/home/liz/.rtaskmgr-logs/log-1751000000000-aabbccdd",
	}
	for _, p := range good {
		if !validLogStagePath(p, id) {
			t.Errorf("validLogStagePath rejected our own path %q", p)
		}
	}
	// A mismatched id must not unlock another collection's directory.
	if validLogStagePath("/data/.rtaskmgr-logs/log-1751000000000-aabbccdd", "log-1751000000000-deadbeef") {
		t.Error("a path was accepted for the wrong collection id")
	}
	// And the remove script re-checks on the host, whatever Go believed.
	sh := logRemoveScript(&session{uid: 1001}, "/data/.rtaskmgr-logs/"+id, "/data/.rtaskmgr-logs/"+id+".tar.gz")
	for _, want := range []string{"rtm_trusted", "*/.rtaskmgr-logs/log-*", "ERR|PATH"} {
		if !strings.Contains(sh, want) {
			t.Errorf("the remove script must contain %q", want)
		}
	}
}

// ---- the disk gate -------------------------------------------------------

func TestLogGate(t *testing.T) {
	const gb = int64(1) << 30
	// Copies and archive must fit at once, with headroom left over.
	if _, ok, _ := logGate(10*gb, 3*gb, 20*gb); !ok {
		t.Error("10GB of copies + 3GB archive must fit in 20GB free")
	}
	if _, ok, why := logGate(10*gb, 3*gb, 14*gb); ok {
		t.Errorf("10GB + 3GB + headroom must not fit in 14GB free (%s)", why)
	}
	// Free space we could not measure is not permission to proceed.
	if _, ok, _ := logGate(gb, gb, 0); ok {
		t.Error("an unknown free space must block the collection")
	}
	// The runaway cap is a backstop, not the real gate.
	if _, ok, why := logGate(logSizeCapBytes+1, 0, 1<<50); ok {
		t.Errorf("the size cap must refuse a runaway selection (%s)", why)
	}
	need, _, _ := logGate(10*gb, 3*gb, 100*gb)
	if need <= 13*gb {
		t.Errorf("the requirement must exceed copies+archive, got %d", need)
	}
}

// ---- selection -----------------------------------------------------------

func TestResolvePicksRefusesPathsTheCatalogDoesNotDescribe(t *testing.T) {
	cat := defaultLogCatalog()
	picks := []LogPick{
		{Category: LogCatMiddleware, Module: "kafka", Dir: "/data/kafka-log"},
		{Category: LogCatModules, Module: "lizcollector", Dir: "/usr/local/liz/lizcollector/logs"},
		{Category: LogCatSystem, Module: "dmesg"},
		// A stale or tampered selection pointing somewhere the catalog never mentions.
		{Category: LogCatMiddleware, Module: "zookeeper", Dir: "/etc"},
		{Category: LogCatSystem, Module: "var-log", Dir: "/root/.ssh"},
		{Category: "nope", Module: "kafka", Dir: "/data/kafka-log"},
		{Category: LogCatMiddleware, Module: "mariadb", Dir: "/data/mariadb log"}, // space
	}
	got, rejected := resolvePicks(cat, picks)
	if len(got) != 3 {
		t.Fatalf("accepted %d picks, want 3: %+v", len(got), got)
	}
	if len(rejected) != 4 {
		t.Errorf("rejected = %v", rejected)
	}
	// A module discovered by a glob has no definition but is still collectable.
	got2, _ := resolvePicks(cat, []LogPick{
		{Category: LogCatModules, Module: "lizadmin", Dir: "/usr/local/liz/lizadmin/logs"},
	})
	if len(got2) != 1 {
		t.Error("a module discovered by a glob must be collectable")
	}
	// The same module ticked twice is one job, not two copies of the same files.
	got3, _ := resolvePicks(cat, []LogPick{
		{Category: LogCatMiddleware, Module: "kafka", Dir: "/data/kafka-log"},
		{Category: LogCatMiddleware, Module: "kafka", Dir: "/data/kafka-log"},
	})
	if len(got3) != 1 {
		t.Errorf("a duplicated pick must collapse: %+v", got3)
	}
}

// ---- reconciliation ------------------------------------------------------

// A file the host ended up taking whole must lose its ".from-" marker: a marker on a
// complete file is a lie the reader has no way to detect.
func TestApplyCopyResultsDropsTheMarkerOnWhole(t *testing.T) {
	entries := assignArchivePaths("srv", []LogEntry{
		{Category: LogCatSystem, Module: "var-log", Rel: "messages", Source: "/var/log/messages",
			Action: LogT1Filter, Cut: true, CutFrom: "20260630"},
		{Category: LogCatSystem, Module: "var-log", Rel: "secure", Source: "/var/log/secure",
			Action: LogT1Filter, Cut: true, CutFrom: "20260630"},
		{Category: LogCatSystem, Module: "var-log", Rel: "cron", Source: "/var/log/cron",
			Action: LogT1Whole},
	})
	stat := "RTM_STAT lines=100 dated=5 emitted=3 first=20260630000000 last=20260705000000 pretrunc=0 tzseen=-"
	got := applyCopyResults(entries, map[int]logCopyResult{
		0: {Idx: 0, Keep: "whole", RC: 0, Note: stat},
		1: {Idx: 1, Keep: "none", RC: 4, Note: stat},
		2: {Idx: 2, Keep: "whole", RC: 0},
	})
	if got[0].Cut || got[0].CutFrom != "" || got[0].Arch != got[0].ArchWhole {
		t.Errorf("a file taken whole must not keep the cut marker: %+v", got[0])
	}
	if got[0].Note == "" || !strings.Contains(got[0].Note, "전체") {
		t.Errorf("the operator must be told the filter was not trusted: %q", got[0].Note)
	}
	if got[1].Skipped == "" {
		t.Error("a file with nothing in range must be recorded as not collected")
	}
	// A plain copy never went through the filter, so it must not inherit the
	// filter's "no stats" wording.
	if got[2].Note != "" {
		t.Errorf("a plain copy picked up a filter note: %q", got[2].Note)
	}
}

// An entry the manifest lists but the staging tree does not hold is the one failure
// that must never pass quietly: the operator would hand over an archive believing a
// log is inside it.
func TestApplyFoundDropsWhatNeverArrived(t *testing.T) {
	entries := assignArchivePaths("srv", []LogEntry{
		{Category: LogCatMiddleware, Module: "kafka", Rel: "server.log", SizeBytes: 100},
		{Category: LogCatMiddleware, Module: "kafka", Rel: "gone.log", SizeBytes: 200},
	})
	got := applyFound(entries, map[string]int64{
		"srv/middleware/kafka/server.log": 512, // grew between survey and copy
	})
	if got[0].SizeBytes != 512 {
		t.Errorf("the manifest must record what was copied, not what was surveyed: %d", got[0].SizeBytes)
	}
	if got[1].Skipped == "" {
		t.Error("a file that never arrived must be marked, not left in the manifest")
	}
	files, bytes := selectedBytes(got)
	if files != 1 || bytes != 512 {
		t.Errorf("totals = %d files / %d bytes", files, bytes)
	}
}

func TestParseLogCopyResults(t *testing.T) {
	out := "R|0|whole|0|\n" +
		"R|1|filtered|0|" + b64("RTM_STAT lines=10 dated=10 emitted=4 first=1 last=2 pretrunc=0 tzseen=-") + "\n" +
		"R|2|fail|1|" + b64("cp: cannot open") + "\n" +
		"noise\nR|bad|whole|0|\nDONE|copy\n"
	res := parseLogCopyResults(out)
	if len(res) != 3 {
		t.Fatalf("results = %+v", res)
	}
	if res[1].Keep != "filtered" || !strings.Contains(res[1].Note, "RTM_STAT") {
		t.Errorf("result 1 = %+v", res[1])
	}
	if res[2].RC != 1 || !strings.Contains(res[2].Note, "cannot open") {
		t.Errorf("result 2 = %+v", res[2])
	}
}

func TestParseLogFound(t *testing.T) {
	out := nulRec("F|120|srv/middleware/kafka/server.log", "F|64|srv/system/var-log/장애 기록.log",
		"F|bad|srv/x", "F|10")
	got := parseLogFound(out)
	if len(got) != 2 || got["srv/middleware/kafka/server.log"] != 120 || got["srv/system/var-log/장애 기록.log"] != 64 {
		t.Fatalf("found = %+v", got)
	}
}

// ---- verification --------------------------------------------------------

func TestVerifyLogArchiveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	entries := assignArchivePaths("srv", []LogEntry{
		{Category: LogCatMiddleware, Module: "kafka", Rel: "server.log", SizeBytes: 5},
		{Category: LogCatSystem, Module: "var-log", Rel: "장애 기록.log", SizeBytes: 3},
	})

	write := func(name string, files map[string]string) string {
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		for n, body := range files {
			if err := tw.WriteHeader(&tar.Header{Name: "./" + n, Mode: 0o600,
				Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		tw.Close()
		gz.Close()
		return p
	}

	full := map[string]string{
		entries[0].Arch:     "hello",
		entries[1].Arch:     "abc",
		"srv/_MANIFEST.txt": "manifest",
	}
	if err := VerifyLogArchive(write("ok.tar.gz", full), "srv", entries); err != nil {
		t.Errorf("a complete archive must verify: %v", err)
	}

	missing := map[string]string{entries[0].Arch: "hello", "srv/_MANIFEST.txt": "m"}
	err := VerifyLogArchive(write("missing.tar.gz", missing), "srv", entries)
	if err == nil || !strings.Contains(err.Error(), "누락") {
		t.Errorf("a missing file must fail verification: %v", err)
	}

	short := map[string]string{entries[0].Arch: "hi", entries[1].Arch: "abc", "srv/_MANIFEST.txt": "m"}
	err = VerifyLogArchive(write("short.tar.gz", short), "srv", entries)
	if err == nil || !strings.Contains(err.Error(), "크기") {
		t.Errorf("a truncated file must fail verification: %v", err)
	}

	// A manifest that was never written means nobody can trace where the files came
	// from — the archive is not the thing we promised.
	noman := map[string]string{entries[0].Arch: "hello", entries[1].Arch: "abc"}
	if err := VerifyLogArchive(write("noman.tar.gz", noman), "srv", entries); err == nil {
		t.Error("an archive without a manifest must fail verification")
	}

	// And a truncated download is caught by gzip before the tar is even read.
	bad := filepath.Join(dir, "torn.tar.gz")
	if err := os.WriteFile(bad, []byte("not a gzip stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyLogArchive(bad, "srv", entries); err == nil {
		t.Error("a torn download must fail verification")
	}
}

func TestMatchArchiveReportsExtras(t *testing.T) {
	entries := assignArchivePaths("srv", []LogEntry{
		{Category: LogCatMiddleware, Module: "kafka", Rel: "server.log", SizeBytes: 5},
	})
	have := map[string]int64{
		entries[0].Arch:                       5,
		"srv/_MANIFEST.txt":                   10,
		"srv/middleware/kafka/unexpected.log": 1,
	}
	err := matchArchive(have, "srv", entries)
	if err == nil || !strings.Contains(err.Error(), "목록에 없는") {
		t.Errorf("an entry nobody asked for must be reported: %v", err)
	}
	// Entries that were skipped are not expected in the archive.
	entries = append(entries, LogEntry{Category: LogCatMiddleware, Module: "kafka",
		Rel: "gone.log", Skipped: "복사 실패"})
	delete(have, "srv/middleware/kafka/unexpected.log")
	if err := matchArchive(have, "srv", entries); err != nil {
		t.Errorf("a skipped entry must not be demanded: %v", err)
	}
}

// ---- leftovers -----------------------------------------------------------

func TestParseLogOrphans(t *testing.T) {
	out := "O|D|4096|1751000000|/data/.rtaskmgr-logs/log-1751000000000-aabbccdd\n" +
		"O|A|900|1751000100|/data/.rtaskmgr-logs/log-1751000000000-aabbccdd.tar.gz\n" +
		"O|D|10|1751000200|/data/.rtaskmgr-logs/log-1751000000000-aabbccdd\n" + // duplicate base
		"O|D|10|1751000300|/data/.rtaskmgr-logs/notours\n" +
		"O|D|10|1751000400|/etc\n" +
		"O|D|10|1751000500|relative\n"
	got := parseLogOrphans("h1", out)
	if len(got) != 2 {
		t.Fatalf("orphans = %+v", got)
	}
	for _, o := range got {
		if !validCollectID(o.ID) {
			t.Errorf("a path that is not one of ours was offered for deletion: %+v", o)
		}
	}
	if got[0].Kind != "archive" {
		t.Errorf("newest first: %+v", got)
	}
}

func TestKeyMs(t *testing.T) {
	got := keyMs(20260630143012)
	want := time.Date(2026, 6, 30, 14, 30, 12, 0, time.Local).UnixMilli()
	if got != want {
		t.Errorf("keyMs = %d, want %d", got, want)
	}
	for _, bad := range []int64{0, -1, 20261330000000, 20260600000000} {
		if keyMs(bad) != 0 {
			t.Errorf("keyMs(%d) must refuse to invent a date", bad)
		}
	}
}

// ---- the enumeration script ---------------------------------------------

func TestLogEnumScriptInvariants(t *testing.T) {
	picks := []logPickDef{
		{Cat: LogCatSystem, Mod: "var-log", Dir: "/var/log",
			Def: LogModuleDef{Name: "var-log", Recursive: true, Exclude: []string{"journal", "audit/*"}}},
		{Cat: LogCatSystem, Mod: "dmesg", Def: LogModuleDef{Name: "dmesg", Cmd: "dmesg -T"}},
	}
	sh := logEnumScript(picks, ms(day(2026, 7, 1)))
	for _, want := range []string{
		"set -u",
		`-printf '%s|%T@|%P\0'`, // NUL records: a filename may contain a newline
		"readlink -f",           // symlinked module dirs must not be counted twice
		"-xdev",                 // a recursive scan must not wander onto another filesystem
		"-newermt",              // mtime may only ever justify skipping something too old
	} {
		if !strings.Contains(sh, want) {
			t.Errorf("the enumeration script must contain %q\n%s", want, sh)
		}
	}
	// A command module has nothing to enumerate.
	if strings.Contains(sh, "dmesg") {
		t.Error("a command module must not be enumerated as a directory")
	}
	// mtime must never be used as an upper bound: that would drop every
	// never-rotated active log from a request for a past window.
	if strings.Contains(sh, "! -newermt") {
		t.Error("mtime must not be used as an upper bound")
	}
	// The exclusions reach find as predicates (they are shell-quoted inside the
	// script, so they are checked on the expression the script carries).
	expr := logFindExpr(picks[0].Def, ms(day(2026, 7, 1)))
	for _, want := range []string{"! -name 'journal'", "! -path '*/journal/*'", "! -path '*/audit/*'"} {
		if !strings.Contains(expr, want) {
			t.Errorf("logFindExpr must contain %q, got %s", want, expr)
		}
	}
	if !strings.Contains(sh, shellQuote(expr)) {
		t.Error("the script must carry the module's find expression verbatim")
	}
	// A non-recursive module must not descend: a log directory that happens to hold
	// a nested one (lift's packet/) is a separate row the operator can switch off.
	if !strings.Contains(logFindExpr(LogModuleDef{}, 0), "-maxdepth 1") {
		t.Error("a non-recursive module must be limited to its own directory")
	}
}

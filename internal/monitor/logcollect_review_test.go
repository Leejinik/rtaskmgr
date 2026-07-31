package monitor

// Regressions from the adversarial review of the collection pipeline. Every test
// here corresponds to a defect that was found by reading the code rather than by
// running it, and most of them share one shape: the collection reports success while
// something the operator ticked is not in the archive. That is the failure this
// feature can least afford, so each one is pinned.

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// The mtime boundary must be an ABSOLUTE instant. A formatted wall-clock string
// carries no offset, so find parses it in the HOST's zone while Go rendered it in the
// CLIENT's: against a RHEL box left at TZ=UTC with a KST operator the threshold moves
// nine hours forward, and every file last written in the first nine hours of the
// window is dropped — not filtered, not summarised, never enumerated at all, so it
// leaves no trace in the manifest either.
func TestFindBoundaryIsTimezoneIndependent(t *testing.T) {
	from := ms(day(2026, 7, 1))
	expr := logFindExpr(LogModuleDef{}, from)
	want := "-newermt '@" + strconv.FormatInt(from/1000, 10) + "'"
	if !strings.Contains(expr, want) {
		t.Errorf("logFindExpr must pass an absolute instant, got %s", expr)
	}
	if strings.Contains(expr, "2026-07-01 00:00:00") {
		t.Errorf("the boundary is a client-local wall clock: %s", expr)
	}
	// The survey uses the same expression, so the tree's sizes cannot disagree with
	// what the collection will actually take.
	if !strings.Contains(logSurveyScript(defaultLogCatalog(), from), "@"+strconv.FormatInt(from/1000, 10)) {
		t.Error("the survey must use the same absolute boundary as the enumeration")
	}
}

// The filter's boundary is compared against timestamps the SERVICE wrote, which are
// in the host's local time — so the host has to convert an absolute instant. The old
// form fed date a wall-clock string under an exported TZ, which is the identity
// function: every TZ produced the same digits, and no value of the parameter could
// correct a host/client mismatch.
func TestHostRangeKeysConvertsOnTheHost(t *testing.T) {
	from := ms(day(2026, 6, 30))
	sh := hostRangeKeys(from, 0, "")
	if !strings.Contains(sh, "date -d '@"+strconv.FormatInt(from/1000, 10)+"'") {
		t.Errorf("hostRangeKeys must hand the host an absolute instant: %s", sh)
	}
	if strings.Contains(sh, "2026-06-30 00:00:00") {
		t.Errorf("hostRangeKeys still passes a wall clock: %s", sh)
	}
	// With no override the HOST's own zone applies — that is the zone its logs are
	// written in. Exporting one unconditionally is what made the parameter a no-op.
	if strings.Contains(sh, "export TZ=") {
		t.Errorf("no override was asked for, so the host's zone must stand: %s", sh)
	}
	if !strings.Contains(hostRangeKeys(from, 0, "UTC"), "export TZ='UTC'") {
		t.Error("an explicit override must be applied")
	}
	if !strings.Contains(hostRangeKeys(0, 0, ""), "S=0; ") {
		t.Error("an open start must be 0, not an epoch")
	}
}

// A dateext file's start bound must be the EARLIEST instant it could begin at.
// logrotate fires around midnight of the day the file is named for, so taking the END
// of the previous rotation day puts the believed start almost a full day too late —
// and classifyFile then skips the very file that holds the incident.
func TestPrevRotationIsALowerBound(t *testing.T) {
	rows := []logFileRow{
		{Cat: "system", Mod: "var-log", Dir: "/var/log", Rel: "messages-20260704",
			Size: 10, MtimeMs: ms(day(2026, 7, 4))},
		{Cat: "system", Mod: "var-log", Dir: "/var/log", Rel: "messages-20260705",
			Size: 10, MtimeMs: ms(day(2026, 7, 5))},
	}
	prev := prevRotationFor(rows, time.Local)
	if prev[1] != ms(day(2026, 7, 4)) {
		t.Errorf("prev = %d, want midnight of the previous rotation day (%d)",
			prev[1], ms(day(2026, 7, 4)))
	}
	// The scenario that made this matter: an incident at noon on 07-04.
	to := time.Date(2026, 7, 4, 12, 0, 0, 0, time.Local).UnixMilli()
	got := planEntries("h1", rows, nil, ms(day(2026, 7, 1)), to, time.Local)
	var collected bool
	for _, e := range got {
		if e.Rel == "messages-20260705" && e.Skipped == "" {
			collected = true
		}
	}
	if !collected {
		t.Fatalf("the file holding 07-04 must be collected for a window ending 07-04 12:00: %+v", got)
	}
}

// A command module that produced nothing is not a collection. journald with
// Storage=none, a masked journald, dmesg under dmesg_restrict — all write a 0-byte
// file, and shipping it as "명령 출력" tells the operator the journal was collected.
func TestCmdModuleFailureIsNotReportedAsSuccess(t *testing.T) {
	entries := assignArchivePaths("srv", []LogEntry{
		{Category: LogCatJournal, Module: "journal", Rel: "journal.log",
			Source: "journalctl --no-pager", IsCmd: true, Action: LogT1Whole},
		{Category: LogCatSystem, Module: "dmesg", Rel: "dmesg.txt",
			Source: "dmesg -T", IsCmd: true, Action: LogT1Whole},
	})
	got := applyCopyResults(entries, map[int]logCopyResult{
		0: {Idx: 0, Keep: "cmd", RC: 1, Note: "No journal files were found."},
		1: {Idx: 1, Keep: "cmdcut", RC: 141},
	})
	if !strings.Contains(got[0].Note, "명령 종료 코드 1") || !strings.Contains(got[0].Note, "No journal files") {
		t.Errorf("a failed command must carry its status AND its stderr: %q", got[0].Note)
	}
	// Truncation is not a failure, but it must be stated: journalctl emits oldest
	// first, so what a cap discards is the NEWEST part — what the incident is about.
	if !strings.Contains(got[1].Note, "잘렸") {
		t.Errorf("a truncated command output must say so: %q", got[1].Note)
	}
	got = applyFound(got, map[string]int64{got[0].Arch: 0, got[1].Arch: 4096})
	if got[0].Skipped == "" {
		t.Error("a 0-byte command output must not be presented as collected")
	}
	if got[1].Skipped != "" {
		t.Errorf("a truncated but non-empty output is still a collection: %q", got[1].Skipped)
	}
	if files, _ := selectedBytes(got); files != 1 {
		t.Errorf("the empty command must not be counted, got %d files", files)
	}
}

// An enumeration that never ran must not look like "this host has no matching logs".
// Reported that way, a selection that also holds a command module sails through the
// gate and hands back an archive containing only dmesg — with every ticked file
// module absent from the archive AND from the manifest.
func TestEnumerationFailureIsDistinguishable(t *testing.T) {
	for _, body := range []string{"", "sudo: Sorry, try again.\n", "bash: line 1: syntax error\n"} {
		if en := parseLogEnum(body); en.Records != 0 {
			t.Errorf("%q parsed as %d records", body, en.Records)
		}
	}
	ok := parseLogEnum(nulRec("HOST|dHJ1bmst|+0900",
		"MOD|middleware|kafka|L2RhdGEva2Fma2EtbG9n", "10|1751000000.0|server.log"))
	if ok.Records < 3 {
		t.Errorf("a real answer must count records: %+v", ok)
	}
	// The host's own UTC offset comes back, because rotated filenames are dates the
	// HOST wrote and reading them in the client's zone shifts a file's believed
	// coverage by hours — in the direction that makes classifyFile skip it.
	if ok.Loc == nil {
		t.Fatal("the host's zone must be captured")
	}
	if _, off := time.Now().In(ok.Loc).Zone(); off != 9*3600 {
		t.Errorf("host offset = %d, want 32400", off)
	}
	if zoneFromOffset("-0500") == nil || zoneFromOffset("bogus") != nil || zoneFromOffset("+9900") != nil {
		t.Error("zoneFromOffset must accept ±HHMM and refuse anything else")
	}
}

// A module whose resolved path Go refuses drops every one of its file records. That
// has to be RECORDED — otherwise the module is missing from the plan, from the
// archive and from the manifest's "수집되지 않은 항목" section, and the collection still
// verifies clean.
func TestUnusablePathIsRecordedNotSwallowed(t *testing.T) {
	en := parseLogEnum(nulRec("MOD|middleware|kafka|"+b64("/mnt/data 1/kafka-log"),
		"64|1751000000.0|server.log"))
	if len(en.Rows) != 0 {
		t.Errorf("rows adopted from an unusable directory: %+v", en.Rows)
	}
	if len(en.Misses) != 1 || en.Misses[0].Mod != "kafka" || en.Misses[0].Why != "unusable" {
		t.Fatalf("the dropped module was not recorded: %+v", en.Misses)
	}
	entries := planEntries("h1", en.Rows, en.Misses, 0, 0, time.Local)
	if len(entries) != 1 || entries[0].Skipped == "" {
		t.Fatalf("the manifest must carry the reason: %+v", entries)
	}
}

// Every ticked module must end up somewhere: collected, or explained.
func TestUnlistedPicksAreReported(t *testing.T) {
	picks := []logPickDef{
		{Cat: LogCatMiddleware, Mod: "kafka", Dir: "/data/kafka-log"},
		{Cat: LogCatMiddleware, Mod: "redis", Dir: "/usr/local/liz/redis/logs"},
		{Cat: LogCatMiddleware, Mod: "zookeeper", Dir: "/data/zookeeper-log"},
		{Cat: LogCatSystem, Mod: "dmesg", Def: LogModuleDef{Name: "dmesg", Cmd: "dmesg -T"}},
	}
	en := logEnumResult{
		Rows:   []logFileRow{{Cat: LogCatMiddleware, Mod: "kafka", Rel: "server.log"}},
		Misses: []logEnumMiss{{Cat: LogCatMiddleware, Mod: "zookeeper", Why: "denied"}},
		Listed: map[string]bool{LogCatMiddleware + "/redis": true},
	}
	got := unlistedPicks(picks, en)
	if len(got) != 1 {
		t.Fatalf("got %+v, want only redis", got)
	}
	// redis WAS reached and simply holds nothing in the window — a different fact
	// from "we never got an answer", and the operator needs to be able to tell.
	if got[0].Mod != "redis" || got[0].Why != "empty" {
		t.Errorf("miss = %+v", got[0])
	}
	en.Listed = nil
	if got = unlistedPicks(picks, en); len(got) != 1 || got[0].Why != "notlisted" {
		t.Errorf("a module the host never reached must say so: %+v", got)
	}
}

func TestCopyScriptCleansUpPartialCopies(t *testing.T) {
	sh := logCopyScript("/data/.rtaskmgr-logs/log-1-aabbccdd", 0, 0, "", -1, 0)
	// cp leaves the partial destination behind on a mid-transfer failure. Go marks
	// that entry as not collected, so a survivor would be archived while absent from
	// the manifest and fail the whole host's verification over one file in three
	// hundred.
	if strings.Count(sh, `rm -f -- "$dst"`) < 2 {
		t.Error("both the plain copy and the filter's fallback must remove a partial destination")
	}
	// The note has to be captured AFTER the fallback copy, or a cp failure is
	// reported with the filter's statistics line instead of "No space left on device".
	iCase := strings.Index(sh, "case $keep in")
	iNote := strings.Index(sh, `note=$(tail -c 400`)
	if iCase < 0 || iNote < 0 || iNote < iCase {
		t.Error("the filter branch must capture its note after the fallback copy runs")
	}
	// The command branch must not hide its producer's exit status behind the pipe.
	if !strings.Contains(sh, `echo $? > "$CTL/crc"`) {
		t.Error("a command module's own exit status must be recorded")
	}
	if !strings.Contains(sh, "keep=cmdcut") {
		t.Error("truncation at the size cap must be distinguishable from failure")
	}
}

// One partial file must not throw away a host's whole collection.
func TestMatchArchiveToleratesAFailedEntryRemnant(t *testing.T) {
	entries := assignArchivePaths("srv", []LogEntry{
		{Category: LogCatMiddleware, Module: "kafka", Rel: "server.log", SizeBytes: 5},
		{Category: LogCatMiddleware, Module: "kafka", Rel: "broken.log", SizeBytes: 9},
	})
	entries[1].Skipped = "복사 실패"
	have := map[string]int64{
		entries[0].Arch:     5,
		entries[1].Arch:     3, // a remnant the copy left behind
		"srv/_MANIFEST.txt": 10,
	}
	if err := matchArchive(have, "srv", entries); err != nil {
		t.Errorf("a known-failed entry's remnant must not fail the host: %v", err)
	}
	// Something nobody has any claim on is still a mismatch.
	have["srv/middleware/kafka/nobody-asked.log"] = 1
	if err := matchArchive(have, "srv", entries); err == nil {
		t.Error("an unexplained extra must still be reported")
	}
}

// The leftover scan runs under sudo whenever the session is elevated, and sudo resets
// HOME and the effective uid to root's — so the operator's own leftovers under
// /home/liz would be invisible in exactly the case that produces the biggest ones.
func TestLogBasesAreResolvedWithoutHomeVar(t *testing.T) {
	sh := logOrphanScript(&session{uid: 1001, user: "liz", stageDir: "/home/liz"})
	if strings.Contains(sh, "$HOME") || strings.Contains(sh, "id -u") {
		t.Errorf("the leftover scan must not depend on the running user's environment:\n%s", sh)
	}
	if !strings.Contains(sh, "getent passwd 'liz'") {
		t.Error("the login user's home must be resolved by name")
	}
	if !strings.Contains(sh, "/run/user/1001") {
		t.Error("the runtime directory must use the login uid, not the effective one")
	}
}

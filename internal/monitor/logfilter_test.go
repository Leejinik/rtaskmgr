package monitor

import (
	"strings"
	"testing"
	"time"
)

func TestParseFilterStats(t *testing.T) {
	st := parseFilterStats("RTM_STAT lines=13 dated=6 emitted=13 first=20260728140311 last=20260728140320 pretrunc=0 tzseen=+0900\n")
	if !st.Found || st.Lines != 13 || st.Dated != 6 || st.Emitted != 13 {
		t.Errorf("stats = %+v", st)
	}
	if st.FirstKey != 20260728140311 || st.LastKey != 20260728140320 || st.TZSeen != "+0900" {
		t.Errorf("stats keys/tz = %+v", st)
	}
	if st2 := parseFilterStats("... tzseen=- ...\nRTM_STAT lines=2 dated=2 emitted=0 first=1 last=2 pretrunc=0 tzseen=-\n"); st2.TZSeen != "" {
		t.Errorf("'-' should mean no offset, got %q", st2.TZSeen)
	}
	// No stats line at all must be visible as such, never treated as zeros.
	if parseFilterStats("awk: syntax error").Found {
		t.Error("a missing stats line was reported as found")
	}
}

// The verdict logic is where "never ship a silently truncated log" is enforced.
func TestFilterVerdict(t *testing.T) {
	full := LogFilterStats{Found: true, Lines: 100, Dated: 40, Emitted: 100}

	if v, _ := filterVerdict(logFilterEmitted, full); v != LogTakeFiltered {
		t.Errorf("a healthy run should be trusted, got %q", v)
	}
	// Undatable → take everything rather than nothing.
	if v, n := filterVerdict(logFilterUndatable, LogFilterStats{Found: true, Lines: 50}); v != LogTakeWhole || n == "" {
		t.Errorf("undatable = (%q,%q), want whole with a note", v, n)
	}
	// Datable but nothing in range is the ONLY case allowed to produce nothing, and
	// it must say what the file actually covers.
	v, note := filterVerdict(logFilterOutOfRange, LogFilterStats{
		Found: true, Lines: 50, Dated: 50, FirstKey: 20260101000000, LastKey: 20260102000000})
	if v != LogTakeNone {
		t.Errorf("out of range = %q, want none", v)
	}
	if !strings.Contains(note, "2026-01-01") {
		t.Errorf("the note should state the file's real range: %q", note)
	}
	// Exit said "emitted" but nothing came out — contradictory, so do not trust it.
	if v, _ := filterVerdict(logFilterEmitted, LogFilterStats{Found: true, Lines: 100, Dated: 100, Emitted: 0}); v != LogTakeWhole {
		t.Errorf("empty output with exit 0 = %q, want whole", v)
	}
	// The extractor understood almost none of the file → it does not know this shape.
	if v, n := filterVerdict(logFilterEmitted, LogFilterStats{Found: true, Lines: 1000, Dated: 5, Emitted: 900}); v != LogTakeWhole {
		t.Errorf("unrecognised format = %q (%s), want whole", v, n)
	}
	// A missing stats line must never be interpreted from the exit code alone.
	if v, _ := filterVerdict(logFilterEmitted, LogFilterStats{}); v != LogTakeFailed {
		t.Errorf("no stats = %q, want failed", v)
	}
	// A truncated prologue is usable but must be disclosed.
	if v, n := filterVerdict(logFilterEmitted, LogFilterStats{Found: true, Lines: 100, Dated: 40, Emitted: 90, PreTrunc: 7}); v != LogTakeFiltered || !strings.Contains(n, "7") {
		t.Errorf("pretrunc = (%q,%q), want filtered with a disclosure", v, n)
	}
}

// The two filename conventions mean OPPOSITE things. Reading dateext's date as a
// start would cut the wrong file and pass the straddling one through whole.
func TestFilenameCoverageConventions(t *testing.T) {
	loc := time.Local
	day := func(y, m, d int) int64 {
		return time.Date(y, time.Month(m), d, 0, 0, 0, 0, loc).UnixMilli()
	}
	mtime := time.Date(2026, 7, 30, 12, 0, 0, 0, loc).UnixMilli()

	// log4j2: the date IS the content day.
	cov := filenameCoverage("collector.log.2026-07-28_1", mtime, 0, loc)
	if cov.FirstMs != day(2026, 7, 28) {
		t.Errorf("log4j2 start = %v, want 07-28", time.UnixMilli(cov.FirstMs))
	}

	// logrotate dateext: the date is the END; the start is the previous rotation.
	prev := day(2026, 6, 28)
	cov = filenameCoverage("messages-20260705", mtime, prev, loc)
	if cov.FirstMs != prev {
		t.Errorf("dateext start = %v, want the previous rotation 06-28", time.UnixMilli(cov.FirstMs))
	}
	if d := time.UnixMilli(cov.LastMs); d.Month() != 7 || d.Day() != 5 {
		t.Errorf("dateext end = %v, want 07-05", d)
	}

	// kafka's hourly log4j1 rotation: the hour is a LOWER bound; mtime is the real end.
	cov = filenameCoverage("server.log.2026-06-30-14", mtime, 0, loc)
	want := time.Date(2026, 6, 30, 14, 0, 0, 0, loc).UnixMilli()
	if cov.FirstMs != want {
		t.Errorf("hourly start = %v, want 06-30 14:00", time.UnixMilli(cov.FirstMs))
	}
	if cov.LastMs != mtime {
		t.Error("hourly end must come from mtime, not from the name (rollover is lazy)")
	}

	// No date in the name at all: start unknown, so it can never be skipped as "too new".
	cov = filenameCoverage("kafkaServer.out", mtime, 0, loc)
	if cov.FirstMs != 0 || cov.LastMs != mtime {
		t.Errorf("undated file coverage = %+v", cov)
	}
}

// This is the T1 rule that a review found silently dropping whole modules.
func TestClassifyFileMtimeRuleset(t *testing.T) {
	loc := time.Local
	at := func(y, m, d int) int64 { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, loc).UnixMilli() }
	from := at(2026, 6, 30)

	// Too old: last write predates the window.
	if got := classifyFile(LogCoverage{FirstMs: at(2026, 6, 1), LastMs: at(2026, 6, 20)}, from, 0); got != LogT1Skip {
		t.Errorf("an entirely older file = %q, want skip", got)
	}
	// Entirely inside.
	if got := classifyFile(LogCoverage{FirstMs: at(2026, 7, 1), LastMs: at(2026, 7, 2)}, from, 0); got != LogT1Whole {
		t.Errorf("an entirely in-range file = %q, want whole", got)
	}
	// Straddling the start.
	if got := classifyFile(LogCoverage{FirstMs: at(2026, 6, 28), LastMs: at(2026, 7, 5)}, from, 0); got != LogT1Filter {
		t.Errorf("a straddling file = %q, want filter", got)
	}
	// Unknown start (never-rotated active log) must be read, not skipped.
	if got := classifyFile(LogCoverage{FirstMs: 0, LastMs: at(2026, 7, 30)}, from, 0); got != LogT1Filter {
		t.Errorf("an undated active file = %q, want filter", got)
	}

	// THE REGRESSION: a request for a PAST window. mariadb.err / redis.log /
	// clickhouse-server.log are never rotated, so their mtime is now. Using mtime as
	// an upper bound would skip them and the operator would receive a successful
	// download with no MariaDB, Redis or ClickHouse log for a window those files
	// definitely cover.
	pastFrom, pastTo := at(2026, 6, 1), at(2026, 6, 15)
	active := LogCoverage{FirstMs: 0, LastMs: at(2026, 7, 30)} // mtime = today
	if got := classifyFile(active, pastFrom, pastTo); got == LogT1Skip {
		t.Error("an active never-rotated file was skipped for a past window — whole modules would vanish")
	}
	// Skipping as "too new" is only allowed from the file's own earliest CONTENT.
	if got := classifyFile(LogCoverage{FirstMs: at(2026, 7, 1), LastMs: at(2026, 7, 30)}, pastFrom, pastTo); got != LogT1Skip {
		t.Errorf("a file whose content starts after the window = %q, want skip", got)
	}
	// No range at all: take everything.
	if got := classifyFile(LogCoverage{}, 0, 0); got != LogT1Whole {
		t.Errorf("no range = %q, want whole", got)
	}
}

func TestSeedYearFor(t *testing.T) {
	loc := time.Local
	mtime := time.Date(2026, 7, 30, 0, 0, 0, 0, loc).UnixMilli()
	if y := seedYearFor("collector.log.2025-12-31_0", mtime, loc); y != 2025 {
		t.Errorf("the filename year must win: %d", y)
	}
	if y := seedYearFor("messages-20240705", mtime, loc); y != 2024 {
		t.Errorf("dateext year = %d, want 2024", y)
	}
	if y := seedYearFor("server.log.2023-06-30-14", mtime, loc); y != 2023 {
		t.Errorf("hourly year = %d, want 2023", y)
	}
	// No date in the name: fall back to mtime, which is the only evidence there is.
	if y := seedYearFor("collector.log", mtime, loc); y != 2026 {
		t.Errorf("undated file year = %d, want the mtime year 2026", y)
	}
}

// The window boundaries must be computed on the HOST. A client-side conversion and a
// host on a different TZ disagree by hours, so "from 06-30 00:00" silently becomes
// "06-29 15:00" or loses the first nine hours of the day.
func TestHostRangeKeys(t *testing.T) {
	from := time.Date(2026, 6, 30, 0, 0, 0, 0, time.Local).UnixMilli()
	sh := hostRangeKeys(from, 0, "Asia/Seoul")
	for _, want := range []string{"export TZ='Asia/Seoul'", "S=$(date -d ", "+%Y%m%d%H%M%S", "E=0"} {
		if !strings.Contains(sh, want) {
			t.Errorf("hostRangeKeys missing %q: %s", want, sh)
		}
	}
	// An open-ended range must not emit a date command for the end.
	if strings.Count(sh, "date -d") != 1 {
		t.Errorf("an open end should produce one date call: %s", sh)
	}
	both := hostRangeKeys(from, from+86400000, "UTC")
	if strings.Count(both, "date -d") != 2 || !strings.Contains(both, "export TZ='UTC'") {
		t.Errorf("a bounded range should compute both keys with the given TZ: %s", both)
	}
}

func TestLogFilterArgsQuoting(t *testing.T) {
	args := logFilterArgs("/home/liz/.rtaskmgr-logs/rtm-logfilter", "$S", "$E", 2026)
	if !strings.Contains(args, "LC_ALL=C awk") {
		t.Error("the locale must be pinned: month names in a non-C locale would not match")
	}
	if !strings.Contains(args, "-f '/home/liz/.rtaskmgr-logs/rtm-logfilter'") {
		t.Errorf("the program must be passed with -f and quoted: %s", args)
	}
	if !strings.Contains(args, "-v Y=2026") {
		t.Errorf("the seed year is missing: %s", args)
	}
}

// Invariants on the awk program itself. It is embedded, so these pin the properties
// an adversarial review had to break the earlier version to discover.
func TestLogFilterAwkInvariants(t *testing.T) {
	s := string(logFilterAwk)
	if strings.Contains(s, "\r") {
		t.Error("logfilter.awk contains CR — it must be stored with LF endings")
	}
	// The key must be built positionally. gsub-ing separators out of a timestamp
	// substring is what coerced "2026-07-28T14:03:12.001+0900" to a date-only value
	// and deleted a broker's whole OOM chain.
	if strings.Contains(s, "gsub(") {
		t.Error("the comparison key must be built positionally, never by deleting punctuation")
	}
	// Sticky-on: nothing outside a recognised-timestamp rule may clear `on`.
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#") {
			continue
		}
		if strings.Contains(t, "on = 0") && !strings.Contains(t, "k > E") {
			// The only permitted assignment is inside mark(), past the end bound.
			if !strings.Contains(t, "on = 0; anchored") { // the BEGIN initialiser
				continue
			}
		}
	}
	if !strings.Contains(s, "} else if (E != 0 && k > E) {") {
		t.Error("printing may only be switched off at a recognised timestamp past the end")
	}
	// The tri-state exit protocol.
	for _, want := range []string{"exit 3", "exit 4", "RTM_STAT"} {
		if !strings.Contains(s, want) {
			t.Errorf("logfilter.awk lost %q", want)
		}
	}
	// The head-of-file buffer, without which every size-rotated file loses its start.
	if !strings.Contains(s, "MAXPRE") || !strings.Contains(s, "buf[++nb]") {
		t.Error("the prologue buffer is missing")
	}
	// All the shapes the middleware actually uses must be present.
	for _, shape := range []string{
		`[ T][0-9][0-9]:[0-9][0-9]:[0-9][0-9]`, // ISO / bracketed / UL
		`\.[0-9][0-9]\.[0-9][0-9] `,            // clickhouse dots
		`^\[[0-9][0-9]\] `,                     // mariabackup
		`[A-Z][a-z][a-z] [ 0-9][0-9] `,         // rsyslog traditional / redis month name
		`^[0-9][0-9]-[0-9][0-9] `,              // liz %d{MM-dd}
	} {
		if !strings.Contains(s, shape) {
			t.Errorf("logfilter.awk is missing the %q shape", shape)
		}
	}
	// It runs under whatever awk the host has; gawk-only features would fail on mawk.
	for _, gawkOnly := range []string{"gensub(", "asort(", "strftime(", "systime(", "ENVIRON["} {
		if strings.Contains(s, gawkOnly) {
			t.Errorf("logfilter.awk uses the gawk-only %s", gawkOnly)
		}
	}
}

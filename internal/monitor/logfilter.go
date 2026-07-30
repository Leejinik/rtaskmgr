package monitor

import (
	_ "embed"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// logFilterAwk is the date-range filter. It is shipped to the host as a file and
// invoked with `awk -f`, never interpolated into a shell command: the program
// contains regexes full of brackets and backslashes that quoting through ssh would
// mangle, and a syntax error in a generated one-liner is silent (awk exits 1 and the
// caller reads that as "nothing matched").
//
//go:embed logfilter.awk
var logFilterAwk []byte

// Filter exit codes, which the caller MUST distinguish. Conflating them is how a
// naive design ends up shipping an empty log file and reporting success.
const (
	logFilterEmitted    = 0 // lines were emitted
	logFilterUndatable  = 3 // no recognisable timestamp anywhere in the file
	logFilterOutOfRange = 4 // timestamps found, none inside the range
)

// LogFilterStats is the RTM_STAT line the filter writes to stderr. It exists so a
// short or empty result is provable rather than silent.
type LogFilterStats struct {
	Lines    int64  // lines read
	Dated    int64  // lines carrying a recognised timestamp
	Emitted  int64  // lines written
	FirstKey int64  // first recognised key (YYYYMMDDHHMMSS)
	LastKey  int64  // last recognised key
	PreTrunc int64  // head-of-file lines dropped because the buffer filled
	TZSeen   string // UTC offset observed in the file, "" if none
	Found    bool
}

var statRe = regexp.MustCompile(`RTM_STAT lines=(\d+) dated=(\d+) emitted=(\d+) first=(\d+) last=(\d+) pretrunc=(\d+) tzseen=(\S+)`)

// parseFilterStats pulls the stats line out of the filter's stderr.
func parseFilterStats(stderr string) LogFilterStats {
	m := statRe.FindStringSubmatch(stderr)
	if m == nil {
		return LogFilterStats{}
	}
	n := func(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }
	st := LogFilterStats{
		Lines: n(m[1]), Dated: n(m[2]), Emitted: n(m[3]),
		FirstKey: n(m[4]), LastKey: n(m[5]), PreTrunc: n(m[6]), Found: true,
	}
	if m[7] != "-" {
		st.TZSeen = m[7]
	}
	return st
}

// Verdicts for what to do with a filter run's output.
const (
	LogTakeFiltered = "filtered" // use the filtered output
	LogTakeWhole    = "whole"    // the filter cannot be trusted here: copy it all
	LogTakeNone     = "none"     // genuinely nothing in range
	LogTakeFailed   = "failed"   // the run itself failed
)

// minRecognisedRatio is how much of a file the filter must understand before its
// output is trusted. Below this the extractor does not know this log's shape, and a
// confident-looking short result is worse than an over-inclusive whole copy.
const minRecognisedRatio = 0.10

// filterVerdict decides what to do with a filter run.
//
// The bias is deliberate and one-directional: when anything is uncertain, take the
// WHOLE file and say so. An over-inclusive log costs the operator some scrolling; a
// silently truncated one costs them the investigation. The only case that may
// produce nothing is "the file is datable and nothing is in range" — and even that
// is recorded rather than presented as an empty file.
func filterVerdict(exitCode int, st LogFilterStats) (verdict, note string) {
	if !st.Found {
		// No stats line means the filter did not run to completion (a broken awk, a
		// killed process). Never guess from the exit code alone.
		return LogTakeFailed, "필터가 정상 종료하지 않았습니다 (통계 없음)"
	}
	switch exitCode {
	case logFilterUndatable:
		return LogTakeWhole, "타임스탬프를 인식할 수 없는 파일 — 전체를 담았습니다"
	case logFilterOutOfRange:
		return LogTakeNone, fmt.Sprintf("요청 기간에 해당하는 내용이 없습니다 (파일 범위 %s~%s)",
			keyDate(st.FirstKey), keyDate(st.LastKey))
	case logFilterEmitted:
		// fall through to the sanity checks
	default:
		return LogTakeFailed, fmt.Sprintf("필터 실패 (exit %d)", exitCode)
	}

	if st.Lines > 0 && st.Emitted == 0 {
		// The exit code said "emitted" but nothing came out — contradictory, so do
		// not trust it.
		return LogTakeWhole, "필터 결과가 비어 있어 전체를 담았습니다"
	}
	if st.Lines > 0 && float64(st.Dated)/float64(st.Lines) < minRecognisedRatio {
		return LogTakeWhole, fmt.Sprintf("이 로그의 시각 형식을 인식하지 못했습니다 (%d/%d 줄) — 전체를 담았습니다",
			st.Dated, st.Lines)
	}
	if st.PreTrunc > 0 {
		return LogTakeFiltered, fmt.Sprintf("파일 앞부분 %d줄은 버퍼 한도로 제외됐습니다", st.PreTrunc)
	}
	return LogTakeFiltered, ""
}

func keyDate(k int64) string {
	if k <= 0 {
		return "-"
	}
	return fmt.Sprintf("%04d-%02d-%02d", k/10000000000, (k/100000000)%100, (k/1000000)%100)
}

// ---- T1: deciding which files need to be read at all ----

// T1 actions.
const (
	LogT1Skip   = "skip"   // entirely outside the range; not one byte is read
	LogT1Whole  = "whole"  // entirely inside the range; copy without filtering
	LogT1Filter = "filter" // straddles a boundary, or its coverage is unknown
)

// LogCoverage is what a file is believed to span.
type LogCoverage struct {
	FirstMs int64 // earliest content, 0 = unknown
	LastMs  int64 // latest content, 0 = unknown
}

var (
	// log4j2 rotation from the liz modules: "<mod>.log.2026-07-28_1".
	log4jDateRe = regexp.MustCompile(`\.log\.(\d{4})-(\d{2})-(\d{2})(?:_(\d+))?(?:\.gz)?$`)
	// logrotate dateext: "messages-20260705", "secure-20260705.gz".
	dateExtRe = regexp.MustCompile(`-(\d{4})(\d{2})(\d{2})(?:\.gz)?$`)
	// log4j1 hourly rotation (kafka): "server.log.2026-06-30-14".
	hourlyRe = regexp.MustCompile(`\.log\.(\d{4})-(\d{2})-(\d{2})-(\d{2})$`)
)

// filenameCoverage reads what a filename claims about its content.
//
// The two conventions mean OPPOSITE things and getting them backwards cuts the
// wrong file:
//   - log4j2 "<mod>.log.2026-07-28_1" — the date IS the day of the content.
//   - logrotate dateext "messages-20260705" — the date is when it was ROTATED, so it
//     is the END of the coverage and the start is the previous rotation.
//
// Both are only bounds, never exact: log4j rolls over lazily (on the first append
// after the boundary), so a file named for 14:00 can hold lines written at 23:00.
// The caller therefore treats mtime as the authoritative upper bound.
func filenameCoverage(name string, mtimeMs, prevRotationMs int64, loc *time.Location) LogCoverage {
	base := path.Base(name)
	if m := hourlyRe.FindStringSubmatch(base); m != nil {
		y, mo, d, h := atoi(m[1]), atoi(m[2]), atoi(m[3]), atoi(m[4])
		start := time.Date(y, time.Month(mo), d, h, 0, 0, 0, loc)
		// Lazy rollover: the end is not start+1h, it is whenever the next append
		// happened — mtime knows, the name does not.
		return LogCoverage{FirstMs: start.UnixMilli(), LastMs: mtimeMs}
	}
	if m := log4jDateRe.FindStringSubmatch(base); m != nil {
		y, mo, d := atoi(m[1]), atoi(m[2]), atoi(m[3])
		start := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, loc)
		return LogCoverage{FirstMs: start.UnixMilli(), LastMs: mtimeMs}
	}
	if m := dateExtRe.FindStringSubmatch(base); m != nil {
		y, mo, d := atoi(m[1]), atoi(m[2]), atoi(m[3])
		end := time.Date(y, time.Month(mo), d, 23, 59, 59, 0, loc)
		// The start is the PREVIOUS rotation. Unknown when there is no previous file,
		// which leaves it a straddler — the safe answer.
		return LogCoverage{FirstMs: prevRotationMs, LastMs: minPos(end.UnixMilli(), mtimeMs)}
	}
	// No date in the name (the active log, kafkaServer.out, zookeeper.out).
	return LogCoverage{FirstMs: 0, LastMs: mtimeMs}
}

func atoi(s string) int { v, _ := strconv.Atoi(s); return v }

func minPos(a, b int64) int64 {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}

// classifyFile decides whether a file must be read.
//
// The one rule about mtime that matters: mtime is the LAST write, so it can only
// ever justify skipping a file for being too OLD (mtime < from). Using it as an
// upper bound the other way — "skip anything whose mtime is after the end of the
// window" — silently removes every never-rotated active file (mariadb.err,
// redis.log, clickhouse-server.log) from any request for a past window, and the
// operator gets a successful download with whole modules missing.
func classifyFile(cov LogCoverage, fromMs, toMs int64) string {
	if fromMs <= 0 && toMs <= 0 {
		return LogT1Whole // no range requested
	}
	// Too old: its last write predates the window.
	if fromMs > 0 && cov.LastMs > 0 && cov.LastMs < fromMs {
		return LogT1Skip
	}
	// Too new: only decidable from the file's own earliest CONTENT, never from mtime.
	if toMs > 0 && cov.FirstMs > 0 && cov.FirstMs > toMs {
		return LogT1Skip
	}
	// Entirely inside: needs both bounds known.
	insideStart := fromMs <= 0 || (cov.FirstMs > 0 && cov.FirstMs >= fromMs)
	insideEnd := toMs <= 0 || (cov.LastMs > 0 && cov.LastMs <= toMs)
	if insideStart && insideEnd {
		return LogT1Whole
	}
	return LogT1Filter
}

// logFilterArgs builds the awk invocation for one file. S/E are computed on the HOST
// (see hostRangeKeys) so a client-side date picker and a host running a different TZ
// cannot disagree.
func logFilterArgs(progPath string, sKey, eKey string, seedYear int) string {
	return "LC_ALL=C awk -v S=" + shellQuote(sKey) +
		" -v E=" + shellQuote(eKey) +
		" -v Y=" + strconv.Itoa(seedYear) +
		" -v MAXPRE=5000 -f " + shellQuote(progPath)
}

// hostRangeKeys renders the shell that turns the requested window into the numeric
// keys the filter compares against, evaluated on the host with an explicit TZ.
//
// Computing these on the client is a real bug source: a Wails date picker produces a
// local-time instant, and a host started with TZ=UTC writes timestamps nine hours
// away from it, so a boundary chosen as "06-30 00:00" silently lands at "06-29
// 15:00" or misses the first nine hours of the day.
func hostRangeKeys(fromMs, toMs int64, tz string) string {
	if tz == "" {
		tz = "Asia/Seoul"
	}
	q := func(ms int64) string {
		if ms <= 0 {
			return "0"
		}
		return shellQuote(time.UnixMilli(ms).Format("2006-01-02 15:04:05"))
	}
	var sb strings.Builder
	sb.WriteString("export TZ=" + shellQuote(tz) + "; ")
	if fromMs > 0 {
		sb.WriteString("S=$(date -d " + q(fromMs) + " +%Y%m%d%H%M%S); ")
	} else {
		sb.WriteString("S=0; ")
	}
	if toMs > 0 {
		sb.WriteString("E=$(date -d " + q(toMs) + " +%Y%m%d%H%M%S); ")
	} else {
		sb.WriteString("E=0; ")
	}
	return sb.String()
}

// seedYearFor picks the year to seed the no-year formats with: the file's own
// filename date when it has one, otherwise its mtime. The filter then advances the
// year on any month rollback, so a file spanning New Year still compares correctly.
func seedYearFor(name string, mtimeMs int64, loc *time.Location) int {
	base := path.Base(name)
	for _, re := range []*regexp.Regexp{hourlyRe, log4jDateRe, dateExtRe} {
		if m := re.FindStringSubmatch(base); m != nil {
			if y := atoi(m[1]); y > 1970 {
				return y
			}
		}
	}
	if mtimeMs > 0 {
		return time.UnixMilli(mtimeMs).In(loc).Year()
	}
	return time.Now().In(loc).Year()
}

// GC log fetch for the memory-trend view.
//
// Read-only by construction: the script lists java processes from /proc, reads the
// GC log each one was started with (-Xloggc), and sends back only the lines that carry
// heap figures. It never attaches to a JVM (jcmd/jstat), because a Full GC or a
// safepoint pause on a shared test server is someone else's broken test.
//
// What goes over the wire is small on purpose. A G1 log with PrintGCDetails spends
// ~30 lines per collection on per-phase timings; the trend needs two of them (the
// header and the "Heap:" line). The awk filter keeps those, the time filter drops what
// the local archive already has, and gzip does the rest — a day of a busy collector
// is ~100 KB instead of ~11 MB.
package monitor

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// GcProc is one java process on a host and whatever of its GC log was fetched.
type GcProc struct {
	PID          int
	User         string
	UpSec        int64
	RSSKB        int64
	Threads      int
	FDs          int
	Args         []string // argv, for naming the module
	GcLog        string   // resolved -Xloggc path; "" when the JVM keeps no GC log
	Files        int      // log files found (base + rotations)
	Archives     int      // gc-logs-*.tar.gz next to the log (earlier runs, archived on restart)
	ArchivesRead int      // of those, the ones opened this time (newer than the last fetch)
	Text         string   // reduced log text (see gcAwk)
	Err          string
}

// GcFetch is one host's answer.
type GcFetch struct {
	NowMs int64
	Procs []GcProc
}

// gcAwk keeps collection headers at or after the cut-off, the "Heap:" detail line
// that belongs to each kept header (G1 puts the totals there), and the
// CommandLine flags line (MaxHeapSize). Concurrent phases and remark carry no heap
// figures and are dropped here rather than shipped.
const gcAwk = `
/CommandLine flags:/ { print; next }
/^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T/ || /^[0-9]+\.[0-9]+: \[/ {
  k = 0
  if (index($0, "[GC concurrent") || index($0, "[GC remark")) next
  if (!index($0, "[GC") && !index($0, "[Full GC")) next
  if (s != "" && $0 ~ /^[0-9][0-9][0-9][0-9]-/ && substr($0, 1, 19) < s) next
  k = 1; print; next
}
/Heap: / { if (k) print; k = 0; next }
`

// gcExtractScript builds the host-side script. since maps a GC log path to the unix
// second of the newest event already archived for it; a log seen for the first time
// is read back firstDays days by the HOST's clock (bounded so a first fetch of a 1 GB
// rotation set does not ship months nobody asked for; 0 = everything).
//
// arcSince is the same for the restart archives. It is kept apart because a log
// fetched before archive support existed has a recent "since" but has never had its
// archives read: using the live log's cut-off would skip every one of them forever.
func gcExtractScript(since, arcSince map[string]int64, firstDays int) string {
	var b strings.Builder
	b.WriteString("set -u\nexport LC_ALL=C\n")
	writeSinceFunc(&b, "since_for", since, firstDays)
	writeSinceFunc(&b, "asince_for", arcSince, firstDays)
	b.WriteString("GCAWK=" + shellQuote(gcAwk) + "\n")
	b.WriteString(`echo "NOW|$(date +%s)"
for p in $(pgrep -x java 2>/dev/null); do
  [ -r /proc/$p/cmdline ] || continue
  u=$(stat -c %U /proc/$p 2>/dev/null)
  et=$(ps -o etimes= -p $p 2>/dev/null | tr -d ' ')
  rss=$(awk '/^VmRSS/{print $2}' /proc/$p/status 2>/dev/null)
  th=$(awk '/^Threads/{print $2}' /proc/$p/status 2>/dev/null)
  if [ -r /proc/$p/fd ]; then fd=$(ls /proc/$p/fd 2>/dev/null | wc -l); else fd=-1; fi
  args=$(tr '\0' '\n' < /proc/$p/cmdline | base64 -w0)
  echo "P|$p|$u|${et:-0}|${rss:-0}|${th:-0}|$fd|$args"
  gc=$(tr '\0' '\n' < /proc/$p/cmdline | sed -n 's/^-Xloggc://p' | tail -1)
  [ -n "$gc" ] || continue
  case "$gc" in /*) ;; *) gc="$(readlink /proc/$p/cwd 2>/dev/null)/$gc" ;; esac
  gc=${gc//%p/$p}
  echo "G|$p|$gc"
  since=$(since_for "$gc")
  asince=$(asince_for "$gc")
  # One cut-off for the awk line filter: the earlier of the two, so lines from
  # archives older than the live log's cut-off survive it. Duplicates are dropped
  # on merge.
  cut=$since; [ "$asince" -lt "$cut" ] && cut=$asince
  ss=""
  [ "$cut" -gt 0 ] && ss=$(date -d @$cut +%Y-%m-%dT%H:%M:%S)
  gc=${gc//%t/*}
  keep=""; n=0; unread=0
  for f in $(ls -1tr -d -- $gc $gc.* 2>/dev/null); do
    [ -f "$f" ] || continue
    n=$((n+1))
    [ -r "$f" ] || { unread=$((unread+1)); continue; }
    [ "$since" -gt 0 ] && [ "$(stat -c %Y -- "$f")" -lt "$since" ] && continue
    keep="$keep $f"
  done
  echo "N|$p|$n|$unread"
  # Earlier runs: MK119's start-*.sh tars the previous GC log up on every restart,
  # next to the live one. Only archives touched since the last fetch are opened.
  akeep=""; an=0; au=0
  for a in $(ls -1tr -d -- "$(dirname -- "$gc")"/gc-logs-*.tar.gz 2>/dev/null); do
    [ -f "$a" ] && [ -r "$a" ] || continue
    an=$((an+1))
    [ "$asince" -gt 0 ] && [ "$(stat -c %Y -- "$a")" -lt "$asince" ] && continue
    akeep="$akeep $a"; au=$((au+1))
  done
  echo "A|$p|$an|$au"
  [ -n "$keep$akeep" ] || continue
  data=$( { for a in $akeep; do tar xzOf "$a" 2>/dev/null; done; for f in $keep; do cat -- "$f"; done; } |
    awk -v s="$ss" "$GCAWK" | gzip -c | base64 -w0)
  echo "D|$p|$data"
done
`)
	return b.String()
}

func writeSinceFunc(b *strings.Builder, name string, since map[string]int64, firstDays int) {
	fmt.Fprintf(b, "%s() {\n  case \"$1\" in\n", name)
	paths := make([]string, 0, len(since))
	for p := range since {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Fprintf(b, "    %s) echo %d ;;\n", shellQuote(p), since[p])
	}
	if firstDays > 0 {
		fmt.Fprintf(b, "    *) echo $(( $(date +%%s) - %d )) ;;\n  esac\n}\n", firstDays*86400)
	} else {
		b.WriteString("    *) echo 0 ;;\n  esac\n}\n")
	}
}

// FetchGcLogs runs the extraction on one host. It is elevated when the session can
// be, so the logs of JVMs owned by other users (kafka, zookeeper run as root on
// MK119) are readable too; unprivileged, those rows come back as "읽을 수 없음".
func (m *Manager) FetchGcLogs(hostID string, since, arcSince map[string]int64, firstDays int) (GcFetch, error) {
	s := m.get(hostID)
	if s == nil {
		return GcFetch{}, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	script := gcExtractScript(since, arcSince, firstDays)
	var out string
	var err error
	if s.elevated {
		out, err = m.sudoRun(s, script)
	} else {
		out, err = m.plainRun(s, script)
	}
	f := parseGcFetch(out)
	if f.NowMs == 0 {
		if err == nil {
			err = fmt.Errorf("응답을 해석할 수 없습니다")
		}
		return f, fmt.Errorf("GC 로그 조회 실패: %v: %s", err, tailLines(out, 2))
	}
	return f, nil
}

func parseGcFetch(out string) GcFetch {
	var f GcFetch
	byPID := map[int]*GcProc{}
	var order []int
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "NOW|"):
			sec, _ := strconv.ParseInt(strings.TrimSpace(line[4:]), 10, 64)
			f.NowMs = sec * 1000
		case strings.HasPrefix(line, "P|"):
			p := strings.SplitN(line[2:], "|", 7)
			if len(p) != 7 {
				continue
			}
			pid, _ := strconv.Atoi(p[0])
			gp := &GcProc{PID: pid, User: p[1]}
			gp.UpSec, _ = strconv.ParseInt(p[2], 10, 64)
			gp.RSSKB, _ = strconv.ParseInt(p[3], 10, 64)
			gp.Threads, _ = strconv.Atoi(p[4])
			gp.FDs, _ = strconv.Atoi(p[5])
			if raw, err := base64.StdEncoding.DecodeString(p[6]); err == nil {
				for _, a := range strings.Split(string(raw), "\n") {
					if a != "" {
						gp.Args = append(gp.Args, a)
					}
				}
			}
			byPID[pid] = gp
			order = append(order, pid)
		case strings.HasPrefix(line, "G|"), strings.HasPrefix(line, "N|"), strings.HasPrefix(line, "A|"), strings.HasPrefix(line, "D|"):
			p := strings.SplitN(line[2:], "|", 3)
			pid, _ := strconv.Atoi(p[0])
			gp := byPID[pid]
			if gp == nil || len(p) < 2 {
				continue
			}
			switch line[0] {
			case 'G':
				gp.GcLog = p[1]
			case 'A':
				gp.Archives, _ = strconv.Atoi(p[1])
				if len(p) == 3 {
					gp.ArchivesRead, _ = strconv.Atoi(p[2])
				}
				if gp.Files == 0 && gp.Archives > 0 {
					gp.Err = "" // the live log is gone but earlier runs are archived
				}
			case 'N':
				gp.Files, _ = strconv.Atoi(p[1])
				if len(p) == 3 {
					if un, _ := strconv.Atoi(p[2]); un > 0 && un == gp.Files {
						gp.Err = "GC 로그를 읽을 권한이 없습니다"
					}
				}
				if gp.Files == 0 {
					gp.Err = "GC 로그 파일이 없습니다 (경로: " + gp.GcLog + ")"
				}
			case 'D':
				txt, err := gunzipB64(p[1])
				if err != nil {
					gp.Err = "GC 로그 전송이 깨졌습니다: " + err.Error()
					continue
				}
				gp.Text = txt
			}
		}
	}
	for _, pid := range order {
		f.Procs = append(f.Procs, *byPID[pid])
	}
	return f
}

func gunzipB64(s string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return "", err
	}
	if len(raw) == 0 {
		return "", nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(zr)
	return string(b), err
}

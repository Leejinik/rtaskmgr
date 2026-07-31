package monitor

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// LogModuleStat is one row of the collection tree: what a module holds on one host.
type LogModuleStat struct {
	Category string `json:"category"`
	Module   string `json:"module"`
	Dir      string `json:"dir"`
	// RealDir is the resolved path. Two catalog entries can name the same physical
	// directory through a symlink (/data/kafka-log and /usr/local/liz/kafka/logs are
	// the same 6489 files on a real host), and without resolving them the collection
	// would copy everything twice.
	RealDir string `json:"realDir"`
	// Status: ok | missing | denied | cmd
	Status    string `json:"status"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	OldestMs  int64  `json:"oldestMs"`
	NewestMs  int64  `json:"newestMs"`
	NeedsRoot bool   `json:"needsRoot"`
	IsCmd     bool   `json:"isCmd"`
	Recursive bool   `json:"recursive"`
	Match     string `json:"match,omitempty"`
	// DupOfDir names the module this one was folded into when both resolve to the
	// same directory.
	DupOfDir string `json:"dupOfDir,omitempty"`
}

// LogSurvey is one host's answer: the tree rows plus where a collection could be
// staged.
type LogSurvey struct {
	HostID   string          `json:"hostId"`
	HostName string          `json:"hostName"` // this app's display name
	Hostname string          `json:"hostname"` // the host's own `hostname`
	DirName  string          `json:"dirName"`  // archive directory name, assigned later
	Elevated bool            `json:"elevated"`
	Modules  []LogModuleStat `json:"modules"`
	Targets  []RecTarget     `json:"targets"`
	Err      string          `json:"err,omitempty"`
}

// logFindExpr renders the find predicates for one module.
func logFindExpr(def LogModuleDef, fromMs int64) string {
	var sb strings.Builder
	if !def.Recursive {
		sb.WriteString(" -maxdepth 1")
	}
	// -xdev keeps a recursive scan from wandering onto another filesystem (/var/log
	// can hold a mount point, and following it would both mis-size the estimate and
	// copy something nobody asked for).
	sb.WriteString(" -xdev -type f")
	if def.Match != "" {
		sb.WriteString(" -name " + shellQuote(def.Match))
	}
	for _, ex := range def.Exclude {
		if ex == "" {
			continue
		}
		// A rule can name a file ("wtmp") or a subtree ("journal/*"); -path covers the
		// latter, -name the former.
		if strings.Contains(ex, "/") {
			sb.WriteString(" ! -path " + shellQuote("*/"+ex))
		} else {
			sb.WriteString(" ! -name " + shellQuote(ex))
			sb.WriteString(" ! -path " + shellQuote("*/"+ex+"/*"))
		}
	}
	// mtime is the LAST write, so it can only justify skipping a file for being too
	// old. Never the other way round: an upper bound here would drop every
	// never-rotated active log (mariadb.err, redis.log, clickhouse-server.log) from a
	// past-window request and the operator would get whole modules missing.
	//
	// The boundary is an ABSOLUTE instant (@epoch), never a wall-clock string. A
	// formatted "2026-07-01 00:00:00" carries no offset, so find parses it in the
	// HOST's zone while Go rendered it in the CLIENT's: against a RHEL box left at
	// TZ=UTC with a KST operator, the threshold moves nine hours forward and every
	// file last written in the first nine hours of the window is dropped — not
	// filtered, not summarised, simply never enumerated, so it leaves no trace in the
	// manifest either. classifyFile compares absolute instants; so must this.
	if fromMs > 0 {
		sb.WriteString(" -newermt " + shellQuote("@"+strconv.FormatInt(fromMs/1000, 10)))
	}
	return sb.String()
}

// logSurveyScript builds the one-round-trip survey for a host.
//
// It aggregates on the host — one line per module, not per file. A real kafka log
// directory holds 6489 files (log4j1 rotates hourly and never prunes), so listing
// every file just to draw the tree would ship megabytes per host for numbers the
// operator only wants as a total. The file list is enumerated later, only for the
// modules actually selected.
func logSurveyScript(cat LogCatalog, fromMs int64) string {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	var sb strings.Builder
	sb.WriteString("set -u\nexport LC_ALL=C\n")
	sb.WriteString("printf 'H|%s\\n' \"$(hostname 2>/dev/null | base64 -w0)\"\n")

	// One shell function driven entirely by RTM_* variables, so a dozen modules do
	// not become a dozen copies of the same aggregation pipeline. It takes no
	// positional arguments because it uses `set --` internally to capture the awk
	// result, which would clobber them.
	sb.WriteString(`rtm_b64(){ printf %s "$1" | base64 -w0; }
rtm_agg(){
  d=$RTM_DIR
  if [ ! -e "$d" ]; then
    printf '%s|%s|%s|missing|0|0|0|0|%s|\n' "$RTM_KIND" "$RTM_CAT" "$RTM_MOD" "$(rtm_b64 "$d")"; return
  fi
  if [ ! -r "$d" ]; then
    printf '%s|%s|%s|denied|0|0|0|0|%s|\n' "$RTM_KIND" "$RTM_CAT" "$RTM_MOD" "$(rtm_b64 "$d")"; return
  fi
  rd=$(readlink -f "$d" 2>/dev/null || printf %s "$d")
  set -- $(eval "find \"\$d\" $RTM_FIND -printf '%s %T@\n' 2>/dev/null" | awk '
    {n++; s+=$1; t=int($2); if(o==0||t<o)o=t; if(t>x)x=t}
    END{printf("%d %d %d %d\n", n+0, s+0, o+0, x+0)}')
  printf '%s|%s|%s|ok|%s|%s|%s|%s|%s|%s\n' "$RTM_KIND" "$RTM_CAT" "$RTM_MOD" \
    "${1:-0}" "${2:-0}" "${3:-0}" "${4:-0}" "$(rtm_b64 "$d")" "$(rtm_b64 "$rd")"
}
`)

	emit := func(kind, catKey, mod, dir, findExpr string) {
		sb.WriteString("RTM_KIND=" + shellQuote(kind) + "; RTM_CAT=" + shellQuote(catKey) +
			"; RTM_MOD=" + shellQuote(mod) + "; RTM_DIR=" + shellQuote(dir) +
			"; RTM_FIND=" + shellQuote(findExpr) + "; rtm_agg\n")
	}

	for _, c := range cat.Categories {
		// Discovered modules: the glob's matched directory becomes a module, so a
		// module deployed after this build still shows up.
		for _, g := range c.DiscoverGlobs {
			expr := logFindExpr(LogModuleDef{}, fromMs)
			sb.WriteString("for gd in " + g + "; do [ -d \"$gd\" ] || continue\n")
			sb.WriteString("  RTM_KIND=G; RTM_CAT=" + shellQuote(c.Key) +
				"; RTM_MOD=-; RTM_DIR=$gd; RTM_FIND=" + shellQuote(expr) + "; rtm_agg\ndone\n")
		}
		for _, d := range c.Modules {
			if d.IsCmd() {
				// A command's size is unknowable until it runs; the tree shows it as a
				// command rather than guessing.
				sb.WriteString("printf 'M|%s|%s|cmd|0|0|0|0|%s|\\n' " +
					shellQuote(c.Key) + " " + shellQuote(d.Name) + " " + shellQuote(b64(d.Cmd)) + "\n")
				continue
			}
			expr := logFindExpr(d, fromMs)
			for _, p := range d.Paths {
				emit("M", c.Key, d.Name, p, expr)
			}
		}
	}
	return sb.String()
}

// parseLogSurvey turns the survey output into tree rows.
func parseLogSurvey(cat LogCatalog, out string) (hostname string, mods []LogModuleStat) {
	defByName := map[string]LogModuleDef{}
	for _, c := range cat.Categories {
		for _, d := range c.Modules {
			defByName[c.Key+"/"+d.Name] = d
		}
	}
	dec := func(s string) string {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
		if err != nil {
			return ""
		}
		return string(b)
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "H|") {
			hostname = strings.TrimSpace(dec(line[2:]))
			continue
		}
		if !strings.HasPrefix(line, "M|") && !strings.HasPrefix(line, "G|") {
			continue
		}
		kind := line[:1]
		p := strings.SplitN(line[2:], "|", 9)
		if len(p) < 9 {
			continue
		}
		st := LogModuleStat{
			Category: p[0],
			Module:   p[1],
			Status:   p[2],
			Dir:      dec(p[7]),
			RealDir:  dec(p[8]),
		}
		if st.RealDir == "" {
			st.RealDir = st.Dir
		}
		st.Files, _ = strconv.Atoi(p[3])
		st.Bytes, _ = strconv.ParseInt(p[4], 10, 64)
		if v, err := strconv.ParseInt(p[5], 10, 64); err == nil && v > 0 {
			st.OldestMs = v * 1000
		}
		if v, err := strconv.ParseInt(p[6], 10, 64); err == nil && v > 0 {
			st.NewestMs = v * 1000
		}
		if kind == "G" {
			// A discovered directory is named after the directory that contains it.
			st.Module = moduleNameFromLogDir(st.Dir)
			if st.Module == "" {
				continue
			}
		}
		if st.Status == "cmd" {
			st.IsCmd = true
			// For a command the "dir" field carries the command itself.
			st.Dir = dec(p[7])
			st.RealDir = ""
		}
		if d, ok := defByName[st.Category+"/"+st.Module]; ok {
			st.NeedsRoot = d.NeedsRoot
			st.Recursive = d.Recursive
			st.Match = d.Match
		}
		if !validAbsPath(st.Dir) && !st.IsCmd {
			continue // I1: the path came back from the host
		}
		mods = append(mods, st)
	}
	return hostname, dedupeModuleStats(mods)
}

// dedupeModuleStats folds rows that resolve to the same physical directory with the
// same filter, so a symlinked path cannot make the tree double-count.
func dedupeModuleStats(mods []LogModuleStat) []LogModuleStat {
	seen := map[string]int{}
	out := make([]LogModuleStat, len(mods))
	copy(out, mods)
	for i := range out {
		if out[i].IsCmd || out[i].Status != "ok" || out[i].RealDir == "" {
			continue
		}
		// The filter is part of the identity: redis and redis-sentinel share a
		// directory and are legitimately two rows.
		k := out[i].RealDir + "\x00" + out[i].Match + "\x00" + strconv.FormatBool(out[i].Recursive)
		if j, ok := seen[k]; ok {
			out[i].DupOfDir = out[j].Category + "/" + out[j].Module
			continue
		}
		seen[k] = i
	}
	sort.SliceStable(out, func(a, b int) bool {
		if ra, rb := categoryRank(out[a].Category), categoryRank(out[b].Category); ra != rb {
			return ra < rb
		}
		return out[a].Module < out[b].Module
	})
	return out
}

// DefaultLogCatalog is the shipped catalog, for the UI to show and the settings
// store to seed a first edit from.
func DefaultLogCatalog() LogCatalog { return defaultLogCatalog() }

// LogSurveyCluster surveys several hosts at once and assigns each one the archive
// directory it will occupy.
//
// The names are assigned HERE, across the whole set, rather than per host: two boxes
// in one data centre both answering "localhost.localdomain" would otherwise merge
// their logs into a single directory, and the operator would have no way to tell
// whose kafka log they were reading.
func (m *Manager) LogSurveyCluster(hosts []ServerIdent, cat LogCatalog, fromMs int64) []LogSurvey {
	if len(cat.Categories) == 0 {
		cat = defaultLogCatalog()
	}
	dirs := serverDirNames(hosts)
	out := make([]LogSurvey, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h ServerIdent) {
			defer wg.Done()
			sv, err := m.LogSurveyFor(h.HostID, cat, fromMs)
			if err != nil {
				sv = LogSurvey{HostID: h.HostID, Err: err.Error()}
			}
			sv.HostName = h.DisplayName
			if sv.Hostname == "" {
				sv.Hostname = h.Hostname
			}
			sv.DirName = dirs[h.HostID]
			out[i] = sv
		}(i, h)
	}
	wg.Wait()
	return out
}

// LogSurveyFor surveys one host. It runs elevated when it can: /var/log/messages and
// /var/log/audit are 0600 root:root, so an unprivileged survey would report the
// system category as unreadable even though the collection would succeed.
func (m *Manager) LogSurveyFor(hostID string, cat LogCatalog, fromMs int64) (LogSurvey, error) {
	s := m.get(hostID)
	if s == nil {
		return LogSurvey{}, fmt.Errorf("호스트가 연결되어 있지 않습니다")
	}
	if len(cat.Categories) == 0 {
		cat = defaultLogCatalog()
	}
	script := logSurveyScript(cat, fromMs)

	var out string
	if s.elevated {
		out, _ = m.sudoRun(s, script)
	} else {
		out, _ = m.plainRun(s, script)
	}
	hostname, mods := parseLogSurvey(cat, out)
	sv := LogSurvey{
		HostID: hostID, Hostname: hostname, Elevated: s.elevated,
		Modules: mods, Targets: m.captureTargets(s),
	}
	if len(mods) == 0 {
		sv.Err = "수집 대상을 조사하지 못했습니다: " + tailLines(out, 2)
	}
	return sv, nil
}

package monitor

import (
	"path"
	"strings"
	"testing"
	"time"
)

func TestValidCollectID(t *testing.T) {
	for i := 0; i < 30; i++ {
		if id := newCollectID(); !validCollectID(id) {
			t.Fatalf("newCollectID() produced %q which fails validCollectID", id)
		}
	}
	bad := []string{
		"", "log", "log-1-abcdef12", "log-1753800000000-ABCDEF12",
		"log-1753800000000-abcdef1", "log-1753800000000-abcdef123",
		"log-1753800000000-abcdef12;id", "../log-1753800000000-abcdef12",
		"pcap-1753800000000-abcdef12", "log-1753800000000-abcdef12 ",
	}
	for _, id := range bad {
		if validCollectID(id) {
			t.Errorf("validCollectID(%q) = true, want false", id)
		}
	}
}

func TestSanitizeSegment(t *testing.T) {
	cases := map[string]string{
		"lizcollector":    "lizcollector",
		"collector.log":   "collector.log",
		"redis-sentinel":  "redis-sentinel",
		"trunk.logan.com": "trunk.logan.com",
		"a b c":           "a_b_c",
		"../etc":          "etc",
		"..":              "",
		".":               "",
		"":                "",
		"   ":             "",
		"a/b":             "a_b",
		"log;rm -rf /":    "log_rm_-rf",
		"log$(id)":        "log_id",
		"한글이름":            "",
		"_leading":        "leading",
		"trailing_":       "trailing",
	}
	for in, want := range cases {
		if got := sanitizeSegment(in); got != want {
			t.Errorf("sanitizeSegment(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sanitizeSegment(strings.Repeat("a", 200)); len(got) != maxSegLen {
		t.Errorf("long segment not capped: %d chars", len(got))
	}
}

// Two boxes in one data centre are frequently both called localhost.localdomain.
// Letting them share a directory would silently merge their logs — the operator
// would read one server's logs believing they were the other's.
func TestServerDirNamesDisambiguatesDuplicates(t *testing.T) {
	items := []ServerIdent{
		{HostID: "a", Hostname: "localhost.localdomain", DisplayName: "trunk-1", Addr: "10.0.0.11"},
		{HostID: "b", Hostname: "localhost.localdomain", DisplayName: "trunk-2", Addr: "10.0.0.12"},
		{HostID: "c", Hostname: "collector-01", DisplayName: "trunk-3", Addr: "10.0.0.13"},
	}
	got := serverDirNames(items)
	if got["a"] == got["b"] {
		t.Fatalf("duplicate hostnames collided: %q", got["a"])
	}
	// BOTH colliding hosts must be qualified — leaving one with the bare name makes
	// it arbitrary which server that directory is.
	for _, id := range []string{"a", "b"} {
		if !strings.Contains(got[id], "10.0.0.1") {
			t.Errorf("host %s = %q, want the address appended", id, got[id])
		}
	}
	if got["c"] != "collector-01" {
		t.Errorf("unique hostname was changed: %q", got["c"])
	}
}

func TestServerDirNamesFallbacks(t *testing.T) {
	items := []ServerIdent{
		{HostID: "a", Hostname: "", DisplayName: "trunk.logan.com-1", Addr: "10.0.0.11"},
		{HostID: "b", Hostname: "   ", DisplayName: "", Addr: "10.0.0.12"},
		{HostID: "c", Hostname: "한글", DisplayName: "", Addr: ""},
	}
	got := serverDirNames(items)
	if got["a"] != "trunk.logan.com-1" {
		t.Errorf("empty hostname should fall back to the display name: %q", got["a"])
	}
	if got["b"] != "10.0.0.12" {
		t.Errorf("no hostname or name should fall back to the address: %q", got["b"])
	}
	if got["c"] == "" {
		t.Error("an unusable hostname with no fallbacks must still yield a name")
	}
	// Every host must get a name, and no two may share one.
	seen := map[string]bool{}
	for id, n := range got {
		if n == "" {
			t.Errorf("host %s got an empty directory name", id)
		}
		if seen[n] {
			t.Errorf("directory name %q used twice", n)
		}
		seen[n] = true
	}
}

func TestArchivePathFor(t *testing.T) {
	cases := []struct {
		server, cat, mod, rel string
		cut                   bool
		from                  string
		want                  string
	}{
		{"trunk-1", LogCatModules, "lizcollector", "collector.log", false, "",
			"trunk-1/modules/lizcollector/collector.log"},
		{"trunk-1", LogCatModules, "lift-packet", "packet/2026-07/onion-2026-07-28_1.log.gz", false, "",
			"trunk-1/modules/lift-packet/packet/2026-07/onion-2026-07-28_1.log.gz"},
		{"trunk-1", LogCatMiddleware, "kafka", "server.log", false, "",
			"trunk-1/middleware/kafka/server.log"},
		{"trunk-1", LogCatSystem, "var-log", "messages-20260705", true, "20260630",
			"trunk-1/system/var-log/messages-20260705.from-20260630"},
		{"trunk-1", LogCatJournal, "journal", "journal.log", false, "",
			"trunk-1/journal/journal/journal.log"},
	}
	for _, c := range cases {
		got, err := archivePathFor(c.server, c.cat, c.mod, c.rel, c.cut, c.from)
		if err != nil {
			t.Errorf("archivePathFor(%q,%q,%q,%q): %v", c.server, c.cat, c.mod, c.rel, err)
			continue
		}
		if got != c.want {
			t.Errorf("archivePathFor(%q,%q,%q,%q) = %q, want %q", c.server, c.cat, c.mod, c.rel, got, c.want)
		}
	}
}

// A hostile or merely odd filename must never produce a path that leaves the server
// directory — this is the only thing between a host-supplied name and the operator's
// filesystem when the archive is extracted.
func TestArchivePathForRefusesEscapes(t *testing.T) {
	bad := []struct{ mod, rel string }{
		{"m", "../../etc/passwd"},
		{"m", "/etc/passwd"},
		{"m", ".."},
		{"m", "."},
		{"m", ""},
		{"m", "///"},
		{"..", "x.log"},
	}
	for _, c := range bad {
		got, err := archivePathFor("srv", LogCatSystem, c.mod, c.rel, false, "")
		if err == nil && (strings.Contains(got, "..") || !strings.HasPrefix(got, "srv/")) {
			t.Errorf("archivePathFor(mod=%q rel=%q) = %q escaped", c.mod, c.rel, got)
		}
		if err == nil && c.rel == "../../etc/passwd" {
			// Sanitised rather than rejected is acceptable, as long as it stays inside.
			if !strings.HasPrefix(got, "srv/") {
				t.Errorf("traversal not contained: %q", got)
			}
		}
	}
	// A cut file with no reference date must be refused rather than named ".from-".
	if _, err := archivePathFor("srv", LogCatSystem, "m", "messages", true, ""); err == nil {
		t.Error("a cut entry with no date was accepted")
	}
}

// /var/log/messages is claimed by middleware(keepalived) AND system(/var/log).
// Copying it twice would double it in the archive; dropping it silently would be
// worse. The specific claim wins and the loser is recorded.
func TestDedupeEntriesFoldsSharedFiles(t *testing.T) {
	entries := []LogEntry{
		{HostID: "h", Category: LogCatMiddleware, Module: "keepalived", Source: "/var/log/messages", Rel: "messages", SizeBytes: 100},
		{HostID: "h", Category: LogCatSystem, Module: "var-log", Source: "/var/log/messages", Rel: "messages", SizeBytes: 100},
		{HostID: "h", Category: LogCatSystem, Module: "var-log", Source: "/var/log/secure", Rel: "secure", SizeBytes: 50},
	}
	specific := map[string]bool{LogCatMiddleware + "/keepalived": true}
	out := dedupeEntries(entries, specific)

	var kept, folded int
	for _, e := range out {
		if e.Source != "/var/log/messages" {
			continue
		}
		if e.DupOf == "" {
			kept++
			if e.Category != LogCatMiddleware {
				t.Errorf("the specific claim should win, kept %s/%s", e.Category, e.Module)
			}
		} else {
			folded++
			if !strings.Contains(e.DupOf, "keepalived") {
				t.Errorf("DupOf should point at the survivor, got %q", e.DupOf)
			}
		}
	}
	if kept != 1 || folded != 1 {
		t.Errorf("kept=%d folded=%d, want 1 and 1", kept, folded)
	}
	// The unrelated file must be untouched.
	if _, b := selectedBytes(out); b != 150 {
		t.Errorf("selected bytes = %d, want 150 (100 + 50, messages counted once)", b)
	}
}

func TestEstimateArchiveBytes(t *testing.T) {
	// Plain text compresses; an already-compressed file does not and must not be
	// projected as if it would.
	entries := []LogEntry{
		{Rel: "collector.log", SizeBytes: 1000},
		{Rel: "old.log.gz", SizeBytes: 1000},
		{Rel: "skipped.log", SizeBytes: 9999, Skipped: "권한 없음"},
		{Rel: "dup.log", SizeBytes: 9999, DupOf: "system/var-log"},
	}
	got := estimateArchiveBytes(entries)
	want := int64(float64(1000)*textCompressRatio) + 1000
	if got != want {
		t.Errorf("estimateArchiveBytes = %d, want %d", got, want)
	}
	if f, b := selectedBytes(entries); f != 2 || b != 2000 {
		t.Errorf("selectedBytes = (%d,%d), want (2,2000) — skipped and duplicate excluded", f, b)
	}
}

func TestLogStagePathsAndGuard(t *testing.T) {
	id := "log-1753800000000-abcdef12"
	root := logStageRoot("/data", id)
	arc := logArchivePath("/data", id)
	if root != "/data/.rtaskmgr-logs/"+id {
		t.Errorf("stage root = %q", root)
	}
	if arc != "/data/.rtaskmgr-logs/"+id+".tar.gz" {
		t.Errorf("archive path = %q", arc)
	}
	if !validLogStagePath(root, id) || !validLogStagePath(arc, id) {
		t.Error("our own paths must pass the cleanup guard")
	}
	// Nothing else may. These are exactly the paths a bug could otherwise delete.
	for _, p := range []string{
		"/var/log", "/var/log/messages", "/usr/local/liz/lizcollector/logs", "/",
		"/data/.rtaskmgr-logs", "/data/.rtaskmgr-logs/log-1753800000000-ffffffff",
		"/data/.rtaskmgr-logs/" + id + "/../..", "/data/.rtaskmgr-logs/" + id + "x",
		"/home/liz", "/etc",
	} {
		if validLogStagePath(p, id) {
			t.Errorf("cleanup guard accepted %q", p)
		}
	}
	// A malformed collect id must fail even for a well-formed-looking path.
	if validLogStagePath("/data/.rtaskmgr-logs/bogus", "bogus") {
		t.Error("cleanup guard accepted an invalid collect id")
	}
}

func TestModuleNameFromLogDir(t *testing.T) {
	// This only ever sees directories matched by the liz discovery globs
	// (/usr/local/liz/liz*/log{,s}); the system category carries a static module
	// name instead. "/var/log" is here to pin the rule rather than because it
	// occurs: "log" is the base, so the containing directory ("var") is the name.
	cases := map[string]string{
		"/usr/local/liz/lizcollector/logs":     "lizcollector",
		"/usr/local/liz/lizstats/log":          "lizstats",
		"/usr/local/liz/liz-lizlogbackup/logs": "liz-lizlogbackup",
		"/usr/local/liz/lift/logs/":            "lift",
		"/data/kafka-log":                      "kafka-log",
		"/var/log":                             "var",
	}
	for in, want := range cases {
		if got := moduleNameFromLogDir(in); got != want {
			t.Errorf("moduleNameFromLogDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClampLogRange(t *testing.T) {
	now := time.Date(2026, 7, 30, 15, 4, 5, 0, time.Local)
	// recentDays wins and snaps to midnight, so "최근 3일" is a whole-day boundary.
	from, to := clampLogRange(0, 0, 3, now)
	want := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local).UnixMilli()
	if from != want || to != 0 {
		t.Errorf("recent 3 days = (%d,%d), want (%d,0)", from, to, want)
	}
	// A reversed explicit range is corrected rather than silently yielding nothing.
	a := time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local).UnixMilli()
	b := time.Date(2026, 6, 1, 0, 0, 0, 0, time.Local).UnixMilli()
	from, to = clampLogRange(a, b, 0, now)
	if from != b || to != a {
		t.Errorf("reversed range not corrected: (%d,%d)", from, to)
	}
	// Zero means "everything".
	if f, tt := clampLogRange(0, 0, 0, now); f != 0 || tt != 0 {
		t.Errorf("no range = (%d,%d), want (0,0)", f, tt)
	}
	if f, _ := clampLogRange(-5, -9, 0, now); f != 0 {
		t.Errorf("negatives not clamped: %d", f)
	}
}

func TestJournalCmd(t *testing.T) {
	from := time.Date(2026, 6, 30, 0, 0, 0, 0, time.Local).UnixMilli()
	cmd, err := journalCmd(from, 0, []string{"lizcollector.service", "kafka.service"}, 512<<20)
	if err != nil {
		t.Fatalf("journalCmd: %v", err)
	}
	for _, want := range []string{"journalctl", "--no-pager", "--since", "-u 'lizcollector.service'", "head -c"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("journalCmd missing %q: %s", want, cmd)
		}
	}
	// A unit name is a host-adjacent string that ends up in a command; it must be
	// validated, not quoted-and-hoped.
	for _, bad := range []string{"kafka.service;id", "$(reboot)", "a b", "x`id`"} {
		if _, err := journalCmd(from, 0, []string{bad}, 0); err == nil {
			t.Errorf("journalCmd accepted unit %q", bad)
		}
	}
	// Empty entries are skipped rather than becoming "-u ''".
	cmd, err = journalCmd(0, 0, []string{"", "kafka.service", "  "}, 0)
	if err != nil {
		t.Fatalf("journalCmd with blanks: %v", err)
	}
	if strings.Count(cmd, "-u ") != 1 {
		t.Errorf("blank units not skipped: %s", cmd)
	}
}

func TestDefaultCatalogMatchesTicket(t *testing.T) {
	cat := defaultLogCatalog()
	byKey := map[string]LogCategoryDef{}
	for _, c := range cat.Categories {
		byKey[c.Key] = c
	}
	for _, k := range []string{LogCatModules, LogCatMiddleware, LogCatSystem, LogCatJournal} {
		if _, ok := byKey[k]; !ok {
			t.Fatalf("catalog is missing category %q", k)
		}
	}
	// The ticket enumerates these middleware modules; a missing one silently drops a
	// whole product's logs from every collection.
	want := []string{"keepalived", "zookeeper", "kafka", "redis", "redis-sentinel", "mariadb", "clickhouse-server"}
	have := map[string]LogModuleDef{}
	for _, m := range byKey[LogCatMiddleware].Modules {
		have[m.Name] = m
	}
	for _, w := range want {
		if _, ok := have[w]; !ok {
			t.Errorf("middleware catalog is missing %q", w)
		}
	}
	// redis and redis-sentinel share a directory, so each must carry a filename
	// filter or they would collect each other's files.
	if have["redis"].Match == "" || have["redis-sentinel"].Match == "" {
		t.Error("redis and redis-sentinel share /usr/local/liz/redis/logs and need a Match")
	}
	// The liz modules must be discovered, not hard-coded: they change per deployment.
	if len(byKey[LogCatModules].DiscoverGlobs) == 0 {
		t.Error("the liz module category must discover its modules")
	}
	// Both spellings exist in the field.
	globs := strings.Join(byKey[LogCatModules].DiscoverGlobs, " ")
	if !strings.Contains(globs, "liz*/logs") || !strings.Contains(globs, "liz*/log") {
		t.Errorf("discover globs should cover log and logs: %q", globs)
	}
	// /var/log needs recursion; a flat module directory must not.
	sys := map[string]LogModuleDef{}
	for _, m := range byKey[LogCatSystem].Modules {
		sys[m.Name] = m
	}
	if !sys["var-log"].Recursive {
		t.Error("/var/log must be scanned recursively")
	}
	if !sys["dmesg"].IsCmd() || !sys["sysctl"].IsCmd() {
		t.Error("dmesg and sysctl are commands, not files")
	}
	if sys["dmesg"].OutFile == "" || sys["sysctl"].OutFile == "" {
		t.Error("command modules need an output filename")
	}
}

// Read as root, /var/log contains far more than logs. Without these exclusions a
// 300MB collection quietly becomes multi-GB (journald's binary database and
// /var/log/audit are routinely the largest things on the box), and we would also be
// shipping binary login records nobody asked for.
func TestVarLogExclusions(t *testing.T) {
	var def LogModuleDef
	for _, c := range defaultLogCatalog().Categories {
		if c.Key != LogCatSystem {
			continue
		}
		for _, m := range c.Modules {
			if m.Name == "var-log" {
				def = m
			}
		}
	}
	if def.Name == "" {
		t.Fatal("var-log module not found")
	}
	excluded := []string{
		"journal", "journal/system.journal", "journal/remote/x.journal",
		"audit", "audit/audit.log", "audit/audit.log.1",
		"sa", "sa/sa30", "lastlog", "wtmp", "btmp", "tallylog",
		"user-1000.journal",
	}
	for _, rel := range excluded {
		if !excludedBy(def, rel) {
			t.Errorf("%q should be excluded from /var/log", rel)
		}
	}
	// The things we actually want must survive.
	kept := []string{
		"messages", "messages-20260705", "secure", "secure-20260712",
		"cron", "dmesg.old", "boot.log", "yum.log", "maillog",
		"anaconda/journal.log.txt", "sssd/sssd.log",
	}
	for _, rel := range kept {
		if excludedBy(def, rel) {
			t.Errorf("%q should NOT be excluded from /var/log", rel)
		}
	}
	// A module with no rules excludes nothing.
	if excludedBy(LogModuleDef{Name: "kafka"}, "server.log") {
		t.Error("a module with no Exclude rules excluded a file")
	}
	if excludedBy(def, "") {
		t.Error("an empty relative path was excluded")
	}
}

// redis and redis-sentinel share /usr/local/liz/redis/logs, and the sentinel's file
// is "redis-sentinel.log" — so a "redis*" pattern swallows both and leaves the
// sentinel row empty. Verified against the real filenames on a live host.
func TestRedisSentinelMatchesAreDisjoint(t *testing.T) {
	var redis, sentinel LogModuleDef
	for _, c := range defaultLogCatalog().Categories {
		for _, m := range c.Modules {
			switch m.Name {
			case "redis":
				redis = m
			case "redis-sentinel":
				sentinel = m
			}
		}
	}
	if redis.Match == "" || sentinel.Match == "" {
		t.Fatal("both redis rows need a filename filter")
	}
	// The real filenames, plus plausible rotations of each.
	cases := map[string]string{
		"redis.log":             "redis",
		"redis.log.1":           "redis",
		"redis.log-20260730.gz": "redis",
		"redis-sentinel.log":    "redis-sentinel",
		"redis-sentinel.log.1":  "redis-sentinel",
	}
	for name, want := range cases {
		mr, _ := path.Match(redis.Match, name)
		ms, _ := path.Match(sentinel.Match, name)
		if mr && ms {
			t.Errorf("%q matches both patterns — it would be collected twice", name)
		}
		got := ""
		if mr {
			got = "redis"
		} else if ms {
			got = "redis-sentinel"
		}
		if got != want {
			t.Errorf("%q matched %q, want %q (redis=%q sentinel=%q)", name, got, want, redis.Match, sentinel.Match)
		}
	}
}

// The modules whose files are root-only must be marked, so the tree can say
// "root 권한 필요" instead of showing them as unreadable. Each of these was verified
// on a live host: /var/log/messages is 0600 root:root, /data/mariadb-log is
// drwxr-x--- mysql:mysql, and zookeeper's rotated files are -rw------- root:root.
func TestRootOnlyModulesMarked(t *testing.T) {
	cat := defaultLogCatalog()
	need := map[string]bool{"keepalived": false, "var-log": false, "mariadb": false, "zookeeper": false}
	for _, c := range cat.Categories {
		for _, m := range c.Modules {
			if _, ok := need[m.Name]; ok {
				need[m.Name] = m.NeedsRoot
			}
		}
	}
	for name, marked := range need {
		if !marked {
			t.Errorf("module %q reads root-only files and must set NeedsRoot", name)
		}
	}
}

func TestOverSizeCap(t *testing.T) {
	if overSizeCap(logSizeCapBytes) {
		t.Error("exactly at the cap should be allowed")
	}
	if !overSizeCap(logSizeCapBytes + 1) {
		t.Error("over the cap should be refused")
	}
}

func TestRenderManifest(t *testing.T) {
	srv := ServerIdent{HostID: "h", Hostname: "collector-01", DisplayName: "trunk.logan.com-1", Addr: "10.0.0.11"}
	jun30 := time.Date(2026, 6, 30, 0, 0, 0, 0, time.Local).UnixMilli()
	jul5 := time.Date(2026, 7, 5, 0, 0, 0, 0, time.Local).UnixMilli()
	jul12 := time.Date(2026, 7, 12, 0, 0, 0, 0, time.Local).UnixMilli()
	entries := []LogEntry{
		// The active log sorts BEFORE its rotated siblings by filename; the manifest
		// must present them oldest-first regardless.
		{HostID: "h", Category: LogCatSystem, Module: "var-log", Source: "/var/log/messages",
			Rel: "messages", SizeBytes: 5 << 20, FirstMs: jul12, LastMs: jul12},
		{HostID: "h", Category: LogCatSystem, Module: "var-log", Source: "/var/log/messages-20260705",
			Rel: "messages-20260705", SizeBytes: 3 << 20, Cut: true, CutFrom: "20260630", FirstMs: jun30, LastMs: jul5},
		{HostID: "h", Category: LogCatModules, Module: "lizcollector", Source: "/usr/local/liz/lizcollector/logs/collector.log",
			Rel: "collector.log", SizeBytes: 1 << 20, FirstMs: jul12},
		{HostID: "h", Category: LogCatMiddleware, Module: "keepalived", Source: "/var/log/messages",
			Rel: "messages", DupOf: "system/var-log"},
		{HostID: "h", Category: LogCatMiddleware, Module: "mariadb", Source: "/data/mariadb-log",
			Rel: "error.log", Skipped: "없음"},
	}
	out := renderManifest(srv, "collector-01", entries, jun30, 0, time.Now())

	for _, want := range []string{
		"collector-01", "10.0.0.11", "trunk.logan.com-1",
		"/usr/local/liz/lizcollector/logs/collector.log", // the original path must survive
		"messages-20260705.from-20260630",                // the cut marker
		"20260630 이전을 잘라냄",
		"수집되지 않은 항목", "없음", "중복",
		"시간순 이어붙이기", "cat ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("manifest missing %q\n---\n%s", want, out)
		}
	}
	// The concatenation line must list the older file before the active one.
	catLine := ""
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "cat ") {
			catLine = ln
			break
		}
	}
	iOld := strings.Index(catLine, "messages-20260705")
	iNew := strings.Index(catLine, " messages ")
	if iOld < 0 || iNew < 0 || iOld > iNew {
		t.Errorf("cat order is not chronological: %q", catLine)
	}
}

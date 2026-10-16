package monitor

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"rtaskmgr/internal/host"
)

func TestGcExtractScriptSince(t *testing.T) {
	s := gcExtractScript(map[string]int64{"/a/gc.log": 1700000000, "/b/it's.log": 5}, map[string]int64{"/a/gc.log": 1600000000}, 30)
	for _, want := range []string{
		"'/a/gc.log') echo 1700000000 ;;",
		`'/b/it'\''s.log') echo 5 ;;`,
		"*) echo $(( $(date +%s) - 2592000 )) ;;",
		"asince_for() {\n  case \"$1\" in\n    '/a/gc.log') echo 1600000000 ;;",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if !strings.Contains(gcExtractScript(nil, nil, 0), "*) echo 0 ;;") {
		t.Error("firstDays=0 must mean everything")
	}
}

func TestParseGcFetch(t *testing.T) {
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	zw.Write([]byte("2026-09-29T10:13:26.492+0900: 0.418: [GC pause (G1 Evacuation Pause) (young), 0.01 secs]\n"))
	zw.Close()
	args := base64.StdEncoding.EncodeToString([]byte("java\n-Dmodule.name=collector\n-Xloggc:/l/gc.log\n"))
	out := "NOW|1700000000\n" +
		"P|42|liz|3600|1048576|120|300|" + args + "\n" +
		"G|42|/l/gc.log\nN|42|2|0\nD|42|" + base64.StdEncoding.EncodeToString(z.Bytes()) + "\n" +
		"P|7|root|10|100|5|-1|" + base64.StdEncoding.EncodeToString([]byte("java\nkafka.Kafka\n")) + "\n" +
		"P|9|root|10|100|5|-1|\nG|9|/r/gc.log\nN|9|1|1\n" +
		"P|11|liz|10|100|5|-1|\nG|11|/x/gc.log\nN|11|0|0\nA|11|12|3\n"
	f := parseGcFetch(out)
	if f.NowMs != 1700000000000 || len(f.Procs) != 4 {
		t.Fatalf("%+v", f)
	}
	p := f.Procs[0]
	if p.PID != 42 || p.UpSec != 3600 || p.RSSKB != 1048576 || p.Threads != 120 || p.FDs != 300 ||
		p.GcLog != "/l/gc.log" || p.Files != 2 || !strings.Contains(p.Text, "GC pause") || len(p.Args) != 3 {
		t.Errorf("%+v", p)
	}
	if f.Procs[1].GcLog != "" || f.Procs[1].FDs != -1 {
		t.Errorf("no-log proc: %+v", f.Procs[1])
	}
	if f.Procs[2].Err == "" {
		t.Error("all files unreadable must be reported")
	}
	if p := f.Procs[3]; p.Archives != 12 || p.ArchivesRead != 3 || p.Err != "" {
		t.Errorf("live log gone but archives present must not be an error: %+v", p)
	}
}

// Read-only live run of the extraction script against a real host.
// RTM_GC_HOST=<host> RTM_GC_USER=liz RTM_GC_PASS=... go test -run TestLiveGcFetch -v ./internal/monitor/
func TestLiveGcFetch(t *testing.T) {
	addr := os.Getenv("RTM_GC_HOST")
	if addr == "" {
		t.Skip("set RTM_GC_HOST for the read-only GC log fetch")
	}
	c, err := dial(host.Host{Addr: addr, Port: 22, User: os.Getenv("RTM_GC_USER"), Password: os.Getenv("RTM_GC_PASS")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	m := &Manager{}
	s := &session{client: c}
	// RTM_GC_SINCEPATH=<gc log path>: pretend that log was fetched an hour ago but its
	// restart archives never were (the backfill case).
	since := map[string]int64{}
	if p := os.Getenv("RTM_GC_SINCEPATH"); p != "" {
		since[p] = time.Now().Unix() - 3600
	}
	out, err := m.plainRun(s, gcExtractScript(since, nil, 30))
	f := parseGcFetch(out)
	t.Logf("err=%v now=%d procs=%d wire=%d bytes", err, f.NowMs, len(f.Procs), len(out))
	for _, p := range f.Procs {
		t.Logf("pid=%d user=%s up=%ds rss=%dMB thr=%d fd=%d files=%d arch=%d/%d text=%dB log=%s err=%s args0=%v",
			p.PID, p.User, p.UpSec, p.RSSKB/1024, p.Threads, p.FDs, p.Files, p.ArchivesRead, p.Archives, len(p.Text), p.GcLog, p.Err, firstN(p.Args, 2))
	}
	if os.Getenv("RTM_GC_DUMP") != "" {
		for _, p := range f.Procs {
			if p.Text != "" {
				os.WriteFile(os.Getenv("RTM_GC_DUMP")+"/"+strings.ReplaceAll(strings.TrimPrefix(p.GcLog, "/"), "/", "_")+".txt", []byte(p.Text), 0o644)
			}
		}
	}
}

func firstN(s []string, n int) []string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

package monitor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The STOP check must sit at the top of the per-file loop — after the read, before
// any copy — so a cancel is honoured at the next file boundary and never leaves a
// half-written copy behind.
func TestCopyScriptStopsOnStopFile(t *testing.T) {
	sh := logCopyScript("/data/.rtaskmgr-logs/log-1-aabbccdd", 0, 0, "", -1, 0)
	iLoop := strings.Index(sh, "extra; do")
	iStop := strings.Index(sh, `[ -e "$CTL/STOP" ]`)
	iCase := strings.Index(sh, "case $mode in")
	if iLoop < 0 || iStop < iLoop || iStop > iCase {
		t.Fatal("the STOP check must be the first thing in the copy loop")
	}
	stopLine := sh[iStop : iStop+strings.Index(sh[iStop:], "\n")]
	if strings.Contains(stopLine, "DONE|copy") || !strings.Contains(stopLine, "STOPPED|copy") {
		t.Errorf("a stopped copy must not claim DONE|copy (Go reads that as complete): %q", stopLine)
	}
}

// StartLogCollect must check the cancel at each boundary and signal the copy.
func TestStartLogCollectHonoursCancel(t *testing.T) {
	src := scriptSources(t)["logcollect_run.go"]
	i := strings.Index(src, "func (m *Manager) StartLogCollect(ctx context.Context")
	if i < 0 {
		t.Fatal("StartLogCollect must take the operator's cancel as a context")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	if n := strings.Count(body, "abandon(ErrLogCollectCanceled)"); n < 3 {
		t.Errorf("cancel must be honoured before copy, after copy and after archive (found %d)", n)
	}
	if !strings.Contains(body, `logCtlDir+"/STOP"`) {
		t.Error("a cancel during the copy must drop the STOP file the script checks")
	}
}

// Runs the real copy script under a local bash: with STOP present nothing is copied
// and the script does not report completion; without it, the file is copied.
func TestCopyScriptStopFileLocalBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	for _, stop := range []bool{false, true} {
		dir := t.TempDir()
		root := filepath.ToSlash(filepath.Join(dir, ".rtaskmgr-logs", "log-1-aabbccdd"))
		ctl := root + "/" + logCtlDir
		if err := os.MkdirAll(filepath.FromSlash(ctl), 0o700); err != nil {
			t.Fatal(err)
		}
		src := filepath.ToSlash(filepath.Join(dir, "a.log"))
		if err := os.WriteFile(filepath.FromSlash(src), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		dst := root + "/srv/a.log"
		jobs := strings.Join([]string{"0", "whole", src, dst, dst + ".cut", ""}, "\x00") + "\x00"
		if err := os.WriteFile(filepath.FromSlash(ctl+"/jobs"), []byte(jobs), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.FromSlash(ctl+"/dirs"), []byte(root+"/srv\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
		if stop {
			if err := os.WriteFile(filepath.FromSlash(ctl+"/STOP"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		out, _ := exec.Command(bash, "-c", logCopyScript(root, 0, 0, "", -1, 0)).CombinedOutput()
		s := string(out)
		_, statErr := os.Stat(filepath.FromSlash(dst))
		copied := statErr == nil
		switch {
		case stop && (copied || strings.Contains(s, "DONE|copy") || !strings.Contains(s, "STOPPED|copy")):
			t.Errorf("with STOP: copied=%v, output=%q", copied, s)
		case !stop && (!copied || !strings.Contains(s, "DONE|copy")):
			t.Errorf("without STOP: copied=%v, output=%q", copied, s)
		}
	}
}

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Two servers must never write to one local file. The download ends in an os.Rename,
// which replaces whatever is there — so a shared name means each host verifies an
// archive the other has already overwritten, and the first to finish then deletes its
// own archive from the server after passing a check against somebody else's bytes.
// Claiming the name up front makes that impossible rather than unlikely.
func TestClaimLocalPathNeverClobbers(t *testing.T) {
	dir := t.TempDir()
	a, err := claimLocalPath(dir, "rtaskmgr-logs-trunk-1-20260731", ".tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	b, err := claimLocalPath(dir, "rtaskmgr-logs-trunk-1-20260731", ".tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("both claims returned %q", a)
	}
	if filepath.Base(a) != "rtaskmgr-logs-trunk-1-20260731.tar.gz" {
		t.Errorf("first claim = %q, want the plain name", filepath.Base(a))
	}
	if filepath.Base(b) != "rtaskmgr-logs-trunk-1-20260731-2.tar.gz" {
		t.Errorf("second claim = %q, want a -2 suffix", filepath.Base(b))
	}
	// A file that already exists on disk from an earlier run is also respected.
	pre := filepath.Join(dir, "other.tar.gz")
	if err := os.WriteFile(pre, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := claimLocalPath(dir, "other", ".tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if c == pre {
		t.Error("an existing file was claimed and would have been overwritten")
	}
	if body, _ := os.ReadFile(pre); string(body) != "x" {
		t.Error("the existing file was modified")
	}
}

// The delete is rebuilt from these two values, so this is where a path from outside
// stops being trusted.
func TestSplitLeftoverPath(t *testing.T) {
	base, id, ok := splitLeftoverPath("/data/.rtaskmgr-logs/log-1751000000000-aabbccdd")
	if !ok || base != "/data" || id != "log-1751000000000-aabbccdd" {
		t.Errorf("got %q/%q/%v", base, id, ok)
	}
	base, id, ok = splitLeftoverPath("/home/liz/.rtaskmgr-logs/log-1751000000000-aabbccdd.tar.gz")
	if !ok || base != "/home/liz" || id != "log-1751000000000-aabbccdd" {
		t.Errorf("got %q/%q/%v", base, id, ok)
	}
	for _, bad := range []string{
		"", "/var/log/messages", "/data/.rtaskmgr-logs", ".rtaskmgr-logs/log-1",
		"/data/.rtaskmgr-logs/log-1/../../etc", "/data/.rtaskmgr-pcap/log-1",
	} {
		if _, _, ok := splitLeftoverPath(bad); ok {
			t.Errorf("splitLeftoverPath(%q) was accepted", bad)
		}
	}
}

package updater

import "testing"

func TestShouldAutoApplyCapsAtFive(t *testing.T) {
	dir := t.TempDir()
	mk := func() *Updater { return New(Config{CurrentVersion: "v1.0.0", ConfigDir: dir}) }
	info := UpdateInfo{Available: true, CurrentVersion: "v1.0.0", LatestVersion: "v1.0.1", DownloadURL: "http://x/y.exe"}
	for i := 1; i <= 5; i++ {
		if !mk().ShouldAutoApply(info) {
			t.Fatalf("attempt %d should be allowed (mis-stamp not yet exhausted)", i)
		}
	}
	if mk().ShouldAutoApply(info) {
		t.Fatalf("6th attempt must be BLOCKED by the 5-try cap")
	}
}

func TestShouldAutoApplyDevNever(t *testing.T) {
	u := New(Config{CurrentVersion: "dev", ConfigDir: t.TempDir()})
	if u.ShouldAutoApply(UpdateInfo{Available: true, LatestVersion: "v9.9.9"}) {
		t.Fatal("dev build must never auto-apply")
	}
}

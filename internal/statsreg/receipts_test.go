package statsreg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecentRegistrationsListsOnlyUnfinished(t *testing.T) {
	dir := t.TempDir()
	s := &Service{Directory: dir}
	done := receipt{Result: Result{ID: "11111111-1111-4111-8111-111111111111", Stage: stageCheckpoints, Committed: true, Notified: true, Verified: true}, GroupName: "A"}
	half := receipt{Result: Result{ID: "22222222-2222-4222-8222-222222222222", Stage: stageDevices}, GroupName: "B", DBHost: "192.0.2.10"}
	for _, r := range []receipt{done, half} {
		if e := s.saveReceipt(r); e != nil {
			t.Fatal(e)
		}
	}
	// A manage record in the same folder is not a registration.
	if e := os.WriteFile(filepath.Join(dir, "manage-33333333-3333-4333-8333-333333333333.json"), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	got, e := s.RecentRegistrations()
	if e != nil || len(got) != 1 || got[0].ID != half.Result.ID || got[0].GroupName != "B" || got[0].Stage != "장비만 저장됨 — 체크포인트가 아직" {
		t.Fatalf("%+v %v", got, e)
	}
}

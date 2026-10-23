package statsreg

import (
	"reflect"
	"testing"
)

// 4th round: 「codex V7 name policy」 (Device Name Contains codex server_2) and a
// rename to 「codex trunk node 2」 — the preview said nothing.
func TestDeviceRuleHits(t *testing.T) {
	conds := []deviceNameCond{
		{rule: 1, operator: operatorContains, value: "codex server_2"},
		{rule: 2, operator: operatorEquals, value: "Codex Trunk Node 2"},
		{rule: 3, operator: operatorContains, value: "codex"},
	}
	got := deviceRuleHits(conds, []DeviceChange{{DeviceID: 7, OldName: "codex server_2", NewName: "codex trunk node 2"}})
	want := map[int64][]string{
		1: {"codex server_2 → codex trunk node 2: 대상에서 빠짐"},
		2: {"codex server_2 → codex trunk node 2: 대상에 새로 들어감"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

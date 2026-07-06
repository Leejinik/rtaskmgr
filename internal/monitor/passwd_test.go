package monitor

import "testing"

func TestExpiryDay(t *testing.T) {
	cases := []struct {
		lc, max string
		want    int64
	}{
		{"19500", "90", 19590}, // normal: lastchange + max
		{"19500", "", -1},      // no max → never
		{"19500", "99999", -1}, // sentinel max → never
		{"19500", "-1", -1},    // disabled max → never
		{"", "90", 0},          // unknown lastchange
		{"0", "90", 0},         // forced-change / unknown
	}
	for _, c := range cases {
		if got := expiryDay(c.lc, c.max); got != c.want {
			t.Errorf("expiryDay(%q,%q)=%d want %d", c.lc, c.max, got, c.want)
		}
	}
}

func TestParseShadow(t *testing.T) {
	out := "NOW:1720000000\nroot:19500:90\nliz:19600:0\n"
	st, err := parseShadow(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !st.HasRoot || !st.HasLiz {
		t.Fatalf("expected both accounts, got %+v", st)
	}
	if st.TodayDays != 1720000000/86400 {
		t.Errorf("TodayDays=%d", st.TodayDays)
	}
	if st.RootExpDays != 19590 {
		t.Errorf("RootExpDays=%d want 19590", st.RootExpDays)
	}
	if st.LizExpDays != 19600 {
		t.Errorf("LizExpDays=%d want 19600", st.LizExpDays)
	}
}

func TestParseShadowMissing(t *testing.T) {
	if _, err := parseShadow("NOW:1720000000\n"); err == nil {
		t.Errorf("expected error when neither account present")
	}
}

func TestManagedAccountsOrder(t *testing.T) {
	// The login account must be rotated LAST.
	if got := managedAccounts("liz"); got[len(got)-1] != "liz" {
		t.Errorf("liz login should end with liz: %v", got)
	}
	if got := managedAccounts("root"); got[len(got)-1] != "root" {
		t.Errorf("root login should end with root: %v", got)
	}
}

package monitor

import "testing"

func TestValidUnit(t *testing.T) {
	ok := []string{
		"kafka.service", "lizstats.service", "foo@bar.service",
		"a_b-c.service", "systemd-journald.service", "nginx",
		"user@1000.service", "getty@tty1.service",
	}
	for _, u := range ok {
		if !validUnit(u) {
			t.Errorf("validUnit(%q) = false, want true", u)
		}
	}
	// Anything that could inject shell syntax must be rejected.
	bad := []string{
		"", "foo bar", "foo;rm -rf /", "foo`id`", "foo$(id)",
		"a|b", "foo&", "x'y", "foo\nbar", "foo>out", "$(reboot)",
	}
	for _, u := range bad {
		if validUnit(u) {
			t.Errorf("validUnit(%q) = true, want false", u)
		}
	}
}

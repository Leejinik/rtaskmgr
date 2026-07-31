//go:build !windows && !linux

package monitor

// ensureLocalRoom is a no-op on platforms with no free-space probe (macOS, a
// convenience build), and a failed pre-check must never be the reason a download
// is refused. The write path still reports ENOSPC. Windows and Linux have real
// implementations — see diskfree_windows.go and diskfree_linux.go.
func ensureLocalRoom(localPath string, need int64) error { return nil }

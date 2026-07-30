//go:build !windows

package monitor

// ensureLocalRoom is a no-op off Windows: the app's own platform is Windows (the
// macOS build is a convenience), and a failed pre-check must never be the reason
// a download is refused. The write path still reports ENOSPC.
func ensureLocalRoom(localPath string, need int64) error { return nil }

//go:build !windows

package updater

import "errors"

// applyPlatform is a stub for non-Windows platforms. These apps ship their
// Windows binary via GitHub Releases; the macOS/Linux side (when it exists) is
// handed off as a source zip, so there's nothing to auto-apply here.
func applyPlatform(exePath, newPath string) error {
	return errors.New("auto-update is not supported on this platform")
}

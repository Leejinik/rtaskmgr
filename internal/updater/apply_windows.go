//go:build windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// applyPlatform swaps the binary in place with NO helper script and NO cmd.exe,
// structurally eliminating the console-flood bug class. Windows allows renaming
// a running/locked image, so we move the live exe aside, drop the new bytes at
// the canonical path, relaunch, and quit. No batch loop can exist.
func applyPlatform(exePath, newPath string) error {
	oldPath := exePath + ".old"
	_ = os.Remove(oldPath)
	if err := os.Rename(exePath, oldPath); err != nil {
		return fmt.Errorf("rename current aside: %w", err)
	}
	if err := os.Rename(newPath, exePath); err != nil {
		_ = os.Rename(oldPath, exePath) // rollback to intact old binary
		return fmt.Errorf("place new binary (rolled back): %w", err)
	}
	cmd := exec.Command(exePath)
	cmd.Dir = filepath.Dir(exePath)
	// GUI subsystem → no console; CREATE_NO_WINDOW is belt-and-suspenders.
	// NEVER add DETACHED_PROCESS (0x08): it wins over CREATE_NO_WINDOW and lets
	// children spawn console windows (that was half the original bug).
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("relaunch: %w", err)
	}
	_ = cmd.Process.Release()
	return nil
}

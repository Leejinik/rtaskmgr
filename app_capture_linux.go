//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// wiresharkExe is the binary name on PATH. The wiresharkIn() probe that uses it
// looks under a "Wireshark" subdirectory, which is a Windows install layout — on
// Linux Wireshark comes from the package manager and lives on PATH, so that
// probe simply never matches here and openWithWireshark resolves it directly.
const wiresharkExe = "wireshark"

// revealInFolder opens the file's directory in the desktop's file manager.
//
// Unlike Explorer's /select and Finder's `open -R`, there is no portable way to
// open a Linux file manager with one entry pre-selected — the closest thing is
// the org.freedesktop.FileManager1 D-Bus interface, which not every environment
// implements. Opening the containing directory is the behaviour every desktop
// supports, and for this app's purpose (get me to the capture/log I just
// downloaded) it is close enough to not be worth a D-Bus dependency.
func revealInFolder(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("파일을 찾을 수 없습니다: %s", path)
	}
	dir := path
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		dir = filepath.Dir(path)
	}
	if _, err := exec.LookPath("xdg-open"); err != nil {
		return fmt.Errorf("xdg-open을 찾지 못했습니다. 파일 위치: %s", dir)
	}
	return exec.Command("xdg-open", dir).Start()
}

// openWithWireshark launches Wireshark on the file from PATH.
func openWithWireshark(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("파일을 찾을 수 없습니다: %s", path)
	}
	exe, err := exec.LookPath(wiresharkExe)
	if err != nil {
		return fmt.Errorf("Wireshark를 찾지 못했습니다(sudo apt install wireshark). '폴더 열기'로 파일을 직접 전달하세요")
	}
	return exec.Command(exe, path).Start()
}

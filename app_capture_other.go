//go:build !windows && !linux

// The `open`/`/Applications` calls below are macOS, so this file must not cover
// Linux — see app_capture_linux.go, which uses xdg-open and PATH instead.
package main

import (
	"fmt"
	"os"
	"os/exec"
)

const wiresharkExe = "Wireshark"

// revealInFolder uses the macOS Finder (`open -R`), which selects the file.
func revealInFolder(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("파일을 찾을 수 없습니다: %s", path)
	}
	return exec.Command("open", "-R", path).Start()
}

// openWithWireshark asks the OS to open the file with the Wireshark app bundle.
func openWithWireshark(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("파일을 찾을 수 없습니다: %s", path)
	}
	if _, err := os.Stat("/Applications/Wireshark.app"); err != nil {
		return fmt.Errorf("Wireshark를 찾지 못했습니다. '폴더 열기'로 파일을 직접 전달하세요")
	}
	return exec.Command("open", "-a", "Wireshark", path).Start()
}

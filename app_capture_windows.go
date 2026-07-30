//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const wiresharkExe = "Wireshark.exe"

// revealInFolder opens Explorer with the file selected.
//
// The raw command line is assembled by hand here, which is normally forbidden:
// explorer.exe does not parse its arguments by the CRT rules Go's EscapeArg
// assumes, so a quoted "/select,C:\path with space\x.pcap" argument arrives
// mangled and Explorer opens the wrong window. The path is checked for quotes and
// NULs first, which is what makes the hand-assembly safe.
func revealInFolder(path string) error {
	if !pathHasNoQuotes(path) {
		return fmt.Errorf("경로에 사용할 수 없는 문자가 있습니다: %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("파일을 찾을 수 없습니다: %s", path)
	}
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	// Explorer returns a non-zero exit code even when it succeeds, so the start is
	// the only thing worth checking.
	return cmd.Start()
}

// openWithWireshark launches Wireshark on the file. Arguments go through the
// normal argv path (no shell, no hand-built command line).
func openWithWireshark(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("파일을 찾을 수 없습니다: %s", path)
	}
	var exe string
	for _, env := range []string{"ProgramW6432", "ProgramFiles", "ProgramFiles(x86)"} {
		if p, ok := wiresharkIn(os.Getenv(env)); ok {
			exe = p
			break
		}
	}
	if exe == "" {
		if p, err := exec.LookPath(wiresharkExe); err == nil {
			exe = p
		}
	}
	if exe == "" {
		return fmt.Errorf("Wireshark를 찾지 못했습니다. '폴더 열기'로 파일을 직접 전달하세요")
	}
	cmd := exec.Command(exe, path)
	cmd.Dir = filepath.Dir(exe)
	return cmd.Start()
}

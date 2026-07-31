//go:build linux

package main

import (
	_ "embed"
	"os"

	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

// appIcon is the window/taskbar icon. Wails only wires this up on Linux (Windows
// takes build/windows/icon.ico, macOS takes the .app bundle), so it is embedded
// in the Linux build only — it would be dead weight elsewhere.
//
//go:embed build/appicon.png
var appIcon []byte

// init disables WebKitGTK's DMA-BUF renderer unless the user asked for it
// explicitly. The DMA-BUF path renders a blank/white window (and "Error 71") on
// a lot of the environments this tool actually runs in — VMs, remote desktops,
// NVIDIA proprietary drivers — and this app has nothing to gain from it.
// Setting it here (before wails.Run initialises GTK) is enough: on a cgo build
// os.Setenv also updates the C environment, which is what WebKit reads.
func init() {
	if os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER") == "" {
		_ = os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1")
	}
}

// linuxOptions builds the Linux-specific window options.
//
// WebviewGpuPolicy MUST be set explicitly here. Wails only defaults it to
// "Never" when options.Linux is nil (its workaround for the blank-window bug,
// wailsapp/wails#2977); the moment we pass a non-nil Options the zero value
// applies, and that zero value is WebviewGpuPolicyAlways — i.e. adding an icon
// would silently turn hardware acceleration back on and bring the bug with it.
func linuxOptions() *linux.Options {
	return &linux.Options{
		Icon:             appIcon,
		ProgramName:      "rtaskmgr",
		WebviewGpuPolicy: linux.WebviewGpuPolicyNever,
	}
}

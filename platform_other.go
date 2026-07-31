//go:build !linux

package main

import "github.com/wailsapp/wails/v2/pkg/options/linux"

// linuxOptions returns nil off Linux — the field is ignored there, and keeping
// it nil avoids embedding the Linux app icon into the Windows/macOS binaries.
func linuxOptions() *linux.Options { return nil }

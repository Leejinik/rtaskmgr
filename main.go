package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// rpmFS carries the offline nethogs RPM bundle (rpms/rhel8, rpms/rhel9) into the
// binary so the network column can be installed on air-gapped RHEL hosts.
//
//go:embed all:rpms
var rpmFS embed.FS

func main() {
	// Create an instance of the app structure
	app := NewApp(rpmFS)

	// Create application with options
	err := wails.Run(&options.App{
		Title:     "rtaskmgr — Linux 작업 관리자",
		Width:     1280,
		Height:    820,
		MinWidth:  900,
		MinHeight: 560,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 32, G: 32, B: 32, A: 1},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:     "StreamerChat",
		Width:     1100,
		Height:    700,
		Frameless: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 13, G: 13, B: 20, A: 1},
		Windows: &windows.Options{
			// See chathub/main.go for the rationale — AV / endpoint-security
			// DLL injection into WebView2's renderer causes a constant busy
			// cursor over the app window. Disabling the integrity check lets
			// the injected DLLs load cleanly.
			WebviewDisableRendererCodeIntegrity: true,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

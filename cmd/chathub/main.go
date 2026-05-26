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
		Title:     "ChatHub",
		Width:     1200,
		Height:    800,
		Frameless: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 13, G: 13, B: 20, A: 1},
		Windows: &windows.Options{
			// Endpoint-security / AV software (Sophos, Trend Micro, etc.) inject
			// DLLs into WebView2's renderer process. WebView2's code-integrity
			// check then logs "Code Integrity" errors and the renderer ends up
			// in a half-broken state where the busy cursor pulses constantly
			// over the window even though the app is idle. Disabling the
			// integrity check lets those DLLs load and clears the spinner.
			// Tradeoff: malicious software could also inject — acceptable for
			// a chat client that runs no user code.
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

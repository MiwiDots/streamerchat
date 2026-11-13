package main

import (
	"context"
	"log"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// domReady runs once the frontend has loaded. Anything the user must see
// at startup goes here: events emitted from startup() fire before the
// frontend registered its listeners and are lost.
func (a *App) domReady(ctx context.Context) {
	if a.cfg == nil || a.cfg.LoadWarning == "" {
		return
	}
	msg := a.cfg.LoadWarning
	a.cfg.LoadWarning = ""
	// MessageDialog blocks until dismissed; keep it off the Wails
	// lifecycle callback so the UI finishes initialising meanwhile.
	go func() {
		if _, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
			Type:    runtime.WarningDialog,
			Title:   "StreamerChat settings",
			Message: msg,
		}); err != nil {
			log.Printf("[CONFIG] could not show load warning: %v", err)
		}
	}()
}

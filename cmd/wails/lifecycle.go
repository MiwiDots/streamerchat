package main

import (
	"context"
	"log"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// maxBufferedUIEvents caps the startup event buffer. Normally only a
// handful of events arrive before the frontend is ready; the cap only
// matters if the window never finishes loading (oldest are dropped).
const maxBufferedUIEvents = 1000

type uiEvent struct {
	name string
	data interface{}
}

// emit sends an event to the frontend, or buffers it until the frontend
// has registered its listeners (FrontendReady). Wails drops events that
// have no listener yet, and bootSequence runs from OnStartup — long before
// the page has loaded — so startup notices ("can't reach Twitch",
// "authentication expired", "ready") used to vanish silently.
// All backend → frontend events must go through here.
func (a *App) emit(name string, data interface{}) {
	if a.ctx == nil {
		return
	}
	a.uiMu.Lock()
	defer a.uiMu.Unlock()
	if !a.uiReady {
		if len(a.uiPending) >= maxBufferedUIEvents {
			a.uiPending = a.uiPending[1:]
		}
		a.uiPending = append(a.uiPending, uiEvent{name, data})
		return
	}
	// Emitting under uiMu keeps buffered and live events in order.
	runtime.EventsEmit(a.ctx, name, data)
}

// FrontendReady is called by the frontend once ALL its event listeners are
// registered. It flushes the startup buffer in order and switches emit to
// direct delivery. Idempotent (a webview reload just calls it again).
func (a *App) FrontendReady() {
	a.uiMu.Lock()
	defer a.uiMu.Unlock()
	if a.uiReady {
		return
	}
	for _, ev := range a.uiPending {
		runtime.EventsEmit(a.ctx, ev.name, ev.data)
	}
	log.Printf("[UI] frontend ready — flushed %d buffered event(s)", len(a.uiPending))
	a.uiPending = nil
	a.uiReady = true
}

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

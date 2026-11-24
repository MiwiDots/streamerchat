package main

import (
	"log"

	"github.com/miwi/streamerchat/internal/platform"
)

// autostartName is the value name under HKCU\...\CurrentVersion\Run.
const autostartName = "StreamerChat"

// AutostartStatus reports the "start with Windows" toggle for the settings
// UI: supported hides the row on other platforms; enabled is true only
// when Windows will really start the app; blocked means the entry exists
// but was switched off in Task Manager (re-ticking the box fixes it).
func (a *App) AutostartStatus() map[string]interface{} {
	st := platform.AutostartStatus(autostartName)
	return map[string]interface{}{
		"supported": platform.AutostartSupported(),
		"enabled":   st.Active(),
		"blocked":   st.Registered && st.BlockedByWindows,
	}
}

// SetAutostart enables or disables the Windows autostart entry. Returns an
// empty string on success, or the error message on failure.
func (a *App) SetAutostart(enabled bool) string {
	if !platform.AutostartSupported() {
		return "autostart is only supported on Windows"
	}
	var err error
	if enabled {
		err = platform.EnableAutostart(autostartName)
	} else {
		err = platform.DisableAutostart(autostartName)
	}
	if err != nil {
		log.Printf("[AUTOSTART] set enabled=%v failed: %v", enabled, err)
		return err.Error()
	}
	log.Printf("[AUTOSTART] enabled=%v", enabled)
	return ""
}

// syncAutostart runs once at startup: it repoints the entry at this exe if
// the app was moved since autostart was enabled, and logs the state so a
// "doesn't start with Windows" report can be diagnosed from the log.
func syncAutostart() {
	if !platform.AutostartSupported() {
		return
	}
	before := platform.AutostartStatus(autostartName)
	st, err := platform.SyncAutostart(autostartName)
	if err != nil {
		log.Printf("[AUTOSTART] repointing to this exe failed: %v", err)
	}
	if !st.Registered {
		return
	}
	if before.StalePath && !st.StalePath {
		log.Printf("[AUTOSTART] entry pointed at an old location (%s); updated to this exe", before.Command)
	}
	log.Printf("[AUTOSTART] registered=%v blockedByWindows=%v stalePath=%v command=%s",
		st.Registered, st.BlockedByWindows, st.StalePath, st.Command)
}

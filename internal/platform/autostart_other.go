//go:build !windows

package platform

// AutostartSupported reports whether "start with Windows" is available.
func AutostartSupported() bool { return false }

// AutostartStatus: never registered outside Windows.
func AutostartStatus(name string) AutostartState { return AutostartState{} }

// EnableAutostart is a no-op outside Windows.
func EnableAutostart(name string) error { return nil }

// DisableAutostart is a no-op outside Windows.
func DisableAutostart(name string) error { return nil }

// SyncAutostart is a no-op outside Windows.
func SyncAutostart(name string) (AutostartState, error) { return AutostartState{}, nil }

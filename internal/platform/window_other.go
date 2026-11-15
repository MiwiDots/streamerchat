//go:build !windows

package platform

// SetupCleanWindow is a no-op on non-Windows platforms.
func SetupCleanWindow() {}

// FlashTaskbar is a no-op outside Windows (the in-app notification is
// shown on every platform).
func FlashTaskbar(windowTitle string) error { return nil }

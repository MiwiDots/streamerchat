//go:build !windows

package platform

// SetupCleanWindow is a no-op on non-Windows platforms.
func SetupCleanWindow() {}

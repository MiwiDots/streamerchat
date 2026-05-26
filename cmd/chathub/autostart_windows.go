//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	autostartKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	autostartName    = "ChatHub"
)

// autostartExePath returns the quoted absolute path to the current executable,
// suitable for writing into the Run registry value. Symlinks are resolved.
// Returns an error in dev/temp builds so we don't pollute the registry with
// a stale %TEMP% path.
func autostartExePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	low := strings.ToLower(exe)
	if strings.Contains(low, `\temp\`) || strings.Contains(low, `\appdata\local\temp`) {
		return "", fmt.Errorf("refusing to register dev/temp build path: %s", exe)
	}
	return `"` + exe + `"`, nil
}

func autostartIsEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(autostartName)
	return err == nil
}

func autostartEnable() error {
	cmd, err := autostartExePath()
	if err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, autostartKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(autostartName, cmd)
}

func autostartDisable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKeyPath, registry.SET_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(autostartName); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

// autostartSupported reports whether the platform supports the autostart
// toggle (compile-time true on windows, false elsewhere).
func autostartSupported() bool { return true }

//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath             = `Software\Microsoft\Windows\CurrentVersion\Run`
	startupApprovedKeyPath = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

// AutostartSupported reports whether "start with Windows" is available.
func AutostartSupported() bool { return true }

func currentExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// AutostartStatus reads the Run value and the Task Manager switch for
// the entry called name (per user, HKCU).
func AutostartStatus(name string) AutostartState {
	var st AutostartState
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE); err == nil {
		if v, _, err := k.GetStringValue(name); err == nil {
			st.Registered = true
			st.Command = v
		}
		k.Close()
	}
	if !st.Registered {
		return st
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, startupApprovedKeyPath, registry.QUERY_VALUE); err == nil {
		if v, _, err := k.GetBinaryValue(name); err == nil {
			st.BlockedByWindows = startupApprovedDisabled(v)
		}
		k.Close()
	}
	if exe, err := currentExe(); err == nil {
		st.StalePath = !samePath(commandExe(st.Command), exe)
	}
	return st
}

// EnableAutostart registers the running exe to start at logon. It also
// clears a "disabled" switch Windows keeps in StartupApproved: without
// that, re-ticking the box in the app would change nothing after the
// user had once disabled the entry in Task Manager.
func EnableAutostart(name string) error {
	exe, err := currentExe()
	if err != nil {
		return err
	}
	if isTempPath(exe) {
		return fmt.Errorf("the app runs from a temporary folder (%s); move it to a permanent folder first", exe)
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	err = k.SetStringValue(name, quoteCommand(exe))
	k.Close()
	if err != nil {
		return err
	}
	return clearStartupApproved(name)
}

// DisableAutostart removes the entry (and its Task Manager switch).
func DisableAutostart(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	err = k.DeleteValue(name)
	k.Close()
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return clearStartupApproved(name)
}

// clearStartupApproved deletes the Task Manager switch; a missing value
// means "enabled".
func clearStartupApproved(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, startupApprovedKeyPath, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// SyncAutostart runs at app start: when autostart is registered for a
// different exe path (the app was moved after enabling it), it points
// the entry at the running exe again. Returns the state after syncing.
func SyncAutostart(name string) (AutostartState, error) {
	st := AutostartStatus(name)
	if !st.Registered || !st.StalePath {
		return st, nil
	}
	exe, err := currentExe()
	if err != nil || isTempPath(exe) {
		return st, err // leave a working entry alone rather than point it at a temp copy
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return st, err
	}
	err = k.SetStringValue(name, quoteCommand(exe))
	k.Close()
	if err != nil {
		return st, err
	}
	return AutostartStatus(name), nil
}

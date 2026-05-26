//go:build !windows

package main

func autostartSupported() bool      { return false }
func autostartIsEnabled() bool      { return false }
func autostartEnable() error        { return nil }
func autostartDisable() error       { return nil }

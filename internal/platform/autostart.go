package platform

import "strings"

// AutostartState describes an app's "start with Windows" entry.
type AutostartState struct {
	// Registered: the Run registry value exists.
	Registered bool
	// BlockedByWindows: the entry was switched off in Task Manager →
	// Startup apps (or Settings → Apps → Startup). Windows then skips it
	// at logon even though the Run value is still there.
	BlockedByWindows bool
	// StalePath: the Run value points at a different exe than the one
	// running now (the exe was moved/renamed after enabling autostart).
	StalePath bool
	// Command is the registered command line (for logging).
	Command string
}

// Active reports whether Windows will actually start the app at logon.
func (s AutostartState) Active() bool {
	return s.Registered && !s.BlockedByWindows && !s.StalePath
}

// startupApprovedDisabled interprets a StartupApproved\Run value. Windows
// writes 12 bytes; the first byte is even (0x02, 0x06) when the entry is
// enabled and odd (0x03, 0x07) when the user disabled it. A missing or
// empty value means enabled.
func startupApprovedDisabled(v []byte) bool {
	return len(v) > 0 && v[0]&1 == 1
}

// quoteCommand quotes an exe path for a Run value.
func quoteCommand(exe string) string {
	return `"` + exe + `"`
}

// commandExe extracts the exe path from a Run value: the quoted part, or
// the whole value when unquoted.
func commandExe(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, `"`) {
		if end := strings.Index(cmd[1:], `"`); end >= 0 {
			return cmd[1 : 1+end]
		}
		return strings.Trim(cmd, `"`)
	}
	return cmd
}

// samePath compares Windows paths (case-insensitive, either slash).
func samePath(a, b string) bool {
	norm := func(p string) string { return strings.ToLower(strings.ReplaceAll(p, "/", `\`)) }
	return norm(a) == norm(b)
}

// isTempPath reports paths that must never be registered: a dev build or
// an exe started straight from a zip / browser temp folder would leave a
// Run entry pointing at a file that is gone after the next cleanup.
func isTempPath(exe string) bool {
	low := strings.ToLower(strings.ReplaceAll(exe, "/", `\`))
	return strings.Contains(low, `\temp\`) || strings.Contains(low, `\tmp\`)
}

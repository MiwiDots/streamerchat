//go:build !windows

package youtube

import "fmt"

// readBrowserProfileCookies is implemented only on Windows for now —
// macOS uses Keychain-protected keys + AES-CBC, Linux uses libsecret +
// PBKDF2. Both are doable but not implemented yet. On non-Windows we
// short-circuit with a clear error.
func readBrowserProfileCookies(profileDir string) (map[string]string, error) {
	_ = profileDir
	return nil, fmt.Errorf("YouTube cookie capture is currently Windows-only (need DPAPI for Chrome/Edge cookie decryption)")
}

// Package credstore centralizes how we read + write OAuth tokens / session
// cookies into the OS-native secure storage (Windows Credential Manager,
// macOS Keychain, Linux Secret Service via libsecret), so the rest of
// the codebase doesn't repeat the keyring boilerplate per platform.
//
// All entries live under a per-feature service name with a single
// "default" account, e.g. "chathub-twitch" / "default" or
// "chathub-youtube" / "default". Pick a stable service constant when
// you store something new; never inline the string at call sites or
// you'll forget to delete it on logout.
package credstore

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// Service is a small enum-ish wrapper so call sites use named constants
// rather than free strings. Add new entries here when adding a new
// authenticated integration (Kick, etc).
type Service string

const (
	Twitch       Service = "chathub-twitch"
	YouTube      Service = "chathub-youtube"
	Kick         Service = "chathub-kick"
	defaultAcct          = "default"
)

// ErrNotFound is returned by Get when no entry exists. Callers that just
// want a clean "logged out" branch can errors.Is(err, ErrNotFound).
var ErrNotFound = errors.New("credstore: entry not found")

// Get reads the secret blob for `svc`. Returns ErrNotFound if nothing has
// been stored yet for that service.
func Get(svc Service) (string, error) {
	v, err := keyring.Get(string(svc), defaultAcct)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	return v, nil
}

// Set writes (or overwrites) the secret blob for `svc`.
func Set(svc Service, value string) error {
	return keyring.Set(string(svc), defaultAcct, value)
}

// Clear deletes the entry. Idempotent — missing entries are not an error.
func Clear(svc Service) error {
	err := keyring.Delete(string(svc), defaultAcct)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

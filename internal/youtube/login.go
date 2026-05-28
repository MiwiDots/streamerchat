package youtube

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// LoginCookies is the subset of YouTube session cookies we need to send a
// live-chat message via InnerTube. The full list YouTube's web client sends
// is larger, but these are the ones the send_message endpoint actually
// checks for. SAPISID is the critical one — the SAPISIDHASH Authorization
// header is computed from it.
type LoginCookies struct {
	SAPISID         string `json:"sapisid"`
	SID             string `json:"sid"`
	HSID            string `json:"hsid"`
	SSID            string `json:"ssid"`
	APISID          string `json:"apisid"`
	Secure3PSID     string `json:"__Secure-3PSID,omitempty"`
	Secure3PAPISID  string `json:"__Secure-3PAPISID,omitempty"`
	Secure1PSID     string `json:"__Secure-1PSID,omitempty"`
	Secure1PAPISID  string `json:"__Secure-1PAPISID,omitempty"`
	LoginInfo       string `json:"login_info,omitempty"`
	VisitorInfo1Live string `json:"visitor_info1_live,omitempty"`

	// Optional human label set after first login (e.g. user's YT handle)
	// so the UI can show "Logged in as @miwi".
	DisplayName string `json:"display_name,omitempty"`
}

// Valid reports whether the cookie set has the bare minimum required to
// authenticate a request — SAPISID (for the hash) plus at least one of the
// SID variants (for the actual session).
func (c LoginCookies) Valid() bool {
	return c.SAPISID != "" && (c.SID != "" || c.Secure3PSID != "" || c.Secure1PSID != "")
}

const (
	keyringService = "chathub-youtube"
	keyringAccount = "session-cookies"
)

// ErrNotLoggedIn is returned by LoadLoginCookies when nothing is stored.
var ErrNotLoggedIn = errors.New("no YouTube session stored")

// SaveLoginCookies persists the cookie set to the OS keyring (Windows
// Credential Manager / macOS Keychain / Linux Secret Service). Values are
// JSON-encoded into a single keyring entry.
func SaveLoginCookies(c LoginCookies) error {
	if !c.Valid() {
		return fmt.Errorf("refusing to store invalid cookie set (need SAPISID + a SID variant)")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return keyring.Set(keyringService, keyringAccount, string(data))
}

// LoadLoginCookies returns the previously saved set or ErrNotLoggedIn if
// the keyring entry is missing.
func LoadLoginCookies() (LoginCookies, error) {
	var c LoginCookies
	raw, err := keyring.Get(keyringService, keyringAccount)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return c, ErrNotLoggedIn
		}
		return c, err
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, err
	}
	return c, nil
}

// ClearLoginCookies deletes the keyring entry. Idempotent — missing entries
// are not an error.
func ClearLoginCookies() error {
	err := keyring.Delete(keyringService, keyringAccount)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

package youtube

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/miwi/streamerchat/internal/credstore"
)

// LoginCookies is the subset of YouTube session cookies we need to send a
// live-chat message via InnerTube. The full list YouTube's web client sends
// is larger, but these are the ones the send_message endpoint actually
// checks for. SAPISID is the critical one — the SAPISIDHASH Authorization
// header is computed from it.
type LoginCookies struct {
	SAPISID          string `json:"sapisid"`
	SID              string `json:"sid"`
	HSID             string `json:"hsid"`
	SSID             string `json:"ssid"`
	APISID           string `json:"apisid"`
	Secure3PSID      string `json:"__Secure-3PSID,omitempty"`
	Secure3PAPISID   string `json:"__Secure-3PAPISID,omitempty"`
	Secure1PSID      string `json:"__Secure-1PSID,omitempty"`
	Secure1PAPISID   string `json:"__Secure-1PAPISID,omitempty"`
	LoginInfo        string `json:"login_info,omitempty"`
	VisitorInfo1Live string `json:"visitor_info1_live,omitempty"`

	// Timestamps so we can show "session age" in the UI and proactively
	// refresh before SAPISID rotates server-side.
	SavedAt     time.Time `json:"saved_at,omitempty"`
	RefreshedAt time.Time `json:"refreshed_at,omitempty"`

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

// ErrNotLoggedIn is returned by LoadLoginCookies when nothing is stored.
var ErrNotLoggedIn = errors.New("no YouTube session stored")

// SaveLoginCookies persists the cookie set to the OS keyring via the
// shared credstore package. Stamps SavedAt if missing.
func SaveLoginCookies(c LoginCookies) error {
	if !c.Valid() {
		return fmt.Errorf("refusing to store invalid cookie set (need SAPISID + a SID variant)")
	}
	if c.SavedAt.IsZero() {
		c.SavedAt = time.Now()
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return credstore.Set(credstore.YouTube, string(data))
}

// LoadLoginCookies returns the previously saved set or ErrNotLoggedIn if
// the keyring entry is missing.
func LoadLoginCookies() (LoginCookies, error) {
	var c LoginCookies
	raw, err := credstore.Get(credstore.YouTube)
	if err != nil {
		if errors.Is(err, credstore.ErrNotFound) {
			return c, ErrNotLoggedIn
		}
		return c, err
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, err
	}
	return c, nil
}

// ClearLoginCookies deletes the keyring entry. Idempotent.
func ClearLoginCookies() error {
	return credstore.Clear(credstore.YouTube)
}

package config

import (
	"encoding/json"
	"errors"
	"log"

	"github.com/miwi/streamerchat/internal/credstore"
)

// Twitch tokens are stored per profile in the OS keyring (macOS Keychain,
// Windows DPAPI-protected file, Linux Secret Service) instead of in
// config.json, where they used to sit in plain text.

type tokenPair struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
}

// TokenStore is the keyring abstraction; swapped out in tests.
type TokenStore interface {
	Get(profileID string) (tokenPair, error)
	Set(profileID string, t tokenPair) error
	Delete(profileID string) error
}

// errNoTokens is returned by TokenStore.Get when nothing is stored.
var errNoTokens = errors.New("no tokens stored")

// tokenStore is the store in use. Tests replace it with an in-memory one.
var tokenStore TokenStore = keyringStore{}

type keyringStore struct{}

func (keyringStore) Get(profileID string) (tokenPair, error) {
	svc, err := credstore.StreamerChatTwitch(profileID)
	if err != nil {
		return tokenPair{}, err
	}
	blob, err := credstore.Get(svc)
	if errors.Is(err, credstore.ErrNotFound) {
		return tokenPair{}, errNoTokens
	}
	if err != nil {
		return tokenPair{}, err
	}
	var t tokenPair
	if err := json.Unmarshal([]byte(blob), &t); err != nil {
		return tokenPair{}, err
	}
	return t, nil
}

func (keyringStore) Set(profileID string, t tokenPair) error {
	svc, err := credstore.StreamerChatTwitch(profileID)
	if err != nil {
		return err
	}
	blob, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return credstore.Set(svc, string(blob))
}

func (keyringStore) Delete(profileID string) error {
	svc, err := credstore.StreamerChatTwitch(profileID)
	if err != nil {
		return err
	}
	return credstore.Clear(svc)
}

// loadTokens hydrates every profile's in-memory tokens, then mirrors the
// active profile into the flat c.Twitch. Called once by Load.
//
// A plaintext copy in config.json (pre-keyring install, or a previous
// keyring-unavailable fallback) wins over the keyring: it is never older,
// because the only path that writes it is a failed keyring write. Twitch
// rotates refresh tokens, so preferring a stale copy would log the user
// out. The next Save moves it into the keyring and wipes it from disk.
func (c *Config) loadTokens() {
	c.savedTokens = make(map[string]tokenPair)
	for i := range c.Profiles {
		p := &c.Profiles[i]
		if p.Twitch.LegacyAccessToken != "" || p.Twitch.LegacyRefreshToken != "" {
			p.Twitch.AccessToken = p.Twitch.LegacyAccessToken
			p.Twitch.RefreshToken = p.Twitch.LegacyRefreshToken
			// Not recorded in savedTokens → storeTokens writes it.
			continue
		}
		t, err := tokenStore.Get(p.ID)
		if err != nil {
			if !errors.Is(err, errNoTokens) {
				log.Printf("[CONFIG] keyring read for profile %q failed: %v", p.ID, err)
			}
			continue
		}
		p.Twitch.AccessToken = t.Access
		p.Twitch.RefreshToken = t.Refresh
		c.savedTokens[p.ID] = t
	}
	if p := c.activeProfile(); p != nil {
		c.Twitch = p.Twitch
	}
}

// storeTokens writes changed per-profile tokens to the keyring (called by
// Save after syncActiveToProfile). On success the plaintext legacy copy is
// cleared; if the keyring write fails the tokens are kept in config.json
// as a logged fallback so the user isn't logged out on the next start.
func (c *Config) storeTokens() {
	if c.savedTokens == nil {
		c.savedTokens = make(map[string]tokenPair)
	}
	for i := range c.Profiles {
		p := &c.Profiles[i]
		cur := tokenPair{Access: p.Twitch.AccessToken, Refresh: p.Twitch.RefreshToken}
		saved, known := c.savedTokens[p.ID]
		if known && saved == cur && p.Twitch.LegacyAccessToken == "" && p.Twitch.LegacyRefreshToken == "" {
			continue // keyring already up to date, nothing on disk
		}
		var err error
		if cur == (tokenPair{}) {
			err = tokenStore.Delete(p.ID) // logout
		} else {
			err = tokenStore.Set(p.ID, cur)
		}
		if err != nil {
			log.Printf("[CONFIG] keyring write for profile %q failed, keeping tokens in config.json: %v", p.ID, err)
			p.Twitch.LegacyAccessToken = cur.Access
			p.Twitch.LegacyRefreshToken = cur.Refresh
			continue
		}
		c.savedTokens[p.ID] = cur
		p.Twitch.LegacyAccessToken = ""
		p.Twitch.LegacyRefreshToken = ""
	}
	// The flat top-level "twitch" block is a mirror of the active profile;
	// it must never carry its own plaintext copy.
	if p := c.activeProfile(); p != nil {
		c.Twitch.LegacyAccessToken = p.Twitch.LegacyAccessToken
		c.Twitch.LegacyRefreshToken = p.Twitch.LegacyRefreshToken
	} else {
		c.Twitch.LegacyAccessToken, c.Twitch.LegacyRefreshToken = "", ""
	}
}

// deleteTokens removes a deleted profile's keyring entry.
// Takes saveMu: savedTokens is otherwise only touched inside Save.
func (c *Config) deleteTokens(profileID string) {
	saveMu.Lock()
	defer saveMu.Unlock()
	if err := tokenStore.Delete(profileID); err != nil {
		log.Printf("[CONFIG] keyring delete for profile %q failed: %v", profileID, err)
	}
	delete(c.savedTokens, profileID)
}

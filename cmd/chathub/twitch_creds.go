package main

import (
	"encoding/json"
	"errors"
	"log"

	"github.com/miwi/streamerchat/internal/credstore"
)

// twitchTokens is the blob we store in the OS keyring for Twitch.
// Mirrors cfg.AccessToken / cfg.RefreshToken, which are memory-only
// (`json:"-"` on HubConfig).
type twitchTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// migrateTwitchTokensFromConfig runs once at startup and hydrates the
// in-memory tokens.
//
// A plaintext copy in config.json WINS over the keyring when present. It
// is never older than the keyring entry: pre-v0.5.2 code wrote config.json
// first and the keyring second with the same tokens, and the current code
// only writes it when the keyring write failed (so the keyring is the
// stale one). Twitch rotates refresh tokens, so picking the stale one
// would force a re-login. The plaintext copy is then moved into the
// keyring and wiped from disk; if the keyring is still unavailable it is
// kept so the user isn't logged out.
func (a *App) migrateTwitchTokensFromConfig() {
	if a.cfg == nil {
		return
	}
	if a.cfg.LegacyAccessToken != "" || a.cfg.LegacyRefreshToken != "" {
		a.cfg.AccessToken = a.cfg.LegacyAccessToken
		a.cfg.RefreshToken = a.cfg.LegacyRefreshToken
		if a.saveTwitchTokens() {
			log.Printf("[CREDSTORE] moved Twitch tokens from config.json to OS keyring")
		}
		return
	}

	blob, err := credstore.Get(credstore.Twitch)
	if err != nil {
		if !errors.Is(err, credstore.ErrNotFound) {
			log.Printf("[CREDSTORE] twitch keyring read failed: %v", err)
		}
		return
	}
	var t twitchTokens
	if err := json.Unmarshal([]byte(blob), &t); err != nil {
		log.Printf("[CREDSTORE] twitch keyring entry unreadable: %v", err)
		return
	}
	a.cfg.AccessToken = t.AccessToken
	a.cfg.RefreshToken = t.RefreshToken
}

// saveTwitchTokens persists the in-memory tokens. Call it after every
// token change. Returns true if they landed in the keyring.
//
// Keyring OK   → any plaintext fallback copy is wiped from config.json.
// Keyring fail → tokens are written to config.json (0600) as a fallback,
//
//	with a log line, so a broken keyring doesn't force a re-login on
//	every start. This is the only path that writes tokens to disk.
func (a *App) saveTwitchTokens() bool {
	if a.cfg == nil || a.cfg.AccessToken == "" {
		return false
	}
	t := twitchTokens{AccessToken: a.cfg.AccessToken, RefreshToken: a.cfg.RefreshToken}
	blob, err := json.Marshal(t)
	if err != nil {
		log.Printf("[CREDSTORE] twitch save marshal: %v", err)
		return false
	}
	if err := credstore.Set(credstore.Twitch, string(blob)); err != nil {
		log.Printf("[CREDSTORE] twitch keyring write failed, falling back to config.json: %v", err)
		a.cfg.LegacyAccessToken = t.AccessToken
		a.cfg.LegacyRefreshToken = t.RefreshToken
		if err := a.cfg.Save(); err != nil {
			log.Printf("[CREDSTORE] fallback save failed, tokens only in memory: %v", err)
		}
		return false
	}
	if a.cfg.LegacyAccessToken != "" || a.cfg.LegacyRefreshToken != "" {
		a.wipeLegacyTokens()
	}
	return true
}

// wipeLegacyTokens removes the plaintext token copy from config.json.
func (a *App) wipeLegacyTokens() {
	a.cfg.LegacyAccessToken, a.cfg.LegacyRefreshToken = "", ""
	if err := a.cfg.Save(); err != nil {
		log.Printf("[CREDSTORE] wiping plaintext tokens from config.json failed: %v", err)
		return
	}
	log.Printf("[CREDSTORE] removed plaintext Twitch tokens from config.json")
}

// clearTwitchTokens drops the saved tokens everywhere (logout path):
// the keyring entry and any plaintext fallback copy.
func (a *App) clearTwitchTokens() {
	if err := credstore.Clear(credstore.Twitch); err != nil && !errors.Is(err, credstore.ErrNotFound) {
		log.Printf("[CREDSTORE] twitch clear: %v", err)
	}
	if a.cfg != nil && (a.cfg.LegacyAccessToken != "" || a.cfg.LegacyRefreshToken != "") {
		a.wipeLegacyTokens()
	}
}

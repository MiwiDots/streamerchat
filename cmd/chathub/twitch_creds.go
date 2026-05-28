package main

import (
	"encoding/json"
	"errors"
	"log"

	"github.com/miwi/streamerchat/internal/credstore"
)

// twitchTokens is the on-disk shape we store in the OS keyring for Twitch.
// Mirrors the access_token + refresh_token fields the rest of the app
// reads off cfg.AccessToken / cfg.RefreshToken in memory.
type twitchTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// migrateTwitchTokensFromConfig is called once per startup. If the saved
// config.json still has access/refresh tokens AND the keyring doesn't yet
// have an entry, copy them into the keyring and wipe the config.json
// fields. Existing users upgrading from <=0.3.x get their session moved
// transparently; fresh installs / re-installs go straight to the keyring.
func (a *App) migrateTwitchTokensFromConfig() {
	if a.cfg == nil {
		return
	}
	// Already migrated? Just hydrate the in-memory cfg from the keyring.
	if blob, err := credstore.Get(credstore.Twitch); err == nil && blob != "" {
		var t twitchTokens
		if json.Unmarshal([]byte(blob), &t) == nil {
			if t.AccessToken != "" {
				a.cfg.AccessToken = t.AccessToken
			}
			if t.RefreshToken != "" {
				a.cfg.RefreshToken = t.RefreshToken
			}
		}
		return
	}
	// Migrate path: tokens currently in config.json (left over from a
	// pre-credstore install).
	if a.cfg.AccessToken == "" && a.cfg.RefreshToken == "" {
		return
	}
	t := twitchTokens{AccessToken: a.cfg.AccessToken, RefreshToken: a.cfg.RefreshToken}
	blob, err := json.Marshal(t)
	if err != nil {
		log.Printf("[CREDSTORE] twitch migrate marshal: %v", err)
		return
	}
	if err := credstore.Set(credstore.Twitch, string(blob)); err != nil {
		log.Printf("[CREDSTORE] twitch migrate write: %v", err)
		return
	}
	// Wipe the plaintext copies from the config file. Keep them in memory
	// so the running session keeps working; on next refresh we'll save
	// the fresh tokens back to the keyring via saveTwitchTokens().
	prevAT, prevRT := a.cfg.AccessToken, a.cfg.RefreshToken
	a.cfg.AccessToken, a.cfg.RefreshToken = "", ""
	_ = a.cfg.Save()
	a.cfg.AccessToken, a.cfg.RefreshToken = prevAT, prevRT
	log.Printf("[CREDSTORE] migrated Twitch tokens from config.json to OS keyring")
}

// saveTwitchTokens persists the current in-memory access/refresh tokens
// into the OS keyring. Call this from anywhere we refresh / update them
// instead of cfg.Save() for the auth fields.
func (a *App) saveTwitchTokens() {
	if a.cfg == nil || a.cfg.AccessToken == "" {
		return
	}
	t := twitchTokens{AccessToken: a.cfg.AccessToken, RefreshToken: a.cfg.RefreshToken}
	blob, err := json.Marshal(t)
	if err != nil {
		log.Printf("[CREDSTORE] twitch save marshal: %v", err)
		return
	}
	if err := credstore.Set(credstore.Twitch, string(blob)); err != nil {
		log.Printf("[CREDSTORE] twitch save: %v", err)
	}
}

// clearTwitchTokens drops the saved keyring entry (logout path).
func (a *App) clearTwitchTokens() {
	if err := credstore.Clear(credstore.Twitch); err != nil && !errors.Is(err, credstore.ErrNotFound) {
		log.Printf("[CREDSTORE] twitch clear: %v", err)
	}
}

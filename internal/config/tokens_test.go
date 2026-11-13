package config

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

// memStore is an in-memory TokenStore so tests never touch the real OS
// keyring. fail makes every Set fail (keyring unavailable).
type memStore struct {
	mu   sync.Mutex
	m    map[string]tokenPair
	fail bool
}

func (s *memStore) Get(id string) (tokenPair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[id]
	if !ok {
		return tokenPair{}, errNoTokens
	}
	return t, nil
}
func (s *memStore) Set(id string, t tokenPair) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("keyring unavailable")
	}
	s.m[id] = t
	return nil
}
func (s *memStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

// TestMain swaps in the in-memory store for every test in this package.
func TestMain(m *testing.M) {
	tokenStore = &memStore{m: map[string]tokenPair{}}
	os.Exit(m.Run())
}

func useStore(t *testing.T) *memStore {
	t.Helper()
	s := &memStore{m: map[string]tokenPair{}}
	prev := tokenStore
	tokenStore = s
	t.Cleanup(func() { tokenStore = prev })
	return s
}

func TestTokensGoToKeyringNotDisk(t *testing.T) {
	store := useStore(t)
	withConfigDir(t)
	cfg, _ := Load()
	cfg.Twitch.AccessToken, cfg.Twitch.RefreshToken = "secret-at", "secret-rt"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if raw := mustRead(t); strings.Contains(raw, "secret-at") || strings.Contains(raw, "secret-rt") {
		t.Fatalf("tokens written to config.json:\n%s", raw)
	}
	if got := store.m["default"]; got.Access != "secret-at" || got.Refresh != "secret-rt" {
		t.Fatalf("keyring = %+v", got)
	}
	again, _ := Load()
	if again.Twitch.AccessToken != "secret-at" || again.Profiles[0].Twitch.RefreshToken != "secret-rt" {
		t.Fatalf("tokens not hydrated from keyring: %+v", again.Twitch)
	}
}

func TestLegacyPlaintextTokensMigrate(t *testing.T) {
	store := useStore(t)
	path := withConfigDir(t)
	os.MkdirAll(dirOf(path), 0700)
	legacy := `{"profiles":[{"id":"default","name":"Default","twitch":{"access_token":"old-at","refresh_token":"old-rt"}}],"active_profile_id":"default","twitch":{"access_token":"old-at","refresh_token":"old-rt"}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	// A stale keyring entry must lose against the plaintext copy.
	store.m["default"] = tokenPair{Access: "stale", Refresh: "stale"}

	cfg, _ := Load()
	if cfg.Twitch.AccessToken != "old-at" {
		t.Fatalf("legacy tokens not loaded: %+v", cfg.Twitch)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mustRead(t), "old-at") {
		t.Fatal("plaintext tokens still on disk after migration")
	}
	if store.m["default"].Access != "old-at" {
		t.Fatalf("keyring not updated: %+v", store.m["default"])
	}
}

func TestKeyringFailureFallsBackToDisk(t *testing.T) {
	store := useStore(t)
	store.fail = true
	withConfigDir(t)
	cfg, _ := Load()
	cfg.Twitch.AccessToken = "at"
	cfg.Twitch.RefreshToken = "rt"
	cfg.Save()
	if !strings.Contains(mustRead(t), `"access_token": "at"`) {
		t.Fatal("without a keyring the tokens must be kept in config.json")
	}
	again, _ := Load()
	if again.Twitch.AccessToken != "at" {
		t.Fatal("fallback tokens not restored")
	}
}

func TestPerProfileTokensAndRemoval(t *testing.T) {
	store := useStore(t)
	withConfigDir(t)
	cfg, _ := Load()
	cfg.Twitch.AccessToken = "a-token"
	p := cfg.AddProfile("Second")
	cfg.Save()
	if err := cfg.SwitchProfile(p.ID); err != nil {
		t.Fatal(err)
	}
	if cfg.Twitch.AccessToken != "" {
		t.Fatal("new profile must not inherit tokens")
	}
	cfg.Twitch.AccessToken = "b-token"
	cfg.Save()
	if store.m["default"].Access != "a-token" || store.m[p.ID].Access != "b-token" {
		t.Fatalf("per-profile keyring wrong: %+v", store.m)
	}
	cfg.SwitchProfile("default")
	if err := cfg.RemoveProfile(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.m[p.ID]; ok {
		t.Fatal("removed profile's keyring entry not deleted")
	}
	// Logout clears the keyring entry.
	cfg.Twitch.AccessToken, cfg.Twitch.RefreshToken = "", ""
	cfg.Save()
	if _, ok := store.m["default"]; ok {
		t.Fatal("logout did not clear keyring entry")
	}
}

func dirOf(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	return p[:i]
}

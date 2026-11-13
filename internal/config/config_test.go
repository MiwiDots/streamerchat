package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withConfigDir points ConfigPath() at a temp dir (it honours APPDATA on
// every OS) and returns the resulting config file path.
func withConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	p, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadCorruptFileIsBackedUpNotFatal(t *testing.T) {
	path := withConfigDir(t)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	// Simulates a file truncated mid-write by an older, non-atomic Save.
	if err := os.WriteFile(path, []byte(`{"twitch":{"access_tok`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load must not fail on a corrupt file, got %v", err)
	}
	if cfg == nil || len(cfg.Profiles) != 1 {
		t.Fatalf("want defaults with one profile, got %+v", cfg)
	}
	if !strings.Contains(cfg.LoadWarning, ".corrupt-") {
		t.Fatalf("LoadWarning should name the backup, got %q", cfg.LoadWarning)
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("want exactly one backup, got %v", matches)
	}
	if b, _ := os.ReadFile(matches[0]); string(b) != `{"twitch":{"access_tok` {
		t.Fatalf("backup content mismatch: %q", b)
	}
}

func TestSaveLoadRoundTripKeepsProfileTokens(t *testing.T) {
	withConfigDir(t)
	cfg, err := Load() // missing file → defaults, no warning
	if err != nil || cfg.LoadWarning != "" {
		t.Fatalf("fresh Load: cfg=%+v err=%v", cfg, err)
	}
	cfg.Twitch.AccessToken = "at"
	cfg.Twitch.RefreshToken = "rt"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Twitch.AccessToken != "at" || got.Profiles[0].Twitch.RefreshToken != "rt" {
		t.Fatalf("tokens not persisted into active profile: %+v", got)
	}
	if strings.Contains(mustRead(t), "LoadWarning") {
		t.Fatal("LoadWarning must never be persisted")
	}
}

func mustRead(t *testing.T) string {
	t.Helper()
	p, _ := ConfigPath()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

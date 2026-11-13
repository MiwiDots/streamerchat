package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression test for the v0.5.1 leak: tokens live in the OS keyring and
// must never be serialized into config.json by a normal Save.
func TestHubConfigNeverSerializesLiveTokens(t *testing.T) {
	cfg := &HubConfig{AccessToken: "secret-at", RefreshToken: "secret-rt", Username: "miwi"}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if strings.Contains(out, "secret-at") || strings.Contains(out, "secret-rt") {
		t.Fatalf("tokens leaked into JSON: %s", out)
	}
	// Empty legacy fields are omitted entirely.
	if strings.Contains(out, "access_token") || strings.Contains(out, "refresh_token") {
		t.Fatalf("empty legacy token keys should be omitted: %s", out)
	}
}

// Old config.json files still carry the plaintext keys; they must land in
// the Legacy* fields (for migration), not in the live token fields.
func TestHubConfigReadsLegacyTokenKeys(t *testing.T) {
	var cfg HubConfig
	if err := json.Unmarshal([]byte(`{"access_token":"old-at","refresh_token":"old-rt"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.LegacyAccessToken != "old-at" || cfg.LegacyRefreshToken != "old-rt" {
		t.Fatalf("legacy fields not populated: %+v", cfg)
	}
	if cfg.AccessToken != "" || cfg.RefreshToken != "" {
		t.Fatalf("live token fields must stay empty after unmarshal: %+v", cfg)
	}
}

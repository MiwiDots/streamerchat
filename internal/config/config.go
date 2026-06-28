package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds all application configuration.
//
// Storage shape on disk:
//
//	{
//	  "ui":       {...},                              // global preferences
//	  "profiles": [{id, name, twitch, youtube}, ...], // per-account state
//	  "active_profile_id": "default",
//
//	  // Legacy top-level fields kept for backwards compat — populated
//	  // from the active profile on Load() so the rest of the app can
//	  // keep accessing cfg.Twitch.X / cfg.YouTube.X unchanged.
//	  "twitch":  {...},
//	  "youtube": {...}
//	}
//
// SwitchProfile / AddProfile / RemoveProfile manage the array; Save()
// syncs the flat Twitch/YouTube fields back into the active profile
// so any in-session token refreshes etc. don't get lost.
type Config struct {
	Twitch  TwitchConfig  `json:"twitch"`
	YouTube YouTubeConfig `json:"youtube"`
	UI      UIConfig      `json:"ui"`

	Profiles        []Profile `json:"profiles"`
	ActiveProfileID string    `json:"active_profile_id"`
}

// Profile is one Twitch+YouTube account context the user can switch
// between via the titlebar dropdown.
type Profile struct {
	ID      string        `json:"id"`   // stable slug; never shown
	Name    string        `json:"name"` // display name in the UI
	Twitch  TwitchConfig  `json:"twitch"`
	YouTube YouTubeConfig `json:"youtube"`
}

// TwitchConfig holds Twitch-specific settings.
type TwitchConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Username     string `json:"username"`
	Channel      string `json:"channel"` // channel to join (without #)
	BotUserID    string `json:"bot_user_id"`
}

// YouTubeConfig holds YouTube-specific settings.
type YouTubeConfig struct {
	Enabled       bool   `json:"enabled"`
	ChannelHandle string `json:"channel_handle"` // @username or channel ID
	ChannelID     string `json:"channel_id"`     // UC... channel ID (optional)
	VideoID       string `json:"video_id"`       // override: specific video ID (optional)
}

// UIConfig holds UI preferences.
type UIConfig struct {
	ShowJoinPart     bool   `json:"show_join_part"`
	ShowTimestamps   bool   `json:"show_timestamps"`
	Layout           string `json:"layout"` // "stacked" or "side-by-side"
	Theme            string `json:"theme"`  // "dark" or "light"
	ChatSoundEnabled bool   `json:"chat_sound_enabled"`
	ChatSoundFile    string `json:"chat_sound_file"` // absolute path to a .wav, empty = use built-in beep
	ChatSoundVolume  int    `json:"chat_sound_volume"` // 0-100
	UpdateChannel    string `json:"update_channel"`    // "stable" (default), "beta", or "alpha"
	FontSize         int    `json:"font_size"`         // chat font size in px (10–28), 0 = use default 14
	Locale           string `json:"locale"`            // "en" or "de", empty = "en"
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		UI: UIConfig{
			ShowJoinPart:    true,
			ShowTimestamps:  true,
			Layout:          "stacked",
			Theme:           "dark",
			ChatSoundVolume: 60,
		},
	}
}

// ConfigPath returns the default config file path.
// Windows: %APPDATA%\streamerchat\config.json
// macOS:   ~/.config/streamerchat/config.json
func ConfigPath() (string, error) {
	if appData := os.Getenv("APPDATA"); appData != "" {
		return filepath.Join(appData, "streamerchat", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".config", "streamerchat", "config.json"), nil
}

// Load reads config from the default path. On first run with the new
// profiles schema, the legacy top-level Twitch+YouTube fields are
// promoted into a synthetic "Default" profile so existing installs
// keep their tokens transparently.
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			cfg.ensureProfile()
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.ensureProfile()
	return cfg, nil
}

// Save writes config to the default path. The flat Twitch/YouTube
// fields are mirrored back into the active profile first so any
// in-session changes (token refresh, channel rename, ...) get
// persisted to the right account slot.
func (c *Config) Save() error {
	c.syncActiveToProfile()

	path, err := ConfigPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(path, data, 0600)
}

// ensureProfile guarantees Profiles is non-empty + ActiveProfileID
// points at a real entry. Called after every Load — handles the
// migration from the pre-profiles config shape (single account,
// fields at the top level only) and self-heals broken configs where
// ActiveProfileID dangles.
func (c *Config) ensureProfile() {
	if len(c.Profiles) == 0 {
		// Legacy migration: synthesise a Default profile from the
		// existing top-level Twitch+YouTube state. Even if both
		// were empty (fresh install) we still want one profile so
		// the rest of the code can rely on having an active one.
		c.Profiles = []Profile{{
			ID:      "default",
			Name:    "Default",
			Twitch:  c.Twitch,
			YouTube: c.YouTube,
		}}
		c.ActiveProfileID = "default"
		return
	}
	// Validate ActiveProfileID — fall back to first entry.
	if c.activeProfile() == nil {
		c.ActiveProfileID = c.Profiles[0].ID
	}
	// Hydrate flat fields from the active profile so app code can
	// keep using cfg.Twitch.X / cfg.YouTube.X without knowing about
	// the profile layer.
	p := c.activeProfile()
	c.Twitch = p.Twitch
	c.YouTube = p.YouTube
}

// activeProfile returns a pointer into c.Profiles or nil if the id
// doesn't resolve. Pointer so callers can mutate in-place.
func (c *Config) activeProfile() *Profile {
	for i := range c.Profiles {
		if c.Profiles[i].ID == c.ActiveProfileID {
			return &c.Profiles[i]
		}
	}
	return nil
}

// syncActiveToProfile copies the current flat Twitch/YouTube fields
// back into the active profile. Run before every Save so token
// refreshes and other in-memory updates persist to disk.
func (c *Config) syncActiveToProfile() {
	if p := c.activeProfile(); p != nil {
		p.Twitch = c.Twitch
		p.YouTube = c.YouTube
	}
}

// ListProfiles returns a copy of the profile list (the slice values,
// not pointers) so callers can render UI without mutating state.
func (c *Config) ListProfiles() []Profile {
	out := make([]Profile, len(c.Profiles))
	copy(out, c.Profiles)
	return out
}

// SwitchProfile activates the profile with the given id. Returns
// error if the id doesn't exist. Before switching, the current flat
// fields are flushed into the outgoing profile so nothing's lost.
func (c *Config) SwitchProfile(id string) error {
	for _, p := range c.Profiles {
		if p.ID == id {
			c.syncActiveToProfile()
			c.ActiveProfileID = id
			next := c.activeProfile()
			c.Twitch = next.Twitch
			c.YouTube = next.YouTube
			return nil
		}
	}
	return fmt.Errorf("profile %q not found", id)
}

// AddProfile creates an empty profile with the given display name,
// generates a stable id, and returns it. The new profile is NOT
// activated automatically — the caller decides whether to switch.
func (c *Config) AddProfile(name string) Profile {
	id := slugify(name)
	if id == "" {
		id = "profile"
	}
	// Disambiguate: if the slug is taken, append -2, -3, ...
	taken := func(candidate string) bool {
		for _, p := range c.Profiles {
			if p.ID == candidate {
				return true
			}
		}
		return false
	}
	base := id
	for i := 2; taken(id); i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	p := Profile{ID: id, Name: name}
	c.Profiles = append(c.Profiles, p)
	return p
}

// RemoveProfile drops the profile with the given id. The active
// profile can't be removed (caller must switch first). Always
// keeps at least one profile alive.
func (c *Config) RemoveProfile(id string) error {
	if id == c.ActiveProfileID {
		return fmt.Errorf("cannot remove the active profile — switch to another one first")
	}
	if len(c.Profiles) <= 1 {
		return fmt.Errorf("cannot remove the last profile")
	}
	for i, p := range c.Profiles {
		if p.ID == id {
			c.Profiles = append(c.Profiles[:i], c.Profiles[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("profile %q not found", id)
}

// RenameProfile updates a profile's display name. ID stays stable so
// keyring lookups + persistence don't break.
func (c *Config) RenameProfile(id, newName string) error {
	for i := range c.Profiles {
		if c.Profiles[i].ID == id {
			c.Profiles[i].Name = newName
			return nil
		}
	}
	return fmt.Errorf("profile %q not found", id)
}

// slugify turns a display name into a lowercase id safe for use in
// keyring service names + JSON keys. Drops anything that isn't
// [a-z0-9_-]; collapses runs of dashes.
func slugify(name string) string {
	var b []rune
	prevDash := true
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b = append(b, r)
			prevDash = false
		case r >= 'A' && r <= 'Z':
			b = append(b, r+32)
			prevDash = false
		case r == '-' || r == ' ':
			if !prevDash {
				b = append(b, '-')
				prevDash = true
			}
		}
	}
	// Trim leading/trailing dashes.
	out := string(b)
	for len(out) > 0 && out[0] == '-' {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	return out
}

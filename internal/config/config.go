package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds all application configuration.
type Config struct {
	Twitch  TwitchConfig  `json:"twitch"`
	YouTube YouTubeConfig `json:"youtube"`
	UI      UIConfig      `json:"ui"`
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

// Load reads config from the default path.
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// Save writes config to the default path.
func (c *Config) Save() error {
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

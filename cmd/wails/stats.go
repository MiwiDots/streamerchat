package main

import (
	"fmt"
	"strings"

	"github.com/miwi/streamerchat/internal/chat"
	"github.com/miwi/streamerchat/internal/config"
)

// GetChatStats returns the stats dashboard data: totals since the last
// reset and the chat.StatsTopN most active chatters, without bots.
func (a *App) GetChatStats() chat.StatsSnapshot {
	ignored := a.statsIgnoreSet()
	bots := a.botDetector // set once in startup, read-only afterwards
	return a.stats.Snapshot(chat.StatsTopN, func(p chat.Platform, username string) bool {
		if ignored[chat.NormalizeChatterName(username)] {
			return true
		}
		// TwitchInsights' list covers bots the user hasn't listed. It
		// only knows Twitch logins, so YouTube names are never checked.
		return p == chat.PlatformTwitch && bots != nil && bots.IsBot(username)
	})
}

// ResetChatStats starts a fresh stats session (e.g. at stream start).
func (a *App) ResetChatStats() {
	a.stats.Reset()
}

// GetStatsIgnoredUsers returns the users hidden from the stats.
func (a *App) GetStatsIgnoredUsers() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil || len(a.cfg.UI.StatsIgnoredUsers) == 0 {
		return append([]string(nil), config.DefaultStatsIgnoredUsers...)
	}
	return append([]string(nil), a.cfg.UI.StatsIgnoredUsers...)
}

// SetStatsIgnoredUsers stores a comma/space separated list of usernames
// to hide from the stats. Empty input restores the default bot list.
// Returns "" or an error message.
func (a *App) SetStatsIgnoredUsers(spec string) string {
	names, err := parseIgnoredUsers(spec)
	if err != nil {
		return err.Error()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return "config not loaded"
	}
	a.cfg.UI.StatsIgnoredUsers = names
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	return ""
}

func (a *App) statsIgnoreSet() map[string]bool {
	names := a.GetStatsIgnoredUsers()
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// parseIgnoredUsers normalizes a user list: split on commas, semicolons
// and whitespace, lowercase, strip "@", drop duplicates, keep the order.
// Empty input returns nil (= defaults).
func parseIgnoredUsers(spec string) ([]string, error) {
	fields := strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		n := chat.NormalizeChatterName(f)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) > config.MaxStatsIgnoredUsers {
		return nil, fmt.Errorf("at most %d ignored users", config.MaxStatsIgnoredUsers)
	}
	return out, nil
}

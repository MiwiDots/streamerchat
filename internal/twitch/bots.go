package twitch

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// BotDetector checks if users are known Twitch bots.
type BotDetector struct {
	knownBots map[string]bool // lowercase username -> is bot
}

type botAPIResponse struct {
	Bots [][]interface{} `json:"bots"` // [username, channel_count, last_seen]
}

// NewBotDetector loads the known bot list from TwitchInsights API.
func NewBotDetector() *BotDetector {
	bd := &BotDetector{
		knownBots: make(map[string]bool),
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.twitchinsights.net/v1/bots/all")
	if err != nil {
		return bd
	}
	defer resp.Body.Close()

	var data botAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return bd
	}

	for _, bot := range data.Bots {
		if len(bot) > 0 {
			if name, ok := bot[0].(string); ok {
				bd.knownBots[strings.ToLower(name)] = true
			}
		}
	}

	return bd
}

// IsBot checks if a username is a known bot.
func (bd *BotDetector) IsBot(username string) bool {
	return bd.knownBots[strings.ToLower(username)]
}

// Count returns the number of known bots in the database.
func (bd *BotDetector) Count() int {
	return len(bd.knownBots)
}

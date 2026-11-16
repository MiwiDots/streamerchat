package twitch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// URL returns the CDN image URL (2x scale) of a 7TV / BTTV / FFZ emote.
func (e EmoteInfo) URL() string {
	switch e.Provider {
	case EmoteProvider7TV:
		return fmt.Sprintf("https://cdn.7tv.app/emote/%s/2x.webp", e.ID)
	case EmoteProviderBTTV:
		ext := "png"
		if e.Animated {
			ext = "gif"
		}
		return fmt.Sprintf("https://cdn.betterttv.net/emote/%s/2x.%s", e.ID, ext)
	case EmoteProviderFFZ:
		return fmt.Sprintf("https://cdn.frankerfacez.com/emote/%s/2", e.ID)
	}
	return ""
}

// PickerEmote is one emote in the emote picker.
type PickerEmote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// PickerSection groups emotes by source for the picker UI.
// Source: twitch | 7tv | bttv | ffz; Scope: channel | global.
type PickerSection struct {
	Source string        `json:"source"`
	Scope  string        `json:"scope"`
	Emotes []PickerEmote `json:"emotes"`
}

// twitchEmoteURL uses the "default" format, which serves the animated
// GIF for animated emotes and a PNG otherwise (verified against the CDN).
func twitchEmoteURL(id string) string {
	return "https://static-cdn.jtvnw.net/emoticons/v2/" + url.PathEscape(id) + "/default/dark/2.0"
}

// Cache lifetimes for Twitch's own emote lists (they rarely change).
const (
	twitchChannelEmotesTTL = 10 * time.Minute
	twitchGlobalEmotesTTL  = time.Hour
)

var emoteListHTTPClient = &http.Client{Timeout: 15 * time.Second}

type cachedEmotes struct {
	at     time.Time
	emotes []PickerEmote
}

// TwitchEmoteCache fetches and caches Twitch's channel and global emotes
// (Helix Get Channel Emotes / Get Global Emotes — no extra scope needed).
type TwitchEmoteCache struct {
	mu      sync.Mutex
	channel map[string]cachedEmotes
	global  cachedEmotes
}

// NewTwitchEmoteCache creates an empty cache.
func NewTwitchEmoteCache() *TwitchEmoteCache {
	return &TwitchEmoteCache{channel: map[string]cachedEmotes{}}
}

// Channel returns broadcasterID's Twitch emotes (cached).
func (c *TwitchEmoteCache) Channel(clientID, token, broadcasterID string) ([]PickerEmote, error) {
	if broadcasterID == "" {
		return nil, nil
	}
	c.mu.Lock()
	hit, ok := c.channel[broadcasterID]
	c.mu.Unlock()
	if ok && time.Since(hit.at) < twitchChannelEmotesTTL {
		return hit.emotes, nil
	}
	list, err := fetchTwitchEmotes(clientID, token, "chat/emotes?broadcaster_id="+url.QueryEscape(broadcasterID))
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.channel[broadcasterID] = cachedEmotes{at: time.Now(), emotes: list}
	c.mu.Unlock()
	return list, nil
}

// Global returns Twitch's global emotes (cached).
func (c *TwitchEmoteCache) Global(clientID, token string) ([]PickerEmote, error) {
	c.mu.Lock()
	hit := c.global
	c.mu.Unlock()
	if hit.emotes != nil && time.Since(hit.at) < twitchGlobalEmotesTTL {
		return hit.emotes, nil
	}
	list, err := fetchTwitchEmotes(clientID, token, "chat/emotes/global")
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.global = cachedEmotes{at: time.Now(), emotes: list}
	c.mu.Unlock()
	return list, nil
}

func fetchTwitchEmotes(clientID, token, endpoint string) ([]PickerEmote, error) {
	if clientID == "" || token == "" {
		return nil, fmt.Errorf("twitch emotes: not logged in")
	}
	req, err := http.NewRequest("GET", "https://api.twitch.tv/helix/"+endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Client-Id", clientID)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := emoteListHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("twitch emotes: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("twitch emotes: status %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("twitch emotes: %w", err)
	}
	out := make([]PickerEmote, 0, len(body.Data))
	for _, e := range body.Data {
		out = append(out, PickerEmote{Name: e.Name, URL: twitchEmoteURL(e.ID)})
	}
	sortPicker(out)
	return out, nil
}

// PickerSections lists the 7TV / BTTV / FFZ emotes usable in
// broadcasterID's chat (channel sets first, then globals), split by
// provider. Emotes a channel set overrides are not repeated globally.
func (tpe *ThirdPartyEmotes) PickerSections(broadcasterID string) []PickerSection {
	tpe.mu.RLock()
	defer tpe.mu.RUnlock()
	channel := tpe.channels[broadcasterID]
	var out []PickerSection
	add := func(scope string, set emoteSet, skip emoteSet) {
		byProvider := map[EmoteProvider][]PickerEmote{}
		for name, info := range set {
			if _, overridden := skip[name]; overridden {
				continue
			}
			byProvider[info.Provider] = append(byProvider[info.Provider], PickerEmote{Name: name, URL: info.URL()})
		}
		for _, p := range []EmoteProvider{EmoteProvider7TV, EmoteProviderBTTV, EmoteProviderFFZ} {
			if list := byProvider[p]; len(list) > 0 {
				sortPicker(list)
				out = append(out, PickerSection{Source: providerSource(p), Scope: scope, Emotes: list})
			}
		}
	}
	add("channel", channel, nil)
	add("global", tpe.global, channel)
	return out
}

func providerSource(p EmoteProvider) string {
	switch p {
	case EmoteProvider7TV:
		return "7tv"
	case EmoteProviderBTTV:
		return "bttv"
	case EmoteProviderFFZ:
		return "ffz"
	}
	return "other"
}

func sortPicker(list []PickerEmote) {
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
}

// BuildEmotePicker assembles the full picker for a channel: Twitch channel
// emotes, the channel's 7TV/BTTV/FFZ sets, the third-party globals and
// Twitch's global emotes. Twitch lists are skipped (not an error) when
// not logged in or Twitch is unreachable.
func BuildEmotePicker(cache *TwitchEmoteCache, tpe *ThirdPartyEmotes, clientID, token, broadcasterID string) []PickerSection {
	var out []PickerSection
	if list, err := cache.Channel(clientID, token, broadcasterID); err == nil && len(list) > 0 {
		out = append(out, PickerSection{Source: "twitch", Scope: "channel", Emotes: list})
	}
	if tpe != nil {
		out = append(out, tpe.PickerSections(broadcasterID)...)
	}
	if list, err := cache.Global(clientID, token); err == nil && len(list) > 0 {
		out = append(out, PickerSection{Source: "twitch", Scope: "global", Emotes: list})
	}
	return out
}

package twitch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// BadgeRegistry holds Twitch badge image URLs, segmented per channel so that
// e.g. `subscriber/24` from channel A doesn't overwrite `subscriber/24` from
// channel B (each broadcaster ships their own custom sub-tier artwork).
// Lookups fall back to global if the channel scope doesn't have a match.
type BadgeRegistry struct {
	mu        sync.RWMutex
	global    map[string]map[string]string            // setID -> version -> URL
	byChannel map[string]map[string]map[string]string // channelUserID -> setID -> version -> URL
}

var badgeHTTPClient = &http.Client{Timeout: 15 * time.Second}

// NewBadgeRegistry creates a new badge registry.
func NewBadgeRegistry() *BadgeRegistry {
	return &BadgeRegistry{
		global:    make(map[string]map[string]string),
		byChannel: make(map[string]map[string]map[string]string),
	}
}

// helixBadgeResponse matches the Helix /chat/badges* response format.
type helixBadgeResponse struct {
	Data []struct {
		SetID    string `json:"set_id"`
		Versions []struct {
			ID         string `json:"id"`
			ImageURL1x string `json:"image_url_1x"`
			ImageURL2x string `json:"image_url_2x"`
			ImageURL4x string `json:"image_url_4x"`
		} `json:"versions"`
	} `json:"data"`
}

// LoadGlobal fetches global Twitch badges via Helix.
// Requires a valid access token + client ID.
func (br *BadgeRegistry) LoadGlobal(clientID, accessToken string) error {
	data, err := br.fetch("https://api.twitch.tv/helix/chat/badges/global", clientID, accessToken)
	if err != nil {
		return err
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	br.store(br.global, data)
	return nil
}

// LoadChannel fetches channel-specific badges (sub badges with month tiers,
// custom badges) and stores them in a per-channel namespace so different
// channels' sub-tier artwork does not collide.
func (br *BadgeRegistry) LoadChannel(userID, clientID, accessToken string) error {
	if userID == "" {
		return nil
	}
	url := fmt.Sprintf("https://api.twitch.tv/helix/chat/badges?broadcaster_id=%s", userID)
	data, err := br.fetch(url, clientID, accessToken)
	if err != nil {
		return err
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.byChannel[userID] == nil {
		br.byChannel[userID] = make(map[string]map[string]string)
	}
	br.store(br.byChannel[userID], data)
	return nil
}

func (br *BadgeRegistry) fetch(url, clientID, accessToken string) (*helixBadgeResponse, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Client-Id", clientID)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := badgeHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var data helixBadgeResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (br *BadgeRegistry) store(target map[string]map[string]string, data *helixBadgeResponse) {
	for _, set := range data.Data {
		if target[set.SetID] == nil {
			target[set.SetID] = make(map[string]string)
		}
		for _, ver := range set.Versions {
			url := ver.ImageURL2x
			if url == "" {
				url = ver.ImageURL1x
			}
			target[set.SetID][ver.ID] = url
		}
	}
}

// Lookup returns the badge image URL for setID/version. If channelUserID is
// non-empty and that channel has its own version of the badge (typical for
// subscriber tier artwork), that wins; otherwise falls back to global.
func (br *BadgeRegistry) Lookup(channelUserID, setID, version string) string {
	br.mu.RLock()
	defer br.mu.RUnlock()
	if channelUserID != "" {
		if sets, ok := br.byChannel[channelUserID]; ok {
			if versions, ok := sets[setID]; ok {
				if url, ok := versions[version]; ok {
					return url
				}
			}
		}
	}
	if versions, ok := br.global[setID]; ok {
		if url, ok := versions[version]; ok {
			return url
		}
	}
	return ""
}

// Count returns the number of global badge sets loaded.
func (br *BadgeRegistry) Count() int {
	br.mu.RLock()
	defer br.mu.RUnlock()
	return len(br.global)
}

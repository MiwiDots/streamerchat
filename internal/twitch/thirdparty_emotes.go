package twitch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// EmoteProvider identifies which 3rd party emote service.
type EmoteProvider int

const (
	EmoteProviderNone EmoteProvider = iota
	EmoteProvider7TV
	EmoteProviderBTTV
	EmoteProviderFFZ
)

// EmoteInfo contains information about a single emote.
type EmoteInfo struct {
	Name     string
	Provider EmoteProvider
	ID       string
	Animated bool
}

// ThirdPartyEmotes holds emote data from 7TV, BTTV, and FFZ.
//
// Global emotes and per-channel emotes are kept apart. They used to share
// one flat name→emote map, so in chathub every watched channel's emotes
// rendered in every other channel, and parallel loads decided name
// collisions by whichever HTTP call finished last.
type ThirdPartyEmotes struct {
	mu       sync.RWMutex
	global   emoteSet
	channels map[string]emoteSet // broadcaster id -> channel emotes
}

type emoteSet map[string]EmoteInfo // name -> info

func (s emoteSet) add(info EmoteInfo) { s[info.Name] = info }

// NewThirdPartyEmotes creates a new emote registry.
func NewThirdPartyEmotes() *ThirdPartyEmotes {
	return &ThirdPartyEmotes{
		global:   emoteSet{},
		channels: make(map[string]emoteSet),
	}
}

// providerLoader fetches one provider's emotes for a scope.
type providerLoader func() emoteSet

// loadAll runs the loaders in parallel and merges the results in a FIXED
// precedence order (later wins): FFZ < BTTV < 7TV, so a name defined by
// several providers resolves the same way every time.
func loadAll(ffz, bttv, sevenTV providerLoader) emoteSet {
	results := make([]emoteSet, 3)
	var wg sync.WaitGroup
	for i, load := range []providerLoader{ffz, bttv, sevenTV} {
		wg.Add(1)
		go func(i int, load providerLoader) {
			defer wg.Done()
			results[i] = load()
		}(i, load)
	}
	wg.Wait()
	merged := emoteSet{}
	for _, set := range results {
		for name, info := range set {
			merged[name] = info
		}
	}
	return merged
}

// LoadGlobal (re)loads the global emote sets of all providers.
func (tpe *ThirdPartyEmotes) LoadGlobal() {
	set := loadAll(loadFFZGlobal, loadBTTVGlobal, load7TVGlobal)
	tpe.mu.Lock()
	tpe.global = set
	tpe.mu.Unlock()
}

// LoadChannel (re)loads the channel emotes of broadcasterID. A failed
// provider simply contributes nothing; existing data for the channel is
// replaced as a whole.
func (tpe *ThirdPartyEmotes) LoadChannel(broadcasterID string) {
	if broadcasterID == "" {
		return
	}
	set := loadAll(
		func() emoteSet { return loadFFZChannel(broadcasterID) },
		func() emoteSet { return loadBTTVChannel(broadcasterID) },
		func() emoteSet { return load7TVChannel(broadcasterID) },
	)
	tpe.mu.Lock()
	tpe.channels[broadcasterID] = set
	tpe.mu.Unlock()
}

// Load fetches global emotes and, if broadcasterID is set, that channel's
// emotes.
func (tpe *ThirdPartyEmotes) Load(broadcasterID string) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); tpe.LoadGlobal() }()
	if broadcasterID != "" {
		wg.Add(1)
		go func() { defer wg.Done(); tpe.LoadChannel(broadcasterID) }()
	}
	wg.Wait()
}

// Lookup returns a GLOBAL emote by name.
func (tpe *ThirdPartyEmotes) Lookup(name string) (EmoteInfo, bool) {
	return tpe.LookupIn("", name)
}

// LookupIn returns the emote `name` as seen in broadcasterID's chat:
// channel emotes win over global ones. Empty broadcasterID = global only.
func (tpe *ThirdPartyEmotes) LookupIn(broadcasterID, name string) (EmoteInfo, bool) {
	tpe.mu.RLock()
	defer tpe.mu.RUnlock()
	if broadcasterID != "" {
		if info, ok := tpe.channels[broadcasterID][name]; ok {
			return info, true
		}
	}
	info, ok := tpe.global[name]
	return info, ok
}

// HasChannel reports whether emotes for broadcasterID have been loaded.
func (tpe *ThirdPartyEmotes) HasChannel(broadcasterID string) bool {
	tpe.mu.RLock()
	defer tpe.mu.RUnlock()
	_, ok := tpe.channels[broadcasterID]
	return ok
}

// Count returns the number of loaded global emotes plus all channel emotes.
func (tpe *ThirdPartyEmotes) Count() int {
	tpe.mu.RLock()
	defer tpe.mu.RUnlock()
	n := len(tpe.global)
	for _, set := range tpe.channels {
		n += len(set)
	}
	return n
}

// HTTP helper
var emoteHTTPClient = &http.Client{Timeout: 15 * time.Second}

func fetchJSON(url string, target interface{}) error {
	resp, err := emoteHTTPClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

// ========== 7TV ==========

type sevenTVResponse struct {
	Emotes []sevenTVEmote `json:"emotes"`
}

type sevenTVUser struct {
	EmoteSet sevenTVResponse `json:"emote_set"`
}

type sevenTVEmote struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Data struct {
		Animated bool `json:"animated"`
	} `json:"data"`
}

func load7TVGlobal() emoteSet {
	set := emoteSet{}
	var resp sevenTVResponse
	if err := fetchJSON("https://7tv.io/v3/emote-sets/global", &resp); err != nil {
		return set
	}
	for _, e := range resp.Emotes {
		set.add(EmoteInfo{
			Name:     e.Name,
			Provider: EmoteProvider7TV,
			ID:       e.ID,
			Animated: e.Data.Animated,
		})
	}
	return set
}

func load7TVChannel(userID string) emoteSet {
	set := emoteSet{}
	var user sevenTVUser
	url := fmt.Sprintf("https://7tv.io/v3/users/twitch/%s", userID)
	if err := fetchJSON(url, &user); err != nil {
		return set
	}
	for _, e := range user.EmoteSet.Emotes {
		set.add(EmoteInfo{
			Name:     e.Name,
			Provider: EmoteProvider7TV,
			ID:       e.ID,
			Animated: e.Data.Animated,
		})
	}
	return set
}

// ========== BTTV ==========

type bttvEmote struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	ImageType string `json:"imageType"`
}

type bttvUser struct {
	ChannelEmotes []bttvEmote `json:"channelEmotes"`
	SharedEmotes  []bttvEmote `json:"sharedEmotes"`
}

func loadBTTVGlobal() emoteSet {
	set := emoteSet{}
	var emotes []bttvEmote
	if err := fetchJSON("https://api.betterttv.net/3/cached/emotes/global", &emotes); err != nil {
		return set
	}
	for _, e := range emotes {
		set.add(EmoteInfo{
			Name:     e.Code,
			Provider: EmoteProviderBTTV,
			ID:       e.ID,
			Animated: e.ImageType == "gif",
		})
	}
	return set
}

func loadBTTVChannel(userID string) emoteSet {
	set := emoteSet{}
	var user bttvUser
	url := fmt.Sprintf("https://api.betterttv.net/3/cached/users/twitch/%s", userID)
	if err := fetchJSON(url, &user); err != nil {
		return set
	}
	for _, e := range user.ChannelEmotes {
		set.add(EmoteInfo{
			Name:     e.Code,
			Provider: EmoteProviderBTTV,
			ID:       e.ID,
			Animated: e.ImageType == "gif",
		})
	}
	for _, e := range user.SharedEmotes {
		set.add(EmoteInfo{
			Name:     e.Code,
			Provider: EmoteProviderBTTV,
			ID:       e.ID,
			Animated: e.ImageType == "gif",
		})
	}
	return set
}

// ========== FFZ ==========

type ffzGlobalResponse struct {
	DefaultSets []int                  `json:"default_sets"`
	Sets        map[string]ffzEmoteSet `json:"sets"`
}

type ffzRoomResponse struct {
	Room struct {
		Set int `json:"set"`
	} `json:"room"`
	Sets map[string]ffzEmoteSet `json:"sets"`
}

type ffzEmoteSet struct {
	Emoticons []ffzEmote `json:"emoticons"`
}

type ffzEmote struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// loadFFZGlobal loads only FFZ's default_sets. /set/global also returns
// restricted sets (e.g. for specific users/add-ons) that are not visible to
// everyone; those used to render for all chatters.
func loadFFZGlobal() emoteSet {
	set := emoteSet{}
	var resp ffzGlobalResponse
	if err := fetchJSON("https://api.frankerfacez.com/v1/set/global", &resp); err != nil {
		return set
	}
	for _, id := range resp.DefaultSets {
		ffzSet, ok := resp.Sets[fmt.Sprintf("%d", id)]
		if !ok {
			continue
		}
		for _, e := range ffzSet.Emoticons {
			set.add(EmoteInfo{
				Name:     e.Name,
				Provider: EmoteProviderFFZ,
				ID:       fmt.Sprintf("%d", e.ID),
			})
		}
	}
	return set
}

func loadFFZChannel(userID string) emoteSet {
	set := emoteSet{}
	var resp ffzRoomResponse
	url := fmt.Sprintf("https://api.frankerfacez.com/v1/room/id/%s", userID)
	if err := fetchJSON(url, &resp); err != nil {
		return set
	}
	for _, ffzSet := range resp.Sets {
		for _, e := range ffzSet.Emoticons {
			set.add(EmoteInfo{
				Name:     e.Name,
				Provider: EmoteProviderFFZ,
				ID:       fmt.Sprintf("%d", e.ID),
			})
		}
	}
	return set
}

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
type ThirdPartyEmotes struct {
	mu     sync.RWMutex
	emotes map[string]EmoteInfo // name -> info
}

// NewThirdPartyEmotes creates a new emote registry.
func NewThirdPartyEmotes() *ThirdPartyEmotes {
	return &ThirdPartyEmotes{
		emotes: make(map[string]EmoteInfo),
	}
}

// Load fetches global emotes and channel-specific emotes for the given broadcaster.
func (tpe *ThirdPartyEmotes) Load(broadcasterID string) {
	var wg sync.WaitGroup

	// 7TV Global
	wg.Add(1)
	go func() {
		defer wg.Done()
		tpe.load7TVGlobal()
	}()

	// 7TV Channel
	if broadcasterID != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tpe.load7TVChannel(broadcasterID)
		}()
	}

	// BTTV Global
	wg.Add(1)
	go func() {
		defer wg.Done()
		tpe.loadBTTVGlobal()
	}()

	// BTTV Channel
	if broadcasterID != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tpe.loadBTTVChannel(broadcasterID)
		}()
	}

	// FFZ Global
	wg.Add(1)
	go func() {
		defer wg.Done()
		tpe.loadFFZGlobal()
	}()

	// FFZ Channel
	if broadcasterID != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tpe.loadFFZChannel(broadcasterID)
		}()
	}

	wg.Wait()
}

// Lookup returns emote info for a name, or nil if not found.
func (tpe *ThirdPartyEmotes) Lookup(name string) (EmoteInfo, bool) {
	tpe.mu.RLock()
	defer tpe.mu.RUnlock()
	info, ok := tpe.emotes[name]
	return info, ok
}

// Count returns the total number of loaded emotes.
func (tpe *ThirdPartyEmotes) Count() int {
	tpe.mu.RLock()
	defer tpe.mu.RUnlock()
	return len(tpe.emotes)
}

func (tpe *ThirdPartyEmotes) add(info EmoteInfo) {
	tpe.mu.Lock()
	tpe.emotes[info.Name] = info
	tpe.mu.Unlock()
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

func (tpe *ThirdPartyEmotes) load7TVGlobal() {
	var resp sevenTVResponse
	if err := fetchJSON("https://7tv.io/v3/emote-sets/global", &resp); err != nil {
		return
	}
	for _, e := range resp.Emotes {
		tpe.add(EmoteInfo{
			Name:     e.Name,
			Provider: EmoteProvider7TV,
			ID:       e.ID,
			Animated: e.Data.Animated,
		})
	}
}

func (tpe *ThirdPartyEmotes) load7TVChannel(userID string) {
	var user sevenTVUser
	url := fmt.Sprintf("https://7tv.io/v3/users/twitch/%s", userID)
	if err := fetchJSON(url, &user); err != nil {
		return
	}
	for _, e := range user.EmoteSet.Emotes {
		tpe.add(EmoteInfo{
			Name:     e.Name,
			Provider: EmoteProvider7TV,
			ID:       e.ID,
			Animated: e.Data.Animated,
		})
	}
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

func (tpe *ThirdPartyEmotes) loadBTTVGlobal() {
	var emotes []bttvEmote
	if err := fetchJSON("https://api.betterttv.net/3/cached/emotes/global", &emotes); err != nil {
		return
	}
	for _, e := range emotes {
		tpe.add(EmoteInfo{
			Name:     e.Code,
			Provider: EmoteProviderBTTV,
			ID:       e.ID,
			Animated: e.ImageType == "gif",
		})
	}
}

func (tpe *ThirdPartyEmotes) loadBTTVChannel(userID string) {
	var user bttvUser
	url := fmt.Sprintf("https://api.betterttv.net/3/cached/users/twitch/%s", userID)
	if err := fetchJSON(url, &user); err != nil {
		return
	}
	for _, e := range user.ChannelEmotes {
		tpe.add(EmoteInfo{
			Name:     e.Code,
			Provider: EmoteProviderBTTV,
			ID:       e.ID,
			Animated: e.ImageType == "gif",
		})
	}
	for _, e := range user.SharedEmotes {
		tpe.add(EmoteInfo{
			Name:     e.Code,
			Provider: EmoteProviderBTTV,
			ID:       e.ID,
			Animated: e.ImageType == "gif",
		})
	}
}

// ========== FFZ ==========

type ffzGlobalResponse struct {
	DefaultSets []int                 `json:"default_sets"`
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

func (tpe *ThirdPartyEmotes) loadFFZGlobal() {
	var resp ffzGlobalResponse
	if err := fetchJSON("https://api.frankerfacez.com/v1/set/global", &resp); err != nil {
		return
	}
	for _, set := range resp.Sets {
		for _, e := range set.Emoticons {
			tpe.add(EmoteInfo{
				Name:     e.Name,
				Provider: EmoteProviderFFZ,
				ID:       fmt.Sprintf("%d", e.ID),
			})
		}
	}
}

func (tpe *ThirdPartyEmotes) loadFFZChannel(userID string) {
	var resp ffzRoomResponse
	url := fmt.Sprintf("https://api.frankerfacez.com/v1/room/id/%s", userID)
	if err := fetchJSON(url, &resp); err != nil {
		return
	}
	for _, set := range resp.Sets {
		for _, e := range set.Emoticons {
			tpe.add(EmoteInfo{
				Name:     e.Name,
				Provider: EmoteProviderFFZ,
				ID:       fmt.Sprintf("%d", e.ID),
			})
		}
	}
}

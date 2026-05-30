package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/miwi/streamerchat/internal/chat"
	"github.com/miwi/streamerchat/internal/selfupdate"
	"github.com/miwi/streamerchat/internal/sevenTV"
	"github.com/miwi/streamerchat/internal/twitch"
	"github.com/miwi/streamerchat/internal/version"
	"github.com/miwi/streamerchat/internal/youtube"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Twitch Client ID for the device-code OAuth flow. Client IDs in
// public OAuth apps are not secrets — only client_secret is, and we
// never use one (device-code flow doesn't need it). Baked in so
// fresh installs without a config can log in out of the box.
// Can still be overridden at build time via -ldflags "-X main.defaultClientID=...".
var defaultClientID = "opez5azi81po1xb4hb581ikw3xo04y"

type HubConfig struct {
	Channels       []string `json:"channels"`
	Highlights     []string `json:"highlights"`
	Theme          string   `json:"theme"`
	OnlyShowLive   bool     `json:"only_show_live"`
	Locale         string   `json:"locale"`
	NotifSound     string   `json:"notif_sound"`
	// HideTimestamps stores the inverse so that the zero value (false)
	// keeps the historic default of "timestamps visible". A simple
	// ShowTimestamps would silently invert for users who upgrade.
	HideTimestamps bool `json:"hide_timestamps"`

	// UpdateChannel selects which GitHub release stream the self-updater
	// follows. Empty / "stable" → /releases/latest (skips prereleases).
	// "beta" → newest release that isn't tagged -alpha. "alpha" → newest
	// release at all (including -alpha prereleases).
	UpdateChannel string `json:"update_channel"`

	// FontSize is the user-chosen chat font size in pixels (10–22).
	// Zero / unset = 14 (the historic default). Applied as a CSS var
	// on :root so all chat messages scale together.
	FontSize int `json:"font_size"`

	// Optional Twitch auth for sending messages
	ClientID     string `json:"client_id"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Username     string `json:"username"`
	UserID       string `json:"user_id"`
}

func loadHubConfig() *HubConfig {
	cfg := &HubConfig{Channels: []string{}, Theme: "dark"}
	path := hubConfigPath()
	log.Printf("[CONFIG] Loading from: %s", path)
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			log.Printf("[CONFIG] Parse error: %v", err)
		} else {
			log.Printf("[CONFIG] Loaded %d channels, %d highlights", len(cfg.Channels), len(cfg.Highlights))
		}
	} else {
		log.Printf("[CONFIG] Not found (will create on save): %v", err)
	}
	return cfg
}

func (c *HubConfig) Save() error {
	path := hubConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		log.Printf("[CONFIG] MkdirAll failed: %v", err)
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		log.Printf("[CONFIG] Marshal failed: %v", err)
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("[CONFIG] WriteFile failed: %v (path=%s)", err, path)
		return err
	}
	log.Printf("[CONFIG] Saved %d channels to %s", len(c.Channels), path)
	return nil
}

func hubConfigPath() string {
	if appData := os.Getenv("APPDATA"); appData != "" {
		return filepath.Join(appData, "chathub", "config.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "chathub", "config.json")
}

// ChannelRef is the parsed form of an entry in HubConfig.Channels. The
// stored format is a plain string for backwards compatibility:
//   - "miwitv"          -> Twitch channel "miwitv"
//   - "yt:@DEmiwitv"    -> YouTube channel handle "@DEmiwitv"
//   - "yt:UCxxxxxxxxxx" -> YouTube channel by UC… ID
// Twitch is the default platform when no prefix is present.
type ChannelRef struct {
	Platform string // "twitch" | "youtube"
	Name     string // login (twitch) or handle/UC-id (youtube)
}

func parseChannelRef(s string) ChannelRef {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ":"); i > 0 && i < 6 {
		prefix := strings.ToLower(s[:i])
		rest := strings.TrimPrefix(strings.TrimSpace(s[i+1:]), "#")
		switch prefix {
		case "yt", "youtube":
			return ChannelRef{Platform: "youtube", Name: rest}
		case "tw", "twitch":
			return ChannelRef{Platform: "twitch", Name: strings.ToLower(rest)}
		}
	}
	return ChannelRef{Platform: "twitch", Name: strings.ToLower(strings.TrimPrefix(s, "#"))}
}

func (c ChannelRef) String() string {
	if c.Platform == "youtube" {
		return "yt:" + c.Name
	}
	return c.Name
}

type App struct {
	ctx    context.Context
	cfg    *HubConfig
	mu     sync.Mutex
	irc    *twitch.MultiIRCClient
	send   *twitch.IRCClient
	emotes      *twitch.ThirdPartyEmotes
	badges      *twitch.BadgeRegistry
	botDetector *twitch.BotDetector
	history     *HistoryWriter

	channelEmotesLoaded map[string]bool
	channelIDCache      map[string]string
	liveStatus          map[string]bool

	// YouTube watchers, keyed by channel handle / UC-id. Each watcher
	// runs a LiveDetector + (when live) an InnerTube chat poller. The
	// cancel func tears down both.
	ytWatchers map[string]context.CancelFunc
	// Per-channel reference to the active InnerTube client so we can
	// pull the live stream's SendParams when the user posts. Keyed
	// by the same tab key ("yt:<handle>") emitMessage uses.
	ytClients map[string]*youtube.InnerTubeClient

	// 7TV cosmetics: a single WebSocket connection subscribes to
	// entitlement.* events for every Twitch channel we know the
	// broadcaster id of, and pushes paint/badge updates down to the
	// frontend so chat messages can render them.
	stv *sevenTV.Client
}

func NewApp() *App {
	// HistoryWriter is initialized HERE rather than later in startup() so
	// it's never nil by the time the Wails bindings are reachable. The
	// frontend's setupEvents() pulls GetInitialState → GetHistory on every
	// configured channel within milliseconds of the runtime being ready,
	// and previously that all raced past the startup-time assignment and
	// returned nil — leaving the chat view permanently empty on restart.
	return &App{
		channelEmotesLoaded: make(map[string]bool),
		channelIDCache:      make(map[string]string),
		liveStatus:          make(map[string]bool),
		ytWatchers:          make(map[string]context.CancelFunc),
		ytClients:           make(map[string]*youtube.InnerTubeClient),
		history:             NewHistoryWriter(),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	logPath := "chathub.log"
	if exe, err := os.Executable(); err == nil {
		logPath = filepath.Join(filepath.Dir(exe), "chathub.log")
	}
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
		log.SetOutput(f)
		log.Printf("[BOOT] ChatHub v%s started, log: %s", version.Version, logPath)
	}

	// Remove leftover <exe>.old from a previous self-update.
	selfupdate.CleanupPrevious()

	// Keep the YouTube cookie session warm in the background. One refresh
	// ~30s after boot validates the saved session and folds in any
	// Set-Cookie updates the YT home page hands back, then a 24h ticker
	// repeats. If Google ever rotates SAPISID server-side we'll pick that
	// up on the next tick instead of waiting for the user to discover a
	// dead send.
	youtube.StartAutoRefresh(ctx, 24*time.Hour)

	a.cfg = loadHubConfig()
	// Twitch access + refresh tokens have moved from config.json into the
	// OS keyring. This call either hydrates them from the keyring if a
	// previous install already migrated, or migrates them in-place from
	// the old config.json fields and wipes them from disk.
	a.migrateTwitchTokensFromConfig()
	log.Printf("[BOOT] Loaded config: channels=%v username=%q tokenLen=%d",
		a.cfg.Channels, a.cfg.Username, len(a.cfg.AccessToken))

	// Set built-in client ID if missing
	if a.cfg.ClientID == "" && defaultClientID != "" {
		a.cfg.ClientID = defaultClientID
		a.cfg.Save()
	}

	a.emotes = twitch.NewThirdPartyEmotes()
	go a.emotes.Load("")

	a.badges = twitch.NewBadgeRegistry()
	go a.loadGlobalBadges()

	// Load the TwitchInsights known-bot list once at boot. The constructor
	// does the HTTP call itself; if it fails we just get an empty detector
	// and nothing gets flagged as a bot.
	a.botDetector = twitch.NewBotDetector()

	// a.history was already initialized in NewApp(). Just log the dir so
	// the startup banner mentions where messages persist.
	log.Printf("[HISTORY] log dir: %s", a.history.HistoryDir())

	a.irc = twitch.NewMultiIRCClient()
	a.irc.OnMessage(func(channel string, msg chat.Message) {
		a.emitMessage(channel, msg)
	})
	a.irc.OnJoinPart(func(channel string, jp chat.UserJoinPart) {
		runtime.EventsEmit(a.ctx, "joinPart", map[string]interface{}{
			"channel":  channel,
			"username": jp.Username,
			"isJoin":   jp.IsJoin,
		})
	})
	a.irc.OnSettings(func(channel string, s chat.ChatSettings) {
		runtime.EventsEmit(a.ctx, "settings", map[string]interface{}{
			"channel":  channel,
			"settings": s,
		})
	})
	a.irc.OnSystem(func(channel string, text string) {
		a.emitMessage(channel, chat.Message{
			Platform:  chat.PlatformTwitch,
			Type:      chat.MessageTypeSystem,
			Channel:   channel,
			Timestamp: time.Now(),
			Text:      text,
		})
	})

	go a.irc.Connect(ctx)

	go func() {
		time.Sleep(500 * time.Millisecond)
		for _, raw := range a.cfg.Channels {
			ref := parseChannelRef(raw)
			switch ref.Platform {
			case "youtube":
				a.startYouTubeWatcher(ref.Name)
			default:
				a.irc.JoinChannel(ref.Name)
				go a.loadChannelEmotes(ref.Name)
			}
		}
	}()

	// Start authenticated client if we have a token
	if a.cfg.AccessToken != "" && a.cfg.Username != "" {
		go a.validateAndConnect()
	}

	// 7TV cosmetics: one WS connection, channels subscribed lazily as
	// we resolve their broadcaster ids in resolveChannelID().
	a.stv = sevenTV.NewClient(ctx, func(c sevenTV.Cosmetic) {
		runtime.EventsEmit(a.ctx, "sevenTVCosmetic", map[string]interface{}{
			"userID":    c.UserID,
			"paintCSS":  c.PaintCSS,
			"badgeURL":  c.BadgeURL,
			"badgeName": c.BadgeName,
		})
	})
	a.stv.Start()

	// Live status poller (every 60s)
	go a.liveStatusLoop()

	// Token auto-refresh (every hour, like StreamerChat)
	go a.tokenRefreshLoop()

	runtime.EventsEmit(a.ctx, "ready", map[string]interface{}{
		"channels":     a.cfg.Channels,
		"highlights":   a.cfg.Highlights,
		"theme":        a.cfg.Theme,
		"onlyShowLive": a.cfg.OnlyShowLive,
		"loggedIn":     a.cfg.AccessToken != "",
		"username":     a.cfg.Username,
		"configPath":   hubConfigPath(),
		"locale":       a.cfg.Locale,
		"notifSound":   a.cfg.NotifSound,
		"showTimestamps": !a.cfg.HideTimestamps,
	})
}

// validateAndConnect validates the saved token, refreshes if needed, then connects.
// Resilient: only logs out if we get an explicit "token expired" error from Twitch.
// Network errors / timeouts keep the saved login state.
func (a *App) validateAndConnect() {
	if a.cfg.AccessToken == "" {
		log.Printf("[AUTH] No access token in config")
		return
	}
	log.Printf("[AUTH] Validating saved token for user=%s", a.cfg.Username)
	info, err := twitch.ValidateToken(a.cfg.AccessToken)
	if err == nil {
		log.Printf("[AUTH] Token valid for %s, proactively refreshing for the session", info.Login)
		if info.Login != "" {
			a.cfg.Username = info.Login
			a.cfg.UserID = info.UserID
			a.cfg.Save()
		}
		// Even when the token validates we kick off one refresh up-front so
		// the new session starts with a token that won't expire mid-use just
		// because the user opened the app right before its TTL elapsed.
		// Failure here is non-fatal — the existing token is still usable.
		if a.cfg.RefreshToken != "" {
			_ = a.refreshTokenSync()
		}
		a.connectSendClient()
		return
	}

	// Only attempt refresh on explicit 401 expired errors
	errStr := err.Error()
	isExpired := strings.Contains(errStr, "401") || strings.Contains(strings.ToLower(errStr), "expired") || strings.Contains(strings.ToLower(errStr), "invalid")

	if !isExpired {
		log.Printf("[AUTH] Validate failed (network/server?), keeping saved login: %v", err)
		// Try to connect anyway - the IRC supervisor will handle real auth failures
		a.connectSendClient()
		return
	}

	log.Printf("[AUTH] Token expired, refreshing...")
	if !a.refreshTokenSync() {
		log.Printf("[AUTH] Refresh failed, user must re-login")
		runtime.EventsEmit(a.ctx, "authExpired", nil)
		return
	}

	// Verify the new token
	if newInfo, vErr := twitch.ValidateToken(a.cfg.AccessToken); vErr == nil {
		a.cfg.Username = newInfo.Login
		a.cfg.UserID = newInfo.UserID
		a.cfg.Save()
		log.Printf("[AUTH] Re-authenticated as %s", newInfo.Login)
	}
	a.connectSendClient()
}

// refreshTokenSync refreshes the access token using the refresh token.
// Returns true on success.
func (a *App) refreshTokenSync() bool {
	if a.cfg.RefreshToken == "" || a.cfg.ClientID == "" {
		return false
	}
	token, err := twitch.RefreshAccessToken(a.cfg.ClientID, "", a.cfg.RefreshToken)
	if err != nil {
		log.Printf("[AUTH] Refresh error: %v", err)
		return false
	}
	a.cfg.AccessToken = token.AccessToken
	a.cfg.RefreshToken = token.RefreshToken
	a.cfg.Save()
	a.saveTwitchTokens()
	log.Printf("[AUTH] Token refreshed successfully")
	return true
}

// tokenRefreshLoop refreshes the access token every 30 minutes. Twitch's
// access tokens can live as little as ~1h (and sometimes less), so a 60min
// cadence used to leave a window where the token expired between ticks and
// the user got disconnected. 30min gives a comfortable safety margin while
// staying well below any rate-limit ceiling.
func (a *App) tokenRefreshLoop() {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
		if a.cfg.AccessToken == "" || a.cfg.RefreshToken == "" {
			continue
		}
		if a.refreshTokenSync() {
			// Reconnect send client with fresh token
			a.mu.Lock()
			if a.send != nil {
				a.send.Disconnect()
				a.send = nil
			}
			a.mu.Unlock()
			a.connectSendClient()
		}
	}
}

// connectSendClient creates an authenticated IRC client that joins all
// tracked channels so we can send to any of them.
func (a *App) connectSendClient() {
	a.mu.Lock()
	if a.send != nil {
		a.send.Disconnect()
	}
	// Filter to Twitch-only channels — YouTube/other-platform entries in
	// cfg.Channels would otherwise poison the IRC JOIN list and silently
	// break sending to every channel.
	var twitchChans []string
	for _, raw := range a.cfg.Channels {
		if parseChannelRef(raw).Platform == "twitch" {
			twitchChans = append(twitchChans, parseChannelRef(raw).Name)
		}
	}
	if len(twitchChans) == 0 {
		a.send = nil
		a.mu.Unlock()
		return
	}
	primary := twitchChans[0]
	send := twitch.NewIRCClient(a.cfg.Username, a.cfg.AccessToken, primary, twitchChans[1:]...)
	a.send = send
	a.mu.Unlock()

	log.Printf("[SEND] connecting IRC for user=%s, channels=%v", a.cfg.Username, twitchChans)
	go func() {
		// gempir's Connect() blocks and auto-reconnects internally. If it
		// returns, the supervision is over (intentional Disconnect or
		// terminal failure) and a.send is now a zombie. Null it out so
		// the next SendMessage triggers a lazy reconnect via the path in
		// SendMessage itself.
		if err := send.Connect(); err != nil {
			log.Printf("[SEND] connect returned with error: %v — clearing zombie sender", err)
		} else {
			log.Printf("[SEND] connect returned cleanly — clearing zombie sender")
		}
		a.mu.Lock()
		if a.send == send {
			a.send = nil
		}
		a.mu.Unlock()
	}()
}

// liveStatusLoop polls Twitch every 60s for live status of all channels.
// YouTube (and any other non-Twitch) channels are skipped — their live
// state is owned by the per-channel LiveDetector and would otherwise be
// overwritten with `false` here because Helix /streams returns 400 for a
// "yt:@..." login, wiping the live indicator the detector just set.
func (a *App) liveStatusLoop() {
	check := func() {
		a.mu.Lock()
		chans := append([]string{}, a.cfg.Channels...)
		a.mu.Unlock()

		for _, raw := range chans {
			ref := parseChannelRef(raw)
			if ref.Platform != "twitch" {
				continue
			}
			ch := ref.Name
			isLive := a.checkLive(ch)
			a.mu.Lock()
			prev, had := a.liveStatus[ch]
			a.liveStatus[ch] = isLive
			a.mu.Unlock()
			if !had || prev != isLive {
				runtime.EventsEmit(a.ctx, "liveStatus", map[string]interface{}{
					"channel": ch,
					"live":    isLive,
				})
			}
		}
	}

	check()
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

// checkLive uses official Helix /streams endpoint. Requires user access token.
func (a *App) checkLive(channel string) bool {
	info := a.fetchStreamInfo(channel)
	return info != nil && info["live"] == true
}

// fetchStreamInfo returns nil on error / not-logged-in, otherwise a map with
// the full Helix /streams response fields the frontend needs to render a
// "channel is live: <title> · <game> · <viewers> · <uptime>" banner.
// {live, title, gameName, viewerCount, startedAt, thumbnailUrl} — empty
// values (e.g. live=false) when the channel is offline.
func (a *App) fetchStreamInfo(channel string) map[string]interface{} {
	if a.cfg.AccessToken == "" || a.cfg.ClientID == "" {
		return map[string]interface{}{"live": false}
	}
	req, err := http.NewRequest("GET", fmt.Sprintf("https://api.twitch.tv/helix/streams?user_login=%s", channel), nil)
	if err != nil {
		log.Printf("[LIVE] %s: request build error: %v", channel, err)
		return nil
	}
	req.Header.Set("Client-Id", a.cfg.ClientID)
	req.Header.Set("Authorization", "Bearer "+a.cfg.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[LIVE] %s: http error: %v", channel, err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("[LIVE] %s: status=%d", channel, resp.StatusCode)
		return nil
	}
	var data struct {
		Data []struct {
			ID           string `json:"id"`
			UserName     string `json:"user_name"`
			GameName     string `json:"game_name"`
			Title        string `json:"title"`
			ViewerCount  int    `json:"viewer_count"`
			StartedAt    string `json:"started_at"`
			ThumbnailURL string `json:"thumbnail_url"`
			Language     string `json:"language"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("[LIVE] %s: decode error: %v", channel, err)
		return nil
	}
	if len(data.Data) == 0 {
		log.Printf("[LIVE] %s: live=false", channel)
		return map[string]interface{}{"live": false}
	}
	s := data.Data[0]
	log.Printf("[LIVE] %s: live=true viewers=%d game=%q", channel, s.ViewerCount, s.GameName)
	return map[string]interface{}{
		"live":         true,
		"title":        s.Title,
		"gameName":     s.GameName,
		"viewerCount":  s.ViewerCount,
		"startedAt":    s.StartedAt,
		"thumbnailUrl": s.ThumbnailURL,
		"language":     s.Language,
	}
}

// GetChannelChatters tries Helix /chat/chatters to fetch the full live
// viewer list for the given channel. The endpoint requires that the
// authenticated user is the broadcaster or a moderator of the channel
// (moderator:read:chatters scope) — for any other channel Twitch returns
// 401 / 403 and we just return an empty list so the frontend falls back
// to the locally-tracked chatter list.
func (a *App) GetChannelChatters(channel string) map[string]interface{} {
	out := map[string]interface{}{"users": []string{}, "source": "none"}
	if a.cfg.AccessToken == "" || a.cfg.ClientID == "" || a.cfg.UserID == "" {
		return out
	}
	channel = strings.ToLower(strings.TrimPrefix(channel, "#"))
	broadcasterID := a.resolveChannelID(channel)
	if broadcasterID == "" {
		return out
	}
	url := fmt.Sprintf("https://api.twitch.tv/helix/chat/chatters?broadcaster_id=%s&moderator_id=%s&first=1000",
		broadcasterID, a.cfg.UserID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return out
	}
	req.Header.Set("Client-Id", a.cfg.ClientID)
	req.Header.Set("Authorization", "Bearer "+a.cfg.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[CHATTERS] %s: http error: %v", channel, err)
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		// 401/403 just means we're not a mod of that channel; not an error.
		log.Printf("[CHATTERS] %s: status=%d (not a mod of this channel?)", channel, resp.StatusCode)
		return out
	}
	var data struct {
		Data []struct {
			UserLogin string `json:"user_login"`
		} `json:"data"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("[CHATTERS] %s: decode error: %v", channel, err)
		return out
	}
	users := make([]map[string]interface{}, 0, len(data.Data))
	logins := make([]string, 0, len(data.Data))
	for _, c := range data.Data {
		if c.UserLogin == "" {
			continue
		}
		isBot := false
		if a.botDetector != nil {
			isBot = a.botDetector.IsBot(c.UserLogin)
		}
		users = append(users, map[string]interface{}{
			"login": c.UserLogin,
			"isBot": isBot,
		})
		logins = append(logins, c.UserLogin)
	}
	log.Printf("[CHATTERS] %s: helix returned %d (total=%d)", channel, len(logins), data.Total)
	// `users` is the new richer shape; `logins` kept for backwards
	// compatibility with the v0.2.24 frontend during rolling updates.
	out["users"] = logins
	out["chatters"] = users
	out["total"] = data.Total
	out["source"] = "helix"
	return out
}

// GetStreamInfo is a frontend-bound version of fetchStreamInfo so the UI
// can refresh the active channel's meta bar on tab-switch and periodically.
func (a *App) GetStreamInfo(channel string) map[string]interface{} {
	if channel == "" {
		return map[string]interface{}{"live": false}
	}
	info := a.fetchStreamInfo(strings.ToLower(strings.TrimPrefix(channel, "#")))
	if info == nil {
		return map[string]interface{}{"live": false, "error": "fetch failed"}
	}
	return info
}

func (a *App) shutdown(_ context.Context) {
	if a.history != nil {
		a.history.Close()
	}
}

// GetHistory returns the last messages for a channel (called by frontend on tab open).
func (a *App) GetHistory(channel string, limit int) []map[string]interface{} {
	log.Printf("[HISTORY] GetHistory called for channel=%q limit=%d", channel, limit)
	if a.history == nil {
		log.Printf("[HISTORY] GetHistory bailing — a.history nil")
		return nil
	}
	if limit <= 0 || limit > 1000 {
		limit = MaxHistoryLines
	}
	return a.history.Load(channel, limit)
}

// ClearHistory deletes the log file for a channel.
func (a *App) ClearHistory(channel string) string {
	if a.history == nil {
		return ""
	}
	if err := a.history.Clear(channel); err != nil {
		return err.Error()
	}
	return ""
}

func (a *App) emitMessage(channel string, msg chat.Message) {
	if a.ctx == nil {
		return
	}
	m := a.chatToMap(channel, msg)
	// Persist real chat messages (skip system/join/part noise)
	if a.history != nil {
		t, _ := m["type"].(string)
		if t == "chat" || t == "sub" || t == "giftsub" || t == "raid" || t == "announcement" {
			a.history.Append(channel, m)
		}
	}
	// 7TV cosmetics lookup: kick off a one-shot HTTP fetch for any
	// Twitch user we haven't queried yet so paint/badge entitlements
	// land even when the EventAPI WebSocket doesn't push them. The
	// stv client dedupes per-user so a chat burst doesn't fire many
	// requests, and the result emits the same "sevenTVCosmetic" event
	// the WS path uses.
	if a.stv != nil && msg.Platform == chat.PlatformTwitch && msg.UserID != "" {
		a.stv.LookupUser(msg.UserID)
	}
	runtime.EventsEmit(a.ctx, "chat", m)
}

func (a *App) chatToMap(channel string, msg chat.Message) map[string]interface{} {
	ts := msg.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	emoteRanges := make([]map[string]interface{}, 0, len(msg.TwitchEmoteRanges))
	for _, e := range msg.TwitchEmoteRanges {
		emoteRanges = append(emoteRanges, map[string]interface{}{
			"id":    e.ID,
			"name":  e.Name,
			"start": e.Start,
			"end":   e.End,
		})
	}

	badges := make([]map[string]string, 0, len(msg.Badges))
	for _, b := range msg.Badges {
		badges = append(badges, map[string]string{
			"name":    b.Name,
			"version": b.Version,
		})
	}

	typeStr := "chat"
	switch msg.Type {
	case chat.MessageTypeJoin:
		typeStr = "join"
	case chat.MessageTypePart:
		typeStr = "part"
	case chat.MessageTypeSub:
		typeStr = "sub"
	case chat.MessageTypeGiftSub:
		typeStr = "giftsub"
	case chat.MessageTypeRaid:
		typeStr = "raid"
	case chat.MessageTypeBan:
		typeStr = "ban"
	case chat.MessageTypeTimeout:
		typeStr = "timeout"
	case chat.MessageTypeDeletedMessage:
		typeStr = "deleted"
	case chat.MessageTypeClearChat:
		typeStr = "clearchat"
	case chat.MessageTypeAnnouncement:
		typeStr = "announcement"
	case chat.MessageTypeSystem:
		typeStr = "system"
	}

	return map[string]interface{}{
		"id":            msg.ID,
		"channel":       channel,
		"type":          typeStr,
		"timestamp":     ts.UnixMilli(),
		"userId":        msg.UserID,
		"username":      msg.Username,
		"displayName":   msg.DisplayName,
		"color":         msg.Color,
		"isMod":         msg.IsMod,
		"isVIP":         msg.IsVIP,
		"isSub":         msg.IsSub,
		"isBroadcaster": msg.IsBroadcaster,
		"text":          msg.Text,
		"twitchEmotes":  emoteRanges,
		"badges":        badges,
		"bits":          msg.Bits,
	}
}

func (a *App) loadChannelEmotes(channel string) {
	a.mu.Lock()
	if a.channelEmotesLoaded[channel] {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	// Wait briefly for auth + a non-empty userID. Without this, channels
	// added at boot may try to resolve before the auth flow finishes; the
	// load would silently no-op and never retry.
	var userID string
	for range 30 {
		if a.cfg.AccessToken != "" && a.cfg.ClientID != "" {
			userID = a.resolveChannelID(channel)
			if userID != "" {
				break
			}
		}
		time.Sleep(1 * time.Second)
	}
	if userID == "" {
		log.Printf("[BADGES] channel %s skipped (no auth / could not resolve user id)", channel)
		return
	}
	a.emotes.Load(userID)
	if err := a.badges.LoadChannel(userID, a.cfg.ClientID, a.cfg.AccessToken); err != nil {
		log.Printf("[BADGES] channel badge load failed for %s: %v", channel, err)
		return
	}
	log.Printf("[BADGES] loaded channel badges for %s", channel)

	a.mu.Lock()
	a.channelEmotesLoaded[channel] = true
	a.mu.Unlock()

	// Notify the frontend so it can drop its per-channel badge cache entries
	// for this channel and re-render. Without this, any badge URLs the
	// frontend cached during the window before LoadChannel completed would
	// remain (incorrectly) pointing at the global fallback artwork.
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "badgesReady", map[string]interface{}{
			"channel": channel,
		})
	}
}

// loadGlobalBadges retries until access token is available, then loads global badges.
func (a *App) loadGlobalBadges() {
	for range 30 { // try for up to 30 seconds
		if a.cfg.AccessToken != "" && a.cfg.ClientID != "" {
			if err := a.badges.LoadGlobal(a.cfg.ClientID, a.cfg.AccessToken); err == nil {
				log.Printf("[BADGES] loaded %d global badge sets", a.badges.Count())
				return
			} else {
				log.Printf("[BADGES] global badge load failed: %v", err)
				return
			}
		}
		time.Sleep(1 * time.Second)
	}
	log.Printf("[BADGES] skipped (not logged in)")
}

// resolveChannelID uses Helix /users (requires auth). Returns empty if not authenticated.
func (a *App) resolveChannelID(channel string) string {
	a.mu.Lock()
	if id, ok := a.channelIDCache[channel]; ok {
		a.mu.Unlock()
		return id
	}
	a.mu.Unlock()

	if a.cfg.AccessToken == "" || a.cfg.ClientID == "" {
		return ""
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("https://api.twitch.tv/helix/users?login=%s", channel), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Client-Id", a.cfg.ClientID)
	req.Header.Set("Authorization", "Bearer "+a.cfg.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}

	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return ""
	}
	if len(data.Data) == 0 {
		return ""
	}
	id := data.Data[0].ID
	a.mu.Lock()
	a.channelIDCache[channel] = id
	stv := a.stv
	a.mu.Unlock()
	// Subscribe to 7TV cosmetics for this channel the moment we know
	// its broadcaster id. stv may be nil if startup is still wiring;
	// AddChannel queues the host id either way.
	if stv != nil {
		stv.AddChannel(id)
	}
	return id
}

// === Frontend-bound methods ===

// AddChannel adds a channel to the watched list. Accepts either:
//   - "name"            (twitch, legacy)
//   - "yt:@handle"      (youtube via handle)
//   - explicit platform via the 2-arg AddChannelOn method
func (a *App) AddChannel(channel string) string {
	ref := parseChannelRef(channel)
	if ref.Name == "" {
		return "empty channel"
	}
	stored := ref.String()
	for _, c := range a.cfg.Channels {
		if c == stored {
			return ""
		}
	}
	a.cfg.Channels = append(a.cfg.Channels, stored)
	a.cfg.Save()
	a.startChannelWatcher(ref)
	return ""
}

// AddChannelOn is the explicit 2-arg variant called from the platform-aware
// add-channel modal. platform is "twitch" or "youtube".
func (a *App) AddChannelOn(platform, name string) string {
	if name == "" {
		return "empty channel"
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform != "youtube" && platform != "twitch" {
		platform = "twitch"
	}
	ref := ChannelRef{Platform: platform, Name: strings.TrimSpace(strings.TrimPrefix(name, "#"))}
	if platform == "twitch" {
		ref.Name = strings.ToLower(ref.Name)
	}
	stored := ref.String()
	for _, c := range a.cfg.Channels {
		if c == stored {
			return ""
		}
	}
	a.cfg.Channels = append(a.cfg.Channels, stored)
	a.cfg.Save()
	a.startChannelWatcher(ref)
	return ""
}

// startChannelWatcher wires up the platform-specific subscriptions for a
// newly added channel ref (Twitch IRC join, YouTube live-detect, etc.).
func (a *App) startChannelWatcher(ref ChannelRef) {
	switch ref.Platform {
	case "youtube":
		go a.startYouTubeWatcher(ref.Name)
	default:
		a.irc.JoinChannel(ref.Name)
		a.mu.Lock()
		sender := a.send
		a.mu.Unlock()
		if sender != nil {
			sender.Join(ref.Name)
		}
		go a.loadChannelEmotes(ref.Name)
		go a.checkAndEmitLive(ref.Name)
	}
}

// startYouTubeWatcher spawns a LiveDetector for `handle` (the YT @handle
// or UC… id). When the channel goes live it spins up an InnerTube chat
// poller; when it goes offline it tears the poller down. Idempotent —
// calling again with the same handle is a no-op.
func (a *App) startYouTubeWatcher(handle string) {
	a.mu.Lock()
	if _, exists := a.ytWatchers[handle]; exists {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.ytWatchers[handle] = cancel
	a.mu.Unlock()

	// Tab key on the frontend side. Matches the stored ref so add / live
	// status / chat events all line up.
	tabKey := "yt:" + handle

	var chatCancel context.CancelFunc
	emitLive := func(live bool) {
		runtime.EventsEmit(a.ctx, "liveStatus", map[string]interface{}{
			"channel": tabKey,
			"live":    live,
		})
	}

	detector := youtube.NewLiveDetector(
		handle,
		func(info youtube.LiveStreamInfo) {
			log.Printf("[YT] %s live: %s (video=%s)", handle, info.Title, info.VideoID)
			emitLive(true)
			a.emitMessage(tabKey, chat.Message{
				Platform:  chat.PlatformYouTube,
				Type:      chat.MessageTypeSystem,
				Channel:   tabKey,
				Timestamp: time.Now(),
				Text:      fmt.Sprintf("YouTube stream detected: %s", info.Title),
			})
			// Wire up an InnerTube chat reader for this stream.
			chatCtx, cc := context.WithCancel(ctx)
			a.mu.Lock()
			if chatCancel != nil {
				chatCancel()
			}
			chatCancel = cc
			a.mu.Unlock()
			ytClient := youtube.NewInnerTubeClient(info.VideoID)
			// Attach cookies (best-effort) so the initial page render
			// includes the sendLiveChatMessageEndpoint — otherwise
			// SendParams() stays empty and posting fails with a
			// misleading "sub-only" error.
			if creds, err := youtube.LoadLoginCookies(); err == nil && creds.Valid() {
				ytClient.SetCookies(creds)
			}
			a.mu.Lock()
			a.ytClients[tabKey] = ytClient
			a.mu.Unlock()
			go func() {
				for {
					select {
					case <-chatCtx.Done():
						a.mu.Lock()
						if a.ytClients[tabKey] == ytClient {
							delete(a.ytClients, tabKey)
						}
						a.mu.Unlock()
						return
					case msg := <-ytClient.Messages():
						msg.Channel = tabKey
						a.emitMessage(tabKey, msg)
					case e := <-ytClient.Errors():
						log.Printf("[YT] %s: %v", handle, e)
					}
				}
			}()
			go func() {
				for attempt := 0; attempt < 5; attempt++ {
					if chatCtx.Err() != nil {
						return
					}
					if err := ytClient.Connect(chatCtx); err != nil {
						log.Printf("[YT] %s: connect attempt %d failed: %v", handle, attempt+1, err)
						select {
						case <-chatCtx.Done():
							return
						case <-time.After(5 * time.Second):
						}
						continue
					}
					return
				}
				log.Printf("[YT] %s: giving up after 5 connect attempts", handle)
			}()
		},
		func() {
			log.Printf("[YT] %s went offline", handle)
			emitLive(false)
			a.mu.Lock()
			if chatCancel != nil {
				chatCancel()
				chatCancel = nil
			}
			a.mu.Unlock()
			a.emitMessage(tabKey, chat.Message{
				Platform:  chat.PlatformYouTube,
				Type:      chat.MessageTypeSystem,
				Channel:   tabKey,
				Timestamp: time.Now(),
				Text:      "YouTube stream ended",
			})
		},
	)
	go detector.Start(ctx)
}

// stopYouTubeWatcher tears down a previously-started watcher.
func (a *App) stopYouTubeWatcher(handle string) {
	a.mu.Lock()
	cancel := a.ytWatchers[handle]
	delete(a.ytWatchers, handle)
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// checkAndEmitLive runs a single Helix live check for `channel` and emits a
// liveStatus event to the frontend. Used both by liveStatusLoop and by
// AddChannel so newly-added channels show their live dot immediately.
func (a *App) checkAndEmitLive(channel string) {
	isLive := a.checkLive(channel)
	a.mu.Lock()
	a.liveStatus[channel] = isLive
	a.mu.Unlock()
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "liveStatus", map[string]interface{}{
			"channel": channel,
			"live":    isLive,
		})
	}
}

func (a *App) RemoveChannel(channel string) {
	ref := parseChannelRef(channel)
	stored := ref.String()
	newList := make([]string, 0, len(a.cfg.Channels))
	for _, c := range a.cfg.Channels {
		if c != stored && c != channel {
			newList = append(newList, c)
		}
	}
	a.cfg.Channels = newList
	a.cfg.Save()
	switch ref.Platform {
	case "youtube":
		a.stopYouTubeWatcher(ref.Name)
	default:
		a.irc.PartChannel(ref.Name)
	}
}

// GetChannels returns the raw stored entries (so existing autocompletes /
// reorder still work) plus a parallel structured list for the platform-aware
// UI in the frontend.
func (a *App) GetChannels() []string {
	return a.cfg.Channels
}

// GetChannelsDetailed returns each tracked channel with its platform so the
// frontend can render the right pill / route the right send target.
func (a *App) GetChannelsDetailed() []map[string]string {
	out := make([]map[string]string, 0, len(a.cfg.Channels))
	for _, c := range a.cfg.Channels {
		ref := parseChannelRef(c)
		out = append(out, map[string]string{
			"raw":      c,
			"platform": ref.Platform,
			"name":     ref.Name,
		})
	}
	return out
}

// SetChannelOrder accepts a reordered list of channel names (must contain the
// same set of channels currently tracked) and persists the new order.
// Unknown channels are ignored; missing channels are appended at the end so
// nothing disappears from a race.
func (a *App) SetChannelOrder(order []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	existing := make(map[string]bool, len(a.cfg.Channels))
	for _, c := range a.cfg.Channels {
		existing[c] = true
	}
	seen := make(map[string]bool, len(order))
	newList := make([]string, 0, len(a.cfg.Channels))
	for _, c := range order {
		c = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(c), "#"))
		if !existing[c] || seen[c] {
			continue
		}
		seen[c] = true
		newList = append(newList, c)
	}
	for _, c := range a.cfg.Channels {
		if !seen[c] {
			newList = append(newList, c)
		}
	}
	a.cfg.Channels = newList
	a.cfg.Save()
}

// LookupBadge returns the URL for a Twitch badge in the context of `channel`.
// Channel-specific badge artwork (sub tiers, custom badges) wins over the
// global default. Pass an empty channel to get the global badge only.
func (a *App) LookupBadge(channel, setID, version string) string {
	if a.badges == nil {
		return ""
	}
	var userID string
	if channel != "" {
		// Synchronously ensure the channel's userID is cached. resolveChannelID
		// is cheap (no-op if cached). Without this, the first lookups after a
		// fresh boot fall back to global because channelIDCache is racing the
		// loadChannelEmotes goroutine.
		userID = a.resolveChannelID(channel)
	}
	return a.badges.Lookup(userID, setID, version)
}

func (a *App) LookupEmote(name string) map[string]interface{} {
	if a.emotes == nil {
		return nil
	}
	info, ok := a.emotes.Lookup(name)
	if !ok {
		return nil
	}
	var url string
	switch info.Provider {
	case twitch.EmoteProvider7TV:
		url = fmt.Sprintf("https://cdn.7tv.app/emote/%s/2x.webp", info.ID)
	case twitch.EmoteProviderBTTV:
		ext := "png"
		if info.Animated {
			ext = "gif"
		}
		url = fmt.Sprintf("https://cdn.betterttv.net/emote/%s/2x.%s", info.ID, ext)
	case twitch.EmoteProviderFFZ:
		url = fmt.Sprintf("https://cdn.frankerfacez.com/emote/%s/2", info.ID)
	}
	return map[string]interface{}{
		"provider": int(info.Provider),
		"id":       info.ID,
		"animated": info.Animated,
		"url":      url,
	}
}

func (a *App) SetHighlights(keywords []string) {
	a.cfg.Highlights = keywords
	a.cfg.Save()
}

func (a *App) GetHighlights() []string {
	return a.cfg.Highlights
}

func (a *App) SetTheme(theme string) {
	a.cfg.Theme = theme
	a.cfg.Save()
}

func (a *App) SetOnlyShowLive(only bool) {
	a.cfg.OnlyShowLive = only
	a.cfg.Save()
}

// SetLocale stores the chosen UI locale ("en" or "de"). Frontend reads it
// back via GetInitialState. Empty or unknown values default to "en".
func (a *App) SetLocale(locale string) {
	switch locale {
	case "de", "en":
		a.cfg.Locale = locale
	default:
		a.cfg.Locale = "en"
	}
	a.cfg.Save()
}

// SetShowTimestamps toggles the always-visible per-message timestamp column.
// Stored inverted (HideTimestamps) so the historic default stays "visible".
func (a *App) SetShowTimestamps(show bool) {
	a.cfg.HideTimestamps = !show
	a.cfg.Save()
}

// SetNotifSound persists the selected mention/live-go notification tone.
// Accepts "none", "bell", or "ping"; anything else falls back to "none".
func (a *App) SetNotifSound(s string) {
	switch s {
	case "bell", "ping", "none":
		a.cfg.NotifSound = s
	default:
		a.cfg.NotifSound = "none"
	}
	a.cfg.Save()
}

// === Auth flow (Device Code) ===

// StartLogin requests a device code and returns the verification URL + code.
func (a *App) StartLogin() map[string]interface{} {
	if a.cfg.ClientID == "" {
		return map[string]interface{}{"error": "no client_id configured"}
	}
	device, err := twitch.RequestDeviceCode(a.cfg.ClientID)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}

	// Poll in background
	go func() {
		token, err := twitch.PollForToken(a.cfg.ClientID, device.DeviceCode, device.Interval)
		if err != nil {
			runtime.EventsEmit(a.ctx, "loginResult", map[string]interface{}{"error": err.Error()})
			return
		}
		info, err := twitch.ValidateToken(token.AccessToken)
		if err != nil {
			runtime.EventsEmit(a.ctx, "loginResult", map[string]interface{}{"error": err.Error()})
			return
		}
		a.cfg.AccessToken = token.AccessToken
		a.cfg.RefreshToken = token.RefreshToken
		a.cfg.Username = info.Login
		a.cfg.UserID = info.UserID
		a.cfg.Save()
		// Tokens go straight into the OS keyring; the config.json copy is
		// wiped by saveTwitchTokens on the next Save round-trip.
		a.saveTwitchTokens()

		// Start the authenticated sender so the user can post immediately.
		go a.connectSendClient()

		// (loadGlobalBadges + liveStatusLoop + tokenRefreshLoop already
		// started during startup() — they're cfg-driven and will start
		// doing real work as soon as we save the fresh token. Don't
		// double-spawn them; that'd just leak goroutines and double the
		// Helix poll load.)
		go a.loadGlobalBadges()

		// Kick an immediate live-check for every configured channel so the
		// green dot appears without waiting up to 60s for the next tick.
		a.mu.Lock()
		chans := append([]string{}, a.cfg.Channels...)
		a.mu.Unlock()
		for _, ch := range chans {
			go a.loadChannelEmotes(ch)
			go a.checkAndEmitLive(ch)
		}

		// Re-emit "ready" so the frontend (which subscribed during startup)
		// can refresh anything that depended on the username being known.
		runtime.EventsEmit(a.ctx, "ready", map[string]interface{}{
			"channels":     a.cfg.Channels,
			"highlights":   a.cfg.Highlights,
			"theme":        a.cfg.Theme,
			"onlyShowLive": a.cfg.OnlyShowLive,
			"loggedIn":     true,
			"username":     info.Login,
			"configPath":   hubConfigPath(),
			"locale":       a.cfg.Locale,
			"notifSound":   a.cfg.NotifSound,
			"showTimestamps": !a.cfg.HideTimestamps,
		})

		runtime.EventsEmit(a.ctx, "loginResult", map[string]interface{}{
			"success":  true,
			"username": info.Login,
		})
	}()

	return map[string]interface{}{
		"verificationUri": device.VerificationURI,
		"userCode":        device.UserCode,
	}
}

// OpenURL opens a URL in the user's default browser.
func (a *App) OpenURL(url string) {
	runtime.BrowserOpenURL(a.ctx, url)
}

// GetInitialState returns the initial app state for the frontend to render.
// Called by the frontend AFTER it subscribes to events to avoid race conditions.
func (a *App) GetInitialState() map[string]interface{} {
	a.mu.Lock()
	liveMap := make(map[string]bool, len(a.liveStatus))
	for k, v := range a.liveStatus {
		liveMap[k] = v
	}
	a.mu.Unlock()

	return map[string]interface{}{
		"channels":     a.cfg.Channels,
		"highlights":   a.cfg.Highlights,
		"theme":        a.cfg.Theme,
		"onlyShowLive": a.cfg.OnlyShowLive,
		"loggedIn":     a.cfg.AccessToken != "",
		"username":     a.cfg.Username,
		"configPath":   hubConfigPath(),
		"liveStatus":   liveMap,
		"locale":       a.cfg.Locale,
		"notifSound":   a.cfg.NotifSound,
		"showTimestamps": !a.cfg.HideTimestamps,
		"fontSize":       a.GetFontSize(),
	}
}

// GetConfigPath returns the file path where config is stored.
func (a *App) GetConfigPath() string {
	return hubConfigPath()
}

// GetLogPath returns the log file path.
func (a *App) GetLogPath() string {
	logPath := "chathub.log"
	if exe, err := os.Executable(); err == nil {
		logPath = filepath.Join(filepath.Dir(exe), "chathub.log")
	}
	return logPath
}

// TestSave forces a config save and returns the error string if any.
func (a *App) TestSave() string {
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	return ""
}

// === Self-update ===

// CheckUpdate queries GitHub releases for the latest published version and
// reports whether it's newer than the running binary. The frontend uses this
// to surface an "update available" button.
func (a *App) CheckUpdate() map[string]interface{} {
	out := map[string]interface{}{
		"current":   version.Version,
		"available": false,
	}
	channel := a.cfg.UpdateChannel
	if channel == "" {
		channel = "stable"
	}
	rel, err := selfupdate.LatestForChannel(version.RepoOwner, version.RepoName, channel)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	out["latest"] = rel.TagName
	out["notes"] = rel.Body
	out["releaseUrl"] = rel.HTMLURL
	out["channel"] = channel
	out["available"] = selfupdate.IsNewer(version.Version, rel.TagName)
	out["downloadUrl"] = selfupdate.FindAsset(rel, "chathub.exe")
	return out
}

// GetFontSize returns the saved chat font size (px), defaulting to 14.
func (a *App) GetFontSize() int {
	if a.cfg.FontSize < 10 || a.cfg.FontSize > 28 {
		return 14
	}
	return a.cfg.FontSize
}

// SetFontSize persists the chat font size. Clamped to [10, 28].
func (a *App) SetFontSize(px int) string {
	if px < 10 {
		px = 10
	}
	if px > 28 {
		px = 28
	}
	a.cfg.FontSize = px
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	return ""
}

// GetUpdateChannel returns the saved channel for the settings UI.
func (a *App) GetUpdateChannel() string {
	if a.cfg.UpdateChannel == "" {
		return "stable"
	}
	return a.cfg.UpdateChannel
}

// SetUpdateChannel persists the chosen update channel. Accepts "stable",
// "beta", or "alpha" — other values are coerced to "stable" so a typo
// can't silently disable updates.
func (a *App) SetUpdateChannel(channel string) string {
	c := strings.ToLower(strings.TrimSpace(channel))
	if c != "stable" && c != "beta" && c != "alpha" {
		c = "stable"
	}
	a.cfg.UpdateChannel = c
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	return ""
}

// ApplyUpdate downloads the new exe, swaps it in-place, and relaunches.
// On non-Windows platforms returns a hint so the UI can fall back to
// opening the release page in a browser.
func (a *App) ApplyUpdate(url string) string {
	if url == "" {
		return "no download URL"
	}
	if err := selfupdate.Apply(url); err != nil {
		log.Printf("[UPDATE] apply failed: %v", err)
		return err.Error()
	}
	log.Printf("[UPDATE] applied, relaunching")
	if err := selfupdate.Restart(); err != nil {
		return err.Error()
	}
	return ""
}

// GetVersion is a tiny helper for the settings UI.
func (a *App) GetVersion() string { return version.Version }

// === Windows autostart ===

// AutostartStatus reports the platform support and current state of the
// "start with Windows" toggle. The frontend uses `supported` to decide
// whether to show the checkbox at all.
func (a *App) AutostartStatus() map[string]interface{} {
	return map[string]interface{}{
		"supported": autostartSupported(),
		"enabled":   autostartSupported() && autostartIsEnabled(),
	}
}

// SetAutostart enables or disables the Windows autostart entry. Returns an
// empty string on success, or the error message on failure (e.g. dev build,
// registry permission issue).
func (a *App) SetAutostart(enabled bool) string {
	if !autostartSupported() {
		return "autostart is only supported on Windows"
	}
	var err error
	if enabled {
		err = autostartEnable()
	} else {
		err = autostartDisable()
	}
	if err != nil {
		log.Printf("[AUTOSTART] %v", err)
		return err.Error()
	}
	log.Printf("[AUTOSTART] enabled=%v", enabled)
	return ""
}

// Logout clears auth.
func (a *App) Logout() {
	a.cfg.AccessToken = ""
	a.cfg.RefreshToken = ""
	a.cfg.Username = ""
	a.cfg.UserID = ""
	a.cfg.Save()
	a.clearTwitchTokens()
	a.mu.Lock()
	if a.send != nil {
		a.send.Disconnect()
		a.send = nil
	}
	a.mu.Unlock()
}

// SendMessage sends a chat message to a specific channel via Twitch's
// Helix POST /chat/messages endpoint. Replaces the previous IRC PRIVMSG
// path which was silently throttled by Twitch for unverified clients
// after idle — Helix returns an explicit HTTP status + drop_reason so
// the user actually sees why a message was rejected (sub-only, banned,
// duplicate, ratelimit, …). IRC connection is retained for READING
// chat only.
func (a *App) SendMessage(channel, text string) string {
	if a.cfg.AccessToken == "" || a.cfg.ClientID == "" {
		log.Printf("[SEND] rejected — no access token / client id")
		return "not logged in"
	}
	if a.cfg.UserID == "" {
		log.Printf("[SEND] rejected — no user id (re-login required?)")
		return "missing user id — please re-login"
	}
	channel = strings.ToLower(strings.TrimPrefix(channel, "#"))
	broadcasterID := a.resolveChannelID(channel)
	if broadcasterID == "" {
		log.Printf("[SEND] could not resolve broadcaster id for #%s", channel)
		return "could not resolve channel"
	}
	log.Printf("[SEND] Helix POST #%s (broadcaster=%s) text=%q", channel, broadcasterID, text)
	if err := twitch.SendChatMessage(a.cfg.ClientID, a.cfg.AccessToken, broadcasterID, a.cfg.UserID, text); err != nil {
		return err.Error()
	}
	return ""
}

// === YouTube auth + send ===

// StartYouTubeLogin spawns an isolated Edge/Chrome window pointed at
// Google's login page. The user authenticates and the resulting session
// cookies get pulled via CDP and saved into the OS keyring. Blocks until
// the user finishes or `timeout` elapses; the frontend should disable the
// button while this is in flight.
func (a *App) StartYouTubeLogin() string {
	// 5 minutes is plenty for typing a password + 2FA. The browser window
	// stays open, so the user can take their time.
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
	defer cancel()
	creds, err := youtube.LaunchLoginFlow(ctx, 5*time.Minute)
	if err != nil {
		log.Printf("[YT-LOGIN] %v", err)
		return err.Error()
	}
	if err := youtube.SaveLoginCookies(creds); err != nil {
		log.Printf("[YT-LOGIN] save failed: %v", err)
		return err.Error()
	}
	log.Printf("[YT-LOGIN] cookies saved to OS keyring")
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "youtubeLoginChanged", map[string]interface{}{
			"loggedIn": true,
		})
	}
	return ""
}

// LogoutYouTube clears the saved cookies.
func (a *App) LogoutYouTube() string {
	if err := youtube.ClearLoginCookies(); err != nil {
		return err.Error()
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "youtubeLoginChanged", map[string]interface{}{
			"loggedIn": false,
		})
	}
	return ""
}

// HasYouTubeLogin reports whether a session is currently saved.
func (a *App) HasYouTubeLogin() bool {
	c, err := youtube.LoadLoginCookies()
	return err == nil && c.Valid()
}

// SendYouTubeMessage posts to the live stream attached to the given tab
// key (e.g. "yt:@DEmiwitv"). The active InnerTube client's SendParams
// identifies the chat; if the channel isn't currently live we surface
// that as an error instead of silently dropping the message.
func (a *App) SendYouTubeMessage(tabKey, text string) string {
	a.mu.Lock()
	client := a.ytClients[tabKey]
	a.mu.Unlock()
	if client == nil {
		return "channel not live (no active YouTube chat session)"
	}
	params := client.SendParams()
	if params == "" {
		return "this channel's chat doesn't accept input (sub-only, follower-only, or chat disabled)"
	}
	creds, err := youtube.LoadLoginCookies()
	if err != nil {
		return "not logged in to YouTube — click Connect YouTube in settings"
	}
	if err := youtube.SendLiveChatMessage(creds, params, text); err != nil {
		log.Printf("[YT-SEND] %v", err)
		return err.Error()
	}
	return ""
}

// GetUserCard returns Chatterino-style "usercard" data for a user in the
// context of a specific channel. All fields are best-effort — missing data
// (e.g. broadcaster's follower count when not visible to us) is returned as
// empty strings/zeros so the frontend can render what it has.
//
// userIDOrLogin can be a Twitch user ID (preferred, all numeric) or a login;
// if it's a login we resolve it via /helix/users?login=...
func (a *App) GetUserCard(channel, userIDOrLogin string) map[string]interface{} {
	out := map[string]interface{}{}
	if a.cfg.AccessToken == "" || a.cfg.ClientID == "" {
		out["error"] = "not logged in"
		return out
	}
	channelID := a.resolveChannelID(strings.ToLower(strings.TrimPrefix(channel, "#")))

	// Resolve userID. If the caller passed a numeric string we trust it; else
	// look it up by login.
	userID := userIDOrLogin
	if userID == "" {
		out["error"] = "missing user"
		return out
	}
	isNumeric := true
	for _, r := range userID {
		if r < '0' || r > '9' {
			isNumeric = false
			break
		}
	}
	var login, displayName, avatar, createdAt, description string
	if !isNumeric {
		// Lookup by login
		v, _ := a.helixGetUserBy("login", userID)
		if v != nil {
			userID = v["id"]
			login = v["login"]
			displayName = v["display_name"]
			avatar = v["profile_image_url"]
			createdAt = v["created_at"]
			description = v["description"]
		}
	} else {
		v, _ := a.helixGetUserBy("id", userID)
		if v != nil {
			login = v["login"]
			displayName = v["display_name"]
			avatar = v["profile_image_url"]
			createdAt = v["created_at"]
			description = v["description"]
		}
	}
	out["userId"] = userID
	out["login"] = login
	out["displayName"] = displayName
	out["avatar"] = avatar
	out["createdAt"] = createdAt
	out["description"] = description

	// Account followers count (the user's own followers — only requires
	// "moderator:read:followers" of THAT user, which we don't have. So this
	// usually returns 0/empty for arbitrary users; we still try.)
	if total, ok := a.helixGetFollowerCount(userID); ok {
		out["followers"] = total
	}

	// Following relationship: is `userID` following `channelID`, and since when.
	if channelID != "" && userID != "" {
		if since, ok := a.helixGetFollowSince(channelID, userID); ok {
			out["followingSince"] = since
		}
	}

	// Subscription status of `userID` to `channelID`. The /subscriptions/user
	// endpoint requires the broadcaster's own token; for other channels Twitch
	// will return 401. We try anyway — if it fails we just skip.
	if channelID != "" && userID != "" {
		if tier, months, ok := a.helixGetSubscription(channelID, userID); ok {
			out["subscribed"] = true
			out["subTier"] = tier
			out["subMonths"] = months
		}
	}

	return out
}

// --- Helix helpers used by GetUserCard ---

func (a *App) helixGet(url string) (map[string]interface{}, int) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, 0
	}
	req.Header.Set("Client-Id", a.cfg.ClientID)
	req.Header.Set("Authorization", "Bearer "+a.cfg.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, resp.StatusCode
	}
	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, resp.StatusCode
	}
	return data, resp.StatusCode
}

func (a *App) helixGetUserBy(field, value string) (map[string]string, bool) {
	data, status := a.helixGet(fmt.Sprintf("https://api.twitch.tv/helix/users?%s=%s", field, value))
	if status != 200 || data == nil {
		return nil, false
	}
	arr, _ := data["data"].([]interface{})
	if len(arr) == 0 {
		return nil, false
	}
	u, _ := arr[0].(map[string]interface{})
	out := make(map[string]string, len(u))
	for k, v := range u {
		s, _ := v.(string)
		out[k] = s
	}
	return out, true
}

func (a *App) helixGetFollowerCount(broadcasterID string) (int, bool) {
	data, status := a.helixGet(fmt.Sprintf("https://api.twitch.tv/helix/channels/followers?broadcaster_id=%s", broadcasterID))
	if status != 200 || data == nil {
		return 0, false
	}
	if total, ok := data["total"].(float64); ok {
		return int(total), true
	}
	return 0, false
}

func (a *App) helixGetFollowSince(broadcasterID, userID string) (string, bool) {
	// /channels/followed lists who `user_id` follows; filter by broadcaster_id.
	data, status := a.helixGet(fmt.Sprintf("https://api.twitch.tv/helix/channels/followed?user_id=%s&broadcaster_id=%s", userID, broadcasterID))
	if status != 200 || data == nil {
		return "", false
	}
	arr, _ := data["data"].([]interface{})
	if len(arr) == 0 {
		return "", false
	}
	row, _ := arr[0].(map[string]interface{})
	since, _ := row["followed_at"].(string)
	return since, since != ""
}

func (a *App) helixGetSubscription(broadcasterID, userID string) (string, int, bool) {
	data, status := a.helixGet(fmt.Sprintf("https://api.twitch.tv/helix/subscriptions/user?broadcaster_id=%s&user_id=%s", broadcasterID, userID))
	if status != 200 || data == nil {
		return "", 0, false
	}
	arr, _ := data["data"].([]interface{})
	if len(arr) == 0 {
		return "", 0, false
	}
	row, _ := arr[0].(map[string]interface{})
	tier, _ := row["tier"].(string)
	// Twitch doesn't expose months via this endpoint; the frontend can compute
	// from badges (subscriber/N) if needed.
	return tier, 0, true
}

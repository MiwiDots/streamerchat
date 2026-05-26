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
	"github.com/miwi/streamerchat/internal/twitch"
	"github.com/miwi/streamerchat/internal/version"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Set at build time via -ldflags "-X main.defaultClientID=..."
var defaultClientID string

type HubConfig struct {
	Channels       []string `json:"channels"`
	Highlights     []string `json:"highlights"`
	Theme          string   `json:"theme"`
	OnlyShowLive   bool     `json:"only_show_live"`
	Locale         string   `json:"locale"`
	NotifSound     string   `json:"notif_sound"`

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

type App struct {
	ctx    context.Context
	cfg    *HubConfig
	mu     sync.Mutex
	irc    *twitch.MultiIRCClient
	send   *twitch.IRCClient
	emotes *twitch.ThirdPartyEmotes
	badges *twitch.BadgeRegistry
	history *HistoryWriter

	channelEmotesLoaded map[string]bool
	channelIDCache      map[string]string
	liveStatus          map[string]bool
}

func NewApp() *App {
	return &App{
		channelEmotesLoaded: make(map[string]bool),
		channelIDCache:      make(map[string]string),
		liveStatus:          make(map[string]bool),
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

	a.cfg = loadHubConfig()
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

	a.history = NewHistoryWriter()
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
		for _, ch := range a.cfg.Channels {
			a.irc.JoinChannel(ch)
			go a.loadChannelEmotes(ch)
		}
	}()

	// Start authenticated client if we have a token
	if a.cfg.AccessToken != "" && a.cfg.Username != "" {
		go a.validateAndConnect()
	}

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
		log.Printf("[AUTH] Token valid for %s", info.Login)
		if info.Login != "" {
			a.cfg.Username = info.Login
			a.cfg.UserID = info.UserID
			a.cfg.Save()
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
	log.Printf("[AUTH] Token refreshed successfully")
	return true
}

// tokenRefreshLoop runs every hour to keep tokens fresh.
func (a *App) tokenRefreshLoop() {
	ticker := time.NewTicker(1 * time.Hour)
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
	if len(a.cfg.Channels) == 0 {
		a.mu.Unlock()
		return
	}
	primary := a.cfg.Channels[0]
	channels := append([]string{}, a.cfg.Channels...)
	send := twitch.NewIRCClient(a.cfg.Username, a.cfg.AccessToken, primary)
	a.send = send
	a.mu.Unlock()

	go func() {
		if err := send.Connect(); err != nil {
			log.Printf("[SEND] connect failed: %v", err)
		}
	}()
	// Join remaining channels after a short delay (need to be connected first)
	go func() {
		time.Sleep(2 * time.Second)
		for _, ch := range channels[1:] {
			send.Join(ch)
		}
	}()
}

// liveStatusLoop polls Twitch every 60s for live status of all channels.
func (a *App) liveStatusLoop() {
	check := func() {
		a.mu.Lock()
		chans := append([]string{}, a.cfg.Channels...)
		a.mu.Unlock()

		for _, ch := range chans {
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
	if a.cfg.AccessToken == "" || a.cfg.ClientID == "" {
		log.Printf("[LIVE] %s: skipped (not logged in)", channel)
		return false
	}
	req, err := http.NewRequest("GET", fmt.Sprintf("https://api.twitch.tv/helix/streams?user_login=%s", channel), nil)
	if err != nil {
		log.Printf("[LIVE] %s: request build error: %v", channel, err)
		return false
	}
	req.Header.Set("Client-Id", a.cfg.ClientID)
	req.Header.Set("Authorization", "Bearer "+a.cfg.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[LIVE] %s: http error: %v", channel, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("[LIVE] %s: status=%d", channel, resp.StatusCode)
		return false
	}
	var data struct {
		Data []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			UserName string `json:"user_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("[LIVE] %s: decode error: %v", channel, err)
		return false
	}
	live := len(data.Data) > 0
	log.Printf("[LIVE] %s: live=%v", channel, live)
	return live
}

func (a *App) shutdown(_ context.Context) {
	if a.history != nil {
		a.history.Close()
	}
}

// GetHistory returns the last messages for a channel (called by frontend on tab open).
func (a *App) GetHistory(channel string, limit int) []map[string]interface{} {
	if a.history == nil {
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
	a.mu.Unlock()
	return id
}

// === Frontend-bound methods ===

func (a *App) AddChannel(channel string) string {
	channel = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(channel), "#"))
	if channel == "" {
		return "empty channel"
	}
	for _, c := range a.cfg.Channels {
		if c == channel {
			return ""
		}
	}
	a.cfg.Channels = append(a.cfg.Channels, channel)
	a.cfg.Save()
	a.irc.JoinChannel(channel)
	// Also join via authenticated sender if logged in
	a.mu.Lock()
	sender := a.send
	a.mu.Unlock()
	if sender != nil {
		sender.Join(channel)
	}
	go a.loadChannelEmotes(channel)
	// Fire an immediate live check so the user doesn't have to wait up to
	// 60s for the next liveStatusLoop tick.
	go a.checkAndEmitLive(channel)
	return ""
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
	channel = strings.ToLower(strings.TrimPrefix(channel, "#"))
	newList := make([]string, 0, len(a.cfg.Channels))
	for _, c := range a.cfg.Channels {
		if c != channel {
			newList = append(newList, c)
		}
	}
	a.cfg.Channels = newList
	a.cfg.Save()
	a.irc.PartChannel(channel)
}

func (a *App) GetChannels() []string {
	return a.cfg.Channels
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

		// Start authenticated sender + add username to highlights
		go a.connectSendClient()

		// Now that we have auth, load badges + start live status checks
		go a.loadGlobalBadges()
		go a.liveStatusLoop()

		// Re-trigger channel badge/emote load for any channels that were
		// added before login (their previous attempts would have skipped
		// for lack of auth).
		a.mu.Lock()
		chans := append([]string{}, a.cfg.Channels...)
		a.mu.Unlock()
		for _, ch := range chans {
			go a.loadChannelEmotes(ch)
		}

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
	rel, err := selfupdate.Latest(version.RepoOwner, version.RepoName)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	out["latest"] = rel.TagName
	out["notes"] = rel.Body
	out["releaseUrl"] = rel.HTMLURL
	out["available"] = selfupdate.IsNewer(version.Version, rel.TagName)
	out["downloadUrl"] = selfupdate.FindAsset(rel, "chathub.exe")
	return out
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
	a.mu.Lock()
	if a.send != nil {
		a.send.Disconnect()
		a.send = nil
	}
	a.mu.Unlock()
}

// SendMessage sends a chat message to a specific channel (requires login).
func (a *App) SendMessage(channel, text string) string {
	if a.cfg.AccessToken == "" {
		return "not logged in"
	}
	a.mu.Lock()
	sender := a.send
	a.mu.Unlock()
	if sender == nil {
		// Lazy connect on first send
		go a.connectSendClient()
		return "connecting, try again in a moment"
	}
	sender.SayTo(channel, text)
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

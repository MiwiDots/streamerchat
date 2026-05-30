package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/miwi/streamerchat/internal/chat"
	"github.com/miwi/streamerchat/internal/config"
	"github.com/miwi/streamerchat/internal/selfupdate"
	"github.com/miwi/streamerchat/internal/twitch"
	"github.com/miwi/streamerchat/internal/version"
	"github.com/miwi/streamerchat/internal/youtube"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Twitch Client ID for the device-code OAuth flow. Public, not a secret.
// Baked in so fresh installs without a config can log in out of the box.
// Can be overridden at build time via -ldflags "-X main.defaultClientID=...".
var defaultClientID = "opez5azi81po1xb4hb581ikw3xo04y"

// App holds the Wails application state.
type App struct {
	ctx context.Context

	cfg *config.Config

	mu          sync.Mutex
	ircClient   *twitch.IRCClient
	helixClient *twitch.HelixClient
	botDetector *twitch.BotDetector
	emotes      *twitch.ThirdPartyEmotes
	badges      *twitch.BadgeRegistry

	ytChatCancel context.CancelFunc
}

func NewApp() *App {
	return &App{}
}

// startup is called when the app starts.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Open log file next to executable
	logPath := "streamerchat.log"
	if exe, err := os.Executable(); err == nil {
		logPath = filepath.Join(filepath.Dir(exe), "streamerchat.log")
	}
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
		log.SetOutput(f)
		log.Printf("[BOOT] StreamerChat v%s (Wails) started, log: %s", version.Version, logPath)
	}

	// Remove leftover <exe>.old from a previous self-update.
	selfupdate.CleanupPrevious()

	// Load config
	cfg, err := config.Load()
	if err != nil {
		log.Printf("[BOOT] config load failed: %v", err)
		return
	}
	a.cfg = cfg

	// Embed default client ID at build time
	if a.cfg.Twitch.ClientID == "" && defaultClientID != "" {
		a.cfg.Twitch.ClientID = defaultClientID
		a.cfg.Save()
	}

	// Bot detector
	a.botDetector = twitch.NewBotDetector()

	// 3rd party emotes (loaded async)
	a.emotes = twitch.NewThirdPartyEmotes()
	go a.emotes.Load(cfg.Twitch.BotUserID)

	// Twitch native badges registry (loaded inside bootSequence after auth is valid)
	a.badges = twitch.NewBadgeRegistry()

	// Start connection sequence
	go a.bootSequence()
}

// shutdown is called when the app exits.
func (a *App) shutdown(_ context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ircClient != nil {
		a.ircClient.Disconnect()
	}
	if a.ytChatCancel != nil {
		a.ytChatCancel()
	}
}

// bootSequence handles auth + initial connection.
func (a *App) bootSequence() {
	if a.cfg == nil {
		return
	}

	if a.cfg.Twitch.AccessToken == "" || a.cfg.Twitch.ClientID == "" {
		a.emitSystem("Authentication required - please run the CLI version first to authenticate")
		return
	}

	info, err := twitch.ValidateToken(a.cfg.Twitch.AccessToken)
	if err != nil {
		log.Printf("[AUTH] Validate failed: %v", err)
		if a.cfg.Twitch.RefreshToken != "" {
			log.Printf("[AUTH] Attempting refresh...")
			if token, refErr := twitch.RefreshAccessToken(a.cfg.Twitch.ClientID, a.cfg.Twitch.ClientSecret, a.cfg.Twitch.RefreshToken); refErr == nil {
				a.cfg.Twitch.AccessToken = token.AccessToken
				a.cfg.Twitch.RefreshToken = token.RefreshToken
				a.cfg.Save()
				if validInfo, vErr := twitch.ValidateToken(token.AccessToken); vErr == nil {
					info = validInfo
					log.Printf("[AUTH] Refreshed and validated as %s", validInfo.Login)
				} else {
					log.Printf("[AUTH] Validate after refresh failed: %v", vErr)
				}
			} else {
				log.Printf("[AUTH] Refresh failed: %v", refErr)
			}
		}
	} else {
		log.Printf("[AUTH] Token valid for %s", info.Login)
		// Proactively refresh even on validate-success so the new session
		// starts with a freshly-issued token. Failure here is non-fatal —
		// the validated token is still good for its remaining TTL.
		if a.cfg.Twitch.RefreshToken != "" {
			if tok, rerr := twitch.RefreshAccessToken(a.cfg.Twitch.ClientID, a.cfg.Twitch.ClientSecret, a.cfg.Twitch.RefreshToken); rerr == nil {
				a.cfg.Twitch.AccessToken = tok.AccessToken
				a.cfg.Twitch.RefreshToken = tok.RefreshToken
				a.cfg.Save()
				log.Printf("[AUTH] Token refreshed at boot")
			} else {
				log.Printf("[AUTH] Boot refresh failed (non-fatal): %v", rerr)
			}
		}
	}
	// If after validate + refresh we still have nothing usable, surface this
	// to the frontend and don't spin up IRC/Helix with a dead token (which
	// would just hammer Twitch with 401s forever).
	if info == nil {
		log.Printf("[AUTH] No valid token after refresh — emitting authExpired, awaiting re-login")
		a.emitSystem("Authentication expired - please re-login via the settings gear")
		runtime.EventsEmit(a.ctx, "authExpired", nil)
		return
	}
	if info != nil {
		a.cfg.Twitch.Username = info.Login
		a.cfg.Twitch.BotUserID = info.UserID
	}
	if a.cfg.Twitch.Channel == "" {
		a.cfg.Twitch.Channel = a.cfg.Twitch.Username
		a.cfg.Save()
	}

	a.helixClient, _ = twitch.NewHelixClient(a.cfg.Twitch.ClientID, a.cfg.Twitch.AccessToken, a.cfg.Twitch.BotUserID, a.cfg.Twitch.BotUserID)

	// Load badges now that auth is valid (was previously a race against bootSequence).
	a.loadBadges()

	go a.runIRCSupervisor()
	go a.tokenRefreshLoop()

	if a.helixClient != nil {
		go a.loadChattersAndRoles()
	}

	if a.cfg.YouTube.VideoID != "" {
		a.startYouTubeChat(a.cfg.YouTube.VideoID)
	} else if a.cfg.YouTube.ChannelHandle != "" {
		go a.startYouTubeAutoDetect(a.cfg.YouTube.ChannelHandle)
	}

	runtime.EventsEmit(a.ctx, "ready", map[string]interface{}{
		"channel":        a.cfg.Twitch.Channel,
		"username":       a.cfg.Twitch.Username,
		"youtube":        a.cfg.YouTube.ChannelHandle != "" || a.cfg.YouTube.VideoID != "",
		"showTimestamps": a.cfg.UI.ShowTimestamps,
	})
}

func (a *App) runIRCSupervisor() {
	backoff := 2 * time.Second
	maxBackoff := 60 * time.Second

	for {
		select {
		case <-a.ctx.Done():
			return
		default:
		}

		client := twitch.NewIRCClient(a.cfg.Twitch.Username, a.cfg.Twitch.AccessToken, a.cfg.Twitch.Channel)
		if a.helixClient != nil {
			client.SetRoomResolver(func(roomID string) string {
				if name, err := a.helixClient.GetChannelName(roomID); err == nil {
					return name
				}
				return roomID
			})
		}
		a.mu.Lock()
		a.ircClient = client
		a.mu.Unlock()

		clientCtx, cancelClient := context.WithCancel(a.ctx)
		go a.forwardIRC(clientCtx, client)

		log.Printf("[IRC] Connecting to #%s", a.cfg.Twitch.Channel)
		err := client.Connect()
		cancelClient()
		client.Disconnect()

		if a.ctx.Err() != nil {
			return
		}

		log.Printf("[IRC] Disconnected: %v", err)
		errStr := ""
		if err != nil {
			errStr = strings.ToLower(err.Error())
		}
		isAuth := strings.Contains(errStr, "login") ||
			strings.Contains(errStr, "auth") ||
			strings.Contains(errStr, "improperly formatted") ||
			strings.Contains(errStr, "invalid")

		if isAuth && a.cfg.Twitch.RefreshToken != "" {
			if token, refErr := twitch.RefreshAccessToken(a.cfg.Twitch.ClientID, a.cfg.Twitch.ClientSecret, a.cfg.Twitch.RefreshToken); refErr == nil {
				a.cfg.Twitch.AccessToken = token.AccessToken
				a.cfg.Twitch.RefreshToken = token.RefreshToken
				a.cfg.Save()
				backoff = 2 * time.Second
				continue
			}
		}

		select {
		case <-a.ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (a *App) forwardIRC(ctx context.Context, client *twitch.IRCClient) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-client.Messages():
			if !ok {
				return
			}
			a.emitChat(msg)
		case jp, ok := <-client.Joins():
			if !ok {
				return
			}
			a.emitJoinPart(jp)
		case settings, ok := <-client.Settings():
			if !ok {
				return
			}
			runtime.EventsEmit(a.ctx, "settings", settings)
		case err, ok := <-client.Errors():
			if !ok {
				return
			}
			a.emitSystem(fmt.Sprintf("IRC error: %v", err))
		}
	}
}

// tokenRefreshLoop refreshes the access token every 30 minutes. See chathub
// for the rationale (TTL can be <1h so a 60min cadence races).
func (a *App) tokenRefreshLoop() {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
		if a.cfg.Twitch.RefreshToken == "" {
			continue
		}
		token, err := twitch.RefreshAccessToken(a.cfg.Twitch.ClientID, a.cfg.Twitch.ClientSecret, a.cfg.Twitch.RefreshToken)
		if err != nil {
			log.Printf("[AUTH] Token refresh failed: %v", err)
			continue
		}
		a.cfg.Twitch.AccessToken = token.AccessToken
		a.cfg.Twitch.RefreshToken = token.RefreshToken
		a.cfg.Save()
		log.Printf("[AUTH] Token refreshed")
		// Reload badges with the fresh token in case the previous load failed.
		a.loadBadges()
	}
}

func (a *App) loadChattersAndRoles() {
	chatters, _ := a.helixClient.GetChatters()
	entries := make([]map[string]string, 0, len(chatters))
	for _, c := range chatters {
		entries = append(entries, map[string]string{
			"username": c.UserLogin,
			"userId":   c.UserID,
		})
	}
	runtime.EventsEmit(a.ctx, "chatters", entries)

	roles := map[string]interface{}{
		"broadcaster": a.cfg.Twitch.Channel,
	}
	if mods, err := a.helixClient.GetModerators(); err == nil {
		roles["mods"] = mods
	}
	if vips, err := a.helixClient.GetVIPs(); err == nil {
		roles["vips"] = vips
	}
	var botNames []string
	for _, c := range chatters {
		if a.botDetector.IsBot(c.UserLogin) {
			botNames = append(botNames, c.UserLogin)
		}
	}
	roles["bots"] = botNames
	runtime.EventsEmit(a.ctx, "roles", roles)

	// Refresh roles every 5 minutes
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
		updated := map[string]interface{}{"broadcaster": a.cfg.Twitch.Channel}
		if mods, err := a.helixClient.GetModerators(); err == nil {
			updated["mods"] = mods
		}
		if vips, err := a.helixClient.GetVIPs(); err == nil {
			updated["vips"] = vips
		}
		runtime.EventsEmit(a.ctx, "roles", updated)
	}
}

func (a *App) startYouTubeChat(videoID string) {
	a.mu.Lock()
	if a.ytChatCancel != nil {
		a.ytChatCancel()
	}
	chatCtx, cancel := context.WithCancel(a.ctx)
	a.ytChatCancel = cancel
	a.mu.Unlock()

	ytClient := youtube.NewInnerTubeClient(videoID)

	go func() {
		for {
			select {
			case <-chatCtx.Done():
				return
			case msg := <-ytClient.Messages():
				a.emitChat(msg)
			case err := <-ytClient.Errors():
				log.Printf("[YT] %v", err)
			}
		}
	}()

	go func() {
		for attempt := 0; attempt < 5; attempt++ {
			if chatCtx.Err() != nil {
				return
			}
			if err := ytClient.Connect(chatCtx); err != nil {
				select {
				case <-chatCtx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				continue
			}
			a.emitSystem(fmt.Sprintf("Connected to YouTube chat (video: %s)", videoID))
			return
		}
	}()
}

func (a *App) startYouTubeAutoDetect(handle string) {
	detector := youtube.NewLiveDetector(
		handle,
		func(info youtube.LiveStreamInfo) {
			title := info.Title
			if title == "" {
				title = info.VideoID
			}
			a.emitSystem(fmt.Sprintf("YouTube stream detected: %s", title))
			a.startYouTubeChat(info.VideoID)
		},
		func() {
			a.mu.Lock()
			if a.ytChatCancel != nil {
				a.ytChatCancel()
				a.ytChatCancel = nil
			}
			a.mu.Unlock()
			a.emitSystem("YouTube stream ended")
		},
	)
	detector.Start(a.ctx)
}

// === Event emitters ===

func (a *App) emitChat(msg chat.Message) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "chat", a.chatToMap(msg))
}

func (a *App) emitJoinPart(jp chat.UserJoinPart) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "joinPart", map[string]interface{}{
		"username": jp.Username,
		"isJoin":   jp.IsJoin,
		"platform": string(jp.Platform),
	})
}

func (a *App) emitSystem(text string) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "chat", map[string]interface{}{
		"type":      "system",
		"text":      text,
		"timestamp": time.Now().UnixMilli(),
		"platform":  "twitch",
	})
}

func (a *App) chatToMap(msg chat.Message) map[string]interface{} {
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
	case chat.MessageTypeSuperChat:
		typeStr = "superchat"
	case chat.MessageTypeMembership:
		typeStr = "membership"
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
		"id":              msg.ID,
		"platform":        string(msg.Platform),
		"type":            typeStr,
		"channel":         msg.Channel,
		"timestamp":       ts.UnixMilli(),
		"userId":          msg.UserID,
		"username":        msg.Username,
		"displayName":     msg.DisplayName,
		"color":           msg.Color,
		"isMod":           msg.IsMod,
		"isVIP":           msg.IsVIP,
		"isSub":           msg.IsSub,
		"isBroadcaster":   msg.IsBroadcaster,
		"text":            msg.Text,
		"twitchEmotes":    emoteRanges,
		"badges":          badges,
		"bits":            msg.Bits,
		"sourceChannel":   msg.SourceChannel,
		"isSharedChat":    msg.IsSharedChat,
		"superChatAmount": msg.SuperChatAmount,
	}
}

// === Methods bound to frontend ===

// CheckUpdate / ApplyUpdate / GetVersion — self-updater wiring identical
// to chathub's; the only difference is the asset name we look for.
func (a *App) CheckUpdate() map[string]interface{} {
	out := map[string]interface{}{
		"current":   version.Version,
		"available": false,
	}
	channel := a.cfg.UI.UpdateChannel
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
	out["downloadUrl"] = selfupdate.FindAsset(rel, "streamerchat-gui.exe")
	return out
}

// GetUpdateChannel returns the saved channel for the settings UI.
func (a *App) GetUpdateChannel() string {
	if a.cfg.UI.UpdateChannel == "" {
		return "stable"
	}
	return a.cfg.UI.UpdateChannel
}

// SetUpdateChannel persists the chosen update channel. Accepts "stable",
// "beta", or "alpha"; everything else collapses to "stable".
func (a *App) SetUpdateChannel(channel string) string {
	c := strings.ToLower(strings.TrimSpace(channel))
	if c != "stable" && c != "beta" && c != "alpha" {
		c = "stable"
	}
	a.cfg.UI.UpdateChannel = c
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	return ""
}

// GetFontSize returns the chat font size in pixels (10–28), defaulting to 14.
func (a *App) GetFontSize() int {
	if a.cfg.UI.FontSize < 10 || a.cfg.UI.FontSize > 28 {
		return 14
	}
	return a.cfg.UI.FontSize
}

// SetFontSize persists the chat font size, clamped to [10, 28].
func (a *App) SetFontSize(px int) string {
	if px < 10 {
		px = 10
	}
	if px > 28 {
		px = 28
	}
	a.cfg.UI.FontSize = px
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	return ""
}

// GetLocale returns the active UI locale ("en" or "de"), defaulting "en".
func (a *App) GetLocale() string {
	l := strings.ToLower(strings.TrimSpace(a.cfg.UI.Locale))
	if l != "de" {
		return "en"
	}
	return l
}

// SetLocale persists the chosen UI locale. Anything other than "de"
// collapses to "en".
func (a *App) SetLocale(loc string) string {
	l := strings.ToLower(strings.TrimSpace(loc))
	if l != "de" {
		l = "en"
	}
	a.cfg.UI.Locale = l
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	return ""
}

// GetShowJoinPart returns whether join/part messages should be rendered
// in chat. Default is true for fresh installs and configs that don't
// have the field — the legacy gui shipped this enabled.
func (a *App) GetShowJoinPart() bool {
	return a.cfg.UI.ShowJoinPart
}

// SetShowJoinPart persists the toggle.
func (a *App) SetShowJoinPart(v bool) string {
	a.cfg.UI.ShowJoinPart = v
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	return ""
}

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

func (a *App) GetVersion() string { return version.Version }

// SetShowTimestamps toggles the always-visible per-message timestamp column.
func (a *App) SetShowTimestamps(show bool) {
	a.cfg.UI.ShowTimestamps = show
	a.cfg.Save()
}

// === Per-message chat sound ===

// GetChatSoundConfig returns the persisted chat-sound preferences.
func (a *App) GetChatSoundConfig() map[string]interface{} {
	return map[string]interface{}{
		"enabled": a.cfg.UI.ChatSoundEnabled,
		"file":    a.cfg.UI.ChatSoundFile,
		"volume":  a.cfg.UI.ChatSoundVolume,
	}
}

// SetChatSoundEnabled toggles the per-message sound playback.
func (a *App) SetChatSoundEnabled(enabled bool) {
	a.cfg.UI.ChatSoundEnabled = enabled
	a.cfg.Save()
}

// SetChatSoundVolume stores a 0-100 volume value for the playback.
func (a *App) SetChatSoundVolume(volume int) {
	if volume < 0 {
		volume = 0
	}
	if volume > 100 {
		volume = 100
	}
	a.cfg.UI.ChatSoundVolume = volume
	a.cfg.Save()
}

// PickChatSoundFile opens a native file dialog limited to .wav and stores
// the chosen path. Returns the absolute path (or empty if the user cancelled).
func (a *App) PickChatSoundFile() string {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select a .wav file",
		Filters: []runtime.FileFilter{
			{DisplayName: "WAV audio (*.wav)", Pattern: "*.wav"},
		},
	})
	if err != nil {
		log.Printf("[SOUND] file dialog error: %v", err)
		return ""
	}
	if path == "" {
		return a.cfg.UI.ChatSoundFile
	}
	a.cfg.UI.ChatSoundFile = path
	a.cfg.Save()
	log.Printf("[SOUND] chat sound file set to %s", path)
	return path
}

// ClearChatSoundFile drops the saved path so playback falls back to the
// built-in beep.
func (a *App) ClearChatSoundFile() {
	a.cfg.UI.ChatSoundFile = ""
	a.cfg.Save()
}

// LoadChatSoundData reads the saved (or supplied) wav file from disk and
// returns it as a data: URL the <audio> tag can play directly. WebView2
// won't load arbitrary file:// URLs from the embedded asset origin, so we
// shovel the bytes through the IPC bridge.
func (a *App) LoadChatSoundData() string {
	path := a.cfg.UI.ChatSoundFile
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[SOUND] read %s failed: %v", path, err)
		return ""
	}
	return "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(data)
}

// === YouTube settings ===

// GetYouTubeConfig returns the saved YouTube channel config so the frontend
// can show what's already set.
func (a *App) GetYouTubeConfig() map[string]interface{} {
	return map[string]interface{}{
		"enabled":       a.cfg.YouTube.Enabled,
		"channelHandle": a.cfg.YouTube.ChannelHandle,
		"channelId":     a.cfg.YouTube.ChannelID,
		"videoId":       a.cfg.YouTube.VideoID,
	}
}

// SetYouTubeConfig persists the YouTube section and (re)starts the live
// auto-detect goroutine if enabled+handle are present. Returns an empty
// string on success, or an error message.
func (a *App) SetYouTubeConfig(enabled bool, channelHandle string) string {
	handle := strings.TrimSpace(channelHandle)
	if enabled && handle == "" {
		return "channel handle required when YouTube is enabled"
	}
	// Normalize: prepend "@" if the user didn't.
	if handle != "" && !strings.HasPrefix(handle, "@") {
		handle = "@" + handle
	}
	a.cfg.YouTube.Enabled = enabled
	a.cfg.YouTube.ChannelHandle = handle
	// Channel ID is derived from the handle on demand; wipe any stale value
	// so the auto-detect loop re-resolves on the new handle.
	if a.cfg.YouTube.ChannelHandle != "" {
		a.cfg.YouTube.ChannelID = ""
	}
	a.cfg.Save()
	log.Printf("[YT] config updated: enabled=%v handle=%q", enabled, handle)

	// Tear down any running YT goroutine and respawn if appropriate.
	if a.ytChatCancel != nil {
		a.ytChatCancel()
		a.ytChatCancel = nil
	}
	if enabled && handle != "" {
		go a.startYouTubeAutoDetect(handle)
	}
	return ""
}

// === Auth flow (Device Code) — re-login UI for when refresh tokens die ===

// StartLogin starts the Twitch device-code flow and returns the verification
// URL + user code for the frontend to display. Polling happens in a goroutine
// and emits "loginResult" when it finishes.
func (a *App) StartLogin() map[string]interface{} {
	if a.cfg.Twitch.ClientID == "" {
		return map[string]interface{}{"error": "no client_id configured"}
	}
	device, err := twitch.RequestDeviceCode(a.cfg.Twitch.ClientID)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}

	go func() {
		token, err := twitch.PollForToken(a.cfg.Twitch.ClientID, device.DeviceCode, device.Interval)
		if err != nil {
			runtime.EventsEmit(a.ctx, "loginResult", map[string]interface{}{"error": err.Error()})
			return
		}
		info, err := twitch.ValidateToken(token.AccessToken)
		if err != nil {
			runtime.EventsEmit(a.ctx, "loginResult", map[string]interface{}{"error": err.Error()})
			return
		}
		a.cfg.Twitch.AccessToken = token.AccessToken
		a.cfg.Twitch.RefreshToken = token.RefreshToken
		a.cfg.Twitch.Username = info.Login
		a.cfg.Twitch.BotUserID = info.UserID
		if a.cfg.Twitch.Channel == "" {
			a.cfg.Twitch.Channel = info.Login
		}
		a.cfg.Save()
		log.Printf("[AUTH] Device-code login complete for %s", info.Login)

		// Rebuild helix client and (re)start everything that depends on auth.
		// On a fresh-install or post-revocation startup, bootSequence will
		// have bailed out before starting these — so we (re)kick them here.
		// Calling them again after a normal startup is idempotent enough
		// (channelEmotesLoaded gates per-channel work, supervisor loops are
		// single instances by construction).
		a.helixClient, _ = twitch.NewHelixClient(a.cfg.Twitch.ClientID, a.cfg.Twitch.AccessToken, a.cfg.Twitch.BotUserID, a.cfg.Twitch.BotUserID)
		a.loadBadges()

		// Tear down any existing IRC client so the supervisor reconnects
		// with the fresh credentials, or starts cold if none was running.
		a.mu.Lock()
		hadSupervisor := a.ircClient != nil
		if a.ircClient != nil {
			a.ircClient.Disconnect()
		}
		a.mu.Unlock()
		if !hadSupervisor {
			go a.runIRCSupervisor()
			go a.tokenRefreshLoop()
		}
		if a.helixClient != nil {
			go a.loadChattersAndRoles()
		}
		// YouTube wasn't started in bootSequence either if we bailed early.
		if a.ytChatCancel == nil {
			if a.cfg.YouTube.VideoID != "" {
				a.startYouTubeChat(a.cfg.YouTube.VideoID)
			} else if a.cfg.YouTube.Enabled && a.cfg.YouTube.ChannelHandle != "" {
				go a.startYouTubeAutoDetect(a.cfg.YouTube.ChannelHandle)
			}
		}

		// Re-emit ready so the frontend picks up the new username / channel
		// and any UI that gates on it (TW/YT pills, login pill) updates.
		runtime.EventsEmit(a.ctx, "ready", map[string]interface{}{
			"channel":        a.cfg.Twitch.Channel,
			"username":       a.cfg.Twitch.Username,
			"youtube":        a.cfg.YouTube.ChannelHandle != "" || a.cfg.YouTube.VideoID != "",
			"showTimestamps": a.cfg.UI.ShowTimestamps,
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

// OpenURL opens a URL in the user's default browser. Used by the login flow.
func (a *App) OpenURL(url string) {
	runtime.BrowserOpenURL(a.ctx, url)
}

// Logout clears auth state. Caller is expected to call StartLogin to recover.
func (a *App) Logout() {
	a.cfg.Twitch.AccessToken = ""
	a.cfg.Twitch.RefreshToken = ""
	a.cfg.Save()
	a.mu.Lock()
	if a.ircClient != nil {
		a.ircClient.Disconnect()
	}
	a.mu.Unlock()
	log.Printf("[AUTH] Logged out")
}

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
// empty string on success, or the error message on failure.
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


// LookupBadge returns the image URL for a badge in the context of `channel`.
// Channel-specific artwork (sub tiers, custom badges) wins over global.
// streamerchat only watches a single primary channel, but signature is kept
// uniform with chathub for the frontend.
func (a *App) LookupBadge(channel, setID, version string) string {
	if a.badges == nil {
		return ""
	}
	return a.badges.Lookup(a.cfg.Twitch.BotUserID, setID, version)
}

// SendMessage sends a chat message to Twitch IRC.
// Twitch does not echo PRIVMSGs back to the sending connection, so we also
// emit a synthetic local-echo event so the user sees their own message.
func (a *App) SendMessage(text string) {
	if text == "" {
		return
	}
	a.mu.Lock()
	c := a.ircClient
	a.mu.Unlock()
	if c == nil {
		return
	}
	c.Say(text)

	username := a.cfg.Twitch.Username
	channel := a.cfg.Twitch.Channel
	a.emitChat(chat.Message{
		Platform:      chat.PlatformTwitch,
		Type:          chat.MessageTypeChat,
		Channel:       channel,
		Timestamp:     time.Now(),
		UserID:        a.cfg.Twitch.BotUserID,
		Username:      username,
		DisplayName:   username,
		IsBroadcaster: strings.EqualFold(username, channel),
		Text:          text,
	})
}

// loadBadges fetches global + channel badges via Helix. Safe to call multiple
// times (e.g. after a token refresh). Logs success/failure.
func (a *App) loadBadges() {
	if a.cfg.Twitch.AccessToken == "" || a.cfg.Twitch.ClientID == "" {
		log.Printf("[BADGES] skipped (no auth)")
		return
	}
	if err := a.badges.LoadGlobal(a.cfg.Twitch.ClientID, a.cfg.Twitch.AccessToken); err != nil {
		log.Printf("[BADGES] global load failed: %v", err)
	}
	if a.cfg.Twitch.BotUserID != "" {
		if err := a.badges.LoadChannel(a.cfg.Twitch.BotUserID, a.cfg.Twitch.ClientID, a.cfg.Twitch.AccessToken); err != nil {
			log.Printf("[BADGES] channel load failed: %v", err)
		}
	}
	log.Printf("[BADGES] loaded %d sets", a.badges.Count())
}

// LookupEmote returns 3rd-party emote info for a name.
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

// GetUserInfo fetches user details + follow status.
func (a *App) GetUserInfo(userID string) map[string]interface{} {
	if a.helixClient == nil || userID == "" {
		return nil
	}
	info, err := a.helixClient.GetUserInfo(userID)
	if err != nil {
		return nil
	}
	return map[string]interface{}{
		"userId":      info.UserID,
		"login":       info.Login,
		"displayName": info.DisplayName,
		"description": info.Description,
		"createdAt":   info.CreatedAt,
		"accountAge":  info.AccountAge,
		"isFollower":  info.IsFollower,
		"followedAt":  info.FollowedAt,
		"followAge":   info.FollowAge,
		"isBot":       a.botDetector != nil && a.botDetector.IsBot(info.Login),
	}
}

// ModAction performs a moderation action.
func (a *App) ModAction(action, userID, msgID string) string {
	if a.helixClient == nil {
		return "no helix client"
	}
	var err error
	switch action {
	case "ban":
		err = a.helixClient.BanUser(userID, "Banned via StreamerChat")
	case "timeout_600":
		err = a.helixClient.TimeoutUser(userID, 600, "Timed out")
	case "timeout_3600":
		err = a.helixClient.TimeoutUser(userID, 3600, "Timed out")
	case "timeout_86400":
		err = a.helixClient.TimeoutUser(userID, 86400, "Timed out")
	case "delete":
		if msgID != "" {
			err = a.helixClient.DeleteMessage(msgID)
		}
	case "unban":
		err = a.helixClient.UnbanUser(userID)
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

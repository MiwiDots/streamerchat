package main

import (
	"context"
	"errors"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/miwi/streamerchat/internal/applog"
	"github.com/miwi/streamerchat/internal/chat"
	"github.com/miwi/streamerchat/internal/config"
	"github.com/miwi/streamerchat/internal/eventsub"
	"github.com/miwi/streamerchat/internal/selfupdate"
	"github.com/miwi/streamerchat/internal/sevenTV"
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

	// Per-profile background work. All guarded by mu, all cancelled in
	// SwitchProfile — they used to run on a.ctx forever, so every profile
	// switch / login / YouTube settings change leaked another copy, and a
	// stale YouTube detector could take over the current profile's chat.
	ytChatCancel   context.CancelFunc // running InnerTube chat poller
	ytClient       *youtube.InnerTubeClient // the live chat being read; needed to send
	ytDetectCancel context.CancelFunc // running YouTube live detector
	rolesCancel    context.CancelFunc // Helix chatter/role poller

	// 7TV cosmetics over EventAPI: subscribes to entitlement events for
	// this streamer's Twitch channel so paint/badge updates can be
	// pushed to the frontend and applied to chat messages.
	stv *sevenTV.Client

	// EventSub WebSocket for the activity feed (follow / sub /
	// resub / gift / raid / cheer / channel-points redemption /
	// hype train). Per-profile: canceled + restarted on profile
	// switch so the subscriptions run against the active token.
	esCancel context.CancelFunc
	// esClient is the running EventSub session (nil when none), kept so
	// token refreshes can hand it the new access token. Guarded by mu.
	esClient *eventsub.Client

	// supervisorsStarted gates the eternal token refresh loop to one
	// launch per app process (started after the first successful auth).
	// The IRC read supervisor is started unconditionally in startup.
	supervisorsStarted bool

	// sendClient is an authenticated, send-only IRC connection used for
	// /me (CTCP ACTION has no Helix equivalent) and as a fallback when a
	// Helix send is not authorized. Created lazily; sendClientKey records
	// the user/token/channel it was built with so a token refresh or
	// profile switch rebuilds it. Guarded by mu.
	sendClient    *twitch.IRCClient
	sendClientKey string

	// helixSendHintOnce: the "re-login for the new chat permission" hint
	// is shown once per session.
	helixSendHintOnce sync.Once

	// Startup event buffer (see emit / FrontendReady in lifecycle.go).
	uiMu      sync.Mutex
	uiReady   bool
	uiPending []uiEvent
}

func NewApp() *App {
	return &App{}
}

// startup is called when the app starts.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Log into the config dir (always writable), keeping the previous run
	// as streamerchat.prev.log; next to the exe only as a fallback.
	var logDirs []string
	if p, err := config.ConfigPath(); err == nil {
		logDirs = append(logDirs, filepath.Dir(p))
	}
	if exe, err := os.Executable(); err == nil {
		logDirs = append(logDirs, filepath.Dir(exe))
	}
	logPath := applog.Setup("streamerchat", logDirs...)
	log.Printf("[BOOT] StreamerChat v%s (Wails) started, log: %s", version.Version, logPath)

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
	// Globals now; the channel set is loaded in bootSequence once the
	// account (and thus the channel id) is validated — per profile.
	go a.emotes.LoadGlobal()

	// Twitch native badges registry (loaded inside bootSequence after auth is valid)
	a.badges = twitch.NewBadgeRegistry()

	// Chat is read anonymously, so it can start right away — independent
	// of the login check below, which used to gate it (no network at boot
	// or any auth hiccup meant no chat at all).
	go a.runIRCSupervisor()

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
	a.dropSendClientLocked()
	if a.ytChatCancel != nil {
		a.ytChatCancel()
	}
}

// bootSequence handles auth + initial connection.
//
// Profile-switch safety: every network call here can take seconds, and
// SwitchProfile starts a NEW bootSequence for the incoming profile. All
// writes back into cfg therefore go through commitTokens / the locked
// block below, which drop the result if the profile changed meanwhile —
// otherwise profile A's tokens would be saved into profile B's slot (and
// A's single-use refresh token would be lost).
func (a *App) bootSequence() {
	if a.cfg == nil {
		return
	}

	a.mu.Lock()
	profileID := a.cfg.ActiveProfileID
	clientID := a.cfg.Twitch.ClientID
	clientSecret := a.cfg.Twitch.ClientSecret
	accessToken := a.cfg.Twitch.AccessToken
	refreshToken := a.cfg.Twitch.RefreshToken
	a.mu.Unlock()

	if accessToken == "" || clientID == "" {
		a.emitSystem("This profile has no Twitch token yet. Open Settings → Twitch and click Login to authenticate.")
		a.emit("authExpired", nil)
		return
	}

	info, accessToken, outcome := a.authenticate(profileID, clientID, clientSecret, accessToken, refreshToken)
	switch outcome {
	case authStale:
		log.Printf("[AUTH] Profile switched / app closing during boot — aborting stale boot for %s", profileID)
		return
	case authExpired:
		// Twitch definitively rejected the tokens: don't spin up IRC/Helix
		// with a dead token (it would just hammer Twitch with 401s).
		log.Printf("[AUTH] Tokens rejected by Twitch — emitting authExpired, awaiting re-login")
		a.emitSystem("Authentication expired - please re-login via the settings gear")
		a.emit("authExpired", nil)
		return
	}

	a.mu.Lock()
	if a.cfg.ActiveProfileID != profileID {
		a.mu.Unlock()
		log.Printf("[AUTH] Profile switched during boot — aborting stale boot for %s", profileID)
		return
	}
	a.cfg.Twitch.Username = info.Login
	a.cfg.Twitch.BotUserID = info.UserID
	if a.cfg.Twitch.Channel == "" {
		a.cfg.Twitch.Channel = info.Login
	}
	a.cfg.Save()
	a.helixClient, _ = twitch.NewHelixClient(clientID, accessToken, info.UserID, info.UserID)
	a.mu.Unlock()

	// Channel 7TV/BTTV/FFZ emotes for this profile. Used to be loaded only
	// once at startup, so after a profile switch (or a first login) the
	// channel's emotes never showed up.
	if !a.emotes.HasChannel(info.UserID) {
		go a.emotes.LoadChannel(info.UserID)
	}

	// EventSub: per-profile, started here after auth so subscribes
	// go out with the fresh access token. Any previous session for
	// a switched-out profile gets canceled first.
	a.startEventSub()

	// 7TV cosmetics: spin the EventAPI client once we know the
	// broadcaster id (single-channel app, so this fires once).
	if a.stv == nil && a.cfg.Twitch.BotUserID != "" {
		a.stv = sevenTV.NewClient(a.ctx, func(c sevenTV.Cosmetic) {
			a.emit("sevenTVCosmetic", map[string]interface{}{
				"userID":    c.UserID,
				"paintCSS":  c.PaintCSS,
				"badgeURL":  c.BadgeURL,
				"badgeName": c.BadgeName,
			})
		})
		a.stv.Start()
		a.stv.AddChannel(a.cfg.Twitch.BotUserID)
	}

	// Load badges now that auth is valid (was previously a race against bootSequence).
	a.loadBadges()

	// Token refresh loop: eternal, one per process. (The IRC read
	// supervisor already runs since startup and needs no login.)
	a.mu.Lock()
	if !a.supervisorsStarted {
		a.supervisorsStarted = true
		go a.tokenRefreshLoop()
	}
	a.mu.Unlock()

	a.startChattersAndRoles()
	a.startYouTube()

	a.emit("ready", map[string]interface{}{
		"channel":        a.cfg.Twitch.Channel,
		"username":       a.cfg.Twitch.Username,
		"youtube":        (a.cfg.YouTube.Enabled && a.cfg.YouTube.ChannelHandle != "") || a.cfg.YouTube.VideoID != "",
		"showTimestamps": a.cfg.UI.ShowTimestamps,
	})
}

// === Multi-account profiles ===
//
// Each profile is a self-contained Twitch+YouTube account context.
// The active profile's credentials are mirrored into cfg.Twitch /
// cfg.YouTube so the rest of the app code doesn't have to know about
// profiles. SwitchProfile teardowns the active connections + reboots
// the auth/connection sequence so the new profile takes over.

// profileSummary is the shape sent down to the frontend dropdown.
type profileSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"` // last validated Twitch login (cosmetic)
	Active   bool   `json:"active"`
}

// ListProfiles returns every saved account profile + which one is
// active. Username is the last known Twitch login for that profile
// — empty if the slot has never been logged in.
func (a *App) ListProfiles() []profileSummary {
	out := make([]profileSummary, 0, len(a.cfg.Profiles))
	for _, p := range a.cfg.Profiles {
		out = append(out, profileSummary{
			ID:       p.ID,
			Name:     p.Name,
			Username: p.Twitch.Username,
			Active:   p.ID == a.cfg.ActiveProfileID,
		})
	}
	return out
}

// AddProfile creates an empty profile with the given display name +
// inherits the build-time default Twitch client id so the new slot
// can immediately run the device-code flow when the user clicks
// Login. Returns the new profile id.
func (a *App) AddProfile(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Account"
	}
	p := a.cfg.AddProfile(name)
	// New profiles inherit the client id so login works out of the
	// box. Tokens stay empty until the user runs the device flow.
	for i := range a.cfg.Profiles {
		if a.cfg.Profiles[i].ID == p.ID {
			a.cfg.Profiles[i].Twitch.ClientID = defaultClientID
			break
		}
	}
	_ = a.cfg.Save()
	return p.ID
}

// RemoveProfile drops a profile. Can't remove the active one — the
// frontend should switch first. Always keeps ≥1 profile alive.
func (a *App) RemoveProfile(id string) string {
	if err := a.cfg.RemoveProfile(id); err != nil {
		return err.Error()
	}
	_ = a.cfg.Save()
	return ""
}

// RenameProfile updates a profile's display name. ID stays so the
// profile-scoped storage (future per-profile YT keyring etc.) is
// unaffected.
func (a *App) RenameProfile(id, newName string) string {
	if err := a.cfg.RenameProfile(id, strings.TrimSpace(newName)); err != nil {
		return err.Error()
	}
	_ = a.cfg.Save()
	return ""
}

// SwitchProfile flips the active account, persists, and tears down
// the current Twitch+YT connections so the IRC supervisor reconnects
// with the new credentials. The frontend gets a "profileSwitched"
// event so it can clear chat view + user list + badge cache.
func (a *App) SwitchProfile(id string) string {
	// The switch itself happens under a.mu: commitTokens and the boot /
	// login goroutines compare ActiveProfileID under the same lock, so
	// they either finish their write before the switch or see the new id
	// and discard their (now foreign) tokens.
	a.mu.Lock()
	if id == a.cfg.ActiveProfileID {
		a.mu.Unlock()
		return ""
	}
	if err := a.cfg.SwitchProfile(id); err != nil {
		a.mu.Unlock()
		return err.Error()
	}
	if err := a.cfg.Save(); err != nil {
		log.Printf("[PROFILE] save after switch failed: %v", err)
	}

	// Tear down old connections. The supervisor's Connect() returns
	// from Disconnect() and the next reconnect picks up the new
	// cfg.Twitch.*. YT gets re-detected from the new profile's
	// channel handle.
	if a.ircClient != nil {
		a.ircClient.Disconnect() // reader reconnects to the new profile's channel
	}
	a.dropSendClientLocked()
	a.stopYouTubeLocked()
	if a.rolesCancel != nil {
		a.rolesCancel()
		a.rolesCancel = nil
	}
	if a.esCancel != nil {
		a.esCancel()
		a.esCancel = nil
		a.esClient = nil
	}
	a.helixClient = nil
	a.mu.Unlock()
	// Frontend's activity list is per-profile — tell it to reset
	// so the incoming profile starts with a clean feed.
	a.emit("activityReset", nil)

	// New 7TV client per profile would mean a new EventAPI WS — but
	// the existing one is a single shared connection that fans out
	// per-user lookups, so keeping it works fine across profile
	// switches (per-user cache only grows).

	// Notify the UI so it clears chat + user list before reconnect
	// chatter starts streaming in.
	a.emit("profileSwitched", map[string]interface{}{
		"id":   id,
		"name": a.activeProfileName(),
	})

	// Re-run the auth + connection sequence in the background. The
	// IRC supervisor is already running — bootSequence's gate stops
	// us from spawning a duplicate.
	go a.bootSequence()
	return ""
}

func (a *App) activeProfileName() string {
	for _, p := range a.cfg.Profiles {
		if p.ID == a.cfg.ActiveProfileID {
			return p.Name
		}
	}
	return ""
}

// IRC supervisor reconnect timing. A connection that stayed up at least
// ircHealthySessionMin resets the backoff; before, it only reset after a
// token refresh, so after ~5 blips (or a few profile switches, which
// disconnect on purpose) every reconnect waited the full minute.
const (
	ircBackoffMin        = 2 * time.Second
	ircBackoffMax        = 60 * time.Second
	ircHealthySessionMin = 30 * time.Second
	// ircChannelPoll: how often the supervisor checks for a channel while
	// none is configured yet (first start, before login).
	ircChannelPoll = 2 * time.Second
)

func (a *App) runIRCSupervisor() {
	backoff := ircBackoffMin
	waitingLogged := false

	for {
		if a.ctx.Err() != nil {
			return
		}

		a.mu.Lock()
		channel := a.cfg.Twitch.Channel
		a.mu.Unlock()
		if channel == "" {
			// First start before any login: no channel known yet. Login
			// (or a profile switch) sets it; pick it up shortly after.
			if !waitingLogged {
				log.Printf("[IRC] No channel configured yet — waiting for login")
				waitingLogged = true
			}
			select {
			case <-a.ctx.Done():
				return
			case <-time.After(ircChannelPoll):
			}
			continue
		}
		waitingLogged = false

		// Chat is READ anonymously (justinfan), exactly like chathub: no
		// token involved, so an expired / refreshing / unreachable login
		// can never keep chat from showing. Sending goes through Helix (or
		// the authenticated send-only client) — see SendMessage.
		client := twitch.NewAnonymousIRCClient(channel)
		// The resolver looks the Helix client up on every call: it outlives
		// profile switches (which nil the client) and token rebuilds.
		client.SetRoomResolver(func(roomID string) string {
			if h := a.helix(); h != nil {
				if name, err := h.GetChannelName(roomID); err == nil {
					return name
				}
			}
			return roomID
		})
		a.mu.Lock()
		a.ircClient = client
		a.mu.Unlock()

		clientCtx, cancelClient := context.WithCancel(a.ctx)
		go a.forwardIRC(clientCtx, client)

		log.Printf("[IRC] Connecting (anonymous, read-only) to #%s", channel)
		started := time.Now()
		err := client.Connect()
		cancelClient()
		client.Disconnect()

		if a.ctx.Err() != nil {
			return
		}
		if time.Since(started) >= ircHealthySessionMin {
			backoff = ircBackoffMin
		}
		log.Printf("[IRC] Disconnected from #%s: %v — reconnecting in %s", channel, err, backoff)

		select {
		case <-a.ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > ircBackoffMax {
			backoff = ircBackoffMax
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
			a.emit("settings", settings)
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
//
// Profile-switch safety: snapshot ActiveProfileID + creds BEFORE the network
// call. When the refresh completes, re-check under lock — if the user
// switched accounts while we were waiting on Twitch, discard the new tokens
// (they belong to the OLD profile; the flat cfg.Twitch now mirrors a
// different account). Writing them anyway would leak Profile A's fresh token
// into Profile B's slot on the next Save().
func (a *App) tokenRefreshLoop() {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
		a.mu.Lock()
		activeAtStart := a.cfg.ActiveProfileID
		clientID := a.cfg.Twitch.ClientID
		clientSecret := a.cfg.Twitch.ClientSecret
		refreshToken := a.cfg.Twitch.RefreshToken
		a.mu.Unlock()
		if refreshToken == "" {
			continue
		}
		token, err := twitch.RefreshAccessToken(clientID, clientSecret, refreshToken)
		if err != nil {
			log.Printf("[AUTH] Token refresh failed: %v", err)
			continue
		}
		if !a.commitTokens(activeAtStart, token) {
			log.Printf("[AUTH] Profile switched during refresh — discarding tokens for %s", activeAtStart)
			continue
		}
		log.Printf("[AUTH] Token refreshed")
		// Reload badges with the fresh token in case the previous load failed.
		a.loadBadges()
	}
}

// startEventSub cancels any existing session (from the previous
// profile) then spawns a fresh eventsub.Client for the active one.
// The client runs forever inside its own goroutine and emits every
// normalized activity event to the frontend as "activity" so the
// sidebar Activity tab can render it. Fires only when we have both
// a broadcaster id and an access token — a partial config would
// hit Twitch with 401s repeatedly.
func (a *App) startEventSub() {
	a.mu.Lock()
	if a.esCancel != nil {
		a.esCancel()
		a.esCancel = nil
		a.esClient = nil
	}
	clientID := a.cfg.Twitch.ClientID
	token := a.cfg.Twitch.AccessToken
	me := a.cfg.Twitch.BotUserID
	a.mu.Unlock()
	if clientID == "" || token == "" || me == "" {
		return
	}
	esCtx, cancel := context.WithCancel(a.ctx)
	client := eventsub.New(esCtx, clientID, token, me, me, func(ev eventsub.Event) {
		if a.ctx == nil {
			return
		}
		a.emit("activity", map[string]interface{}{
			"type":      string(ev.Type),
			"user":      ev.User,
			"userId":    ev.UserID,
			"message":   ev.Message,
			"meta":      ev.Meta,
			"timestamp": ev.Timestamp.UnixMilli(),
		})
	})
	a.mu.Lock()
	a.esCancel = cancel
	a.esClient = client
	a.mu.Unlock()
	go client.Start()
}

// helix returns the current Helix client (nil when logged out or during a
// profile switch). Always take a local snapshot through this and use only
// that: a.helixClient is replaced / nil'd under a.mu by SwitchProfile,
// login and boot, so reading the field twice can hit nil in between.
func (a *App) helix() *twitch.HelixClient {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.helixClient
}

// Twitch auth calls used by authenticate; vars so tests can fake them.
var (
	validateToken      = twitch.ValidateToken
	refreshAccessToken = twitch.RefreshAccessToken
)

// authOutcome is the result of App.authenticate.
type authOutcome int

const (
	authOK      authOutcome = iota // info + usable access token returned
	authExpired                    // Twitch rejected the tokens → re-login needed
	authStale                      // profile switched / app closing → abandon
)

// Boot authentication retry backoff for transient failures.
const (
	authRetryMin = 2 * time.Second
	authRetryMax = 60 * time.Second
)

// authenticate validates (and refreshes) the profile's Twitch tokens,
// RETRYING transient failures until it succeeds, Twitch rejects the tokens,
// or the boot becomes stale.
//
// Before, ANY validate failure — including "no network yet" right after
// Windows boot / autostart, a DNS hiccup or a Twitch 5xx — was treated as
// an expired login: bootSequence returned without ever connecting chat or
// retrying, and the "Authentication expired" notice was emitted before the
// window had loaded, so the user saw an empty chat and nothing else. Only
// a 400/401 from Twitch (twitch.IsTokenRejected) now counts as expired.
func (a *App) authenticate(profileID, clientID, clientSecret, accessToken, refreshToken string) (*twitch.ValidateResponse, string, authOutcome) {
	backoff := authRetryMin
	notified := false
	for {
		if a.ctx.Err() != nil || !a.isActiveProfile(profileID) {
			return nil, "", authStale
		}

		info, err := validateToken(accessToken)
		if err == nil {
			log.Printf("[AUTH] Token valid for %s", info.Login)
			// Proactively refresh so the session starts with a fresh token.
			// Non-fatal: the validated token is good for its remaining TTL.
			if refreshToken != "" {
				if tok, rerr := refreshAccessToken(clientID, clientSecret, refreshToken); rerr == nil {
					if !a.commitTokens(profileID, tok) {
						return nil, "", authStale
					}
					accessToken = tok.AccessToken
					log.Printf("[AUTH] Token refreshed at boot")
				} else {
					log.Printf("[AUTH] Boot refresh failed (non-fatal): %v", rerr)
				}
			}
			if notified {
				a.emitSystem("Twitch reachable again — connecting chat…")
			}
			return info, accessToken, authOK
		}

		if twitch.IsTokenRejected(err) {
			log.Printf("[AUTH] Access token rejected: %v", err)
			if refreshToken == "" {
				return nil, "", authExpired
			}
			tok, rerr := refreshAccessToken(clientID, clientSecret, refreshToken)
			if rerr == nil {
				if !a.commitTokens(profileID, tok) {
					return nil, "", authStale
				}
				log.Printf("[AUTH] Refreshed after rejected access token; validating again")
				accessToken, refreshToken = tok.AccessToken, tok.RefreshToken
				backoff = authRetryMin
				continue
			}
			if twitch.IsTokenRejected(rerr) {
				log.Printf("[AUTH] Refresh token rejected too: %v", rerr)
				return nil, "", authExpired
			}
			err = rerr // transient refresh failure → retry below
		}

		log.Printf("[AUTH] Twitch not reachable (%v) — retrying in %s", err, backoff)
		if !notified {
			a.emitSystem(fmt.Sprintf("Can't reach Twitch right now (%v) — retrying automatically…", err))
			notified = true
		}
		select {
		case <-a.ctx.Done():
			return nil, "", authStale
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > authRetryMax {
			backoff = authRetryMax
		}
	}
}

// isActiveProfile reports whether profileID is still the active profile.
func (a *App) isActiveProfile(profileID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.ActiveProfileID == profileID
}

// commitTokens stores freshly issued tokens for profileID, but only if
// that profile is still the active one. Returns false (and writes nothing)
// when the user switched profiles while the network call was in flight —
// the flat cfg.Twitch now mirrors a different account and Save() would
// copy the tokens into the wrong profile slot.
// Must be called WITHOUT a.mu held.
func (a *App) commitTokens(profileID string, tok *twitch.TokenResponse) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg.ActiveProfileID != profileID {
		return false
	}
	a.cfg.Twitch.AccessToken = tok.AccessToken
	a.cfg.Twitch.RefreshToken = tok.RefreshToken
	if err := a.cfg.Save(); err != nil {
		log.Printf("[AUTH] saving refreshed tokens failed: %v", err)
	}
	a.applyRefreshedToken(tok.AccessToken)
	return true
}

// applyRefreshedToken pushes a freshly refreshed access token into every
// long-lived consumer that captured the old one at construction time.
// Without this, the Helix client and EventSub keep using the boot token,
// which Twitch expires after ~4h — from then on mod actions, slash commands,
// chatter/role polling and EventSub resubscribes all 401.
// Caller must hold a.mu.
func (a *App) applyRefreshedToken(accessToken string) {
	if a.helixClient != nil {
		a.helixClient.SetAccessToken(accessToken)
	}
	if a.esClient != nil {
		a.esClient.SetAccessToken(accessToken)
	}
}

// startChattersAndRoles (re)starts the Helix chatter/role poller for the
// active profile, cancelling the previous one. No-op without a Helix client.
func (a *App) startChattersAndRoles() {
	if a.helix() == nil {
		return
	}
	a.mu.Lock()
	if a.rolesCancel != nil {
		a.rolesCancel()
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.rolesCancel = cancel
	a.mu.Unlock()
	go a.loadChattersAndRoles(ctx)
}

// loadChattersAndRoles bootstraps the sidebar user list + moderator/vip
// state from Helix, then starts a background loop that keeps everything
// authoritative. Chatters get refreshed every 60s so users who left get
// pruned (Twitch IRC PART is unreliable — batched, delayed, sometimes
// never fires) and Stream Together co-hosts / their chatters don't leak
// into our sidebar. This is the pattern Chatterino uses: IRC is the
// realtime feed, Helix is the ground truth.
func (a *App) loadChattersAndRoles(ctx context.Context) {
	a.refreshChatters()
	a.refreshRoles(true)

	chattersTicker := time.NewTicker(60 * time.Second)
	rolesTicker := time.NewTicker(5 * time.Minute)
	defer chattersTicker.Stop()
	defer rolesTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-chattersTicker.C:
			a.refreshChatters()
		case <-rolesTicker.C:
			a.refreshRoles(false)
		}
	}
}

// refreshChatters fetches the authoritative chatter list and emits it as
// "chattersAuthoritative" — the frontend REPLACES its user list with
// exactly these users. This is what makes stuck/banned/left users
// clear out.
//
// Two guardrails against wiping the list wrongly:
//  1. On any error (401 missing scope, network fail, etc.) we return
//     without emitting — frontend keeps what it has.
//  2. On empty response we ALSO don't emit. Twitch returns an empty
//     list for channels where the logged-in user isn't a moderator,
//     or for channels the current profile can't authenticate against.
//     If we emitted [] the frontend would wipe every user and the
//     sidebar would go blank every 60s. The visible chatters we
//     already have (from IRC join + chat messages) stay.
func (a *App) refreshChatters() {
	h := a.helix()
	if h == nil {
		return
	}
	chatters, err := h.GetChatters()
	if err != nil {
		log.Printf("[CHATTERS] refresh failed: %v", err)
		return
	}
	if len(chatters) == 0 {
		// Treat "no chatters returned" as "endpoint unavailable for
		// this profile" rather than "channel is empty" — the
		// logged-in user is always in their own chat if the token
		// works, so a legitimate response has ≥1 entry.
		return
	}
	entries := make([]map[string]string, 0, len(chatters))
	for _, c := range chatters {
		entries = append(entries, map[string]string{
			"username": c.UserLogin,
			"userId":   c.UserID,
		})
	}
	a.emit("chattersAuthoritative", entries)
}

func (a *App) refreshRoles(includeBots bool) {
	h := a.helix()
	if h == nil {
		return
	}
	roles := map[string]interface{}{
		"broadcaster": a.cfg.Twitch.Channel,
	}
	if mods, err := h.GetModerators(); err == nil {
		roles["mods"] = mods
	}
	if vips, err := h.GetVIPs(); err == nil {
		roles["vips"] = vips
	}
	if includeBots {
		chatters, _ := h.GetChatters()
		var botNames []string
		for _, c := range chatters {
			if a.botDetector.IsBot(c.UserLogin) {
				botNames = append(botNames, c.UserLogin)
			}
		}
		roles["bots"] = botNames
	}
	a.emit("roles", roles)
}

// startYouTube (re)starts YouTube for the active profile from cfg,
// stopping whatever ran before. A manual VideoID override connects
// directly; otherwise the live detector runs only if YouTube is enabled
// (the Enabled flag used to be ignored at boot, so "disable YouTube" came
// back after every restart).
func (a *App) startYouTube() {
	a.mu.Lock()
	a.stopYouTubeLocked()
	videoID := a.cfg.YouTube.VideoID
	handle := a.cfg.YouTube.ChannelHandle
	enabled := a.cfg.YouTube.Enabled
	var detectCtx context.Context
	if videoID == "" && enabled && handle != "" {
		var cancel context.CancelFunc
		detectCtx, cancel = context.WithCancel(a.ctx)
		a.ytDetectCancel = cancel
	}
	a.mu.Unlock()

	switch {
	case videoID != "":
		a.startYouTubeChat(a.ctx, videoID)
	case detectCtx != nil:
		go a.runYouTubeDetector(detectCtx, handle)
	}
}

// stopYouTubeLocked cancels the detector and the chat poller.
// Caller must hold a.mu.
func (a *App) stopYouTubeLocked() {
	if a.ytDetectCancel != nil {
		a.ytDetectCancel()
		a.ytDetectCancel = nil
	}
	if a.ytChatCancel != nil {
		a.ytChatCancel()
		a.ytChatCancel = nil
	}
	a.ytClient = nil
}

// startYouTubeChat connects the InnerTube poller for videoID as a child of
// parent (the detector's context, or a.ctx for a manual VideoID), replacing
// any running chat. A parent that is already cancelled is ignored — that
// is a stale detector firing after a profile switch, and it must not
// cancel the current profile's chat.
func (a *App) startYouTubeChat(parent context.Context, videoID string) {
	a.mu.Lock()
	if parent.Err() != nil {
		a.mu.Unlock()
		return
	}
	if a.ytChatCancel != nil {
		a.ytChatCancel()
	}
	chatCtx, cancel := context.WithCancel(parent)
	a.ytChatCancel = cancel
	ytClient := youtube.NewInnerTubeClient(videoID)
	// With the YouTube login cookies the chat page is rendered for the
	// logged-in user and includes the send endpoint (SendParams); without
	// them the chat is read-only.
	if creds, err := youtube.LoadLoginCookies(); err == nil && creds.Valid() {
		ytClient.SetCookies(creds)
	}
	a.ytClient = ytClient
	a.mu.Unlock()

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
		// On success Connect itself posts "Connected to live chat".
		_ = ytClient.ConnectWithRetry(chatCtx, func(err error, wait time.Duration) {
			log.Printf("[YT] connect to %s failed, retrying in %s: %v", videoID, wait, err)
		})
	}()
}

// runYouTubeDetector polls handle's live state until ctx is cancelled.
// Its callbacks re-check ctx so a detector that was replaced (profile
// switch, settings change) can't start or stop the current chat.
func (a *App) runYouTubeDetector(ctx context.Context, handle string) {
	detector := youtube.NewLiveDetector(
		handle,
		func(info youtube.LiveStreamInfo) {
			if ctx.Err() != nil {
				return
			}
			title := info.Title
			if title == "" {
				title = info.VideoID
			}
			a.emitSystem(fmt.Sprintf("YouTube stream detected: %s", title))
			a.startYouTubeChat(ctx, info.VideoID)
		},
		func() {
			a.mu.Lock()
			if ctx.Err() != nil {
				a.mu.Unlock()
				return
			}
			if a.ytChatCancel != nil {
				a.ytChatCancel()
				a.ytChatCancel = nil
			}
			a.ytClient = nil
			a.mu.Unlock()
			a.emitSystem("YouTube stream ended")
		},
	)
	detector.Start(ctx)
}

// === Event emitters ===

func (a *App) emitChat(msg chat.Message) {
	if a.ctx == nil {
		return
	}
	// 7TV per-user lookup — see chathub's emitMessage for rationale.
	// Stv client dedupes by user id so a chat burst is harmless.
	if a.stv != nil && msg.Platform == chat.PlatformTwitch && msg.UserID != "" {
		a.stv.LookupUser(msg.UserID)
	}
	a.emit("chat", a.chatToMap(msg))
}

func (a *App) emitJoinPart(jp chat.UserJoinPart) {
	if a.ctx == nil {
		return
	}
	a.emit("joinPart", map[string]interface{}{
		"username": jp.Username,
		"isJoin":   jp.IsJoin,
		"platform": string(jp.Platform),
	})
}

func (a *App) emitSystem(text string) {
	if a.ctx == nil {
		return
	}
	a.emit("chat", map[string]interface{}{
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
	case chat.MessageTypeWatchStreak:
		typeStr = "watchstreak"
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
		"firstMsg":        msg.IsFirstMessage,
		"returningChatter": msg.IsReturningChatter,
		"streak":           msg.StreakCount,
		"superChatAmount": msg.SuperChatAmount,
		"targetUsername":  msg.TargetUsername,
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

// ApplyUpdate downloads the new exe, swaps it in-place, and relaunches.
//
// It deliberately takes no URL: the webview renders untrusted chat content,
// so a URL parameter would let injected script install arbitrary code.
// selfupdate.Update re-resolves the release and verifies its SHA-256.
func (a *App) ApplyUpdate() string {
	channel := a.cfg.UI.UpdateChannel
	if channel == "" {
		channel = "stable"
	}
	if err := selfupdate.Update(version.RepoOwner, version.RepoName, channel, version.Version, "streamerchat-gui.exe"); err != nil {
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
	a.mu.Lock()
	a.cfg.YouTube.Enabled = enabled
	a.cfg.YouTube.ChannelHandle = handle
	// Channel ID is derived from the handle on demand; wipe any stale value
	// so the auto-detect loop re-resolves on the new handle.
	if a.cfg.YouTube.ChannelHandle != "" {
		a.cfg.YouTube.ChannelID = ""
	}
	a.cfg.Save()
	a.mu.Unlock()
	log.Printf("[YT] config updated: enabled=%v handle=%q", enabled, handle)

	// Stops the old detector + chat (they used to keep running alongside
	// the new one) and starts whatever the new settings call for.
	a.startYouTube()
	return ""
}

// === YouTube login + send ===

// StartYouTubeLogin opens an isolated browser window for the Google login
// and stores the session cookies in the OS keyring (Windows only for now —
// see youtube.LaunchLoginFlow). Blocks until done or 5 minutes pass; the
// frontend disables the button meanwhile. On success the running YouTube
// chat is reconnected so it picks up the cookies (and the send endpoint).
func (a *App) StartYouTubeLogin() string {
	ctx, cancel := context.WithTimeout(a.ctx, ytLoginTimeout)
	defer cancel()
	creds, err := youtube.LaunchLoginFlow(ctx, ytLoginTimeout)
	if err != nil {
		log.Printf("[YT-LOGIN] %v", err)
		return err.Error()
	}
	if err := youtube.SaveLoginCookies(creds); err != nil {
		log.Printf("[YT-LOGIN] save failed: %v", err)
		return err.Error()
	}
	log.Printf("[YT-LOGIN] cookies saved to OS keyring")
	a.emit("youtubeLoginChanged", map[string]interface{}{"loggedIn": true})
	go a.startYouTube()
	return ""
}

// ytLoginTimeout: plenty for typing a password + 2FA in the login window.
const ytLoginTimeout = 5 * time.Minute

// LogoutYouTube removes the saved YouTube session.
func (a *App) LogoutYouTube() string {
	if err := youtube.ClearLoginCookies(); err != nil {
		return err.Error()
	}
	a.emit("youtubeLoginChanged", map[string]interface{}{"loggedIn": false})
	go a.startYouTube() // reconnect read-only, without the old cookies
	return ""
}

// HasYouTubeLogin reports whether a usable YouTube session is saved.
func (a *App) HasYouTubeLogin() bool {
	c, err := youtube.LoadLoginCookies()
	return err == nil && c.Valid()
}

// SendYouTubeMessage posts text into the YouTube live chat StreamerChat is
// currently reading. Returns "" on success or a user-facing error. No local
// echo: the InnerTube poller receives our own message back.
func (a *App) SendYouTubeMessage(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "/") {
		return "slash commands only work for Twitch"
	}
	a.mu.Lock()
	client := a.ytClient
	a.mu.Unlock()
	if client == nil {
		return "YouTube chat not connected (stream not live or YouTube disabled)"
	}
	creds, err := youtube.LoadLoginCookies()
	if err != nil || !creds.Valid() {
		return "not logged in to YouTube — Settings → YouTube → Connect YouTube"
	}
	params := client.SendParams()
	if params == "" {
		return "YouTube chat doesn't accept messages from this account right now (log in again, or chat is restricted)"
	}
	if err := youtube.SendLiveChatMessage(creds, params, text); err != nil {
		log.Printf("[YT-SEND] %v", err)
		return err.Error()
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
	a.mu.Lock()
	clientID := a.cfg.Twitch.ClientID
	// The login belongs to the profile that was active when the user
	// clicked Login. The device-code flow can take minutes (user approves
	// in the browser); if they switch profiles meanwhile, writing the
	// result would overwrite the OTHER profile's account.
	profileID := a.cfg.ActiveProfileID
	a.mu.Unlock()

	device, err := twitch.RequestDeviceCode(clientID)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}

	go func() {
		token, err := twitch.PollForToken(clientID, device.DeviceCode, device.Interval)
		if err != nil {
			a.emit("loginResult", map[string]interface{}{"error": err.Error()})
			return
		}
		info, err := twitch.ValidateToken(token.AccessToken)
		if err != nil {
			a.emit("loginResult", map[string]interface{}{"error": err.Error()})
			return
		}
		a.mu.Lock()
		if a.cfg.ActiveProfileID != profileID {
			a.mu.Unlock()
			log.Printf("[AUTH] Profile switched during device login — discarding login for %s", profileID)
			a.emit("loginResult", map[string]interface{}{
				"error": "Profile was switched during login. Please log in again on this profile.",
			})
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
		a.helixClient, _ = twitch.NewHelixClient(clientID, token.AccessToken, info.UserID, info.UserID)
		a.mu.Unlock()
		log.Printf("[AUTH] Device-code login complete for %s", info.Login)

		// Rebuild helix client and (re)start everything that depends on auth.
		// On a fresh-install or post-revocation startup, bootSequence will
		// have bailed out before starting these — so we (re)kick them here.
		// Calling them again after a normal startup is idempotent enough
		// (channelEmotesLoaded gates per-channel work, supervisor loops are
		// single instances by construction). The Helix client was rebuilt
		// above under a.mu. EventSub is restarted so it subscribes with the
		// new token — on a fresh install bootSequence never started it, and
		// after a re-login the old session still holds the dead token.
		a.loadBadges()
		a.startEventSub()

		// Reading chat is anonymous and unaffected by a login; the read
		// supervisor picks up a newly set channel by itself. Only the
		// token refresh loop needs starting (once per process), and the
		// send-only connection must be rebuilt with the new token.
		a.mu.Lock()
		a.dropSendClientLocked()
		needStart := !a.supervisorsStarted
		if needStart {
			a.supervisorsStarted = true
		}
		a.mu.Unlock()
		if needStart {
			go a.tokenRefreshLoop()
		}
		a.startChattersAndRoles()
		// YouTube wasn't started in bootSequence either if we bailed early.
		// Don't interrupt a YouTube session that is already running — a
		// Twitch re-login has nothing to do with it.
		a.mu.Lock()
		ytRunning := a.ytChatCancel != nil || a.ytDetectCancel != nil
		a.mu.Unlock()
		if !ytRunning {
			a.startYouTube()
		}

		// Re-emit ready so the frontend picks up the new username / channel
		// and any UI that gates on it (TW/YT pills, login pill) updates.
		a.emit("ready", map[string]interface{}{
			"channel":        a.cfg.Twitch.Channel,
			"username":       a.cfg.Twitch.Username,
			"youtube":        (a.cfg.YouTube.Enabled && a.cfg.YouTube.ChannelHandle != "") || a.cfg.YouTube.VideoID != "",
			"showTimestamps": a.cfg.UI.ShowTimestamps,
		})

		a.emit("loginResult", map[string]interface{}{
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
	// Only the authenticated send connection goes away; chat keeps being
	// READ anonymously after logout.
	a.mu.Lock()
	a.dropSendClientLocked()
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

// SendMessage sends a chat message to Twitch IRC, OR routes a /command
// through the matching Helix endpoint. Twitch deprecated raw IRC mod
// commands (PRIVMSG #ch :/ban foo) in early 2023 — they all go via
// Helix now (ban/unban/mod/vip/clear/slow/announce/raid/shoutout/…).
//
// Twitch does not echo PRIVMSGs back to the sending connection, so on
// the regular-message path we also emit a synthetic local-echo event
// so the user sees their own message.
func (a *App) SendMessage(text string) {
	if text == "" {
		return
	}

	// "/me <text>" is a chat message, not a moderation command. CTCP
	// ACTION only exists on IRC, so it goes through the authenticated
	// send-only connection.
	if rest, ok := cutCommand(text, "/me"); ok {
		if rest != "" {
			a.sendViaIRC(rest, true)
		}
		return
	}

	// Slash command? Route through Helix and surface the response as a
	// system message in chat.
	if strings.HasPrefix(text, "/") {
		h := a.helix()
		status, handled := twitch.HandleSlashCommand(h, text)
		if handled {
			a.emitSystem(status)
			return
		}
	}

	// Regular message: Helix Send Chat Message. No local echo needed —
	// chat is read on a separate (anonymous) connection, which receives
	// our own message back like everyone else's, with real badges/emotes.
	a.mu.Lock()
	clientID := a.cfg.Twitch.ClientID
	token := a.cfg.Twitch.AccessToken
	senderID := a.cfg.Twitch.BotUserID
	channel := a.cfg.Twitch.Channel
	username := a.cfg.Twitch.Username
	a.mu.Unlock()
	if token == "" || senderID == "" {
		a.emitSystem("Not logged in — open Settings → Twitch to log in before sending.")
		return
	}
	broadcasterID := senderID
	if !strings.EqualFold(channel, username) {
		h := a.helix()
		if h == nil {
			a.emitSystem("Message not sent: Twitch login not ready yet.")
			return
		}
		id, err := h.ResolveLogin(channel)
		if err != nil {
			a.emitSystem("Message not sent: " + err.Error())
			return
		}
		broadcasterID = id
	}
	err := twitch.SendChatMessage(clientID, token, broadcasterID, senderID, text)
	switch {
	case err == nil:
	case errors.Is(err, twitch.ErrSendNotAuthorized):
		// Logins from before the user:write:chat scope existed can't send
		// via Helix; IRC only needs chat:edit. Fall back and say why once.
		log.Printf("[SEND] Helix not authorized, falling back to IRC: %v", err)
		a.sendViaIRC(text, false)
		a.helixSendHintOnce.Do(func() {
			a.emitSystem("Sent via IRC fallback. Log out and in again (Settings → Twitch) to grant the newer chat permission.")
		})
	default:
		a.emitSystem("Message not sent: " + err.Error())
	}
}

// sendViaIRC sends text (or a /me action) over the authenticated send-only
// IRC connection, creating it on first use or after the token / channel
// changed. Messages queued before the connection is up are flushed by the
// IRC library once it has connected.
func (a *App) sendViaIRC(text string, action bool) {
	a.mu.Lock()
	username, token, channel := a.cfg.Twitch.Username, a.cfg.Twitch.AccessToken, a.cfg.Twitch.Channel
	if token == "" || username == "" || channel == "" {
		a.mu.Unlock()
		a.emitSystem("Not logged in — open Settings → Twitch to log in before sending.")
		return
	}
	key := username + "\x00" + token + "\x00" + channel
	if a.sendClient == nil || a.sendClientKey != key {
		a.dropSendClientLocked()
		c := twitch.NewSendOnlyIRCClient(username, token, channel)
		a.sendClient, a.sendClientKey = c, key
		go func() {
			err := c.Connect()
			log.Printf("[SEND] send-only IRC connection ended: %v", err)
			a.mu.Lock()
			if a.sendClient == c {
				a.sendClient, a.sendClientKey = nil, ""
			}
			a.mu.Unlock()
		}()
	}
	c := a.sendClient
	a.mu.Unlock()
	if action {
		c.Action(text)
	} else {
		c.Say(text)
	}
}

// dropSendClientLocked closes the send-only IRC connection. Caller holds mu.
func (a *App) dropSendClientLocked() {
	if a.sendClient != nil {
		a.sendClient.Disconnect()
		a.sendClient, a.sendClientKey = nil, ""
	}
}

// cutCommand reports whether text is the slash command cmd (case-
// insensitive, e.g. "/me" or "/ME hi") and returns its trimmed argument.
func cutCommand(text, cmd string) (string, bool) {
	t := strings.TrimSpace(text)
	if len(t) < len(cmd) || !strings.EqualFold(t[:len(cmd)], cmd) {
		return "", false
	}
	rest := t[len(cmd):]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return "", false // e.g. "/mention" is not "/me"
	}
	return strings.TrimSpace(rest), true
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
// Channel emotes of the active profile's channel win over global ones.
func (a *App) LookupEmote(name string) map[string]interface{} {
	if a.emotes == nil {
		return nil
	}
	a.mu.Lock()
	broadcasterID := a.cfg.Twitch.BotUserID
	a.mu.Unlock()
	info, ok := a.emotes.LookupIn(broadcasterID, name)
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
	h := a.helix()
	if h == nil || userID == "" {
		return nil
	}
	info, err := h.GetUserInfo(userID)
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
	// One snapshot for the whole action: SwitchProfile nils a.helixClient,
	// and re-reading the field between the check and the call used to
	// panic the app with a nil dereference.
	h := a.helix()
	if h == nil {
		return "no helix client"
	}
	var err error
	switch action {
	case "ban":
		err = h.BanUser(userID, "Banned via StreamerChat")
	case "timeout_600":
		err = h.TimeoutUser(userID, 600, "Timed out")
	case "timeout_3600":
		err = h.TimeoutUser(userID, 3600, "Timed out")
	case "timeout_86400":
		err = h.TimeoutUser(userID, 86400, "Timed out")
	case "delete":
		// No id (card opened from the sidebar / a local echo): nothing
		// to delete — say so instead of reporting a fake success.
		if msgID == "" {
			return "no message id — can only delete a message opened from chat"
		}
		err = h.DeleteMessage(msgID)
	case "unban":
		err = h.UnbanUser(userID)
	default:
		return "unknown action: " + action
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

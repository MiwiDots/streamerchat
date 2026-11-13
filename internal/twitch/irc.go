package twitch

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miwi/streamerchat/internal/chat"

	twitch "github.com/gempir/go-twitch-irc/v4"
)

// IRCClient wraps the Twitch IRC connection.
type IRCClient struct {
	client       *twitch.Client
	channel      string
	extraJoins   []string // joined inside OnConnect alongside `channel`
	messages     chan chat.Message
	joins        chan chat.UserJoinPart
	settings     chan chat.ChatSettings
	errors       chan error
	roomNames    map[string]string           // roomID -> channel name cache
	resolveRoom  func(roomID string) string  // callback to resolve room IDs

	// dropped counts events discarded because a consumer channel was full
	// (see push). Logged every dropLogEvery drops.
	dropped atomic.Int64

	// Send-only clients queue outgoing lines in outbox; a worker forwards
	// them in order once the server has welcomed us (ready closed in
	// OnConnect). gempir would otherwise write lines queued before the
	// connection was up right after PASS/NICK — before the login is
	// confirmed and before the JOIN — and Twitch drops them.
	ready     chan struct{}
	readyOnce sync.Once
	outbox    chan string
	stop      chan struct{}
	stopOnce  sync.Once
}

// sendOutboxSize bounds queued messages while a send-only client connects.
const sendOutboxSize = 64

// sendReadyTimeout: queued messages are dropped (and logged) if the
// send-only connection doesn't come up within this time.
const sendReadyTimeout = 20 * time.Second

// dropLogEvery throttles the "event dropped" log line.
const dropLogEvery = 100

// push hands an event to a consumer channel WITHOUT blocking.
//
// The gempir handlers run on the library's single parser goroutine. A
// blocking send there wedges the whole connection once a buffer is full:
// PONGs stop, the socket times out, and Connect() never returns, so even
// Disconnect() can't recover it. Dropping (and logging) an event when the
// consumer is gone or stalled is strictly better.
func push[T any](c *IRCClient, ch chan T, v T) {
	select {
	case ch <- v:
	default:
		if n := c.dropped.Add(1); n%dropLogEvery == 1 {
			log.Printf("[IRC] consumer for #%s not keeping up — dropped %d event(s) so far", c.channel, n)
		}
	}
}

// NewIRCClient creates a new Twitch IRC client. extraChannels are joined
// alongside the primary one as soon as the underlying connection signals
// "connected" — this replaces a race where a goroutine sleeps 2s after
// Connect() and then issues Join() calls that may land before the IRC
// server has authenticated us.
//
// The caller MUST drain Messages(), Joins() and Settings(); events are
// dropped (not blocked on) once a buffer is full. A client that only sends
// should use NewSendOnlyIRCClient instead.
func NewIRCClient(username, oauthToken, channel string, extraChannels ...string) *IRCClient {
	irc := newIRCClient(authedClient(username, oauthToken), channel, extraChannels, []string{
		"twitch.tv/tags",
		"twitch.tv/commands",
		// membership = JOIN/PART events (core requirement for the chatter list)
		"twitch.tv/membership",
	})
	irc.registerHandlers()
	return irc
}

// NewSendOnlyIRCClient creates a client used purely to PRIVMSG into the
// given channels (chathub reads chat through MultiIRCClient). It does not
// request the membership capability and registers no event handlers, so
// nothing accumulates in its channels; NOTICE rejections and connects are
// logged. Messages()/Joins()/Settings() never deliver anything.
func NewSendOnlyIRCClient(username, oauthToken, channel string, extraChannels ...string) *IRCClient {
	irc := newIRCClient(authedClient(username, oauthToken), channel, extraChannels, []string{
		"twitch.tv/tags",
		"twitch.tv/commands",
	})
	irc.client.OnNoticeMessage(func(msg twitch.NoticeMessage) {
		log.Printf("[SEND] NOTICE %s in #%s: %s", msg.MsgID, msg.Channel, msg.Message)
	})
	irc.ready = make(chan struct{})
	irc.outbox = make(chan string, sendOutboxSize)
	irc.stop = make(chan struct{})
	irc.client.OnConnect(func() {
		log.Printf("[SEND] send-only client connected (primary=#%s extras=%v)", irc.channel, irc.extraJoins)
		irc.readyOnce.Do(func() { close(irc.ready) })
	})
	go irc.runOutbox()
	return irc
}

// runOutbox forwards queued lines in order once the connection is ready.
func (c *IRCClient) runOutbox() {
	select {
	case <-c.ready:
	case <-c.stop:
		return
	case <-time.After(sendReadyTimeout):
		log.Printf("[SEND] send-only connection to #%s not ready after %s — dropping %d queued message(s)", c.channel, sendReadyTimeout, len(c.outbox))
		return
	}
	for {
		select {
		case line := <-c.outbox:
			c.client.Say(c.channel, line)
		case <-c.stop:
			return
		}
	}
}

// say sends a line to the primary channel — through the ordered outbox
// for send-only clients, directly otherwise.
func (c *IRCClient) say(line string) {
	if c.outbox == nil {
		c.client.Say(c.channel, line)
		return
	}
	select {
	case c.outbox <- line:
	default:
		log.Printf("[SEND] outbox for #%s full — message dropped", c.channel)
	}
}

// NewAnonymousIRCClient creates a READ-ONLY client logged in as an
// anonymous "justinfan" user — no token involved. Reading chat (incl.
// JOIN/PART via the membership capability) therefore works the moment the
// app starts, independent of whether the Twitch login is valid, expired or
// still being refreshed. Say/Action on it are silently ignored by Twitch;
// send through Helix or an authenticated client instead.
func NewAnonymousIRCClient(channel string) *IRCClient {
	irc := newIRCClient(twitch.NewAnonymousClient(), channel, nil, []string{
		"twitch.tv/tags",
		"twitch.tv/commands",
		"twitch.tv/membership",
	})
	irc.registerHandlers()
	return irc
}

func authedClient(username, oauthToken string) *twitch.Client {
	return twitch.NewClient(username, "oauth:"+strings.TrimPrefix(oauthToken, "oauth:"))
}

func newIRCClient(client *twitch.Client, channel string, extraChannels, caps []string) *IRCClient {
	client.Capabilities = caps

	extras := make([]string, 0, len(extraChannels))
	for _, ch := range extraChannels {
		extras = append(extras, strings.TrimPrefix(ch, "#"))
	}

	return &IRCClient{
		client:     client,
		channel:    strings.TrimPrefix(channel, "#"),
		extraJoins: extras,
		messages:   make(chan chat.Message, 256),
		joins:      make(chan chat.UserJoinPart, 256),
		settings:   make(chan chat.ChatSettings, 16),
		errors:     make(chan error, 16),
		roomNames:  make(map[string]string),
	}
}

// Messages returns the channel for incoming chat messages.
func (c *IRCClient) Messages() <-chan chat.Message {
	return c.messages
}

// Joins returns the channel for join/part events.
func (c *IRCClient) Joins() <-chan chat.UserJoinPart {
	return c.joins
}

// Settings returns the channel for room setting changes.
func (c *IRCClient) Settings() <-chan chat.ChatSettings {
	return c.settings
}

// Errors returns the channel for connection errors.
func (c *IRCClient) Errors() <-chan error {
	return c.errors
}

// Connect starts the IRC connection and joins the primary channel +
// every extraChannel passed to NewIRCClient. Blocks until the underlying
// client returns (i.e. terminal disconnect).
func (c *IRCClient) Connect() error {
	c.client.Join(c.channel)
	for _, ch := range c.extraJoins {
		c.client.Join(ch)
	}
	return c.client.Connect()
}

// Disconnect closes the IRC connection (and stops a send-only outbox).
func (c *IRCClient) Disconnect() {
	if c.stop != nil {
		c.stopOnce.Do(func() { close(c.stop) })
	}
	c.client.Disconnect()
}

// sanitizeIRCText makes user text safe to put into a single IRC line.
//
// SECURITY: gempir's Say() formats "PRIVMSG #chan :<text>" and writes it
// verbatim. A CR or LF in text would terminate that line and let the rest
// be sent as an arbitrary raw IRC command (PRIVMSG another channel, JOIN,
// PART, ...). Line breaks become spaces; other control characters except
// the CTCP delimiter \x01 (used by Action) are dropped.
func sanitizeIRCText(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\r' || r == '\n':
			return ' '
		case r == '\x01':
			return r
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, text)
}

// Say sends a message to the channel.
func (c *IRCClient) Say(text string) {
	c.say(sanitizeIRCText(text))
}

// Action sends a "/me" message as CTCP ACTION — the IRC-native form, which
// doesn't depend on Twitch's server-side parsing of "/commands" in PRIVMSG
// text (most of which Twitch removed in 2023).
func (c *IRCClient) Action(text string) {
	text = strings.ReplaceAll(sanitizeIRCText(text), "\x01", "")
	c.say("\x01ACTION " + text + "\x01")
}

// SayTo sends a message to a specific channel (must be joined first).
func (c *IRCClient) SayTo(channel, text string) {
	ch := strings.TrimPrefix(channel, "#")
	text = sanitizeIRCText(text)
	log.Printf("[SEND] PRIVMSG #%s: %q", ch, text)
	c.client.Say(ch, text)
}

// Join adds a channel to the client's joined channels.
func (c *IRCClient) Join(channel string) {
	c.client.Join(strings.TrimPrefix(channel, "#"))
}

// SetRoomResolver sets a callback to resolve room IDs to channel names.
func (c *IRCClient) SetRoomResolver(fn func(roomID string) string) {
	c.resolveRoom = fn
}

// resolveRoomName looks up a channel name for a room ID.
func (c *IRCClient) resolveRoomName(roomID string) string {
	if name, ok := c.roomNames[roomID]; ok {
		return name
	}
	if c.resolveRoom != nil {
		if name := c.resolveRoom(roomID); name != "" {
			c.roomNames[roomID] = name
			return name
		}
	}
	return roomID
}

func (c *IRCClient) registerHandlers() {
	c.client.OnPrivateMessage(func(msg twitch.PrivateMessage) {
		m := convertPrivateMessage(msg, c.channel)
		// Resolve shared chat room ID to channel name
		if m.IsSharedChat && m.SourceChannel == msg.Source.RoomID {
			m.SourceChannel = c.resolveRoomName(msg.Source.RoomID)
		}
		push(c, c.messages, m)
	})

	c.client.OnUserJoinMessage(func(msg twitch.UserJoinMessage) {
		log.Printf("[IRC] JOIN %s in #%s", msg.User, msg.Channel)
		push(c, c.joins, chat.UserJoinPart{
			Platform: chat.PlatformTwitch,
			Channel:  msg.Channel,
			Username: msg.User,
			IsJoin:   true,
			Time:     time.Now(),
		})
	})

	c.client.OnUserPartMessage(func(msg twitch.UserPartMessage) {
		log.Printf("[IRC] PART %s from #%s", msg.User, msg.Channel)
		push(c, c.joins, chat.UserJoinPart{
			Platform: chat.PlatformTwitch,
			Channel:  msg.Channel,
			Username: msg.User,
			IsJoin:   false,
			Time:     time.Now(),
		})
	})

	c.client.OnClearChatMessage(func(msg twitch.ClearChatMessage) {
		if msg.TargetUsername != "" {
			// User was banned/timed out
			msgType := chat.MessageTypeBan
			if msg.BanDuration > 0 {
				msgType = chat.MessageTypeTimeout
			}
			push(c, c.messages, chat.Message{
				Platform:       chat.PlatformTwitch,
				Type:           msgType,
				Channel:        msg.Channel,
				Timestamp:      time.Now(),
				TargetUsername: msg.TargetUsername,
				BanDuration:    msg.BanDuration,
				Text:           fmt.Sprintf("%s was %s", msg.TargetUsername, banText(msg.BanDuration)),
			})
		} else {
			// Chat was cleared
			push(c, c.messages, chat.Message{
				Platform:  chat.PlatformTwitch,
				Type:      chat.MessageTypeClearChat,
				Channel:   msg.Channel,
				Timestamp: time.Now(),
				Text:      "Chat was cleared by a moderator",
			})
		}
	})

	c.client.OnClearMessage(func(msg twitch.ClearMessage) {
		push(c, c.messages, chat.Message{
			Platform:       chat.PlatformTwitch,
			Type:           chat.MessageTypeDeletedMessage,
			Channel:        msg.Channel,
			Timestamp:      time.Now(),
			TargetUsername: msg.Login,
			Text:           msg.Message,
			ID:             msg.TargetMsgID,
		})
	})

	c.client.OnUserNoticeMessage(func(msg twitch.UserNoticeMessage) {
		m := chat.Message{
			Platform:    chat.PlatformTwitch,
			Channel:     msg.Channel,
			Timestamp:   time.Now(),
			UserID:      msg.User.ID,
			Username:    msg.User.Name,
			DisplayName: msg.User.DisplayName,
			Color:       msg.User.Color,
			Badges:      convertBadges(msg.User.Badges),
			Text:        userNoticeText(msg),
		}

		switch {
		case applyWatchStreak(&m, msg):
			// type, text and emotes set by applyWatchStreak
		case msg.MsgID == "sub" || msg.MsgID == "resub":
			m.Type = chat.MessageTypeSub
		case msg.MsgID == "subgift" || msg.MsgID == "anonsubgift":
			m.Type = chat.MessageTypeGiftSub
		case msg.MsgID == "raid":
			m.Type = chat.MessageTypeRaid
		case msg.MsgID == "announcement":
			m.Type = chat.MessageTypeAnnouncement
		default:
			m.Type = chat.MessageTypeSystem
		}

		push(c, c.messages, m)
	})

	// OnNoticeMessage covers Twitch's NOTICE replies — these are how the
	// IRC layer tells us a PRIVMSG was rejected (msg_followersonly,
	// msg_subsonly, msg_banned, msg_duplicate, msg_ratelimit, …) or the
	// auth failed (login_authentication_failed). Without this handler all
	// rejected sends look like "nothing happened" to the user.
	c.client.OnNoticeMessage(func(msg twitch.NoticeMessage) {
		msgID := msg.MsgID
		log.Printf("[SEND] NOTICE %s in #%s: %s", msgID, msg.Channel, msg.Message)
		push(c, c.messages, chat.Message{
			Platform:  chat.PlatformTwitch,
			Type:      chat.MessageTypeSystem,
			Channel:   strings.TrimPrefix(msg.Channel, "#"),
			Timestamp: time.Now(),
			Text:      fmt.Sprintf("Twitch: %s", msg.Message),
		})
	})

	c.client.OnRoomStateMessage(func(msg twitch.RoomStateMessage) {
		s := chat.ChatSettings{
			SlowMode:     msg.Tags["slow"] != "0" && msg.Tags["slow"] != "",
			SubOnly:      msg.Tags["subs-only"] == "1",
			EmoteOnly:    msg.Tags["emote-only"] == "1",
			FollowerOnly: msg.Tags["followers-only"] != "-1" && msg.Tags["followers-only"] != "",
		}
		push(c, c.settings, s)
	})

	c.client.OnReconnectMessage(func(_ twitch.ReconnectMessage) {
		log.Printf("[IRC] Twitch asked us to reconnect (#%s)", c.channel)
		push(c, c.messages, chat.Message{
			Platform:  chat.PlatformTwitch,
			Type:      chat.MessageTypeSystem,
			Timestamp: time.Now(),
			Text:      "Reconnecting...",
		})
	})

	c.client.OnConnect(func() {
		log.Printf("[IRC] connected + joined #%s (extras=%v)", c.channel, c.extraJoins)
		push(c, c.messages, chat.Message{
			Platform:  chat.PlatformTwitch,
			Type:      chat.MessageTypeSystem,
			Timestamp: time.Now(),
			Text:      fmt.Sprintf("Connected to #%s", c.channel),
		})
	})
}

// applyWatchStreak turns a shared watch streak into a chat-like message:
// Twitch sends USERNOTICE msg-id=viewermilestone with
// msg-param-category=watch-streak and the streak length in
// msg-param-value. The user's optional message becomes the text (with its
// native emotes) so it renders like a normal chat line plus a streak
// marker. Returns false for any other USERNOTICE.
func applyWatchStreak(m *chat.Message, msg twitch.UserNoticeMessage) bool {
	if msg.MsgID != "viewermilestone" || msg.MsgParams["msg-param-category"] != "watch-streak" {
		return false
	}
	m.Type = chat.MessageTypeWatchStreak
	m.StreakCount, _ = strconv.Atoi(msg.MsgParams["msg-param-value"])
	m.ID = msg.ID
	m.Text = msg.Message
	m.TwitchEmoteRanges = emoteRanges(msg.Emotes)
	m.IsMod = hasBadge(msg.User.Badges, "moderator")
	m.IsVIP = hasBadge(msg.User.Badges, "vip")
	m.IsSub = hasBadge(msg.User.Badges, "subscriber")
	m.IsBroadcaster = hasBadge(msg.User.Badges, "broadcaster")
	return true
}

// emoteRanges flattens the library's parsed native emotes into rune ranges.
func emoteRanges(emotes []*twitch.Emote) []chat.EmoteRange {
	var out []chat.EmoteRange
	for _, e := range emotes {
		for _, pos := range e.Positions {
			out = append(out, chat.EmoteRange{ID: e.ID, Name: e.Name, Start: pos.Start, End: pos.End})
		}
	}
	return out
}

// userNoticeText builds the display text for a USERNOTICE.
//
//   - announcement: system-msg is EMPTY for announcements — the text is
//     the user's message (it used to render as a blank line).
//   - sub / resub / raid / ...: system-msg ("X subscribed for 12
//     months!") plus the user's own message when they attached one
//     (it used to be dropped).
func userNoticeText(msg twitch.UserNoticeMessage) string {
	if msg.MsgID == "announcement" {
		return msg.Message
	}
	if msg.Message == "" {
		return msg.SystemMsg
	}
	if msg.SystemMsg == "" {
		return msg.Message
	}
	return msg.SystemMsg + " — " + msg.Message
}

func convertPrivateMessage(msg twitch.PrivateMessage, channel string) chat.Message {
	m := chat.Message{
		ID:            msg.ID,
		Platform:      chat.PlatformTwitch,
		Type:          chat.MessageTypeChat,
		Channel:       channel,
		Timestamp:     msg.Time,
		UserID:        msg.User.ID,
		Username:      msg.User.Name,
		DisplayName:   msg.User.DisplayName,
		Color:         msg.User.Color,
		Badges:        convertBadges(msg.User.Badges),
		IsMod:         hasBadge(msg.User.Badges, "moderator"),
		IsVIP:         hasBadge(msg.User.Badges, "vip"),
		IsSub:         hasBadge(msg.User.Badges, "subscriber"),
		IsBroadcaster: hasBadge(msg.User.Badges, "broadcaster"),
		Text:          msg.Message,
		TwitchEmotes:  msg.Tags["emotes"],
		Bits:          msg.Bits,
		// Tag is "1" only on the user's first message ever in this channel.
		IsFirstMessage: msg.Tags["first-msg"] == "1",
		// "1" when a returning viewer chats again (Twitch "returning-chatter").
		IsReturningChatter: msg.Tags["returning-chatter"] == "1",
	}

	// Extract parsed emote positions from the library
	m.TwitchEmoteRanges = emoteRanges(msg.Emotes)

	// Shared Chat / Chat Together
	if msg.Source != nil && msg.Source.RoomID != "" && msg.Source.RoomID != msg.RoomID {
		m.IsSharedChat = true
		// Try to get source channel name from tags
		if sourceName := msg.Tags["source-room-login"]; sourceName != "" {
			m.SourceChannel = sourceName
		} else {
			m.SourceChannel = msg.Source.RoomID
		}
		// Use source badges for correct role display
		if len(msg.Source.Badges) > 0 {
			m.Badges = convertBadges(msg.Source.Badges)
			m.IsMod = hasBadge(msg.Source.Badges, "moderator")
			m.IsVIP = hasBadge(msg.Source.Badges, "vip")
			m.IsSub = hasBadge(msg.Source.Badges, "subscriber")
			m.IsBroadcaster = hasBadge(msg.Source.Badges, "broadcaster")
		}
	}

	return m
}

func convertBadges(badges map[string]int) []chat.Badge {
	result := make([]chat.Badge, 0, len(badges))
	for name, version := range badges {
		result = append(result, chat.Badge{
			Name:    name,
			Version: fmt.Sprintf("%d", version),
		})
	}
	return result
}

func hasBadge(badges map[string]int, name string) bool {
	_, ok := badges[name]
	return ok
}

func banText(duration int) string {
	if duration == 0 {
		return "permanently banned"
	}
	if duration < 60 {
		return fmt.Sprintf("timed out for %ds", duration)
	}
	if duration < 3600 {
		return fmt.Sprintf("timed out for %dm", duration/60)
	}
	return fmt.Sprintf("timed out for %dh", duration/3600)
}

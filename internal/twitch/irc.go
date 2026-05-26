package twitch

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/miwi/streamerchat/internal/chat"

	twitch "github.com/gempir/go-twitch-irc/v4"
)

// IRCClient wraps the Twitch IRC connection.
type IRCClient struct {
	client       *twitch.Client
	channel      string
	messages     chan chat.Message
	joins        chan chat.UserJoinPart
	settings     chan chat.ChatSettings
	errors       chan error
	roomNames    map[string]string           // roomID -> channel name cache
	resolveRoom  func(roomID string) string  // callback to resolve room IDs
}

// NewIRCClient creates a new Twitch IRC client.
func NewIRCClient(username, oauthToken, channel string) *IRCClient {
	client := twitch.NewClient(username, "oauth:"+strings.TrimPrefix(oauthToken, "oauth:"))

	// Explicitly request all capabilities including membership for JOIN/PART
	client.Capabilities = []string{
		"twitch.tv/tags",
		"twitch.tv/commands",
		"twitch.tv/membership",
	}

	irc := &IRCClient{
		client:    client,
		channel:   strings.TrimPrefix(channel, "#"),
		messages:  make(chan chat.Message, 256),
		joins:     make(chan chat.UserJoinPart, 256),
		settings:  make(chan chat.ChatSettings, 16),
		errors:    make(chan error, 16),
		roomNames: make(map[string]string),
	}

	irc.registerHandlers()
	return irc
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

// Connect starts the IRC connection and joins the channel.
func (c *IRCClient) Connect() error {
	c.client.Join(c.channel)
	return c.client.Connect()
}

// Disconnect closes the IRC connection.
func (c *IRCClient) Disconnect() {
	c.client.Disconnect()
}

// Say sends a message to the channel.
func (c *IRCClient) Say(text string) {
	c.client.Say(c.channel, text)
}

// SayTo sends a message to a specific channel (must be joined first).
func (c *IRCClient) SayTo(channel, text string) {
	c.client.Say(strings.TrimPrefix(channel, "#"), text)
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
		c.messages <- m
	})

	c.client.OnUserJoinMessage(func(msg twitch.UserJoinMessage) {
		log.Printf("[IRC] JOIN %s in #%s", msg.User, msg.Channel)
		c.joins <- chat.UserJoinPart{
			Platform: chat.PlatformTwitch,
			Channel:  msg.Channel,
			Username: msg.User,
			IsJoin:   true,
			Time:     time.Now(),
		}
	})

	c.client.OnUserPartMessage(func(msg twitch.UserPartMessage) {
		log.Printf("[IRC] PART %s from #%s", msg.User, msg.Channel)
		c.joins <- chat.UserJoinPart{
			Platform: chat.PlatformTwitch,
			Channel:  msg.Channel,
			Username: msg.User,
			IsJoin:   false,
			Time:     time.Now(),
		}
	})

	c.client.OnClearChatMessage(func(msg twitch.ClearChatMessage) {
		if msg.TargetUsername != "" {
			// User was banned/timed out
			msgType := chat.MessageTypeBan
			if msg.BanDuration > 0 {
				msgType = chat.MessageTypeTimeout
			}
			c.messages <- chat.Message{
				Platform:       chat.PlatformTwitch,
				Type:           msgType,
				Channel:        msg.Channel,
				Timestamp:      time.Now(),
				TargetUsername: msg.TargetUsername,
				BanDuration:    msg.BanDuration,
				Text:           fmt.Sprintf("%s was %s", msg.TargetUsername, banText(msg.BanDuration)),
			}
		} else {
			// Chat was cleared
			c.messages <- chat.Message{
				Platform:  chat.PlatformTwitch,
				Type:      chat.MessageTypeClearChat,
				Channel:   msg.Channel,
				Timestamp: time.Now(),
				Text:      "Chat was cleared by a moderator",
			}
		}
	})

	c.client.OnClearMessage(func(msg twitch.ClearMessage) {
		c.messages <- chat.Message{
			Platform:       chat.PlatformTwitch,
			Type:           chat.MessageTypeDeletedMessage,
			Channel:        msg.Channel,
			Timestamp:      time.Now(),
			TargetUsername: msg.Login,
			Text:           msg.Message,
			ID:             msg.TargetMsgID,
		}
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
			Text:        msg.SystemMsg,
		}

		switch msg.MsgID {
		case "sub", "resub":
			m.Type = chat.MessageTypeSub
		case "subgift", "anonsubgift":
			m.Type = chat.MessageTypeGiftSub
		case "raid":
			m.Type = chat.MessageTypeRaid
		case "announcement":
			m.Type = chat.MessageTypeAnnouncement
		default:
			m.Type = chat.MessageTypeSystem
		}

		c.messages <- m
	})

	c.client.OnRoomStateMessage(func(msg twitch.RoomStateMessage) {
		s := chat.ChatSettings{
			SlowMode:     msg.Tags["slow"] != "0" && msg.Tags["slow"] != "",
			SubOnly:      msg.Tags["subs-only"] == "1",
			EmoteOnly:    msg.Tags["emote-only"] == "1",
			FollowerOnly: msg.Tags["followers-only"] != "-1" && msg.Tags["followers-only"] != "",
		}
		c.settings <- s
	})

	c.client.OnReconnectMessage(func(_ twitch.ReconnectMessage) {
		c.messages <- chat.Message{
			Platform:  chat.PlatformTwitch,
			Type:      chat.MessageTypeSystem,
			Timestamp: time.Now(),
			Text:      "Reconnecting...",
		}
	})

	c.client.OnConnect(func() {
		c.messages <- chat.Message{
			Platform:  chat.PlatformTwitch,
			Type:      chat.MessageTypeSystem,
			Timestamp: time.Now(),
			Text:      fmt.Sprintf("Connected to #%s", c.channel),
		}
	})
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
	}

	// Extract parsed emote positions from the library
	for _, e := range msg.Emotes {
		for _, pos := range e.Positions {
			m.TwitchEmoteRanges = append(m.TwitchEmoteRanges, chat.EmoteRange{
				ID:    e.ID,
				Name:  e.Name,
				Start: pos.Start,
				End:   pos.End,
			})
		}
	}

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

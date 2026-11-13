package twitch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/miwi/streamerchat/internal/chat"

	twitch "github.com/gempir/go-twitch-irc/v4"
)

// MultiIRCClient connects anonymously to Twitch IRC and reads from multiple channels.
// Used by ChatHub viewer. No write access (anonymous accounts can't send messages).
type MultiIRCClient struct {
	// mu guards client and channels. client is replaced on every
	// reconnect while Join/Part are called from Wails-bound methods on
	// other goroutines, so it must never be read without mu.
	mu       sync.RWMutex
	client   *twitch.Client
	channels map[string]bool

	onMessage  func(channel string, msg chat.Message)
	onJoinPart func(channel string, jp chat.UserJoinPart)
	onSettings func(channel string, s chat.ChatSettings)
	onSystem   func(channel string, text string)

	roomNames   map[string]string
	resolveRoom func(roomID string) string
	roomMu      sync.RWMutex
}

// NewMultiIRCClient creates an anonymous IRC client.
// Twitch allows anonymous read-only login with username "justinfan<number>".
func NewMultiIRCClient() *MultiIRCClient {
	m := &MultiIRCClient{
		channels:  make(map[string]bool),
		roomNames: make(map[string]string),
	}
	m.client = m.newClient()
	return m
}

// newClient builds an anonymous (justinfan) read-only client with all
// handlers registered on it.
func (m *MultiIRCClient) newClient() *twitch.Client {
	c := twitch.NewAnonymousClient()
	c.Capabilities = []string{
		"twitch.tv/tags",
		"twitch.tv/commands",
		"twitch.tv/membership",
	}
	m.registerHandlers(c)
	return c
}

// Reconnect timing. A connection that stayed up at least
// healthySessionMin counts as healthy and resets the backoff — before, the
// backoff only grew, so after a few blips every reconnect took 60s.
const (
	reconnectBackoffMin = 2 * time.Second
	reconnectBackoffMax = 60 * time.Second
	healthySessionMin   = 30 * time.Second
)

func (m *MultiIRCClient) OnMessage(fn func(channel string, msg chat.Message))     { m.onMessage = fn }
func (m *MultiIRCClient) OnJoinPart(fn func(channel string, jp chat.UserJoinPart)) { m.onJoinPart = fn }
func (m *MultiIRCClient) OnSettings(fn func(channel string, s chat.ChatSettings)) { m.onSettings = fn }
func (m *MultiIRCClient) OnSystem(fn func(channel string, text string))            { m.onSystem = fn }

func (m *MultiIRCClient) SetRoomResolver(fn func(roomID string) string) {
	m.resolveRoom = fn
}

func (m *MultiIRCClient) resolveRoomName(roomID string) string {
	m.roomMu.RLock()
	if name, ok := m.roomNames[roomID]; ok {
		m.roomMu.RUnlock()
		return name
	}
	m.roomMu.RUnlock()
	if m.resolveRoom != nil {
		if name := m.resolveRoom(roomID); name != "" {
			m.roomMu.Lock()
			m.roomNames[roomID] = name
			m.roomMu.Unlock()
			return name
		}
	}
	return roomID
}

// Channels returns the currently joined channel list.
func (m *MultiIRCClient) Channels() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.channels))
	for c := range m.channels {
		out = append(out, c)
	}
	return out
}

// JoinChannel adds a channel to the watch list. We deliberately don't
// emit a "Joined #channel" system message — the tab + live indicator
// already convey that the channel is being watched, and at startup
// firing 22 of these floods the chat view before any real chatter
// activity shows up.
func (m *MultiIRCClient) JoinChannel(channel string) {
	channel = strings.ToLower(strings.TrimPrefix(channel, "#"))
	m.mu.Lock()
	already := m.channels[channel]
	m.channels[channel] = true
	client := m.client
	m.mu.Unlock()
	if !already {
		client.Join(channel)
	}
}

// PartChannel removes a channel from the watch list.
func (m *MultiIRCClient) PartChannel(channel string) {
	channel = strings.ToLower(strings.TrimPrefix(channel, "#"))
	m.mu.Lock()
	delete(m.channels, channel)
	client := m.client
	m.mu.Unlock()
	client.Depart(channel)
	if m.onSystem != nil {
		m.onSystem(channel, fmt.Sprintf("Left #%s", channel))
	}
}

// Connect starts the IRC connection. Reconnects automatically on disconnect.
func (m *MultiIRCClient) Connect(ctx context.Context) {
	backoff := reconnectBackoffMin

	for {
		m.mu.RLock()
		client := m.client
		m.mu.RUnlock()

		select {
		case <-ctx.Done():
			client.Disconnect()
			return
		default:
		}

		started := time.Now()
		err := client.Connect()
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= healthySessionMin {
			backoff = reconnectBackoffMin
		}

		// Connection ended - notify all channels
		m.mu.RLock()
		for ch := range m.channels {
			if m.onSystem != nil {
				m.onSystem(ch, fmt.Sprintf("Disconnected: %v - reconnecting...", err))
			}
		}
		m.mu.RUnlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > reconnectBackoffMax {
			backoff = reconnectBackoffMax
		}

		// Fresh client, re-join all channels. Swap + joins happen under
		// mu so a concurrent JoinChannel either lands in m.channels before
		// the loop (joined here) or sees the new client.
		m.mu.Lock()
		m.client = m.newClient()
		for ch := range m.channels {
			m.client.Join(ch)
		}
		m.mu.Unlock()
	}
}

func (m *MultiIRCClient) registerHandlers(c *twitch.Client) {
	c.OnPrivateMessage(func(msg twitch.PrivateMessage) {
		converted := convertPrivateMessage(msg, msg.Channel)
		if converted.IsSharedChat && converted.SourceChannel == msg.Source.RoomID {
			converted.SourceChannel = m.resolveRoomName(msg.Source.RoomID)
		}
		if m.onMessage != nil {
			m.onMessage(msg.Channel, converted)
		}
	})

	c.OnUserJoinMessage(func(msg twitch.UserJoinMessage) {
		if m.onJoinPart != nil {
			m.onJoinPart(msg.Channel, chat.UserJoinPart{
				Platform: chat.PlatformTwitch,
				Channel:  msg.Channel,
				Username: msg.User,
				IsJoin:   true,
				Time:     time.Now(),
			})
		}
	})

	c.OnUserPartMessage(func(msg twitch.UserPartMessage) {
		if m.onJoinPart != nil {
			m.onJoinPart(msg.Channel, chat.UserJoinPart{
				Platform: chat.PlatformTwitch,
				Channel:  msg.Channel,
				Username: msg.User,
				IsJoin:   false,
				Time:     time.Now(),
			})
		}
	})

	c.OnClearChatMessage(func(msg twitch.ClearChatMessage) {
		if m.onMessage == nil {
			return
		}
		if msg.TargetUsername != "" {
			msgType := chat.MessageTypeBan
			if msg.BanDuration > 0 {
				msgType = chat.MessageTypeTimeout
			}
			m.onMessage(msg.Channel, chat.Message{
				Platform:       chat.PlatformTwitch,
				Type:           msgType,
				Channel:        msg.Channel,
				Timestamp:      time.Now(),
				TargetUsername: msg.TargetUsername,
				BanDuration:    msg.BanDuration,
				Text:           fmt.Sprintf("%s was %s", msg.TargetUsername, banText(msg.BanDuration)),
			})
		} else {
			m.onMessage(msg.Channel, chat.Message{
				Platform:  chat.PlatformTwitch,
				Type:      chat.MessageTypeClearChat,
				Channel:   msg.Channel,
				Timestamp: time.Now(),
				Text:      "Chat was cleared by a moderator",
			})
		}
	})

	c.OnClearMessage(func(msg twitch.ClearMessage) {
		if m.onMessage != nil {
			m.onMessage(msg.Channel, chat.Message{
				Platform:       chat.PlatformTwitch,
				Type:           chat.MessageTypeDeletedMessage,
				Channel:        msg.Channel,
				Timestamp:      time.Now(),
				TargetUsername: msg.Login,
				Text:           msg.Message,
				ID:             msg.TargetMsgID,
			})
		}
	})

	c.OnUserNoticeMessage(func(msg twitch.UserNoticeMessage) {
		if m.onMessage == nil {
			return
		}
		out := chat.Message{
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
		switch msg.MsgID {
		case "sub", "resub":
			out.Type = chat.MessageTypeSub
		case "subgift", "anonsubgift":
			out.Type = chat.MessageTypeGiftSub
		case "raid":
			out.Type = chat.MessageTypeRaid
		case "announcement":
			out.Type = chat.MessageTypeAnnouncement
		default:
			out.Type = chat.MessageTypeSystem
		}
		m.onMessage(msg.Channel, out)
	})

	c.OnRoomStateMessage(func(msg twitch.RoomStateMessage) {
		if m.onSettings != nil {
			m.onSettings(msg.Channel, chat.ChatSettings{
				SlowMode:     msg.Tags["slow"] != "0" && msg.Tags["slow"] != "",
				SubOnly:      msg.Tags["subs-only"] == "1",
				EmoteOnly:    msg.Tags["emote-only"] == "1",
				FollowerOnly: msg.Tags["followers-only"] != "-1" && msg.Tags["followers-only"] != "",
			})
		}
	})

	c.OnConnect(func() {
		// IRC connection is implicit — at startup this would fire one
		// "Connected to #channel" line per watched channel, drowning the
		// historical messages we just loaded. The connect lifecycle
		// stays in the log instead.
		m.mu.RLock()
		n := len(m.channels)
		m.mu.RUnlock()
		log.Printf("[IRC] read client connected (%d channels)", n)
	})
}

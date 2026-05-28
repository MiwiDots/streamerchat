package twitch

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/miwi/streamerchat/internal/chat"

	twitch "github.com/gempir/go-twitch-irc/v4"
)

// MultiIRCClient connects anonymously to Twitch IRC and reads from multiple channels.
// Used by ChatHub viewer. No write access (anonymous accounts can't send messages).
type MultiIRCClient struct {
	client *twitch.Client

	mu       sync.RWMutex
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
	anonName := fmt.Sprintf("justinfan%d", rand.Intn(99999)+10000)
	client := twitch.NewAnonymousClient()
	_ = anonName
	client.Capabilities = []string{
		"twitch.tv/tags",
		"twitch.tv/commands",
		"twitch.tv/membership",
	}

	m := &MultiIRCClient{
		client:    client,
		channels:  make(map[string]bool),
		roomNames: make(map[string]string),
	}
	m.registerHandlers()
	return m
}

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
	m.mu.Unlock()
	if !already {
		m.client.Join(channel)
	}
}

// PartChannel removes a channel from the watch list.
func (m *MultiIRCClient) PartChannel(channel string) {
	channel = strings.ToLower(strings.TrimPrefix(channel, "#"))
	m.mu.Lock()
	delete(m.channels, channel)
	m.mu.Unlock()
	m.client.Depart(channel)
	if m.onSystem != nil {
		m.onSystem(channel, fmt.Sprintf("Left #%s", channel))
	}
}

// Connect starts the IRC connection. Reconnects automatically on disconnect.
func (m *MultiIRCClient) Connect(ctx context.Context) {
	backoff := 2 * time.Second
	maxBackoff := 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			m.client.Disconnect()
			return
		default:
		}

		err := m.client.Connect()
		if ctx.Err() != nil {
			return
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
		if backoff > maxBackoff {
			backoff = maxBackoff
		}

		// Re-join all channels after reconnect
		m.client = twitch.NewAnonymousClient()
		m.client.Capabilities = []string{
			"twitch.tv/tags",
			"twitch.tv/commands",
			"twitch.tv/membership",
		}
		m.registerHandlers()
		m.mu.RLock()
		for ch := range m.channels {
			m.client.Join(ch)
		}
		m.mu.RUnlock()
	}
}

func (m *MultiIRCClient) registerHandlers() {
	m.client.OnPrivateMessage(func(msg twitch.PrivateMessage) {
		converted := convertPrivateMessage(msg, msg.Channel)
		if converted.IsSharedChat && converted.SourceChannel == msg.Source.RoomID {
			converted.SourceChannel = m.resolveRoomName(msg.Source.RoomID)
		}
		if m.onMessage != nil {
			m.onMessage(msg.Channel, converted)
		}
	})

	m.client.OnUserJoinMessage(func(msg twitch.UserJoinMessage) {
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

	m.client.OnUserPartMessage(func(msg twitch.UserPartMessage) {
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

	m.client.OnClearChatMessage(func(msg twitch.ClearChatMessage) {
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

	m.client.OnClearMessage(func(msg twitch.ClearMessage) {
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

	m.client.OnUserNoticeMessage(func(msg twitch.UserNoticeMessage) {
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
			Text:        msg.SystemMsg,
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

	m.client.OnRoomStateMessage(func(msg twitch.RoomStateMessage) {
		if m.onSettings != nil {
			m.onSettings(msg.Channel, chat.ChatSettings{
				SlowMode:     msg.Tags["slow"] != "0" && msg.Tags["slow"] != "",
				SubOnly:      msg.Tags["subs-only"] == "1",
				EmoteOnly:    msg.Tags["emote-only"] == "1",
				FollowerOnly: msg.Tags["followers-only"] != "-1" && msg.Tags["followers-only"] != "",
			})
		}
	})

	m.client.OnConnect(func() {
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

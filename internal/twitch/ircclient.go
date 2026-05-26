package twitch

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miwi/streamerchat/internal/chat"
)

// FastIRCClient is a minimal, low-latency Twitch IRC client.
// Uses direct callbacks instead of channels for zero-latency message delivery.
type FastIRCClient struct {
	conn        net.Conn
	writer      *bufio.Writer
	writeMu     sync.Mutex
	username    string
	oauthToken  string
	channel     string
	done        chan struct{}
	roomNames   map[string]string
	resolveRoom func(roomID string) string

	// Direct callbacks - called inline from the read loop
	onMessage  func(chat.Message)
	onJoinPart func(chat.UserJoinPart)
	onSettings func(chat.ChatSettings)
	onError    func(error)
}

// NewFastIRCClient creates a new low-latency Twitch IRC client.
func NewFastIRCClient(username, oauthToken, channel string) *FastIRCClient {
	return &FastIRCClient{
		username:   username,
		oauthToken: strings.TrimPrefix(oauthToken, "oauth:"),
		channel:    strings.ToLower(strings.TrimPrefix(channel, "#")),
		done:       make(chan struct{}),
		roomNames:  make(map[string]string),
	}
}

func (c *FastIRCClient) OnMessage(fn func(chat.Message))      { c.onMessage = fn }
func (c *FastIRCClient) OnJoinPart(fn func(chat.UserJoinPart)) { c.onJoinPart = fn }
func (c *FastIRCClient) OnSettings(fn func(chat.ChatSettings)) { c.onSettings = fn }
func (c *FastIRCClient) OnError(fn func(error))                { c.onError = fn }

func (c *FastIRCClient) SetRoomResolver(fn func(roomID string) string) {
	c.resolveRoom = fn
}

func (c *FastIRCClient) resolveRoomName(roomID string) string {
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

// Connect establishes the IRC connection and starts the read loop.
func (c *FastIRCClient) Connect() error {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 10 * time.Second,
	}

	rawConn, err := dialer.Dial("tcp", "irc.chat.twitch.tv:6697")
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	if tcpConn, ok := rawConn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
	}

	c.conn = tls.Client(rawConn, &tls.Config{ServerName: "irc.chat.twitch.tv"})
	c.writer = bufio.NewWriterSize(c.conn, 512)

	c.sendRaw("CAP REQ :twitch.tv/tags twitch.tv/commands twitch.tv/membership")
	c.sendRaw("PASS oauth:" + c.oauthToken)
	c.sendRaw("NICK " + c.username)
	c.sendRaw("JOIN #" + c.channel)

	if c.onMessage != nil {
		c.onMessage(chat.Message{
			Platform:  chat.PlatformTwitch,
			Type:      chat.MessageTypeSystem,
			Timestamp: time.Now(),
			Text:      fmt.Sprintf("Connected to #%s", c.channel),
		})
	}

	go c.pingLoop()

	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 0, 4096), 65536)

	for scanner.Scan() {
		c.handleLine(scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		select {
		case <-c.done:
			return nil
		default:
			return fmt.Errorf("read: %w", err)
		}
	}
	return nil
}

func (c *FastIRCClient) Disconnect() {
	close(c.done)
	if c.conn != nil {
		c.conn.Close()
	}
}

func (c *FastIRCClient) Say(text string) {
	c.sendRaw("PRIVMSG #" + c.channel + " :" + text)
}

func (c *FastIRCClient) sendRaw(line string) {
	c.writeMu.Lock()
	c.writer.WriteString(line + "\r\n")
	c.writer.Flush()
	c.writeMu.Unlock()
}

func (c *FastIRCClient) pingLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.sendRaw("PING :tmi.twitch.tv")
		}
	}
}

func (c *FastIRCClient) handleLine(raw string) {
	if strings.HasPrefix(raw, "PING") {
		c.sendRaw("PONG" + raw[4:])
		return
	}

	tags, prefix, command, params := parseIRC(raw)

	switch command {
	case "PRIVMSG":
		c.handlePrivmsg(tags, prefix, params)
	case "JOIN":
		if user := parseUser(prefix); user != "" && c.onJoinPart != nil {
			c.onJoinPart(chat.UserJoinPart{
				Platform: chat.PlatformTwitch, Channel: c.channel,
				Username: user, IsJoin: true, Time: time.Now(),
			})
		}
	case "PART":
		if user := parseUser(prefix); user != "" && c.onJoinPart != nil {
			c.onJoinPart(chat.UserJoinPart{
				Platform: chat.PlatformTwitch, Channel: c.channel,
				Username: user, IsJoin: false, Time: time.Now(),
			})
		}
	case "CLEARCHAT":
		c.handleClearChat(tags, params)
	case "CLEARMSG":
		c.handleClearMsg(tags, params)
	case "USERNOTICE":
		c.handleUserNotice(tags, params)
	case "353", "366": // NAMES - ignored, using Helix API instead (like Chatterino)
	case "ROOMSTATE":
		if c.onSettings != nil {
			c.onSettings(chat.ChatSettings{
				SlowMode:     tags["slow"] != "0" && tags["slow"] != "",
				SubOnly:      tags["subs-only"] == "1",
				EmoteOnly:    tags["emote-only"] == "1",
				FollowerOnly: tags["followers-only"] != "-1" && tags["followers-only"] != "",
			})
		}
	case "RECONNECT":
		if c.onMessage != nil {
			c.onMessage(chat.Message{
				Platform: chat.PlatformTwitch, Type: chat.MessageTypeSystem,
				Timestamp: time.Now(), Text: "Reconnecting...",
			})
		}
	}
}

func (c *FastIRCClient) handlePrivmsg(tags map[string]string, prefix, params string) {
	if c.onMessage == nil {
		return
	}

	text := ""
	if idx := strings.Index(params, " :"); idx != -1 {
		text = params[idx+2:]
	}

	user := parseUser(prefix)
	displayName := tags["display-name"]
	if displayName == "" {
		displayName = user
	}

	badges := parseBadgeTag(tags["badges"])
	badgeMap := badgeTagToMap(tags["badges"])

	msg := chat.Message{
		ID:            tags["id"],
		Platform:      chat.PlatformTwitch,
		Type:          chat.MessageTypeChat,
		Channel:       c.channel,
		Timestamp:     parseTimestamp(tags["tmi-sent-ts"]),
		UserID:        tags["user-id"],
		Username:      user,
		DisplayName:   displayName,
		Color:         tags["color"],
		Badges:        badges,
		IsMod:         badgeMap["moderator"],
		IsVIP:         badgeMap["vip"],
		IsSub:         badgeMap["subscriber"],
		IsBroadcaster: badgeMap["broadcaster"],
		Text:          text,
		TwitchEmotes:  tags["emotes"],
	}

	if bits := tags["bits"]; bits != "" {
		msg.Bits, _ = strconv.Atoi(bits)
	}

	sourceRoomID := tags["source-room-id"]
	roomID := tags["room-id"]
	if sourceRoomID != "" && sourceRoomID != roomID {
		msg.IsSharedChat = true
		msg.SourceChannel = c.resolveRoomName(sourceRoomID)
		if sourceBadges := tags["source-badges"]; sourceBadges != "" {
			msg.Badges = parseBadgeTag(sourceBadges)
			srcMap := badgeTagToMap(sourceBadges)
			msg.IsMod = srcMap["moderator"]
			msg.IsVIP = srcMap["vip"]
			msg.IsSub = srcMap["subscriber"]
			msg.IsBroadcaster = srcMap["broadcaster"]
		}
	}

	// Replace text emotes with Unicode and collapse spam
	msg.Text = ReplaceTextEmotes(msg.Text)
	msg.Text = CollapseSpam(msg.Text)

	c.onMessage(msg)
}

func (c *FastIRCClient) handleClearChat(tags map[string]string, params string) {
	if c.onMessage == nil {
		return
	}
	target := ""
	if idx := strings.Index(params, " :"); idx != -1 {
		target = params[idx+2:]
	}

	if target != "" {
		duration, _ := strconv.Atoi(tags["ban-duration"])
		msgType := chat.MessageTypeBan
		if duration > 0 {
			msgType = chat.MessageTypeTimeout
		}
		c.onMessage(chat.Message{
			Platform: chat.PlatformTwitch, Type: msgType, Channel: c.channel,
			Timestamp: time.Now(), TargetUsername: target, BanDuration: duration,
			Text: fmt.Sprintf("%s was %s", target, banText(duration)),
		})
	} else {
		c.onMessage(chat.Message{
			Platform: chat.PlatformTwitch, Type: chat.MessageTypeClearChat,
			Channel: c.channel, Timestamp: time.Now(),
			Text: "Chat was cleared by a moderator",
		})
	}
}

func (c *FastIRCClient) handleClearMsg(tags map[string]string, params string) {
	if c.onMessage == nil {
		return
	}
	text := ""
	if idx := strings.Index(params, " :"); idx != -1 {
		text = params[idx+2:]
	}
	c.onMessage(chat.Message{
		Platform: chat.PlatformTwitch, Type: chat.MessageTypeDeletedMessage,
		Channel: c.channel, Timestamp: time.Now(),
		TargetUsername: tags["login"], Text: text, ID: tags["target-msg-id"],
	})
}

func (c *FastIRCClient) handleUserNotice(tags map[string]string, params string) {
	if c.onMessage == nil {
		return
	}
	text := ""
	if idx := strings.Index(params, " :"); idx != -1 {
		text = params[idx+2:]
	}

	m := chat.Message{
		Platform: chat.PlatformTwitch, Channel: c.channel, Timestamp: time.Now(),
		UserID: tags["user-id"], Username: tags["login"],
		DisplayName: tags["display-name"], Text: tags["system-msg"],
	}

	switch tags["msg-id"] {
	case "sub", "resub":
		m.Type = chat.MessageTypeSub
	case "subgift", "anonsubgift":
		m.Type = chat.MessageTypeGiftSub
	case "raid":
		m.Type = chat.MessageTypeRaid
	case "announcement":
		m.Type = chat.MessageTypeAnnouncement
		m.Text = text
	default:
		m.Type = chat.MessageTypeSystem
	}

	c.onMessage(m)
}

// unescapeIRCTag decodes IRC tag value escapes per IRCv3 spec.
func unescapeIRCTag(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer(
		`\s`, " ",
		`\n`, "\n",
		`\r`, "\r",
		`\\`, `\`,
		`\:`, ";",
	)
	return r.Replace(s)
}

// IRC parsing helpers

func parseIRC(raw string) (tags map[string]string, prefix, command, params string) {
	tags = make(map[string]string)
	line := raw

	if strings.HasPrefix(line, "@") {
		idx := strings.Index(line, " ")
		if idx == -1 {
			return
		}
		tagStr := line[1:idx]
		line = line[idx+1:]
		for _, pair := range strings.Split(tagStr, ";") {
			if eqIdx := strings.Index(pair, "="); eqIdx != -1 {
				tags[pair[:eqIdx]] = unescapeIRCTag(pair[eqIdx+1:])
			}
		}
	}

	if strings.HasPrefix(line, ":") {
		idx := strings.Index(line, " ")
		if idx == -1 {
			return
		}
		prefix = line[1:idx]
		line = line[idx+1:]
	}

	idx := strings.Index(line, " ")
	if idx == -1 {
		command = line
		return
	}
	command = line[:idx]
	params = line[idx+1:]
	return
}

func parseUser(prefix string) string {
	if idx := strings.Index(prefix, "!"); idx != -1 {
		return prefix[:idx]
	}
	return prefix
}

func parseBadgeTag(badgeStr string) []chat.Badge {
	if badgeStr == "" {
		return nil
	}
	parts := strings.Split(badgeStr, ",")
	badges := make([]chat.Badge, 0, len(parts))
	for _, part := range parts {
		if idx := strings.Index(part, "/"); idx != -1 {
			badges = append(badges, chat.Badge{Name: part[:idx], Version: part[idx+1:]})
		}
	}
	return badges
}

func badgeTagToMap(badgeStr string) map[string]bool {
	m := make(map[string]bool)
	for _, part := range strings.Split(badgeStr, ",") {
		if idx := strings.Index(part, "/"); idx != -1 {
			m[part[:idx]] = true
		}
	}
	return m
}

func parseTimestamp(ts string) time.Time {
	if ts == "" {
		return time.Now()
	}
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return time.Now()
	}
	return time.UnixMilli(ms)
}

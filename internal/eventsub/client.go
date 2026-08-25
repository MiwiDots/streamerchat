// Package eventsub implements a Twitch EventSub client over the
// WebSocket transport. It subscribes to the activity events the
// broadcaster cares about (follows, subs, resubs, gifts, raids,
// bits/cheers, channel-points redemptions, hype train) and fans
// them out as normalized Event values via a single callback.
//
// Flow:
//  1. Dial wss://eventsub.wss.twitch.tv/ws.
//  2. First message is session_welcome; extract session.id.
//  3. For each subscription type, POST /helix/eventsub/subscriptions
//     with transport {method:"websocket", session_id: <id>}.
//  4. Server pushes notification messages; we normalize into Event
//     and hand off to the caller.
//  5. session_keepalive → ignore. session_reconnect → dial the new
//     URL, then swap the connection with no missed events (Twitch
//     keeps the old socket open for 30s during handoff).
//  6. On unexpected disconnect / read error, exponential backoff
//     then start over from step 1 (Twitch re-issues session id, so
//     we re-subscribe).
//
// Deliberately simple: one goroutine reads, dispatches inline. The
// caller's callback runs on the reader goroutine — keep it cheap.
package eventsub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const defaultWSURL = "wss://eventsub.wss.twitch.tv/ws"
const subscriptionsURL = "https://api.twitch.tv/helix/eventsub/subscriptions"

// EventType is the normalized activity kind the frontend renders.
type EventType string

const (
	EventFollow    EventType = "follow"
	EventSubscribe EventType = "subscribe"
	EventResub     EventType = "resub"
	EventGift      EventType = "gift"
	EventRaid      EventType = "raid"
	EventCheer     EventType = "cheer"
	EventReward    EventType = "reward"
	EventHypeTrain EventType = "hype_train"
)

// Event is the normalized shape the caller consumes. Meta carries
// per-type details (tier, viewer count, bits amount, reward name,
// hype train level, ...) so the UI can render without knowing the
// full Twitch schema.
type Event struct {
	Type      EventType              `json:"type"`
	User      string                 `json:"user"`
	UserID    string                 `json:"userId"`
	Message   string                 `json:"message"`
	Meta      map[string]interface{} `json:"meta"`
	Timestamp time.Time              `json:"timestamp"`
}

// Client is a running EventSub session. Construct with New, then
// call Start (blocking) or launch it in a goroutine.
type Client struct {
	ctx           context.Context
	clientID      string
	accessToken   string
	broadcasterID string
	moderatorID   string
	onEvent       func(Event)

	mu        sync.Mutex
	sessionID string
}

// New builds a Client. broadcasterID is the channel owner's Twitch
// user id; moderatorID is who's asking (usually the same for
// streamerchat since the broadcaster IS logged in). onEvent runs
// on the reader goroutine — must not block.
func New(ctx context.Context, clientID, accessToken, broadcasterID, moderatorID string, onEvent func(Event)) *Client {
	return &Client{
		ctx:           ctx,
		clientID:      clientID,
		accessToken:   accessToken,
		broadcasterID: broadcasterID,
		moderatorID:   moderatorID,
		onEvent:       onEvent,
	}
}

// Start runs the session forever. Returns when ctx is cancelled.
// Reconnects on any error with exponential backoff up to 60s.
func (c *Client) Start() {
	backoff := 2 * time.Second
	for {
		if c.ctx.Err() != nil {
			return
		}
		if err := c.runSession(defaultWSURL); err != nil {
			log.Printf("[EVENTSUB] session error: %v", err)
		}
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
	}
}

// runSession dials, welcomes, subscribes, then reads until an error
// occurs. On session_reconnect it dials the new URL and swaps.
func (c *Client) runSession(url string) error {
	conn, _, err := websocket.DefaultDialer.DialContext(c.ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	log.Printf("[EVENTSUB] connected to %s", url)

	// First message MUST be session_welcome. Give Twitch a bounded
	// amount of time to send it before giving up on the connection.
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read welcome: %w", err)
	}
	var welcome wsMessage
	if err := json.Unmarshal(data, &welcome); err != nil {
		return fmt.Errorf("parse welcome: %w", err)
	}
	if welcome.Metadata.MessageType != "session_welcome" {
		return fmt.Errorf("expected session_welcome, got %s", welcome.Metadata.MessageType)
	}
	sessionID := welcome.Payload.Session.ID
	c.mu.Lock()
	c.sessionID = sessionID
	c.mu.Unlock()
	log.Printf("[EVENTSUB] session id: %s", sessionID)

	// Subscribe. Failures here are fatal for this session — we bail
	// so Start() reconnects with a fresh id.
	if err := c.subscribeAll(sessionID); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	// Twitch says keepalive comes every 10s by default. Use a 30s
	// read deadline to catch dead sockets without being too twitchy.
	for {
		if c.ctx.Err() != nil {
			return nil
		}
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		var msg wsMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[EVENTSUB] parse: %v", err)
			continue
		}
		switch msg.Metadata.MessageType {
		case "session_keepalive":
			// nothing to do — the read itself resets the deadline
		case "notification":
			c.dispatch(msg)
		case "session_reconnect":
			newURL := msg.Payload.Session.ReconnectURL
			if newURL == "" {
				return errors.New("session_reconnect without url")
			}
			log.Printf("[EVENTSUB] reconnect to %s", newURL)
			// Dial the new URL first; if that fails we let Start
			// handle the full reconnect cycle.
			return c.runSession(newURL)
		case "revocation":
			log.Printf("[EVENTSUB] subscription revoked: %+v", msg.Payload.Subscription)
		default:
			log.Printf("[EVENTSUB] unknown message type: %s", msg.Metadata.MessageType)
		}
	}
}

// subscribeAll fires the individual subscribe calls in sequence.
// Any single failure aborts because a partially subscribed session
// gives a misleading UI (some activity types missing). Every type
// carries the scope it needs in the comment so future-me knows
// exactly which login scope enables what.
func (c *Client) subscribeAll(sessionID string) error {
	me := c.broadcasterID
	mod := c.moderatorID
	subs := []subscribeRequest{
		// Follows use v2 which requires moderator_user_id + scope
		// moderator:read:followers.
		{Type: "channel.follow", Version: "2", Condition: map[string]string{
			"broadcaster_user_id": me, "moderator_user_id": mod,
		}},
		// New paid subscription (first time). Scope: channel:read:subscriptions.
		{Type: "channel.subscribe", Version: "1", Condition: map[string]string{"broadcaster_user_id": me}},
		// Gift sub bomb (batched gifts). Scope: channel:read:subscriptions.
		{Type: "channel.subscription.gift", Version: "1", Condition: map[string]string{"broadcaster_user_id": me}},
		// Resub with user-provided message. Scope: channel:read:subscriptions.
		{Type: "channel.subscription.message", Version: "1", Condition: map[string]string{"broadcaster_user_id": me}},
		// Bits / cheer. Scope: bits:read.
		{Type: "channel.cheer", Version: "1", Condition: map[string]string{"broadcaster_user_id": me}},
		// Raid TO me — no extra scope needed for the receiving side.
		{Type: "channel.raid", Version: "1", Condition: map[string]string{"to_broadcaster_user_id": me}},
		// Channel points reward redemption. Scope: channel:read:redemptions.
		{Type: "channel.channel_points_custom_reward_redemption.add", Version: "1",
			Condition: map[string]string{"broadcaster_user_id": me}},
		// Hype train. Scope: channel:read:hype_train.
		{Type: "channel.hype_train.begin", Version: "1", Condition: map[string]string{"broadcaster_user_id": me}},
		{Type: "channel.hype_train.progress", Version: "1", Condition: map[string]string{"broadcaster_user_id": me}},
		{Type: "channel.hype_train.end", Version: "1", Condition: map[string]string{"broadcaster_user_id": me}},
	}
	for _, s := range subs {
		if err := c.subscribe(sessionID, s); err != nil {
			// Missing-scope errors are per-type: log and continue
			// instead of aborting so the user still gets the events
			// their token DOES have scope for. Full re-login gives
			// them everything.
			log.Printf("[EVENTSUB] subscribe %s failed: %v", s.Type, err)
			continue
		}
	}
	return nil
}

type subscribeRequest struct {
	Type      string
	Version   string
	Condition map[string]string
}

func (c *Client) subscribe(sessionID string, s subscribeRequest) error {
	body := map[string]interface{}{
		"type":      s.Type,
		"version":   s.Version,
		"condition": s.Condition,
		"transport": map[string]string{
			"method":     "websocket",
			"session_id": sessionID,
		},
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(c.ctx, "POST", subscriptionsURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Client-Id", c.clientID)
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(msg))
	}
	return nil
}

// dispatch maps a raw notification into the normalized Event shape
// and hands it to the caller's callback.
func (c *Client) dispatch(msg wsMessage) {
	ev := Event{Timestamp: time.Now(), Meta: map[string]interface{}{}}
	sub := msg.Payload.Subscription.Type
	raw := msg.Payload.Event
	// The individual event shapes are documented at
	// https://dev.twitch.tv/docs/eventsub/eventsub-subscription-types/
	// — decoded into a permissive map so we don't have to keep a
	// per-type struct up to date.
	var e map[string]interface{}
	if err := json.Unmarshal(raw, &e); err != nil {
		log.Printf("[EVENTSUB] event parse (%s): %v", sub, err)
		return
	}
	switch sub {
	case "channel.follow":
		ev.Type = EventFollow
		ev.User = getStr(e, "user_name")
		ev.UserID = getStr(e, "user_id")
	case "channel.subscribe":
		ev.Type = EventSubscribe
		ev.User = getStr(e, "user_name")
		ev.UserID = getStr(e, "user_id")
		ev.Meta["tier"] = getStr(e, "tier")
		ev.Meta["isGift"] = getBool(e, "is_gift")
	case "channel.subscription.gift":
		ev.Type = EventGift
		ev.User = giftUserName(e)
		ev.UserID = getStr(e, "user_id")
		ev.Meta["total"] = getNum(e, "total")
		ev.Meta["tier"] = getStr(e, "tier")
		ev.Meta["cumulativeTotal"] = getNum(e, "cumulative_total")
		ev.Meta["isAnonymous"] = getBool(e, "is_anonymous")
	case "channel.subscription.message":
		ev.Type = EventResub
		ev.User = getStr(e, "user_name")
		ev.UserID = getStr(e, "user_id")
		if m, ok := e["message"].(map[string]interface{}); ok {
			ev.Message = getStr(m, "text")
		}
		ev.Meta["tier"] = getStr(e, "tier")
		ev.Meta["cumulativeMonths"] = getNum(e, "cumulative_months")
		ev.Meta["streakMonths"] = getNum(e, "streak_months")
	case "channel.cheer":
		ev.Type = EventCheer
		if getBool(e, "is_anonymous") {
			ev.User = "Anonymous"
		} else {
			ev.User = getStr(e, "user_name")
		}
		ev.UserID = getStr(e, "user_id")
		ev.Message = getStr(e, "message")
		ev.Meta["bits"] = getNum(e, "bits")
	case "channel.raid":
		ev.Type = EventRaid
		ev.User = getStr(e, "from_broadcaster_user_name")
		ev.UserID = getStr(e, "from_broadcaster_user_id")
		ev.Meta["viewers"] = getNum(e, "viewers")
	case "channel.channel_points_custom_reward_redemption.add":
		ev.Type = EventReward
		ev.User = getStr(e, "user_name")
		ev.UserID = getStr(e, "user_id")
		ev.Message = getStr(e, "user_input")
		if r, ok := e["reward"].(map[string]interface{}); ok {
			ev.Meta["reward"] = getStr(r, "title")
			ev.Meta["cost"] = getNum(r, "cost")
		}
	case "channel.hype_train.begin",
		"channel.hype_train.progress",
		"channel.hype_train.end":
		ev.Type = EventHypeTrain
		ev.User = "" // hype train has no single triggering user
		ev.Meta["phase"] = sub[len("channel.hype_train."):]
		ev.Meta["level"] = getNum(e, "level")
		ev.Meta["progress"] = getNum(e, "progress")
		ev.Meta["goal"] = getNum(e, "goal")
		ev.Meta["total"] = getNum(e, "total")
	default:
		log.Printf("[EVENTSUB] unhandled type: %s", sub)
		return
	}
	if c.onEvent != nil {
		c.onEvent(ev)
	}
}

// giftUserName handles the anonymous-gift edge case: is_anonymous
// true means user_name is empty; render as "Anonymous".
func giftUserName(e map[string]interface{}) string {
	if getBool(e, "is_anonymous") {
		return "Anonymous"
	}
	return getStr(e, "user_name")
}

// --- wire types ---

// wsMessage covers every message we receive on the socket. Payload
// is a lax union — the fields we don't need for a given metadata
// type stay zero.
type wsMessage struct {
	Metadata struct {
		MessageID           string    `json:"message_id"`
		MessageType         string    `json:"message_type"`
		MessageTimestamp    time.Time `json:"message_timestamp"`
		SubscriptionType    string    `json:"subscription_type,omitempty"`
		SubscriptionVersion string    `json:"subscription_version,omitempty"`
	} `json:"metadata"`
	Payload struct {
		Session struct {
			ID                      string `json:"id"`
			Status                  string `json:"status"`
			ConnectedAt             string `json:"connected_at"`
			KeepaliveTimeoutSeconds int    `json:"keepalive_timeout_seconds"`
			ReconnectURL            string `json:"reconnect_url"`
		} `json:"session"`
		Subscription struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Version string `json:"version"`
			Status  string `json:"status"`
		} `json:"subscription"`
		Event json.RawMessage `json:"event"`
	} `json:"payload"`
}

func getStr(m map[string]interface{}, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
func getBool(m map[string]interface{}, k string) bool {
	if v, ok := m[k].(bool); ok {
		return v
	}
	return false
}
func getNum(m map[string]interface{}, k string) float64 {
	if v, ok := m[k].(float64); ok {
		return v
	}
	return 0
}

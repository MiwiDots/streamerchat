// Package sevenTV provides a minimal client for the 7TV EventAPI v3.
// We subscribe to entitlement.* events per Twitch broadcaster so we
// learn which chatters have a paint / badge applied, then push those
// cosmetics down to the frontend so chat messages can render them.
//
// Endpoint: wss://events.7tv.io/v3
// Subscribe payload (opcode 35):
//
//	{"op":35,"d":{"type":"entitlement.*","condition":{"host_id":"<twitch_broadcaster_id>"}}}
//
// The exact entitlement payload shape isn't well documented; we log
// the raw frames at first so the wire format can be inspected and the
// parser hardened iteratively.
package sevenTV

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const wsURL = "wss://events.7tv.io/v3"

// Cosmetic is a flattened paint/badge entry the frontend can render.
// One per affected user_id. For a "solid" paint it's just the color
// hex; for a gradient/animated paint the CSS string carries the full
// background+webkit-background-clip recipe.
type Cosmetic struct {
	UserID    string `json:"userID"`    // Twitch user id this applies to
	PaintCSS  string `json:"paintCSS"`  // CSS to apply to the username span
	BadgeURL  string `json:"badgeURL"`  // small badge image to draw next to the name
	BadgeName string `json:"badgeName"` // tooltip for the badge
}

// Client is a single WebSocket connection that fans out subscriptions
// per Twitch channel. It runs an internal reconnect loop. Cosmetics
// learned from the wire are pushed via OnCosmetic.
type Client struct {
	ctx context.Context

	mu          sync.Mutex
	hostIDs     map[string]bool // pending + already-subscribed broadcaster ids
	conn        *websocket.Conn
	onCosmetic  func(c Cosmetic)
	paintCache  map[string]string // paint id -> css
	badgeCache  map[string]badge  // badge id -> info
	cosmeticsAt time.Time
}

type badge struct {
	URL  string
	Name string
}

// NewClient builds an idle client. Call Start to spin up the WS loop.
func NewClient(ctx context.Context, onCosmetic func(Cosmetic)) *Client {
	return &Client{
		ctx:        ctx,
		hostIDs:    make(map[string]bool),
		onCosmetic: onCosmetic,
		paintCache: make(map[string]string),
		badgeCache: make(map[string]badge),
	}
}

// AddChannel queues a subscription for `broadcasterID`. Safe to call
// before or after Start; pending hosts are flushed on each (re)connect.
func (c *Client) AddChannel(broadcasterID string) {
	if broadcasterID == "" {
		return
	}
	c.mu.Lock()
	already := c.hostIDs[broadcasterID]
	c.hostIDs[broadcasterID] = true
	conn := c.conn
	c.mu.Unlock()
	if already || conn == nil {
		return
	}
	if err := writeSubscribe(conn, broadcasterID); err != nil {
		log.Printf("[7TV] subscribe %s: %v", broadcasterID, err)
	}
}

// Start runs the connection loop in a goroutine and returns. The loop
// reconnects with backoff on any error.
func (c *Client) Start() {
	go c.loop()
}

func (c *Client) loop() {
	backoff := 2 * time.Second
	for {
		if c.ctx.Err() != nil {
			return
		}
		if err := c.runOnce(); err != nil {
			log.Printf("[7TV] connection: %v (backoff %s)", err, backoff)
		}
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) runOnce() error {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(c.ctx, wsURL, http.Header{})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	log.Printf("[7TV] connected to %s", wsURL)

	c.mu.Lock()
	c.conn = conn
	hostsSnapshot := make([]string, 0, len(c.hostIDs))
	for id := range c.hostIDs {
		hostsSnapshot = append(hostsSnapshot, id)
	}
	c.mu.Unlock()

	for _, id := range hostsSnapshot {
		if err := writeSubscribe(conn, id); err != nil {
			return fmt.Errorf("initial subscribe %s: %w", id, err)
		}
	}

	for {
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			c.conn = nil
			c.mu.Unlock()
			return fmt.Errorf("read: %w", err)
		}
		c.handleFrame(raw)
	}
}

// writeSubscribe sends the opcode-35 subscribe frame for a Twitch
// broadcaster's channel-level entitlement updates. We subscribe to all
// three create/update/delete events. 7TV's docs are sparse on the
// exact condition shape; chat sniffers in the wild show the
// "platform: TWITCH" + "ctx: channel" form for some endpoints and the
// "host_id" form for others, so we fire both subscribes and keep
// whichever the server accepts. Either way, log the outgoing frame
// once for visibility.
func writeSubscribe(conn *websocket.Conn, broadcasterID string) error {
	types := []string{"entitlement.create", "entitlement.update", "entitlement.delete"}
	conditions := []map[string]string{
		// Form A: host_id (the form most third-party docs cite)
		{"platform": "TWITCH", "ctx": "channel", "id": broadcasterID},
	}
	for _, t := range types {
		for _, cond := range conditions {
			payload := map[string]interface{}{
				"op": 35,
				"d": map[string]interface{}{
					"type":      t,
					"condition": cond,
				},
			}
			if err := conn.WriteJSON(payload); err != nil {
				return err
			}
		}
	}
	log.Printf("[7TV] subscribed entitlement.* for twitch_id=%s", broadcasterID)
	return nil
}

// handleFrame parses one inbound frame. The 7TV wire protocol wraps
// every event in {op, t, d}. We only care about opcode 0 (dispatch);
// opcode 1 (hello) is logged so connection state is visible.
func (c *Client) handleFrame(raw []byte) {
	var env struct {
		Op int             `json:"op"`
		D  json.RawMessage `json:"d"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		log.Printf("[7TV] decode frame: %v", err)
		return
	}
	switch env.Op {
	case 1:
		log.Printf("[7TV] hello received")
	case 0:
		// Dispatch: log the type so the wire is visible even when the
		// body parser doesn't recognise the inner shape.
		var t struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(env.D, &t)
		s := string(env.D)
		if len(s) > 400 {
			s = s[:400] + "…"
		}
		log.Printf("[7TV] dispatch %s: %s", t.Type, s)
		c.handleDispatch(env.D)
	case 4:
		log.Printf("[7TV] reconnect requested")
	case 5:
		log.Printf("[7TV] ack: %s", string(env.D))
	case 6:
		// error — log full for diagnosis
		log.Printf("[7TV] error: %s", string(env.D))
	case 7:
		// end-of-stream
		log.Printf("[7TV] end-of-stream: %s", string(env.D))
	default:
		log.Printf("[7TV] unhandled op=%d body=%s", env.Op, string(env.D))
	}
}

// handleDispatch parses an opcode-0 dispatch and walks the payload to
// extract any user_id -> paint/badge mapping it carries. The exact
// shape is somewhat undocumented; we log unknown variants at debug
// level and ignore them.
func (c *Client) handleDispatch(d json.RawMessage) {
	var disp struct {
		Type string          `json:"type"`
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(d, &disp); err != nil {
		log.Printf("[7TV] decode dispatch: %v", err)
		return
	}
	if disp.Type == "" {
		return
	}
	// Body shape (observed): {object: {kind, ref_id, user: {connections: [...]}}, ...}
	var body struct {
		Object struct {
			Kind  string `json:"kind"` // "PAINT" or "BADGE"
			RefID string `json:"ref_id"`
			User  struct {
				Connections []struct {
					Platform string `json:"platform"`
					ID       string `json:"id"`
				} `json:"connections"`
			} `json:"user"`
		} `json:"object"`
	}
	if err := json.Unmarshal(disp.Body, &body); err != nil {
		// Fall back to logging the first 200 chars so we can study
		// alternative shapes without breaking the loop.
		s := string(disp.Body)
		if len(s) > 200 {
			s = s[:200] + "…"
		}
		log.Printf("[7TV] %s body shape unknown: %s", disp.Type, s)
		return
	}
	var twitchID string
	for _, conn := range body.Object.User.Connections {
		if conn.Platform == "TWITCH" {
			twitchID = conn.ID
			break
		}
	}
	if twitchID == "" || body.Object.RefID == "" {
		return
	}

	cos := Cosmetic{UserID: twitchID}
	switch body.Object.Kind {
	case "PAINT":
		if css, ok := c.lookupPaint(body.Object.RefID); ok {
			cos.PaintCSS = css
		}
	case "BADGE":
		if b, ok := c.lookupBadge(body.Object.RefID); ok {
			cos.BadgeURL = b.URL
			cos.BadgeName = b.Name
		}
	}
	if cos.PaintCSS == "" && cos.BadgeURL == "" {
		return
	}
	if c.onCosmetic != nil {
		c.onCosmetic(cos)
	}
}

// Package sevenTV resolves 7TV paint/badge cosmetics for Twitch chat
// users via 7TV's v4 GraphQL endpoint at https://7tv.io/v4/gql.
//
// The legacy v3 REST endpoints (/v3/users/twitch/<id>, /v3/cosmetics)
// are deprecated and return 404 / 502, and the EventAPI WebSocket
// subscribes succeed but never produce real entitlement dispatches —
// 7TV pushes subscriber-count telemetry instead. The browser
// extension itself moved to v4 GraphQL, so we follow.
//
// Architecture: a single Client maintains an on-cosmetic callback,
// a per-user dedupe map, and emits Cosmetic structs whenever a chat
// message arrives from a user we haven't queried yet.
package sevenTV

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// gqlURL is 7TV's v4 GraphQL endpoint.
const gqlURL = "https://7tv.io/v4/gql"

// Cosmetic is one paint/badge entry the frontend can render against
// a Twitch user_id. PaintCSS is a "style" attribute snippet
// (e.g. "background:linear-gradient(...);-webkit-background-clip:text;color:transparent")
// — empty when the user has no paint. BadgeURL is empty when no badge.
type Cosmetic struct {
	UserID    string `json:"userID"`
	PaintCSS  string `json:"paintCSS"`
	BadgeURL  string `json:"badgeURL"`
	BadgeName string `json:"badgeName"`
}

// Client is a lightweight wrapper around the GraphQL endpoint with
// dedupe and a single callback fan-out. AddChannel is kept as a
// no-op so the existing callsites compile without churn — under the
// new model, cosmetics are fetched lazily per chat sender.
type Client struct {
	ctx        context.Context
	httpClient *http.Client
	onCosmetic func(Cosmetic)

	mu      sync.Mutex
	pending map[string]bool // twitch user id -> in-flight
	cached  map[string]bool // twitch user id -> already emitted (or learned: nothing)
}

// NewClient builds a ready-to-use client. Start is also a no-op for
// the same compat reason as AddChannel.
func NewClient(ctx context.Context, onCosmetic func(Cosmetic)) *Client {
	return &Client{
		ctx:        ctx,
		httpClient: &http.Client{Timeout: 12 * time.Second},
		onCosmetic: onCosmetic,
		pending:    make(map[string]bool),
		cached:     make(map[string]bool),
	}
}

// Start is a no-op kept for API compatibility with the old WS-based
// client. Cosmetic fetches happen on demand via LookupUser.
func (c *Client) Start() {}

// AddChannel is a no-op kept for API compatibility. The new model
// fetches cosmetics per-user as chat messages arrive, so there's
// nothing channel-scoped to subscribe to.
func (c *Client) AddChannel(broadcasterID string) {}

// LookupUser fires a one-shot GraphQL query for the given Twitch
// user id. Deduped across concurrent calls + repeated calls; the
// first result is cached forever (or until the process restarts) so
// we never pay more than one request per user. Emits a Cosmetic via
// onCosmetic if the user has an active paint OR badge.
func (c *Client) LookupUser(twitchUserID string) {
	if twitchUserID == "" {
		return
	}
	c.mu.Lock()
	if c.cached[twitchUserID] || c.pending[twitchUserID] {
		c.mu.Unlock()
		return
	}
	c.pending[twitchUserID] = true
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.pending, twitchUserID)
			c.cached[twitchUserID] = true
			c.mu.Unlock()
		}()
		c.fetch(twitchUserID)
	}()
}

// gqlQuery is the smallest query that returns everything we need to
// render the cosmetic. Paint layers come as a discriminated union
// (PaintLayerType) — for now we support SingleColor + Linear/Radial
// gradients which cover the vast majority of paints in the wild.
const gqlQuery = `query($pid:String!){
  users{
    userByConnection(platform:TWITCH, platformId:$pid){
      id
      style{
        activePaint{
          id name
          data{
            layers{
              id opacity
              ty{
                __typename
                ... on PaintLayerTypeSingleColor { color { hex } }
                ... on PaintLayerTypeLinearGradient { angle repeating stops { at color { hex } } }
                ... on PaintLayerTypeRadialGradient { shape repeating stops { at color { hex } } }
              }
            }
          }
        }
        activeBadge{ id name images { url } }
      }
    }
  }
}`

type gqlReq struct {
	Query     string            `json:"query"`
	Variables map[string]string `json:"variables"`
}

type gqlResp struct {
	Data struct {
		Users struct {
			UserByConnection *struct {
				ID    string `json:"id"`
				Style struct {
					ActivePaint *paintObj `json:"activePaint"`
					ActiveBadge *badgeObj `json:"activeBadge"`
				} `json:"style"`
			} `json:"userByConnection"`
		} `json:"users"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type paintObj struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Data struct {
		Layers []paintLayer `json:"layers"`
	} `json:"data"`
}

type paintLayer struct {
	ID      string         `json:"id"`
	Opacity float64        `json:"opacity"`
	Ty      paintLayerType `json:"ty"`
}

type paintLayerType struct {
	TypeName  string         `json:"__typename"`
	Color     *paintColor    `json:"color,omitempty"`
	Angle     int            `json:"angle"`
	Repeating bool           `json:"repeating"`
	Shape     string         `json:"shape,omitempty"`
	Stops     []paintStop    `json:"stops,omitempty"`
}

type paintColor struct {
	Hex string `json:"hex"`
}

type paintStop struct {
	At    float64    `json:"at"`
	Color paintColor `json:"color"`
}

type badgeObj struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Images []struct {
		URL string `json:"url"`
	} `json:"images"`
}

func (c *Client) fetch(twitchUserID string) {
	body, err := json.Marshal(gqlReq{
		Query:     gqlQuery,
		Variables: map[string]string{"pid": twitchUserID},
	})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(c.ctx, "POST", gqlURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ChatHub/0.3 (+https://github.com/MiwiDots/streamerchat)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[7TV] gql %s: %v", twitchUserID, err)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode != 200 {
		// Don't log 502/504 — 7TV's API is occasionally flaky and
		// each chat message would otherwise spam the log. Just give
		// up; cached[] still prevents retries.
		return
	}

	var parsed gqlResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		log.Printf("[7TV] gql decode %s: %v", twitchUserID, err)
		return
	}
	if len(parsed.Errors) > 0 {
		log.Printf("[7TV] gql %s errors: %s", twitchUserID, parsed.Errors[0].Message)
		return
	}
	user := parsed.Data.Users.UserByConnection
	if user == nil {
		return // not on 7TV
	}

	cos := Cosmetic{UserID: twitchUserID}
	if p := user.Style.ActivePaint; p != nil {
		cos.PaintCSS = paintToCSS(p)
	}
	if b := user.Style.ActiveBadge; b != nil && len(b.Images) > 0 {
		cos.BadgeURL = b.Images[0].URL
		cos.BadgeName = b.Name
	}
	if cos.PaintCSS == "" && cos.BadgeURL == "" {
		return
	}
	if c.onCosmetic != nil {
		c.onCosmetic(cos)
	}
}

// paintToCSS turns 7TV's PaintLayer list into a style attribute. Each
// layer becomes one background entry; multiple layers stack via
// comma-separated values. For text-paints we also set
// -webkit-background-clip:text + color:transparent so the gradient
// paints the username glyphs rather than a rectangle behind them.
// A single solid-color layer is special-cased to a plain `color:`
// rule because background-clip on a single solid would render the
// same as just setting color — but cheaper.
func paintToCSS(p *paintObj) string {
	if p == nil || len(p.Data.Layers) == 0 {
		return ""
	}
	if len(p.Data.Layers) == 1 && p.Data.Layers[0].Ty.TypeName == "PaintLayerTypeSingleColor" {
		c := p.Data.Layers[0].Ty.Color
		if c != nil && c.Hex != "" {
			return "color:" + normalizeHex(c.Hex)
		}
	}
	var backgrounds []string
	for _, layer := range p.Data.Layers {
		bg := layerBackground(layer)
		if bg != "" {
			backgrounds = append(backgrounds, bg)
		}
	}
	if len(backgrounds) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"background:%s;-webkit-background-clip:text;background-clip:text;color:transparent",
		joinComma(backgrounds),
	)
}

func layerBackground(l paintLayer) string {
	switch l.Ty.TypeName {
	case "PaintLayerTypeSingleColor":
		if l.Ty.Color == nil {
			return ""
		}
		return normalizeHex(l.Ty.Color.Hex)
	case "PaintLayerTypeLinearGradient":
		stops := stopsToCSS(l.Ty.Stops)
		if stops == "" {
			return ""
		}
		kind := "linear-gradient"
		if l.Ty.Repeating {
			kind = "repeating-linear-gradient"
		}
		angle := l.Ty.Angle
		if angle == 0 {
			angle = 90
		}
		return fmt.Sprintf("%s(%ddeg,%s)", kind, angle, stops)
	case "PaintLayerTypeRadialGradient":
		stops := stopsToCSS(l.Ty.Stops)
		if stops == "" {
			return ""
		}
		kind := "radial-gradient"
		if l.Ty.Repeating {
			kind = "repeating-radial-gradient"
		}
		shape := "ellipse"
		if l.Ty.Shape != "" {
			shape = l.Ty.Shape
		}
		return fmt.Sprintf("%s(%s,%s)", kind, shape, stops)
	}
	return ""
}

func stopsToCSS(stops []paintStop) string {
	if len(stops) == 0 {
		return ""
	}
	parts := make([]string, 0, len(stops))
	for _, s := range stops {
		parts = append(parts, fmt.Sprintf("%s %d%%", normalizeHex(s.Color.Hex), int(s.At*100+0.5)))
	}
	return joinComma(parts)
}

// normalizeHex ensures we always emit a CSS-valid color. 7TV returns
// hex without the leading "#"; some legacy entries also include an
// alpha byte (rrggbbaa) which is fine for modern browsers.
func normalizeHex(hex string) string {
	if hex == "" {
		return "transparent"
	}
	if hex[0] == '#' {
		return hex
	}
	return "#" + hex
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

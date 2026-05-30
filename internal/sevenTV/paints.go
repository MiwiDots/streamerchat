package sevenTV

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const cosmeticsURL = "https://7tv.io/v3/cosmetics?user_identifier=object_id"

// fetchCosmetics is invoked lazily on first paint/badge id resolution.
// It pulls the bulk list of all paint + badge definitions in one shot
// and caches them on the client. Worth ~250 KB total per refresh, so
// we refresh every 30 minutes rather than per-message.
type rawCosmetics struct {
	Paints []rawPaint `json:"paints"`
	Badges []rawBadge `json:"badges"`
}

type rawPaint struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Function string         `json:"function"` // "LINEAR_GRADIENT", "RADIAL_GRADIENT", "URL", "SOLID"
	Color    *int64         `json:"color"`    // packed RGBA when present
	Angle    int            `json:"angle"`
	Shape    string         `json:"shape"`
	ImageURL string         `json:"image_url"`
	Stops    []rawPaintStop `json:"stops"`
	Shadows  []rawShadow    `json:"shadows"`
	Repeat   bool           `json:"repeat"`
}

type rawPaintStop struct {
	At    float64 `json:"at"`
	Color int64   `json:"color"`
}

type rawShadow struct {
	XOffset float64 `json:"x_offset"`
	YOffset float64 `json:"y_offset"`
	Radius  float64 `json:"radius"`
	Color   int64   `json:"color"`
}

type rawBadge struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Tooltip string   `json:"tooltip"`
	Host    rawHost  `json:"host"`
}

type rawHost struct {
	URL   string `json:"url"`
	Files []struct {
		Name   string `json:"name"`
		Format string `json:"format"`
	} `json:"files"`
}

var cosmeticsMu sync.Mutex

// lookupPaint resolves a paint id to a CSS rule string, refreshing the
// bulk cosmetics cache if it's been more than 30 minutes since we
// last did. Returns ("", false) if the id stays unknown after refresh.
func (c *Client) lookupPaint(id string) (string, bool) {
	c.mu.Lock()
	css, ok := c.paintCache[id]
	stale := time.Since(c.cosmeticsAt) > 30*time.Minute
	c.mu.Unlock()
	if ok && !stale {
		return css, true
	}
	if err := c.refreshCosmetics(); err != nil {
		log.Printf("[7TV] refresh cosmetics: %v", err)
	}
	c.mu.Lock()
	css, ok = c.paintCache[id]
	c.mu.Unlock()
	return css, ok
}

func (c *Client) lookupBadge(id string) (badge, bool) {
	c.mu.Lock()
	b, ok := c.badgeCache[id]
	stale := time.Since(c.cosmeticsAt) > 30*time.Minute
	c.mu.Unlock()
	if ok && !stale {
		return b, true
	}
	if err := c.refreshCosmetics(); err != nil {
		log.Printf("[7TV] refresh cosmetics: %v", err)
	}
	c.mu.Lock()
	b, ok = c.badgeCache[id]
	c.mu.Unlock()
	return b, ok
}

func (c *Client) refreshCosmetics() error {
	cosmeticsMu.Lock()
	defer cosmeticsMu.Unlock()
	// Double-check pattern: another goroutine may have already
	// finished while we waited for the lock.
	c.mu.Lock()
	if time.Since(c.cosmeticsAt) < 5*time.Minute {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(c.ctx, "GET", cosmeticsURL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(raw))
	}
	var data rawCosmetics
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}

	paints := make(map[string]string, len(data.Paints))
	for _, p := range data.Paints {
		paints[p.ID] = paintToCSS(p)
	}
	badges := make(map[string]badge, len(data.Badges))
	for _, b := range data.Badges {
		url := badgeURL(b)
		if url == "" {
			continue
		}
		badges[b.ID] = badge{URL: url, Name: firstNonEmpty(b.Tooltip, b.Name)}
	}

	c.mu.Lock()
	c.paintCache = paints
	c.badgeCache = badges
	c.cosmeticsAt = time.Now()
	c.mu.Unlock()
	log.Printf("[7TV] cosmetics refreshed: %d paints, %d badges", len(paints), len(badges))
	return nil
}

// paintToCSS converts a 7TV paint definition into a CSS rule snippet
// the frontend can splice onto a username span. SOLID maps to a plain
// color; gradients use background + -webkit-background-clip so the
// gradient paints the text. text-shadow is appended for paint shadows.
func paintToCSS(p rawPaint) string {
	var rules []string
	switch strings.ToUpper(p.Function) {
	case "URL":
		if p.ImageURL != "" {
			rules = append(rules,
				fmt.Sprintf("background:url(%s) center/cover", p.ImageURL),
				"-webkit-background-clip:text",
				"color:transparent",
			)
		}
	case "LINEAR_GRADIENT", "RADIAL_GRADIENT":
		grad := buildGradient(p)
		if grad != "" {
			rules = append(rules,
				"background:"+grad,
				"-webkit-background-clip:text",
				"color:transparent",
			)
		}
	default: // SOLID or unknown -> single color
		if p.Color != nil {
			rules = append(rules, "color:"+rgbaToCSS(*p.Color))
		}
	}
	if shadow := buildShadow(p.Shadows); shadow != "" {
		rules = append(rules, "text-shadow:"+shadow)
	}
	return strings.Join(rules, ";")
}

func buildGradient(p rawPaint) string {
	if len(p.Stops) == 0 {
		return ""
	}
	stops := make([]string, 0, len(p.Stops))
	for _, s := range p.Stops {
		stops = append(stops, fmt.Sprintf("%s %d%%", rgbaToCSS(s.Color), int(s.At*100)))
	}
	if strings.ToUpper(p.Function) == "RADIAL_GRADIENT" {
		return fmt.Sprintf("radial-gradient(%s)", strings.Join(stops, ","))
	}
	angle := p.Angle
	if angle == 0 {
		angle = 90
	}
	return fmt.Sprintf("linear-gradient(%ddeg,%s)", angle, strings.Join(stops, ","))
}

func buildShadow(shadows []rawShadow) string {
	if len(shadows) == 0 {
		return ""
	}
	parts := make([]string, 0, len(shadows))
	for _, s := range shadows {
		parts = append(parts, fmt.Sprintf("%gpx %gpx %gpx %s", s.XOffset, s.YOffset, s.Radius, rgbaToCSS(s.Color)))
	}
	return strings.Join(parts, ",")
}

// rgbaToCSS converts a 7TV-packed RGBA int (rrggbbaa) into a CSS
// rgba() string. The wire format is signed because some tooling
// produces negative values when the top bit is set; mask + cast first.
func rgbaToCSS(packed int64) string {
	u := uint32(packed)
	r := (u >> 24) & 0xff
	g := (u >> 16) & 0xff
	b := (u >> 8) & 0xff
	a := u & 0xff
	return fmt.Sprintf("rgba(%d,%d,%d,%g)", r, g, b, float64(a)/255.0)
}

func badgeURL(b rawBadge) string {
	if b.Host.URL == "" {
		return ""
	}
	// Prefer 1x WEBP if present, else first file we see.
	for _, f := range b.Host.Files {
		if f.Format == "WEBP" && strings.HasPrefix(f.Name, "1x") {
			return "https:" + b.Host.URL + "/" + f.Name
		}
	}
	if len(b.Host.Files) > 0 {
		return "https:" + b.Host.URL + "/" + b.Host.Files[0].Name
	}
	return ""
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

package sevenTV

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// per-twitch-user-id lookup. The shape returned by /v3/users/twitch is
// stable and well documented, so this is a more reliable path than
// fishing through opaque entitlement events.
//
// We dedupe in-flight requests so a chat burst from the same user
// doesn't fire 30 HTTP calls. Cached "not found" entries get an empty
// Cosmetic so we don't retry forever on users with no 7TV link.

var (
	userMu      sync.Mutex
	userPending = make(map[string]bool)
	userCached  = make(map[string]bool) // we already tried & emitted (or learned: no cosmetics)
)

// LookupUser fires a one-shot HTTP GET against /v3/users/twitch/<id>
// and emits a Cosmetic via the client's callback if the user has a
// paint or badge applied. Safe to call from any goroutine; dedups
// concurrent calls for the same twitch user id. Idempotent — first
// call wins, subsequent calls are no-ops.
func (c *Client) LookupUser(twitchUserID string) {
	if twitchUserID == "" {
		return
	}
	userMu.Lock()
	if userCached[twitchUserID] || userPending[twitchUserID] {
		userMu.Unlock()
		return
	}
	userPending[twitchUserID] = true
	userMu.Unlock()

	go func() {
		defer func() {
			userMu.Lock()
			delete(userPending, twitchUserID)
			userCached[twitchUserID] = true
			userMu.Unlock()
		}()
		c.lookupUserSync(twitchUserID)
	}()
}

func (c *Client) lookupUserSync(twitchUserID string) {
	url := fmt.Sprintf("https://7tv.io/v3/users/twitch/%s", twitchUserID)
	req, err := http.NewRequestWithContext(c.ctx, "GET", url, nil)
	if err != nil {
		return
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return // user not on 7TV, fine
	}
	if resp.StatusCode != 200 {
		log.Printf("[7TV] lookup %s: status %d", twitchUserID, resp.StatusCode)
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	// The /v3/users/twitch payload has two flavours in the wild:
	//   * legacy: top-level fields (id, style:{paint_id, badge_id})
	//   * current: {user: {style: {paint_id, badge_id}}}
	// Parse both into the same shape.
	var current struct {
		User struct {
			Style struct {
				PaintID string `json:"paint_id"`
				BadgeID string `json:"badge_id"`
			} `json:"style"`
		} `json:"user"`
		Style struct {
			PaintID string `json:"paint_id"`
			BadgeID string `json:"badge_id"`
		} `json:"style"`
	}
	if err := json.Unmarshal(raw, &current); err != nil {
		return
	}
	paintID := firstNonEmpty(current.User.Style.PaintID, current.Style.PaintID)
	badgeID := firstNonEmpty(current.User.Style.BadgeID, current.Style.BadgeID)
	if paintID == "" && badgeID == "" {
		return
	}

	cos := Cosmetic{UserID: twitchUserID}
	if paintID != "" {
		if css, ok := c.lookupPaint(paintID); ok {
			cos.PaintCSS = css
		}
	}
	if badgeID != "" {
		if b, ok := c.lookupBadge(badgeID); ok {
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

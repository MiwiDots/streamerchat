package youtube

import (
	"context"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// RefreshSession hits the YouTube home page with the current cookies and
// folds any Set-Cookie values the server hands back into the saved set.
// Browsers do exactly this every time the user visits youtube.com — it's
// what keeps the session alive indefinitely without the user needing to
// re-login. We piggyback on the same behaviour from outside the browser.
//
// On success the updated cookies are written back to the keyring and the
// new struct is returned. If the response indicates the session is dead
// (HTTP 302 to accounts.google.com, or auth-required JSON), the saved
// entry is cleared and ErrNotLoggedIn is returned so the caller can
// surface "please re-login".
func RefreshSession(parent context.Context) (LoginCookies, error) {
	c, err := LoadLoginCookies()
	if err != nil {
		return c, err
	}

	jar, _ := cookiejar.New(nil)
	yt, _ := url.Parse("https://www.youtube.com")
	seed := cookiesFor(c)
	jar.SetCookies(yt, seed)
	// Also seed for accounts.google.com so SAPISID etc. are mirrored on the
	// auth domain — same set, Chrome stores them on both.
	gacc, _ := url.Parse("https://accounts.google.com")
	jar.SetCookies(gacc, seed)

	client := &http.Client{
		Jar:     jar,
		Timeout: 20 * time.Second,
		// Don't auto-follow into login redirects — that'd hide the "session
		// dead" signal. Stop at the first 3xx so we can inspect it.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(parent, "GET", "https://www.youtube.com/", nil)
	if err != nil {
		return c, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return c, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		if strings.Contains(loc, "accounts.google.com") && strings.Contains(loc, "ServiceLogin") {
			log.Printf("[YT-REFRESH] session dead (redirect to %s) — clearing keyring", loc)
			_ = ClearLoginCookies()
			return LoginCookies{}, ErrNotLoggedIn
		}
	}

	// Fold any Set-Cookie updates back into our struct so the next send
	// uses the freshest values. The jar already has the merged set.
	merged := map[string]string{}
	for _, ck := range jar.Cookies(yt) {
		merged[ck.Name] = ck.Value
	}
	for _, ck := range jar.Cookies(gacc) {
		merged[ck.Name] = ck.Value
	}
	updated := mergeCookies(c, merged)
	updated.RefreshedAt = time.Now()
	if err := SaveLoginCookies(updated); err != nil {
		return c, err
	}
	log.Printf("[YT-REFRESH] session refreshed (SAPISID still %d bytes)", len(updated.SAPISID))
	return updated, nil
}

// IsSessionAlive does a quick HEAD request to YouTube's home page with the
// current cookies and reports whether the server still treats us as
// logged in. Used at boot to decide whether to surface the YT auth-pill
// as connected.
func IsSessionAlive(ctx context.Context) bool {
	c, err := LoadLoginCookies()
	if err != nil {
		return false
	}
	jar, _ := cookiejar.New(nil)
	yt, _ := url.Parse("https://www.youtube.com")
	jar.SetCookies(yt, cookiesFor(c))
	client := &http.Client{
		Jar:     jar,
		Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://www.youtube.com/", nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		if strings.Contains(loc, "ServiceLogin") {
			return false
		}
	}
	return resp.StatusCode < 400
}

// StartAutoRefresh runs RefreshSession every `interval` (typically 24h)
// while ctx is alive. Errors are logged but never block — the next tick
// will try again. Returns immediately; the loop is a goroutine.
func StartAutoRefresh(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		// Do one refresh ~30s after boot so we validate the session
		// without delaying app startup.
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		if _, err := RefreshSession(ctx); err != nil {
			log.Printf("[YT-REFRESH] initial: %v", err)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if _, err := RefreshSession(ctx); err != nil {
				log.Printf("[YT-REFRESH] tick: %v", err)
			}
		}
	}()
}

func cookiesFor(c LoginCookies) []*http.Cookie {
	var out []*http.Cookie
	add := func(name, val string) {
		if val != "" {
			out = append(out, &http.Cookie{Name: name, Value: val, Path: "/"})
		}
	}
	add("SAPISID", c.SAPISID)
	add("SID", c.SID)
	add("HSID", c.HSID)
	add("SSID", c.SSID)
	add("APISID", c.APISID)
	add("__Secure-3PSID", c.Secure3PSID)
	add("__Secure-3PAPISID", c.Secure3PAPISID)
	add("__Secure-1PSID", c.Secure1PSID)
	add("__Secure-1PAPISID", c.Secure1PAPISID)
	add("LOGIN_INFO", c.LoginInfo)
	add("VISITOR_INFO1_LIVE", c.VisitorInfo1Live)
	return out
}

// mergeCookies takes the current saved set and a fresh map (typically the
// cookie jar's state after a request) and returns a new struct with any
// updated values folded in. Unknown names are ignored.
func mergeCookies(orig LoginCookies, fresh map[string]string) LoginCookies {
	pick := func(prev string, name string) string {
		if v, ok := fresh[name]; ok && v != "" {
			return v
		}
		return prev
	}
	return LoginCookies{
		SAPISID:          pick(orig.SAPISID, "SAPISID"),
		SID:              pick(orig.SID, "SID"),
		HSID:             pick(orig.HSID, "HSID"),
		SSID:             pick(orig.SSID, "SSID"),
		APISID:           pick(orig.APISID, "APISID"),
		Secure3PSID:      pick(orig.Secure3PSID, "__Secure-3PSID"),
		Secure3PAPISID:   pick(orig.Secure3PAPISID, "__Secure-3PAPISID"),
		Secure1PSID:      pick(orig.Secure1PSID, "__Secure-1PSID"),
		Secure1PAPISID:   pick(orig.Secure1PAPISID, "__Secure-1PAPISID"),
		LoginInfo:        pick(orig.LoginInfo, "LOGIN_INFO"),
		VisitorInfo1Live: pick(orig.VisitorInfo1Live, "VISITOR_INFO1_LIVE"),
		SavedAt:          orig.SavedAt,
		DisplayName:      orig.DisplayName,
	}
}

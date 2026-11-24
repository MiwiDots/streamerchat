package platform

import (
	"net/url"
	"regexp"
)

// IsWebURL reports whether s is an absolute http(s) URL. Every URL handed
// to the OS browser goes through this: the webview renders untrusted chat
// content, and a bound "open URL" method must not open file:, ms-settings:
// or other protocol handlers.
func IsWebURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

var twitchLoginRe = regexp.MustCompile(`^[a-z0-9_]{1,25}$`)

// IsTwitchLogin reports whether s is a valid (lowercase) Twitch login.
func IsTwitchLogin(s string) bool { return twitchLoginRe.MatchString(s) }

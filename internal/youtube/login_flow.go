package youtube

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// LaunchLoginFlow spawns the user's preferred Chromium-family browser
// (Edge / Chrome / Brave) in an isolated temporary user-data-dir, opens
// the Google ServiceLogin URL, and polls the browser's cookie store on
// disk every few seconds. The instant the SAPISID cookie shows up we
// snapshot the session, kill the browser, wipe the temp profile and
// return.
//
// We intentionally do NOT drive the browser via CDP/chromedp. Google
// detects automation-controlled browsers and refuses to sign you in
// ("Couldn't sign you in / This browser or app may not be secure").
// Without CDP we're indistinguishable from a normal browser launch.
func LaunchLoginFlow(parent context.Context, timeout time.Duration) (LoginCookies, error) {
	exes := findChromiumBinaries()
	if len(exes) == 0 {
		return LoginCookies{}, fmt.Errorf("no Chromium-family browser found (need Edge, Chrome, or Brave)")
	}
	exe := exes[0]
	log.Printf("[YT-LOGIN] launching browser: %s", exe)

	tmpProfile, err := os.MkdirTemp("", "chathub-yt-login-*")
	if err != nil {
		return LoginCookies{}, fmt.Errorf("temp profile: %w", err)
	}
	defer os.RemoveAll(tmpProfile)

	const loginURL = "https://accounts.google.com/ServiceLogin?service=youtube&continue=" +
		"https%3A%2F%2Fwww.youtube.com%2F"

	args := []string{
		"--user-data-dir=" + tmpProfile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--disable-extensions",
		"--new-window",
		loginURL,
	}
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		return LoginCookies{}, fmt.Errorf("spawn browser: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	// Cookie file lives under <tmpProfile>/Default/Network/Cookies after
	// the user lands on a Google page and the browser writes its first
	// session cookies.
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	// Give the browser a head start before the first poll — opening a
	// fresh profile + loading the login page takes a couple of seconds
	// and reading an empty/half-initialised SQLite db spams errors.
	select {
	case <-ctx.Done():
		return LoginCookies{}, fmt.Errorf("login flow cancelled before browser came up")
	case <-time.After(4 * time.Second):
	}
	for {
		cookies, err := readBrowserProfileCookies(tmpProfile)
		if err == nil {
			creds := buildLoginCookies(cookies)
			if creds.Valid() {
				log.Printf("[YT-LOGIN] captured SAPISID (%d-byte) — closing browser", len(creds.SAPISID))
				return creds, nil
			}
		}
		select {
		case <-ctx.Done():
			return LoginCookies{}, fmt.Errorf("login flow timed out / cancelled: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// buildLoginCookies turns a name->value map (any cookies harvested from
// .google.com or .youtube.com) into our LoginCookies struct.
func buildLoginCookies(m map[string]string) LoginCookies {
	return LoginCookies{
		SAPISID:          m["SAPISID"],
		SID:              m["SID"],
		HSID:             m["HSID"],
		SSID:             m["SSID"],
		APISID:           m["APISID"],
		Secure3PSID:      m["__Secure-3PSID"],
		Secure3PAPISID:   m["__Secure-3PAPISID"],
		Secure1PSID:      m["__Secure-1PSID"],
		Secure1PAPISID:   m["__Secure-1PAPISID"],
		LoginInfo:        m["LOGIN_INFO"],
		VisitorInfo1Live: m["VISITOR_INFO1_LIVE"],
	}
}

// findChromiumBinaries returns every Chromium-family browser we can drive
// via CDP, in preference order. Edge first (always present on Win10+),
// then Chrome / Brave / Vivaldi. Caller tries them in order.
func findChromiumBinaries() []string {
	var out []string
	for _, p := range chromiumCandidates() {
		if p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

func chromiumCandidates() []string {
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		candidates = []string{
			// Prefer Chrome first: Chrome users tend to have all their
			// existing logins there and it avoids Edge's "this is a work
			// account" enterprise gating that some users hit.
			filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("LocalAppData"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
		}
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	default: // linux
		candidates = []string{
			"/usr/bin/google-chrome",
			"/usr/bin/microsoft-edge",
			"/usr/bin/chromium",
			"/usr/bin/brave-browser",
		}
	}
	return candidates
}

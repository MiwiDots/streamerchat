package youtube

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// LaunchLoginFlow spawns a headed Edge/Chrome window in an isolated user
// profile, sends the user to Google's ServiceLogin (which redirects to
// YouTube after successful auth), and polls the CDP cookie store every
// couple of seconds until the SAPISID cookie appears — which is our proxy
// for "the user is now logged in". At that point we snapshot all the
// session cookies we need, kill the browser, wipe the temporary profile
// and return.
//
// The flow times out after `timeout` (typically a few minutes). The caller
// is expected to surface a settings-modal-level status while it runs and
// to persist the returned LoginCookies via SaveLoginCookies on success.
func LaunchLoginFlow(parent context.Context, timeout time.Duration) (LoginCookies, error) {
	exe, err := findChromiumBinary()
	if err != nil {
		return LoginCookies{}, err
	}

	tmpProfile, err := os.MkdirTemp("", "chathub-yt-login-*")
	if err != nil {
		return LoginCookies{}, fmt.Errorf("temp profile: %w", err)
	}
	defer os.RemoveAll(tmpProfile)

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(exe),
		chromedp.UserDataDir(tmpProfile),
		chromedp.Flag("headless", false),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		// Single-process model so the parent CDP attach is reliable; the
		// "browser process" + "renderer" are the same OS process.
		chromedp.WindowSize(480, 720),
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	defer allocCancel()

	taskCtx, taskCancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
	defer taskCancel()

	// Navigate to the canonical Google login URL that hands the user off
	// to YouTube after success. After the redirect chain settles the
	// browser ends up on www.youtube.com with all relevant cookies set.
	const loginURL = "https://accounts.google.com/ServiceLogin?service=youtube&continue=" +
		"https%3A%2F%2Fwww.youtube.com%2F"
	if err := chromedp.Run(taskCtx, chromedp.Navigate(loginURL)); err != nil {
		return LoginCookies{}, fmt.Errorf("navigate: %w", err)
	}
	log.Printf("[YT-LOGIN] browser window open at %s", loginURL)

	// Poll cookies every 2s. Bail when we see SAPISID for .google.com or
	// .youtube.com (both should appear simultaneously once login completes).
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return LoginCookies{}, fmt.Errorf("login flow timed out / cancelled: %w", ctx.Err())
		case <-ticker.C:
		}
		var cookies []*network.Cookie
		err := chromedp.Run(taskCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			res, err := network.GetCookies().Do(ctx)
			if err != nil {
				return err
			}
			cookies = res
			return nil
		}))
		if err != nil {
			// If the user closed the window manually the context errors out;
			// treat that as cancellation.
			if ctx.Err() != nil {
				return LoginCookies{}, fmt.Errorf("login window closed before SAPISID appeared")
			}
			log.Printf("[YT-LOGIN] cookie fetch error (will retry): %v", err)
			continue
		}
		if creds, ok := extractLoginCookies(cookies); ok {
			log.Printf("[YT-LOGIN] success — captured %d-byte SAPISID", len(creds.SAPISID))
			// Best-effort: close the browser nicely. The defers handle the
			// hard kill if this errors.
			_ = chromedp.Run(taskCtx, chromedp.ActionFunc(func(ctx context.Context) error {
				return chromedp.Stop().Do(ctx)
			}))
			return creds, nil
		}
	}
}

// extractLoginCookies walks the raw CDP cookie list, picks the ones we
// need for SAPISIDHASH auth, and returns them in our LoginCookies shape.
// Returns ok=false until SAPISID is present (the bare minimum).
func extractLoginCookies(cookies []*network.Cookie) (LoginCookies, bool) {
	var c LoginCookies
	for _, k := range cookies {
		// Cookies live on .google.com or .youtube.com depending on which
		// host set them; we accept either domain.
		host := strings.TrimPrefix(k.Domain, ".")
		if !strings.HasSuffix(host, "google.com") && !strings.HasSuffix(host, "youtube.com") {
			continue
		}
		switch k.Name {
		case "SAPISID":
			c.SAPISID = k.Value
		case "SID":
			if c.SID == "" {
				c.SID = k.Value
			}
		case "HSID":
			c.HSID = k.Value
		case "SSID":
			c.SSID = k.Value
		case "APISID":
			c.APISID = k.Value
		case "__Secure-3PSID":
			c.Secure3PSID = k.Value
		case "__Secure-3PAPISID":
			c.Secure3PAPISID = k.Value
		case "__Secure-1PSID":
			c.Secure1PSID = k.Value
		case "__Secure-1PAPISID":
			c.Secure1PAPISID = k.Value
		case "LOGIN_INFO":
			c.LoginInfo = k.Value
		case "VISITOR_INFO1_LIVE":
			c.VisitorInfo1Live = k.Value
		}
	}
	return c, c.Valid()
}

// findChromiumBinary returns the path to a Chromium-family browser we can
// drive via CDP. Prefers Edge (always present on Win10+), falls back to
// Chrome / Brave / Vivaldi. On macOS and Linux we mostly use this for
// local dev so we cover the common defaults there too.
func findChromiumBinary() (string, error) {
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		candidates = []string{
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("LocalAppData"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
		}
	case "darwin":
		candidates = []string{
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	default: // linux
		candidates = []string{
			"/usr/bin/microsoft-edge",
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/brave-browser",
		}
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			log.Printf("[YT-LOGIN] using browser: %s", p)
			return p, nil
		}
	}
	return "", fmt.Errorf("no Chromium-family browser found (need Edge, Chrome, or Brave on PATH/standard install paths)")
}

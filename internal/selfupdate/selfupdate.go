// Package selfupdate implements a minimal GitHub-releases-based self-updater.
//
// Strategy on Windows:
//   - Download the new exe to <exe>.new
//   - Rename current <exe> to <exe>.old (Windows allows renaming a running exe)
//   - Rename <exe>.new to <exe>
//   - Spawn the fresh exe and exit. On next launch the .old is removed.
//
// On non-Windows platforms Apply returns ErrUnsupported and the UI should
// just open the release page in a browser.
//
// Security model: Apply only accepts an Asset taken from the GitHub API
// response (never a URL coming from the webview), only downloads from this
// repo's release-download path, and refuses to install anything whose
// SHA-256 doesn't match the digest GitHub computed for the uploaded asset.
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var ErrUnsupported = errors.New("self-update apply is only supported on Windows")

// Release mirrors the subset of github's "latest release" JSON we care about.
type Release struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Asset is one downloadable file of a release.
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
	// Digest is "sha256:<hex>", computed by GitHub on upload. Present on
	// every asset uploaded since mid-2025 (all releases from v0.4.8 on).
	Digest string `json:"digest"`
}

// apiClient is for the small JSON API calls.
var apiClient = &http.Client{Timeout: 30 * time.Second}

// downloadClient is for the ~16 MB exe. http.Client.Timeout covers the
// WHOLE body read, so the API client's 30s killed downloads on anything
// slower than ~0.5 MB/s. Here only the time-to-first-byte is tight; the
// overall cap is generous (16 MB at ~20 KB/s).
var downloadClient = &http.Client{
	Timeout: 15 * time.Minute,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
	},
}

// Latest returns the latest published release for owner/repo. Pre-releases
// are excluded by the /releases/latest endpoint. Equivalent to
// LatestForChannel(owner, repo, "stable") and kept for compatibility.
func Latest(owner, repo string) (*Release, error) {
	return LatestForChannel(owner, repo, "stable")
}

// listedRelease is the subset of /releases JSON we need when picking from
// the full list — the only extra field over Release is Prerelease, which
// the /releases/latest endpoint doesn't expose because it's never set
// for what it returns.
type listedRelease struct {
	Release
	Prerelease bool `json:"prerelease"`
}

// LatestForChannel picks the newest release that matches the given
// channel:
//   - "stable": only non-prerelease tags (this is what /releases/latest
//     returns by definition)
//   - "beta": skip tags containing "-alpha" — stable + "-beta" releases
//     qualify. Falls back to stable if no beta-or-newer exists.
//   - "alpha": newest release published, including any prerelease
//
// Tags published on GitHub are ordered newest-first by /releases so the
// first match wins.
func LatestForChannel(owner, repo, channel string) (*Release, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" {
		channel = "stable"
	}
	if channel == "stable" {
		return fetchLatestStable(owner, repo)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=30", owner, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := apiClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github releases status %d", resp.StatusCode)
	}
	var list []listedRelease
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	for _, r := range list {
		tag := strings.ToLower(r.TagName)
		isAlpha := strings.Contains(tag, "-alpha")
		switch channel {
		case "alpha":
			return &r.Release, nil
		case "beta":
			if !isAlpha {
				return &r.Release, nil
			}
		}
	}
	// Beta with no beta-or-stable in the recent list: fall back to stable.
	return fetchLatestStable(owner, repo)
}

func fetchLatestStable(owner, repo string) (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := apiClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github releases status %d", resp.StatusCode)
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// FindAsset returns the download URL of the asset whose Name matches `name`.
func FindAsset(rel *Release, name string) string {
	if a := FindAssetInfo(rel, name); a != nil {
		return a.DownloadURL
	}
	return ""
}

// FindAssetInfo returns the full asset (URL, size, digest) named `name`,
// or nil if the release doesn't have it.
func FindAssetInfo(rel *Release, name string) *Asset {
	if rel == nil {
		return nil
	}
	for i := range rel.Assets {
		if rel.Assets[i].Name == name {
			return &rel.Assets[i]
		}
	}
	return nil
}

// IsNewer reports whether `latest` is a higher version than `current`,
// following semver precedence (leading "v" allowed, "+build" ignored):
//
//	0.3.9-alpha < 0.3.9-beta < 0.3.9-beta.2 < 0.3.9-beta.10 < 0.3.9 < 0.3.10
//
// The old implementation dropped the pre-release suffix, so 0.3.9-alpha and
// 0.3.9 compared equal and alpha users never got offered the stable build.
// Unparseable input is never "newer" — an odd tag must not trigger an
// install.
func IsNewer(current, latest string) bool {
	a, okA := parseVersion(current)
	b, okB := parseVersion(latest)
	if !okA || !okB {
		return false
	}
	return compareVersions(b, a) > 0
}

type version struct {
	core [3]int
	pre  []string // dot-separated pre-release identifiers; nil = release
}

func parseVersion(s string) (version, bool) {
	var v version
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i] // build metadata has no precedence
	}
	core := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core = s[:i]
		if s[i+1:] == "" {
			return v, false
		}
		v.pre = strings.Split(s[i+1:], ".")
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v.core[i] = n
	}
	return v, true
}

// compareVersions returns -1, 0 or 1 (semver 2.0.0 §11 precedence).
func compareVersions(a, b version) int {
	for i := 0; i < 3; i++ {
		if a.core[i] != b.core[i] {
			if a.core[i] > b.core[i] {
				return 1
			}
			return -1
		}
	}
	// A release outranks any pre-release of the same core version.
	switch {
	case a.pre == nil && b.pre == nil:
		return 0
	case a.pre == nil:
		return 1
	case b.pre == nil:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := compareIdent(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.pre) > len(b.pre):
		return 1
	case len(a.pre) < len(b.pre):
		return -1
	}
	return 0
}

// compareIdent: numeric identifiers compare numerically and rank below
// alphanumeric ones; alphanumerics compare in ASCII order.
func compareIdent(x, y string) int {
	nx, errX := strconv.Atoi(x)
	ny, errY := strconv.Atoi(y)
	switch {
	case errX == nil && errY == nil:
		switch {
		case nx > ny:
			return 1
		case nx < ny:
			return -1
		}
		return 0
	case errX == nil:
		return -1
	case errY == nil:
		return 1
	}
	return strings.Compare(x, y)
}

// validateAsset checks that an asset is safe to download before any byte
// is fetched: it must live under this repo's release-download path and
// carry a SHA-256 digest we can verify against.
func validateAsset(owner, repo string, a *Asset) (wantHash string, err error) {
	if a == nil {
		return "", errors.New("no update asset")
	}
	prefix := fmt.Sprintf("https://github.com/%s/%s/releases/download/", owner, repo)
	if !strings.HasPrefix(a.DownloadURL, prefix) {
		return "", fmt.Errorf("refusing update from unexpected URL %q", a.DownloadURL)
	}
	const algo = "sha256:"
	if !strings.HasPrefix(a.Digest, algo) {
		return "", fmt.Errorf("release asset %s has no sha256 digest — refusing unverifiable update", a.Name)
	}
	h := strings.ToLower(strings.TrimPrefix(a.Digest, algo))
	if b, err := hex.DecodeString(h); err != nil || len(b) != sha256.Size {
		return "", fmt.Errorf("release asset %s has a malformed digest %q", a.Name, a.Digest)
	}
	if a.Size <= 0 {
		return "", fmt.Errorf("release asset %s has no size", a.Name)
	}
	return h, nil
}

// Apply downloads the asset, verifies size and SHA-256, and swaps it in
// for the running executable. The caller is expected to relaunch via
// Restart() and quit the running process.
//
// `a` must come from the GitHub API response (LatestForChannel +
// FindAssetInfo), never from the frontend: the webview renders untrusted
// chat content, so a URL argument from it would be a remote-code-execution
// primitive.
func Apply(owner, repo string, a *Asset) error {
	if runtime.GOOS != "windows" {
		return ErrUnsupported
	}
	wantHash, err := validateAsset(owner, repo, a)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	tmp := exe + ".new"
	_ = os.Remove(tmp) // clear any stale partial download
	if err := download(a.DownloadURL, tmp, a.Size, wantHash); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		// Roll back
		_ = os.Rename(old, exe)
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// download streams url into dst while hashing it, then checks the byte
// count and SHA-256. dst is fsynced so a crash right after the swap can't
// leave a half-written exe in place.
func download(url, dst string, wantSize int64, wantHash string) error {
	resp, err := downloadClient.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}

	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	// Read at most one byte past the expected size so an oversized body is
	// detected without downloading it entirely.
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, wantSize+1))
	if err != nil {
		f.Close()
		return fmt.Errorf("download: %w", err)
	}
	if n != wantSize {
		f.Close()
		return fmt.Errorf("download size mismatch: got %d bytes, want %d", n, wantSize)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantHash {
		f.Close()
		return fmt.Errorf("download checksum mismatch: got sha256 %s, want %s", got, wantHash)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Restart re-launches the (now-updated) executable and exits the current
// process. The caller should call this immediately after a successful Apply.
func Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	cmd := exec.Command(exe)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	// Detach so the new instance survives our os.Exit.
	_ = cmd.Process.Release()
	os.Exit(0)
	return nil
}

// CleanupPrevious removes a leftover <exe>.old from a previous update.
// Best-effort: errors are ignored. Call this once on app startup.
func CleanupPrevious() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	_ = os.Remove(exe + ".old")
}

// Update resolves the newest release on `channel` and, if it is newer than
// `current`, installs its `assetName` (verified, see Apply). This is the
// only entry point the apps' frontend-bound ApplyUpdate should use: it
// takes no URL, so nothing the webview sends can choose what gets run.
func Update(owner, repo, channel, current, assetName string) error {
	rel, err := LatestForChannel(owner, repo, channel)
	if err != nil {
		return fmt.Errorf("check for update: %w", err)
	}
	if !IsNewer(current, rel.TagName) {
		return fmt.Errorf("already up to date (v%s, latest %s)", current, rel.TagName)
	}
	asset := FindAssetInfo(rel, assetName)
	if asset == nil {
		return fmt.Errorf("release %s has no %s", rel.TagName, assetName)
	}
	return Apply(owner, repo, asset)
}

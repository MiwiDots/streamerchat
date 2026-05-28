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
package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	Assets  []struct {
		Name        string `json:"name"`
		DownloadURL string `json:"browser_download_url"`
		Size        int64  `json:"size"`
	} `json:"assets"`
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

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
	resp, err := httpClient.Do(req)
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
	resp, err := httpClient.Do(req)
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
	for _, a := range rel.Assets {
		if a.Name == name {
			return a.DownloadURL
		}
	}
	return ""
}

// IsNewer reports whether `latest` is a higher semver than `current`. Leading
// "v" is stripped. Non-numeric suffixes (e.g. "0.2.0-rc1") are ignored at the
// first non-digit run.
func IsNewer(current, latest string) bool {
	a := parseSemver(current)
	b := parseSemver(latest)
	for i := 0; i < 3; i++ {
		if b[i] > a[i] {
			return true
		}
		if b[i] < a[i] {
			return false
		}
	}
	return false
}

func parseSemver(s string) [3]int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var out [3]int
	parts := strings.SplitN(s, ".", 4)
	for i := 0; i < 3 && i < len(parts); i++ {
		end := 0
		for end < len(parts[i]) && parts[i][end] >= '0' && parts[i][end] <= '9' {
			end++
		}
		if end == 0 {
			continue
		}
		fmt.Sscanf(parts[i][:end], "%d", &out[i])
	}
	return out
}

// Apply downloads `url`, replaces the current executable atomically, and
// returns nil on success. The caller is expected to relaunch via Restart()
// and quit the running process.
func Apply(url string) error {
	if runtime.GOOS != "windows" {
		return ErrUnsupported
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

	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
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

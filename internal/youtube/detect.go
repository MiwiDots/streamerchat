package youtube

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"regexp"
	"strings"
	"time"
)

var (
	videoIDRe = regexp.MustCompile(`"videoId"\s*:\s*"([a-zA-Z0-9_-]{11})"`)
	isLiveRe  = regexp.MustCompile(`"isLive"\s*:\s*true`)
)

// LiveStreamInfo contains details about a detected live stream.
type LiveStreamInfo struct {
	VideoID string
	Title   string
}

// LiveDetector monitors a YouTube channel and detects when it goes live.
type LiveDetector struct {
	httpClient     *http.Client
	channelHandle  string
	pollInterval   time.Duration
	onLive         func(info LiveStreamInfo)
	onOffline      func()
	currentVideo   string
	offlineMisses  int // consecutive offline checks (debounce)
}

// offlineThreshold: number of consecutive offline checks before declaring offline.
// This prevents killing an active connection due to temporary YouTube page issues.
const offlineThreshold = 3

// NewLiveDetector creates a detector for the given YouTube channel.
// channelHandle can be "@username", a channel ID, or a custom URL slug.
func NewLiveDetector(channelHandle string, onLive func(LiveStreamInfo), onOffline func()) *LiveDetector {
	// Normalize handle
	if !strings.HasPrefix(channelHandle, "@") && !strings.HasPrefix(channelHandle, "UC") {
		channelHandle = "@" + channelHandle
	}

	return &LiveDetector{
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		channelHandle: channelHandle,
		pollInterval:  30 * time.Second,
		onLive:        onLive,
		onOffline:     onOffline,
	}
}

// Start begins polling for live status. Blocks until context is cancelled.
func (d *LiveDetector) Start(ctx context.Context) {
	log.Printf("[YT DETECT] Starting for channel %s (poll every %s)", d.channelHandle, d.pollInterval)
	// Check immediately on start
	d.check(ctx)

	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.check(ctx)
		}
	}
}

func (d *LiveDetector) check(ctx context.Context) {
	info, err := d.CheckLive(ctx)
	if err != nil {
		log.Printf("[YT DETECT] CheckLive error: %v", err)
		return
	}
	if info == nil {
		log.Printf("[YT DETECT] Not live (or not detected)")
	} else {
		log.Printf("[YT DETECT] LIVE detected: videoID=%s title=%q", info.VideoID, info.Title)
	}

	if info != nil {
		// Stream is live - reset offline counter
		d.offlineMisses = 0
		if d.currentVideo != info.VideoID {
			d.currentVideo = info.VideoID
			if d.onLive != nil {
				d.onLive(*info)
			}
		}
	} else {
		// Not detected as live - but debounce before disconnecting
		if d.currentVideo != "" {
			d.offlineMisses++
			if d.offlineMisses >= offlineThreshold {
				d.currentVideo = ""
				d.offlineMisses = 0
				if d.onOffline != nil {
					d.onOffline()
				}
			}
		}
	}
}

// CheckLive checks if the channel is currently live.
// Returns nil if not live.
func (d *LiveDetector) CheckLive(ctx context.Context) (*LiveStreamInfo, error) {
	// Fetch the channel's /live page - this redirects to the live stream if active
	url := fmt.Sprintf("https://www.youtube.com/%s/live", d.channelHandle)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch channel live page: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	html := string(body)

	// Check if the page indicates a live stream
	if !isLiveRe.MatchString(html) {
		return nil, nil // Not live
	}

	// Extract video ID
	matches := videoIDRe.FindStringSubmatch(html)
	if len(matches) < 2 {
		return nil, nil // Can't find video ID
	}

	videoID := matches[1]

	// Extract title if possible
	title := extractTitle(html)

	return &LiveStreamInfo{
		VideoID: videoID,
		Title:   title,
	}, nil
}

func extractTitle(html string) string {
	var title string
	re := regexp.MustCompile(`"title"\s*:\s*\{\s*"runs"\s*:\s*\[\s*\{\s*"text"\s*:\s*"([^"]+)"`)
	if matches := re.FindStringSubmatch(html); len(matches) > 1 {
		title = matches[1]
	} else {
		re2 := regexp.MustCompile(`<title>([^<]+)</title>`)
		if matches := re2.FindStringSubmatch(html); len(matches) > 1 {
			title = strings.TrimSuffix(matches[1], " - YouTube")
		}
	}

	// Decode Unicode escapes like \u0026 -> &
	if strings.Contains(title, `\u`) {
		decoded, err := strconv.Unquote(`"` + title + `"`)
		if err == nil {
			title = decoded
		}
	}

	return title
}

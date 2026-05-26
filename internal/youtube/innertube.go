package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/miwi/streamerchat/internal/chat"
)

var (
	ytInitialDataRe = regexp.MustCompile(`(?:window\["ytInitialData"\]|var ytInitialData)\s*=\s*({.*?});`)
	clientVersionRe = regexp.MustCompile(`"clientVersion":"([\d.]+)"`)
)

// InnerTubeClient reads YouTube live chat via the internal InnerTube API.
type InnerTubeClient struct {
	httpClient    *http.Client
	videoID       string
	continuation  string
	clientVersion string
	messages      chan chat.Message
	errors        chan error
}

// NewInnerTubeClient creates a new YouTube chat client for the given video.
func NewInnerTubeClient(videoID string) *InnerTubeClient {
	return &InnerTubeClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		videoID:    videoID,
		messages:   make(chan chat.Message, 256),
		errors:     make(chan error, 16),
	}
}

// Messages returns the channel for incoming chat messages.
func (c *InnerTubeClient) Messages() <-chan chat.Message {
	return c.messages
}

// Errors returns the channel for errors.
func (c *InnerTubeClient) Errors() <-chan error {
	return c.errors
}

// Connect fetches the initial page and starts polling.
func (c *InnerTubeClient) Connect(ctx context.Context) error {
	if err := c.fetchInitialData(ctx); err != nil {
		return fmt.Errorf("fetch initial data: %w", err)
	}

	go c.pollLoop(ctx)
	return nil
}

func (c *InnerTubeClient) fetchInitialData(ctx context.Context) error {
	url := fmt.Sprintf("https://www.youtube.com/live_chat?is_popout=1&v=%s", c.videoID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	html := string(body)

	// Extract client version
	if matches := clientVersionRe.FindStringSubmatch(html); len(matches) > 1 {
		c.clientVersion = matches[1]
	} else {
		c.clientVersion = "2.20260401.00.00" // fallback
	}

	// Extract ytInitialData
	matches := ytInitialDataRe.FindStringSubmatch(html)
	if len(matches) < 2 {
		return fmt.Errorf("could not find ytInitialData in page")
	}

	var initialData map[string]interface{}
	if err := json.Unmarshal([]byte(matches[1]), &initialData); err != nil {
		return fmt.Errorf("parse ytInitialData: %w", err)
	}

	// Navigate to continuation token
	c.continuation = extractContinuation(initialData)
	if c.continuation == "" {
		return fmt.Errorf("could not find continuation token - stream may not be live")
	}

	c.messages <- chat.Message{
		Platform:  chat.PlatformYouTube,
		Type:      chat.MessageTypeSystem,
		Timestamp: time.Now(),
		Text:      fmt.Sprintf("Connected to live chat (video: %s)", c.videoID),
	}
	return nil
}

func (c *InnerTubeClient) pollLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		timeoutMs, err := c.fetchMessages(ctx)
		if err != nil {
			c.errors <- fmt.Errorf("poll: %w", err)
			// Back off on error
			timeoutMs = 10000
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(timeoutMs) * time.Millisecond):
		}
	}
}

// innerTubeRequest is the request body for the InnerTube API.
type innerTubeRequest struct {
	Context      innerTubeContext `json:"context"`
	Continuation string          `json:"continuation"`
}

type innerTubeContext struct {
	Client innerTubeClient `json:"client"`
}

type innerTubeClient struct {
	ClientName    string `json:"clientName"`
	ClientVersion string `json:"clientVersion"`
}

func (c *InnerTubeClient) fetchMessages(ctx context.Context) (int, error) {
	reqBody := innerTubeRequest{
		Context: innerTubeContext{
			Client: innerTubeClient{
				ClientName:    "WEB",
				ClientVersion: c.clientVersion,
			},
		},
		Continuation: c.continuation,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return 5000, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://www.youtube.com/youtubei/v1/live_chat/get_live_chat",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return 5000, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")
	req.Header.Set("Origin", "https://www.youtube.com")
	req.Header.Set("Referer", fmt.Sprintf("https://www.youtube.com/live_chat?is_popout=1&v=%s", c.videoID))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 5000, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 5000, fmt.Errorf("decode response: %w", err)
	}

	// Extract continuation and timeout
	timeoutMs := 5000
	contContents := navigateJSON(result, "continuationContents", "liveChatContinuation")
	if contContents == nil {
		return timeoutMs, fmt.Errorf("no liveChatContinuation in response")
	}

	contMap, ok := contContents.(map[string]interface{})
	if !ok {
		return timeoutMs, fmt.Errorf("liveChatContinuation is not an object")
	}

	// Update continuation token
	if continuations, ok := contMap["continuations"].([]interface{}); ok && len(continuations) > 0 {
		if cont, ok := continuations[0].(map[string]interface{}); ok {
			for _, key := range []string{"invalidationContinuationData", "timedContinuationData", "reloadContinuationData"} {
				if data, ok := cont[key].(map[string]interface{}); ok {
					if token, ok := data["continuation"].(string); ok {
						c.continuation = token
					}
					if ms, ok := data["timeoutMs"].(float64); ok {
						timeoutMs = int(ms)
					}
					break
				}
			}
		}
	}

	// Parse chat actions
	if actions, ok := contMap["actions"].([]interface{}); ok {
		for _, action := range actions {
			if msg := c.parseAction(action); msg != nil {
				c.messages <- *msg
			}
		}
	}

	return timeoutMs, nil
}

func (c *InnerTubeClient) parseAction(action interface{}) *chat.Message {
	actionMap, ok := action.(map[string]interface{})
	if !ok {
		return nil
	}

	// Handle addChatItemAction
	addItem, ok := actionMap["addChatItemAction"].(map[string]interface{})
	if !ok {
		return nil
	}

	item, ok := addItem["item"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Normal text message
	if renderer, ok := item["liveChatTextMessageRenderer"].(map[string]interface{}); ok {
		return c.parseTextMessage(renderer)
	}

	// Super Chat
	if renderer, ok := item["liveChatPaidMessageRenderer"].(map[string]interface{}); ok {
		return c.parseSuperChat(renderer)
	}

	// Membership
	if renderer, ok := item["liveChatMembershipItemRenderer"].(map[string]interface{}); ok {
		return c.parseMembership(renderer)
	}

	return nil
}

func (c *InnerTubeClient) parseTextMessage(renderer map[string]interface{}) *chat.Message {
	msg := &chat.Message{
		Platform:  chat.PlatformYouTube,
		Type:      chat.MessageTypeChat,
		Timestamp: extractTimestamp(renderer),
	}

	msg.ID, _ = renderer["id"].(string)
	msg.Text = extractMessageText(renderer)

	if author, ok := renderer["authorName"].(map[string]interface{}); ok {
		if runs, ok := author["simpleText"].(string); ok {
			msg.DisplayName = runs
			msg.Username = runs
		}
	}

	if channelID, ok := renderer["authorExternalChannelId"].(string); ok {
		msg.UserID = channelID
	}

	// Parse badges
	if badges, ok := renderer["authorBadges"].([]interface{}); ok {
		for _, badge := range badges {
			if b, ok := badge.(map[string]interface{}); ok {
				if renderer, ok := b["liveChatAuthorBadgeRenderer"].(map[string]interface{}); ok {
					if tooltip, ok := renderer["tooltip"].(string); ok {
						msg.Badges = append(msg.Badges, chat.Badge{Name: tooltip})
						switch {
						case tooltip == "Moderator":
							msg.IsMod = true
						case tooltip == "Owner":
							msg.IsBroadcaster = true
						case contains(tooltip, "Member"):
							msg.IsSub = true
						}
					}
				}
			}
		}
	}

	return msg
}

func (c *InnerTubeClient) parseSuperChat(renderer map[string]interface{}) *chat.Message {
	msg := c.parseTextMessage(renderer)
	if msg == nil {
		msg = &chat.Message{
			Platform:  chat.PlatformYouTube,
			Timestamp: extractTimestamp(renderer),
		}
	}
	msg.Type = chat.MessageTypeSuperChat

	if amount, ok := renderer["purchaseAmountText"].(map[string]interface{}); ok {
		if text, ok := amount["simpleText"].(string); ok {
			msg.SuperChatAmount = text
		}
	}

	return msg
}

func (c *InnerTubeClient) parseMembership(renderer map[string]interface{}) *chat.Message {
	msg := &chat.Message{
		Platform:  chat.PlatformYouTube,
		Type:      chat.MessageTypeMembership,
		Timestamp: extractTimestamp(renderer),
	}

	if header, ok := renderer["headerSubtext"].(map[string]interface{}); ok {
		msg.Text = extractRunsText(header)
	}

	if author, ok := renderer["authorName"].(map[string]interface{}); ok {
		if text, ok := author["simpleText"].(string); ok {
			msg.DisplayName = text
			msg.Username = text
		}
	}

	if channelID, ok := renderer["authorExternalChannelId"].(string); ok {
		msg.UserID = channelID
	}

	return msg
}

// Helper functions

func extractContinuation(data map[string]interface{}) string {
	// Try multiple paths where the continuation token can be
	paths := [][]string{
		{"contents", "liveChatRenderer", "continuations"},
		{"continuationContents", "liveChatContinuation", "continuations"},
	}

	for _, path := range paths {
		current := interface{}(data)
		for _, key := range path[:len(path)-1] {
			if m, ok := current.(map[string]interface{}); ok {
				current = m[key]
			} else {
				current = nil
				break
			}
		}

		if continuations, ok := current.(map[string]interface{}); ok {
			if conts, ok := continuations["continuations"].([]interface{}); ok && len(conts) > 0 {
				if cont, ok := conts[0].(map[string]interface{}); ok {
					for _, key := range []string{"invalidationContinuationData", "timedContinuationData", "reloadContinuationData"} {
						if data, ok := cont[key].(map[string]interface{}); ok {
							if token, ok := data["continuation"].(string); ok {
								return token
							}
						}
					}
				}
			}
		}
	}

	return ""
}

func navigateJSON(data map[string]interface{}, keys ...string) interface{} {
	var current interface{} = data
	for _, key := range keys {
		if m, ok := current.(map[string]interface{}); ok {
			current = m[key]
		} else {
			return nil
		}
	}
	return current
}

func extractMessageText(renderer map[string]interface{}) string {
	if message, ok := renderer["message"].(map[string]interface{}); ok {
		return extractRunsText(message)
	}
	return ""
}

func extractRunsText(obj map[string]interface{}) string {
	if runs, ok := obj["runs"].([]interface{}); ok {
		var text string
		for _, run := range runs {
			if r, ok := run.(map[string]interface{}); ok {
				if t, ok := r["text"].(string); ok {
					text += t
				}
				// Emoji
				if emoji, ok := r["emoji"].(map[string]interface{}); ok {
					if shortcuts, ok := emoji["shortcuts"].([]interface{}); ok && len(shortcuts) > 0 {
						if s, ok := shortcuts[0].(string); ok {
							text += s
						}
					} else if emojiID, ok := emoji["emojiId"].(string); ok {
						text += emojiID
					}
				}
			}
		}
		return text
	}
	if simple, ok := obj["simpleText"].(string); ok {
		return simple
	}
	return ""
}

func extractTimestamp(renderer map[string]interface{}) time.Time {
	if ts, ok := renderer["timestampUsec"].(string); ok {
		var usec int64
		fmt.Sscanf(ts, "%d", &usec)
		if usec > 0 {
			return time.UnixMicro(usec)
		}
	}
	return time.Now()
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsLower(s, substr))
}

func containsLower(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

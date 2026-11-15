package twitch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nicklaw5/helix/v2"
)

// Broadcaster / moderator tools beyond ban & timeout: warnings, shield
// mode, polls, predictions, clips, stream markers, title and category.
//
// Twitch limits (from the Helix reference):
//   - poll: title ≤ 60 chars, 2–5 choices of ≤ 25 chars, 15–1800 s
//   - prediction: title ≤ 45 chars, 2–10 outcomes of ≤ 25 chars, 30–1800 s
const (
	pollTitleMax         = 60
	pollChoicesMin       = 2
	pollChoicesMax       = 5
	pollDurationMin      = 15
	pollDurationMax      = 1800
	pollDefaultDuration  = 120
	predTitleMax         = 45
	predOutcomesMin      = 2
	predOutcomesMax      = 10
	predWindowMin        = 30
	predWindowMax        = 1800
	predDefaultWindow    = 120
	choiceTitleMax       = 25
	warnReasonMax        = 500
	markerDescriptionMax = 140
)

// ErrMissingScope marks a Helix 401 caused by a scope the login doesn't
// have yet (new features ask for new scopes) — the fix is to log in again.
var ErrMissingScope = errors.New("missing permission — log out and in again (Settings → Twitch) to grant it")

// scopeErr wraps a Helix error answer, turning "Missing scope" 401s into
// ErrMissingScope so the UI can tell the user what to do.
func scopeErr(op string, rc helix.ResponseCommon) error {
	if rc.StatusCode == http.StatusUnauthorized && strings.Contains(strings.ToLower(rc.ErrorMessage), "scope") {
		return fmt.Errorf("%s: %w", op, ErrMissingScope)
	}
	return statusErr(op, rc)
}

// rawJSON performs a Helix request the helix library doesn't cover (or
// covers wrongly). body may be nil. 2xx = success.
func (h *HelixClient) rawJSON(op, method, endpoint string, query url.Values, body interface{}) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	u := "https://api.twitch.tv/helix/" + endpoint
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Client-Id", h.clientID)
	req.Header.Set("Authorization", "Bearer "+h.client.GetUserAccessToken())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := heldMessageHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	var apiErr struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&apiErr)
	return scopeErr(op, helix.ResponseCommon{StatusCode: resp.StatusCode, ErrorMessage: apiErr.Message})
}

// WarnUser issues a Twitch warning: the user must acknowledge it before
// they can chat again. reason is required by Twitch.
func (h *HelixClient) WarnUser(userID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("warn: a reason is required")
	}
	if len([]rune(reason)) > warnReasonMax {
		return fmt.Errorf("warn: reason is longer than %d characters", warnReasonMax)
	}
	resp, err := h.client.SendModeratorWarnMessage(&helix.SendModeratorWarnChatMessageParams{
		BroadcasterID: h.broadcasterID,
		ModeratorID:   h.moderatorID,
		Body:          helix.SendModeratorWarnMessageRequestBody{UserID: userID, Reason: reason},
	})
	if err != nil {
		return fmt.Errorf("warn: %w", err)
	}
	return scopeErr("warn", resp.ResponseCommon)
}

// SetShieldMode turns Shield Mode on or off (not in the helix library).
// Scope: moderator:manage:shield_mode.
func (h *HelixClient) SetShieldMode(active bool) error {
	q := url.Values{"broadcaster_id": {h.broadcasterID}, "moderator_id": {h.moderatorID}}
	return h.rawJSON("shield mode", http.MethodPut, "moderation/shield_mode", q, map[string]bool{"is_active": active})
}

// CreatePoll starts a poll. Scope: channel:manage:polls.
func (h *HelixClient) CreatePoll(title string, choices []string, seconds int) error {
	if err := validateChoices("poll", title, pollTitleMax, choices, pollChoicesMin, pollChoicesMax); err != nil {
		return err
	}
	if seconds < pollDurationMin || seconds > pollDurationMax {
		return fmt.Errorf("poll: duration must be %d–%d seconds", pollDurationMin, pollDurationMax)
	}
	params := &helix.CreatePollParams{BroadcasterID: h.broadcasterID, Title: title, Duration: seconds}
	for _, c := range choices {
		params.Choices = append(params.Choices, helix.PollChoiceParam{Title: c})
	}
	resp, err := h.client.CreatePoll(params)
	if err != nil {
		return fmt.Errorf("poll: %w", err)
	}
	return scopeErr("poll", resp.ResponseCommon)
}

// EndActivePoll ends the running poll; archive=true also hides it from
// viewers (Twitch status ARCHIVED instead of TERMINATED).
func (h *HelixClient) EndActivePoll(archive bool) error {
	resp, err := h.client.GetPolls(&helix.PollsParams{BroadcasterID: h.broadcasterID, First: "1"})
	if err != nil {
		return fmt.Errorf("end poll: %w", err)
	}
	if err := scopeErr("end poll", resp.ResponseCommon); err != nil {
		return err
	}
	if len(resp.Data.Polls) == 0 || resp.Data.Polls[0].Status != "ACTIVE" {
		return errors.New("end poll: no poll is running")
	}
	status := "TERMINATED"
	if archive {
		status = "ARCHIVED"
	}
	end, err := h.client.EndPoll(&helix.EndPollParams{BroadcasterID: h.broadcasterID, ID: resp.Data.Polls[0].ID, Status: status})
	if err != nil {
		return fmt.Errorf("end poll: %w", err)
	}
	return scopeErr("end poll", end.ResponseCommon)
}

// CreatePrediction starts a prediction. Scope: channel:manage:predictions.
func (h *HelixClient) CreatePrediction(title string, outcomes []string, windowSeconds int) error {
	if err := validateChoices("prediction", title, predTitleMax, outcomes, predOutcomesMin, predOutcomesMax); err != nil {
		return err
	}
	if windowSeconds < predWindowMin || windowSeconds > predWindowMax {
		return fmt.Errorf("prediction: window must be %d–%d seconds", predWindowMin, predWindowMax)
	}
	params := &helix.CreatePredictionParams{BroadcasterID: h.broadcasterID, Title: title, PredictionWindow: windowSeconds}
	for _, o := range outcomes {
		params.Outcomes = append(params.Outcomes, helix.PredictionChoiceParam{Title: o})
	}
	resp, err := h.client.CreatePrediction(params)
	if err != nil {
		return fmt.Errorf("prediction: %w", err)
	}
	return scopeErr("prediction", resp.ResponseCommon)
}

// activePrediction returns the newest prediction if it is still open
// (ACTIVE or LOCKED).
func (h *HelixClient) activePrediction(op string) (*helix.Prediction, error) {
	resp, err := h.client.GetPredictions(&helix.PredictionsParams{BroadcasterID: h.broadcasterID, First: "1"})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if err := scopeErr(op, resp.ResponseCommon); err != nil {
		return nil, err
	}
	if len(resp.Data.Predictions) == 0 {
		return nil, fmt.Errorf("%s: no prediction is open", op)
	}
	p := resp.Data.Predictions[0]
	if p.Status != "ACTIVE" && p.Status != "LOCKED" {
		return nil, fmt.Errorf("%s: no prediction is open", op)
	}
	return &p, nil
}

// LockPrediction stops accepting predictions.
func (h *HelixClient) LockPrediction() error { return h.endPrediction("lock prediction", "LOCKED", 0) }

// CancelPrediction cancels the open prediction and refunds the points.
func (h *HelixClient) CancelPrediction() error {
	return h.endPrediction("cancel prediction", "CANCELED", 0)
}

// ResolvePrediction pays out the outcome at position n (1-based).
func (h *HelixClient) ResolvePrediction(n int) error {
	return h.endPrediction("resolve prediction", "RESOLVED", n)
}

func (h *HelixClient) endPrediction(op, status string, winner int) error {
	p, err := h.activePrediction(op)
	if err != nil {
		return err
	}
	params := &helix.EndPredictionParams{BroadcasterID: h.broadcasterID, ID: p.ID, Status: status}
	if status == "RESOLVED" {
		if winner < 1 || winner > len(p.Outcomes) {
			return fmt.Errorf("%s: choose an outcome 1–%d", op, len(p.Outcomes))
		}
		params.WinningOutcomeID = p.Outcomes[winner-1].ID
	}
	resp, err := h.client.EndPrediction(params)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return scopeErr(op, resp.ResponseCommon)
}

// CreateClip clips the live stream and returns the clip's edit URL.
// Scope: clips:edit.
func (h *HelixClient) CreateClip() (string, error) {
	resp, err := h.client.CreateClip(&helix.CreateClipParams{BroadcasterID: h.broadcasterID})
	if err != nil {
		return "", fmt.Errorf("clip: %w", err)
	}
	if err := scopeErr("clip", resp.ResponseCommon); err != nil {
		return "", err
	}
	if len(resp.Data.ClipEditURLs) == 0 {
		return "", errors.New("clip: Twitch returned no clip (is the stream live?)")
	}
	return resp.Data.ClipEditURLs[0].EditURL, nil
}

// CreateMarker adds a stream marker at the current position (live only).
// Scope: channel:manage:broadcast.
func (h *HelixClient) CreateMarker(description string) error {
	if len([]rune(description)) > markerDescriptionMax {
		return fmt.Errorf("marker: description is longer than %d characters", markerDescriptionMax)
	}
	resp, err := h.client.CreateStreamMarker(&helix.CreateStreamMarkerParams{UserID: h.broadcasterID, Description: description})
	if err != nil {
		return fmt.Errorf("marker: %w", err)
	}
	return scopeErr("marker", resp.ResponseCommon)
}

// SetTitle changes the stream title. Scope: channel:manage:broadcast.
func (h *HelixClient) SetTitle(title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("title: empty")
	}
	resp, err := h.client.EditChannelInformation(&helix.EditChannelInformationParams{BroadcasterID: h.broadcasterID, Title: title})
	if err != nil {
		return fmt.Errorf("title: %w", err)
	}
	return scopeErr("title", resp.ResponseCommon)
}

// SetCategory changes the stream category by name: exact match first,
// otherwise the best search hit. Returns the category actually set.
func (h *HelixClient) SetCategory(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("game: empty")
	}
	id, resolved := "", ""
	if g, err := h.client.GetGames(&helix.GamesParams{Names: []string{name}}); err == nil && g.StatusCode < 400 && len(g.Data.Games) > 0 {
		id, resolved = g.Data.Games[0].ID, g.Data.Games[0].Name
	} else {
		s, err := h.client.SearchCategories(&helix.SearchCategoriesParams{Query: name, First: 1})
		if err != nil {
			return "", fmt.Errorf("game: %w", err)
		}
		if err := scopeErr("game", s.ResponseCommon); err != nil {
			return "", err
		}
		if len(s.Data.Categories) == 0 {
			return "", fmt.Errorf("game: no category found for %q", name)
		}
		id, resolved = s.Data.Categories[0].ID, s.Data.Categories[0].Name
	}
	resp, err := h.client.EditChannelInformation(&helix.EditChannelInformationParams{BroadcasterID: h.broadcasterID, GameID: id})
	if err != nil {
		return "", fmt.Errorf("game: %w", err)
	}
	return resolved, scopeErr("game", resp.ResponseCommon)
}

// validateChoices checks title / option lengths against Twitch's limits
// before calling the API, so the user gets a precise message.
func validateChoices(op, title string, titleMax int, choices []string, min, max int) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("%s: a question is required", op)
	}
	if n := len([]rune(title)); n > titleMax {
		return fmt.Errorf("%s: question is %d characters, max %d", op, n, titleMax)
	}
	if len(choices) < min || len(choices) > max {
		return fmt.Errorf("%s: needs %d–%d options, got %d", op, min, max, len(choices))
	}
	for _, c := range choices {
		if c == "" {
			return fmt.Errorf("%s: empty option", op)
		}
		if n := len([]rune(c)); n > choiceTitleMax {
			return fmt.Errorf("%s: option %q is %d characters, max %d", op, c, n, choiceTitleMax)
		}
	}
	return nil
}

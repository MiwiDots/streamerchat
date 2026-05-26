package twitch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Required OAuth scopes for full mod support.
var requiredScopes = []string{
	// Chat
	"chat:read",
	"chat:edit",
	// Moderation
	"moderator:manage:banned_users",
	"moderator:manage:chat_messages",
	"moderator:manage:chat_settings",
	"moderator:manage:automod",
	"moderator:manage:automod_settings",
	"moderator:manage:blocked_terms",
	"moderator:manage:announcements",
	"moderator:manage:shoutouts",
	"moderator:manage:warnings",
	"moderator:read:chat_settings",
	"moderator:read:chatters",
	"moderator:read:followers",
	"moderator:read:automod_settings",
	"moderator:read:blocked_terms",
	// Channel
	"channel:moderate",
	"channel:read:subscriptions",
	"channel:read:vips",
	"channel:manage:vips",
	"channel:manage:moderators",
	"channel:manage:raids",
	"channel:manage:broadcast",
	"channel:read:redemptions",
}

// DeviceCodeResponse is returned when requesting a device code.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
}

// TokenResponse is returned when the user completes authorization.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        []string `json:"scope"`
	TokenType    string `json:"token_type"`
}

// ValidateResponse is returned when validating a token.
type ValidateResponse struct {
	ClientID  string   `json:"client_id"`
	Login     string   `json:"login"`
	UserID    string   `json:"user_id"`
	Scopes    []string `json:"scopes"`
	ExpiresIn int      `json:"expires_in"`
}

// RequestDeviceCode starts the device code flow.
func RequestDeviceCode(clientID string) (*DeviceCodeResponse, error) {
	data := url.Values{
		"client_id": {clientID},
		"scopes":    {strings.Join(requiredScopes, " ")},
	}

	resp, err := http.PostForm("https://id.twitch.tv/oauth2/device", data)
	if err != nil {
		return nil, fmt.Errorf("request device code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errBody)
		return nil, fmt.Errorf("device code request failed (%d): %v", resp.StatusCode, errBody)
	}

	var result DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse device code response: %w", err)
	}

	return &result, nil
}

// PollForToken polls Twitch until the user completes authorization.
// Returns the token response or an error if the code expires.
func PollForToken(clientID, deviceCode string, interval int) (*TokenResponse, error) {
	pollInterval := time.Duration(interval) * time.Second
	if pollInterval < 5*time.Second {
		pollInterval = 5 * time.Second
	}

	for {
		time.Sleep(pollInterval)

		data := url.Values{
			"client_id":   {clientID},
			"device_code": {deviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}

		resp, err := http.PostForm("https://id.twitch.tv/oauth2/token", data)
		if err != nil {
			return nil, fmt.Errorf("poll token: %w", err)
		}

		body := make(map[string]interface{})
		json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			// Success - parse the token
			tokenBytes, _ := json.Marshal(body)
			var token TokenResponse
			if err := json.Unmarshal(tokenBytes, &token); err != nil {
				return nil, fmt.Errorf("parse token: %w", err)
			}
			return &token, nil
		}

		// Check error status
		if status, ok := body["status"].(float64); ok && int(status) == 400 {
			if msg, ok := body["message"].(string); ok {
				if strings.Contains(msg, "authorization_pending") || strings.Contains(msg, "authorization pending") {
					// Still waiting for user - continue polling
					continue
				}
				if strings.Contains(msg, "expired") {
					return nil, fmt.Errorf("device code expired - please try again")
				}
				if strings.Contains(msg, "slow down") {
					pollInterval += 5 * time.Second
					continue
				}
			}
		}

		return nil, fmt.Errorf("unexpected response (%d): %v", resp.StatusCode, body)
	}
}

// ValidateToken checks if an access token is still valid and returns user info.
func ValidateToken(accessToken string) (*ValidateResponse, error) {
	req, err := http.NewRequest("GET", "https://id.twitch.tv/oauth2/validate", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "OAuth "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("validate token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token invalid or expired (status %d)", resp.StatusCode)
	}

	var result ValidateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse validate response: %w", err)
	}

	return &result, nil
}

// RefreshAccessToken uses a refresh token to get a new access token.
// Works with public clients (no client secret needed for device code flow).
func RefreshAccessToken(clientID, clientSecret, refreshToken string) (*TokenResponse, error) {
	data := url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}

	resp, err := http.PostForm("https://id.twitch.tv/oauth2/token", data)
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed (status %d)", resp.StatusCode)
	}

	var token TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}

	return &token, nil
}

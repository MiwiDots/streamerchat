package twitch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// SendChatMessage posts a chat message via the Helix Send Chat Message
// endpoint. This is Twitch's modern replacement for raw-IRC PRIVMSG —
// IRC sending is documented as "historically" supported but silently
// throttled / dropped for unverified clients, while Helix returns
// explicit HTTP status + a JSON drop_reason so the caller can surface
// "sub-only", "follower-only", "ratelimit" etc. as real errors.
//
// Spec: https://dev.twitch.tv/docs/api/reference/#send-chat-message
// Required scope on the user access token: user:write:chat (newer than
// the old chat:edit scope — accounts will be re-prompted to grant it
// on next login if missing).
func SendChatMessage(clientID, accessToken, broadcasterID, senderID, message string) error {
	if broadcasterID == "" {
		return fmt.Errorf("twitch: broadcaster_id missing")
	}
	if senderID == "" {
		return fmt.Errorf("twitch: sender_id missing (user not logged in?)")
	}

	body := map[string]string{
		"broadcaster_id": broadcasterID,
		"sender_id":      senderID,
		"message":        message,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", "https://api.twitch.tv/helix/chat/messages", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Client-Id", clientID)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode != 200 {
		log.Printf("[SEND] Helix status %d body=%s", resp.StatusCode, string(raw))
		return fmt.Errorf("twitch: send_message status %d: %s", resp.StatusCode, string(raw))
	}

	// Even with 200 OK Twitch can report is_sent=false + a drop_reason
	// (e.g. duplicate). Surface that as an error so the UI shows it.
	var parsed struct {
		Data []struct {
			MessageID  string `json:"message_id"`
			IsSent     bool   `json:"is_sent"`
			DropReason *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"drop_reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		log.Printf("[SEND] Helix decode: %v body=%s", err, string(raw))
		return nil // assume success — message went out
	}
	if len(parsed.Data) == 0 {
		return nil
	}
	d := parsed.Data[0]
	if !d.IsSent {
		reason := "rejected by Twitch"
		if d.DropReason != nil {
			reason = d.DropReason.Message
			if reason == "" {
				reason = d.DropReason.Code
			}
		}
		log.Printf("[SEND] Helix is_sent=false reason=%s", reason)
		return fmt.Errorf("twitch: %s", reason)
	}
	return nil
}

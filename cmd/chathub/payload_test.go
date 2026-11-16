package main

import (
	"testing"

	"github.com/miwi/streamerchat/internal/chat"
)

// The frontend keys features off these payload fields (reply button needs
// platform + id + channel; markers need the flags). A missing key fails
// silently in the UI, so pin them here.
func TestChatPayloadHasFieldsTheFrontendNeeds(t *testing.T) {
	a := &App{cfg: &HubConfig{}}
	m := a.chatToMap("miwitv", chat.Message{
		ID: "m1", Platform: chat.PlatformTwitch, Type: chat.MessageTypeChat,
		Username: "alice", Text: "hi", ReplyParentID: "p1", ReplyParentUser: "Bob",
	})
	for _, k := range []string{"id", "platform", "channel", "type", "username", "text",
		"replyParentId", "replyParentUser", "replyParentBody", "firstMsg", "returningChatter",
		"subTier", "subPrime", "subMonths", "giftCount", "powerUp", "animationId", "highlighted", "rewardId", "gifs", "streak"} {
		if _, ok := m[k]; !ok {
			t.Errorf("payload is missing %q", k)
		}
	}
	if m["platform"] != "twitch" || m["channel"] != "miwitv" || m["id"] != "m1" {
		t.Fatalf("wrong values: platform=%v channel=%v id=%v", m["platform"], m["channel"], m["id"])
	}
}

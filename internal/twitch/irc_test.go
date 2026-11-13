package twitch

import (
	"strings"
	"testing"
	"time"

	"github.com/miwi/streamerchat/internal/chat"

	twitch "github.com/gempir/go-twitch-irc/v4"
)

// The gempir parser goroutine calls our handlers; a blocking send there
// froze the whole connection once nobody drained the channel (chathub's
// sender). push must drop instead.
func TestPushNeverBlocksOnFullChannel(t *testing.T) {
	c := NewSendOnlyIRCClient("u", "tok", "chan")
	done := make(chan struct{})
	go func() {
		for i := 0; i < cap(c.messages)+50; i++ {
			push(c, c.messages, chat.Message{Text: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("push blocked on a full channel")
	}
	if got := c.dropped.Load(); got != 50 {
		t.Fatalf("dropped = %d, want 50", got)
	}
}

func TestSanitizeIRCTextBlocksLineInjection(t *testing.T) {
	got := sanitizeIRCText("hi\r\nPRIVMSG #other :spam\nJOIN #x")
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("line break survived: %q", got)
	}
	if got != "hi  PRIVMSG #other :spam JOIN #x" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeIRCText("a\x00b\x07c\x01d"); got != "abc\x01d" {
		t.Fatalf("control chars: got %q", got)
	}
	if got := sanitizeIRCText("normal text äöü 🎉"); got != "normal text äöü 🎉" {
		t.Fatalf("unicode mangled: %q", got)
	}
}

func TestUserNoticeText(t *testing.T) {
	cases := []struct {
		id, sys, msg, want string
	}{
		{"announcement", "", "Stream starts at 8!", "Stream starts at 8!"},
		{"resub", "bob subscribed for 12 months!", "love the stream", "bob subscribed for 12 months! — love the stream"},
		{"resub", "bob subscribed for 12 months!", "", "bob subscribed for 12 months!"},
		{"raid", "50 raiders from x", "", "50 raiders from x"},
	}
	for _, c := range cases {
		var m twitch.UserNoticeMessage
		m.MsgID, m.SystemMsg, m.Message = c.id, c.sys, c.msg
		if got := userNoticeText(m); got != c.want {
			t.Errorf("%s: got %q, want %q", c.id, got, c.want)
		}
	}
}

func TestConvertPrivateMessageFirstMsg(t *testing.T) {
	var msg twitch.PrivateMessage
	msg.Tags = map[string]string{"first-msg": "1"}
	if !convertPrivateMessage(msg, "c").IsFirstMessage {
		t.Fatal("first-msg=1 not detected")
	}
	for _, v := range []string{"0", ""} {
		msg.Tags = map[string]string{"first-msg": v}
		if convertPrivateMessage(msg, "c").IsFirstMessage {
			t.Fatalf("first-msg=%q wrongly flagged", v)
		}
	}
}

func TestConvertPrivateMessageReturningChatter(t *testing.T) {
	var msg twitch.PrivateMessage
	msg.Tags = map[string]string{"returning-chatter": "1"}
	if !convertPrivateMessage(msg, "c").IsReturningChatter {
		t.Fatal("returning-chatter=1 not detected")
	}
	msg.Tags = map[string]string{"returning-chatter": "0"}
	if convertPrivateMessage(msg, "c").IsReturningChatter {
		t.Fatal("returning-chatter=0 wrongly flagged")
	}
}

func TestApplyWatchStreak(t *testing.T) {
	var msg twitch.UserNoticeMessage
	msg.MsgID = "viewermilestone"
	msg.ID = "abc"
	msg.Message = "lets go"
	msg.MsgParams = map[string]string{"msg-param-category": "watch-streak", "msg-param-value": "12"}
	msg.User.Badges = map[string]int{"subscriber": 6}
	var m chat.Message
	if !applyWatchStreak(&m, msg) {
		t.Fatal("watch streak not recognised")
	}
	if m.Type != chat.MessageTypeWatchStreak || m.StreakCount != 12 || m.Text != "lets go" || m.ID != "abc" || !m.IsSub {
		t.Fatalf("unexpected message: %+v", m)
	}

	// Other milestone categories / notices are left alone.
	msg.MsgParams["msg-param-category"] = "something-else"
	if applyWatchStreak(&chat.Message{}, msg) {
		t.Fatal("non watch-streak milestone must not match")
	}
	msg.MsgID = "resub"
	msg.MsgParams["msg-param-category"] = "watch-streak"
	if applyWatchStreak(&chat.Message{}, msg) {
		t.Fatal("resub must not match")
	}
}

// Lines sent before the connection is ready are queued in order, never
// handed to gempir early (Twitch would drop them before login/JOIN).
func TestSendOnlyQueuesUntilReady(t *testing.T) {
	c := NewSendOnlyIRCClient("u", "tok", "chan")
	defer c.Disconnect()
	c.Say("first\r\nline")
	c.Action("waves")
	if got := len(c.outbox); got != 2 {
		t.Fatalf("outbox len = %d, want 2 (nothing may be sent before ready)", got)
	}
	if l := <-c.outbox; l != "first  line" {
		t.Fatalf("first queued line = %q (must be sanitized, in order)", l)
	}
	if l := <-c.outbox; l != "\x01ACTION waves\x01" {
		t.Fatalf("second queued line = %q", l)
	}
	// A full outbox drops instead of blocking the caller.
	done := make(chan struct{})
	go func() {
		for i := 0; i < sendOutboxSize+5; i++ {
			c.Say("x")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Say blocked on a full outbox")
	}
	c.Disconnect() // second Disconnect must not panic (stopOnce)
}

func TestAnonymousClientNeedsNoToken(t *testing.T) {
	c := NewAnonymousIRCClient("#SomeChannel")
	if c.channel != "SomeChannel" || c.outbox != nil {
		t.Fatalf("unexpected client: channel=%q outbox=%v", c.channel, c.outbox != nil)
	}
	if len(c.client.Capabilities) != 3 {
		t.Fatalf("reader needs tags+commands+membership caps, got %v", c.client.Capabilities)
	}
}

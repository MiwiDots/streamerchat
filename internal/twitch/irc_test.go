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

package twitch

import (
	"testing"
	"time"

	"github.com/miwi/streamerchat/internal/chat"
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

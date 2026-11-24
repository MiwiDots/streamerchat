package twitch

import (
	"sync/atomic"
	"testing"
	"time"
)

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRoomNameCacheDoesNotBlockAndResolvesOnce(t *testing.T) {
	c := newRoomNameCache()
	release := make(chan struct{})
	var calls atomic.Int32
	c.setResolver(func(id string) string {
		calls.Add(1)
		<-release // a stalled Helix request
		return "otherchannel"
	})
	start := time.Now()
	for i := 0; i < 5; i++ {
		if got := c.name("123"); got != "123" {
			t.Fatalf("while resolving, want the room id, got %q", got)
		}
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("name() must not wait for the resolver")
	}
	close(release)
	waitFor(t, func() bool { return c.name("123") == "otherchannel" })
	if n := calls.Load(); n != 1 {
		t.Fatalf("want one lookup for repeated misses, got %d", n)
	}
}

func TestRoomNameCacheRetriesFailuresLater(t *testing.T) {
	c := newRoomNameCache()
	now := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return now }
	var calls atomic.Int32
	c.setResolver(func(string) string { calls.Add(1); return "" })
	c.name("9")
	waitFor(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return !c.pending["9"] })
	if got := c.name("9"); got != "9" || calls.Load() != 1 {
		t.Fatalf("a failure must not be cached as the name nor retried at once (got %q, %d calls)", got, calls.Load())
	}
	c.mu.Lock()
	now = now.Add(roomRetryAfter + time.Second)
	c.mu.Unlock()
	c.name("9")
	waitFor(t, func() bool { return calls.Load() == 2 })
}

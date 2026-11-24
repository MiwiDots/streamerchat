package twitch

import (
	"sync"
	"time"
)

// roomRetryAfter is how long a failed room-name lookup waits before it is
// tried again (Helix down, not logged in yet).
const roomRetryAfter = time.Minute

// roomNameCache maps Shared Chat room IDs to channel names. Lookups never
// block the caller: a miss starts one background resolve per room and
// returns the room ID until the name is known. The IRC message handler
// calls this, and blocking it on an HTTP request would also hold up the
// connection's PING/PONG.
type roomNameCache struct {
	mu       sync.Mutex
	names    map[string]string
	pending  map[string]bool
	failedAt map[string]time.Time
	resolve  func(roomID string) string // "" = not resolvable right now
	now      func() time.Time
}

func newRoomNameCache() *roomNameCache {
	return &roomNameCache{
		names:    map[string]string{},
		pending:  map[string]bool{},
		failedAt: map[string]time.Time{},
		now:      time.Now,
	}
}

func (c *roomNameCache) setResolver(fn func(roomID string) string) {
	c.mu.Lock()
	c.resolve = fn
	c.mu.Unlock()
}

// name returns the channel name for roomID, or roomID itself while it is
// unknown.
func (c *roomNameCache) name(roomID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n, ok := c.names[roomID]; ok {
		return n
	}
	if c.resolve == nil || c.pending[roomID] {
		return roomID
	}
	if t, failed := c.failedAt[roomID]; failed && c.now().Sub(t) < roomRetryAfter {
		return roomID
	}
	c.pending[roomID] = true
	resolve := c.resolve
	go func() {
		n := resolve(roomID)
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.pending, roomID)
		if n == "" {
			c.failedAt[roomID] = c.now()
			return
		}
		delete(c.failedAt, roomID)
		c.names[roomID] = n
	}()
	return roomID
}

package chat

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// StatsTopN is how many chatters the stats dashboard ranks.
const StatsTopN = 5

// ChatterCount is one chatter's message count in the current session.
type ChatterCount struct {
	Platform    Platform `json:"platform"`
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	UserID      string   `json:"userId"`
	Messages    int      `json:"messages"`

	// lastAt is when the chatter reached their current count. Ties in
	// the ranking go to whoever got there first.
	lastAt time.Time
}

// StatsSnapshot is what the dashboard shows. Ignored users (bots) are
// left out of every number except IgnoredMessages.
type StatsSnapshot struct {
	Since           int64          `json:"since"` // unix ms of the last reset
	Messages        int            `json:"messages"`
	Chatters        int            `json:"chatters"`
	IgnoredMessages int            `json:"ignoredMessages"`
	Top             []ChatterCount `json:"top"`
}

// ChatStats counts chat messages per chatter for the stats dashboard.
//
// Every chatter is counted, ignored or not: the ignore filter is applied
// when a snapshot is taken, so editing the bot list takes effect
// retroactively instead of only for messages that arrive afterwards.
type ChatStats struct {
	mu     sync.Mutex
	since  time.Time
	counts map[string]*ChatterCount // key: platform + ":" + lowercase username
	now    func() time.Time
}

// NewChatStats returns empty stats starting now.
func NewChatStats() *ChatStats {
	s := &ChatStats{now: time.Now}
	s.Reset()
	return s
}

// Reset clears all counts and restarts the session clock.
func (s *ChatStats) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.since = s.now()
	s.counts = map[string]*ChatterCount{}
}

// Countable reports whether msg is a viewer's chat message: plain chat
// only (no subs, redemptions, system lines), not the broadcaster, and not
// a message mirrored in from another channel's Shared Chat.
func Countable(msg Message) bool {
	return msg.Type == MessageTypeChat &&
		msg.Username != "" &&
		!msg.IsBroadcaster &&
		!msg.IsSharedChat
}

// Record counts msg if it is Countable.
func (s *ChatStats) Record(msg Message) {
	if !Countable(msg) {
		return
	}
	key := string(msg.Platform) + ":" + NormalizeChatterName(msg.Username)
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.counts[key]
	if !ok {
		c = &ChatterCount{Platform: msg.Platform, Username: msg.Username}
		s.counts[key] = c
	}
	c.Messages++
	c.lastAt = s.now()
	// Keep the newest display name / id (a rename mid-stream shows the new one).
	if msg.DisplayName != "" {
		c.DisplayName = msg.DisplayName
	}
	if msg.UserID != "" {
		c.UserID = msg.UserID
	}
}

// Snapshot ranks the top n chatters. ignored reports chatters to leave
// out (bots); nil ignores nobody.
func (s *ChatStats) Snapshot(n int, ignored func(Platform, string) bool) StatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := StatsSnapshot{Since: s.since.UnixMilli(), Top: []ChatterCount{}}
	ranked := make([]ChatterCount, 0, len(s.counts))
	for _, c := range s.counts {
		if ignored != nil && ignored(c.Platform, c.Username) {
			snap.IgnoredMessages += c.Messages
			continue
		}
		snap.Messages += c.Messages
		snap.Chatters++
		ranked = append(ranked, *c)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Messages != ranked[j].Messages {
			return ranked[i].Messages > ranked[j].Messages
		}
		return ranked[i].lastAt.Before(ranked[j].lastAt)
	})
	if n > 0 && len(ranked) > n {
		ranked = ranked[:n]
	}
	snap.Top = append(snap.Top, ranked...)
	return snap
}

// NormalizeChatterName lowercases a username and drops a leading "@"
// (YouTube handles), so "@Nightbot" and "nightbot" match.
func NormalizeChatterName(name string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "@"))
}

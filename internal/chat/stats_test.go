package chat

import (
	"encoding/json"
	"testing"
	"time"
)

// fakeClock makes "reached the count first" deterministic.
func fakeClock(s *ChatStats) {
	t := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { t = t.Add(time.Second); return t }
}

func msg(user string) Message {
	return Message{Type: MessageTypeChat, Platform: PlatformTwitch, Username: user, DisplayName: user}
}

func record(s *ChatStats, user string, n int) {
	for i := 0; i < n; i++ {
		s.Record(msg(user))
	}
}

func TestStatsRanksTopChattersAndSkipsIgnored(t *testing.T) {
	s := NewChatStats()
	fakeClock(s)
	record(s, "Nightbot", 50)
	record(s, "alice", 3)
	record(s, "bob", 7)
	record(s, "carol", 3) // ties with alice but reached 3 later
	record(s, "dave", 1)
	record(s, "eve", 2)
	record(s, "frank", 1)

	snap := s.Snapshot(5, func(_ Platform, u string) bool { return NormalizeChatterName(u) == "nightbot" })

	want := []string{"bob", "alice", "carol", "eve", "dave"}
	if len(snap.Top) != len(want) {
		t.Fatalf("top = %+v, want %v", snap.Top, want)
	}
	for i, w := range want {
		if snap.Top[i].Username != w {
			t.Errorf("rank %d = %s, want %s (top %+v)", i+1, snap.Top[i].Username, w, snap.Top)
		}
	}
	if snap.Messages != 17 || snap.Chatters != 6 || snap.IgnoredMessages != 50 {
		t.Errorf("totals = %d msgs / %d chatters / %d ignored, want 17/6/50", snap.Messages, snap.Chatters, snap.IgnoredMessages)
	}
}

func TestStatsCountsOnlyViewerChat(t *testing.T) {
	s := NewChatStats()
	sub := msg("subber")
	sub.Type = MessageTypeSub
	host := msg("streamer")
	host.IsBroadcaster = true
	shared := msg("guest")
	shared.IsSharedChat = true
	for _, m := range []Message{sub, host, shared, {Type: MessageTypeSystem}, msg("viewer")} {
		s.Record(m)
	}
	snap := s.Snapshot(5, nil)
	if snap.Messages != 1 || len(snap.Top) != 1 || snap.Top[0].Username != "viewer" {
		t.Fatalf("only the viewer's chat line should count, got %+v", snap)
	}
}

func TestStatsMergesNameCaseAndKeepsPlatformsApart(t *testing.T) {
	s := NewChatStats()
	s.Record(msg("Alice"))
	s.Record(msg("alice"))
	yt := msg("alice")
	yt.Platform = PlatformYouTube
	s.Record(yt)
	snap := s.Snapshot(5, nil)
	if snap.Chatters != 2 || snap.Top[0].Messages != 2 || snap.Top[0].Platform != PlatformTwitch {
		t.Fatalf("want twitch alice=2 and youtube alice=1, got %+v", snap.Top)
	}
}

func TestStatsIgnoreIsRetroactiveAndResetClears(t *testing.T) {
	s := NewChatStats()
	record(s, "stammtischbot", 4)
	record(s, "viewer", 1)
	if top := s.Snapshot(5, nil).Top; top[0].Username != "stammtischbot" {
		t.Fatalf("without a filter the bot ranks first, got %+v", top)
	}
	snap := s.Snapshot(5, func(_ Platform, u string) bool { return u == "stammtischbot" })
	if len(snap.Top) != 1 || snap.Top[0].Username != "viewer" {
		t.Fatalf("adding the bot later must hide its earlier messages, got %+v", snap.Top)
	}
	s.Reset()
	if snap := s.Snapshot(5, nil); snap.Messages != 0 || len(snap.Top) != 0 || snap.Top == nil {
		t.Fatalf("after reset want empty non-nil top, got %+v", snap)
	}
}

// The stats pane reads these JSON keys; renaming one breaks it silently.
func TestStatsSnapshotJSONFields(t *testing.T) {
	s := NewChatStats()
	m := msg("alice")
	m.UserID = "42"
	s.Record(m)
	raw, _ := json.Marshal(s.Snapshot(5, nil))
	var got map[string]interface{}
	json.Unmarshal(raw, &got)
	for _, k := range []string{"since", "messages", "chatters", "ignoredMessages", "top"} {
		if _, ok := got[k]; !ok {
			t.Errorf("snapshot JSON lacks %q: %s", k, raw)
		}
	}
	row := got["top"].([]interface{})[0].(map[string]interface{})
	for _, k := range []string{"platform", "username", "displayName", "userId", "messages"} {
		if _, ok := row[k]; !ok {
			t.Errorf("top row JSON lacks %q: %s", k, raw)
		}
	}
}

package eventsub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{}

func welcome(id string) string {
	return fmt.Sprintf(`{"metadata":{"message_type":"session_welcome"},"payload":{"session":{"id":%q}}}`, id)
}

func follow(msgID, user string) string {
	return fmt.Sprintf(`{"metadata":{"message_id":%q,"message_type":"notification"},`+
		`"payload":{"subscription":{"type":"channel.follow"},"event":{"user_name":%q,"user_id":"1"}}}`, msgID, user)
}

func reconnect(url string) string {
	return fmt.Sprintf(`{"metadata":{"message_type":"session_reconnect"},"payload":{"session":{"reconnect_url":%q}}}`, url)
}

// wsServer serves one scripted connection: it writes msgs in order, then
// holds the socket open until hold elapses.
func wsServer(t *testing.T, msgs func() []string, hold time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for _, m := range msgs() {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(m)); err != nil {
				return
			}
		}
		time.Sleep(hold)
	}))
}

func wsURL(s *httptest.Server) string { return "ws" + strings.TrimPrefix(s.URL, "http") }

// session_reconnect must: keep events from the old socket, continue on the
// new one, NOT re-subscribe, and drop the duplicate delivered on both.
func TestReconnectHandoffNoResubscribeNoDuplicates(t *testing.T) {
	var subscribeCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subscribeCalls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer api.Close()

	newSrv := wsServer(t, func() []string {
		return []string{welcome("s2"), follow("dup", "bob"), follow("m3", "carol")}
	}, 5*time.Second)
	defer newSrv.Close()
	oldSrv := wsServer(t, func() []string {
		// "dup" also arrives on the old socket during the handoff.
		return []string{welcome("s1"), follow("m1", "alice"), reconnect(wsURL(newSrv)), follow("dup", "bob")}
	}, 200*time.Millisecond)
	defer oldSrv.Close()

	defaultWSURL, subscriptionsURL = wsURL(oldSrv), api.URL
	t.Cleanup(func() {
		defaultWSURL = "wss://eventsub.wss.twitch.tv/ws"
		subscriptionsURL = "https://api.twitch.tv/helix/eventsub/subscriptions"
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var users []string
	got3 := make(chan struct{})
	c := New(ctx, "cid", "tok", "1", "1", func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		users = append(users, ev.User)
		if len(users) == 3 {
			close(got3)
		}
	})
	go c.Start()

	select {
	case <-got3:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for 3 events")
	}
	time.Sleep(300 * time.Millisecond) // give a duplicate the chance to show up
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(users) != 3 {
		t.Fatalf("want 3 unique events, got %v", users)
	}
	seen := map[string]bool{}
	for _, u := range users {
		seen[u] = true
	}
	for _, want := range []string{"alice", "bob", "carol"} {
		if !seen[want] {
			t.Fatalf("missing %s in %v", want, users)
		}
	}
	// Every subscription type exactly once (not again after reconnect).
	if n, want := subscribeCalls.Load(), int32(len(subscriptionRequests("1", "1"))); n != want {
		t.Fatalf("subscribe calls = %d, want %d", n, want)
	}
}

func TestSetAccessTokenUsedForSubscribe(t *testing.T) {
	var gotAuth atomic.Value
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer api.Close()
	subscriptionsURL = api.URL
	t.Cleanup(func() { subscriptionsURL = "https://api.twitch.tv/helix/eventsub/subscriptions" })

	c := New(context.Background(), "cid", "old", "1", "1", func(Event) {})
	c.SetAccessToken("fresh")
	if err := c.subscribe("sid", subscribeRequest{Type: "channel.follow", Version: "2"}); err != nil {
		t.Fatal(err)
	}
	if got := gotAuth.Load(); got != "Bearer fresh" {
		t.Fatalf("Authorization = %v, want Bearer fresh", got)
	}
}

func TestDispatchAutoModEvents(t *testing.T) {
	var got []Event
	c := New(context.Background(), "cid", "tok", "1", "1", func(ev Event) { got = append(got, ev) })
	hold := `{"metadata":{"message_id":"n1","message_type":"notification"},"payload":{"subscription":{"type":"automod.message.hold"},` +
		`"event":{"user_id":"9","user_login":"troll99","user_name":"Troll99","message_id":"m1","message":{"text":"bad words","fragments":[]},` +
		`"reason":"automod","automod":{"category":"swearing","level":3,"boundaries":[]},"blocked_term":null,"held_at":"2026-11-15T10:00:00Z"}}}`
	update := `{"metadata":{"message_id":"n2","message_type":"notification"},"payload":{"subscription":{"type":"automod.message.update"},` +
		`"event":{"user_id":"9","user_login":"troll99","user_name":"Troll99","message_id":"m1","message":{"text":"bad words"},` +
		`"moderator_user_name":"ModAnna","status":"Approved","reason":"automod"}}}`
	for _, raw := range []string{hold, update} {
		var msg wsMessage
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			t.Fatal(err)
		}
		c.dispatchOnce(msg)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events", len(got))
	}
	h := got[0]
	if h.Type != EventAutoModHold || h.User != "Troll99" || h.Message != "bad words" ||
		h.Meta["messageId"] != "m1" || h.Meta["category"] != "swearing" || h.Meta["level"] != float64(3) || h.Meta["reason"] != "automod" {
		t.Fatalf("hold parsed wrong: %+v", h)
	}
	u := got[1]
	if u.Type != EventAutoModUpdate || u.Meta["status"] != "approved" || u.Meta["moderator"] != "ModAnna" || u.Meta["messageId"] != "m1" {
		t.Fatalf("update parsed wrong: %+v", u)
	}
}

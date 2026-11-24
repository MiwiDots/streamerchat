package sevenTV

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLookupUserBoundsConcurrencyAndBacksOff(t *testing.T) {
	var inFlight, maxSeen, calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n := inFlight.Add(1)
		for {
			m := maxSeen.Load()
			if n <= m || maxSeen.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		w.WriteHeader(http.StatusBadGateway) // 7TV outage
	}))
	defer srv.Close()
	old := gqlURL
	gqlURL = srv.URL
	defer func() { gqlURL = old }()

	c := NewClient(context.Background(), func(Cosmetic) {})
	const users = 20
	for i := 0; i < users; i++ {
		c.LookupUser(fmt.Sprint(i))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		left := len(c.pending)
		c.mu.Unlock()
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lookups did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if m := maxSeen.Load(); m > maxConcurrentLookups {
		t.Fatalf("max concurrent requests = %d, want <= %d", m, maxConcurrentLookups)
	}
	before := calls.Load()
	for i := 0; i < users; i++ {
		c.LookupUser(fmt.Sprint(i)) // more messages from the same users during the outage
	}
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != before {
		t.Fatalf("failed users were retried right away (%d -> %d calls)", before, calls.Load())
	}
}

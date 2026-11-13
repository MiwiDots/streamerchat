package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/miwi/streamerchat/internal/config"
	"github.com/miwi/streamerchat/internal/twitch"
)

// newTestApp returns an App with an in-memory config; uiReady stays false
// so emit only buffers (no Wails runtime needed).
func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Profiles = []config.Profile{{ID: "default", Name: "Default"}}
	cfg.ActiveProfileID = "default"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &App{ctx: ctx, cfg: cfg}
}

func fakeAuth(t *testing.T, validate func(string) (*twitch.ValidateResponse, error)) {
	t.Helper()
	pv, pr := validateToken, refreshAccessToken
	validateToken = validate
	refreshAccessToken = func(_, _, _ string) (*twitch.TokenResponse, error) {
		t.Fatal("refresh must not be called in this test")
		return nil, nil
	}
	t.Cleanup(func() { validateToken, refreshAccessToken = pv, pr })
}

// The reported bug: no network at startup was treated as "login expired"
// and chat was never connected. Transient errors must be retried.
func TestAuthenticateRetriesTransientErrors(t *testing.T) {
	a := newTestApp(t)
	calls := 0
	fakeAuth(t, func(string) (*twitch.ValidateResponse, error) {
		calls++
		if calls < 3 {
			return nil, fmt.Errorf("validate token: dial tcp: no such host")
		}
		return &twitch.ValidateResponse{Login: "miwi", UserID: "1"}, nil
	})
	start := time.Now()
	info, _, outcome := a.authenticate("default", "cid", "", "at", "")
	if outcome != authOK || info == nil || info.Login != "miwi" {
		t.Fatalf("outcome=%v info=%+v", outcome, info)
	}
	if calls != 3 {
		t.Fatalf("validate calls = %d, want 3", calls)
	}
	if time.Since(start) < authRetryMin {
		t.Fatal("retries must back off")
	}
	// The user is told what's going on (buffered until the UI is ready).
	if len(a.uiPending) < 2 {
		t.Fatalf("expected waiting + recovered notices, got %d events", len(a.uiPending))
	}
}

func TestAuthenticateRejectedWithoutRefreshIsExpired(t *testing.T) {
	a := newTestApp(t)
	fakeAuth(t, func(string) (*twitch.ValidateResponse, error) {
		return nil, fmt.Errorf("validate token: %w (status 401)", twitch.ErrTokenRejected)
	})
	if _, _, outcome := a.authenticate("default", "cid", "", "at", ""); outcome != authExpired {
		t.Fatalf("outcome = %v, want authExpired", outcome)
	}
}

func TestAuthenticateStopsWhenAppCloses(t *testing.T) {
	a := newTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	a.ctx = ctx
	fakeAuth(t, func(string) (*twitch.ValidateResponse, error) {
		return nil, errors.New("network down")
	})
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	done := make(chan authOutcome, 1)
	go func() { _, _, o := a.authenticate("default", "cid", "", "at", ""); done <- o }()
	select {
	case o := <-done:
		if o != authStale {
			t.Fatalf("outcome = %v, want authStale", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("authenticate kept retrying after the app context was cancelled")
	}
}

func TestEmitBuffersUntilFrontendReady(t *testing.T) {
	a := newTestApp(t)
	for i := 0; i < maxBufferedUIEvents+5; i++ {
		a.emit("chat", i)
	}
	if len(a.uiPending) != maxBufferedUIEvents {
		t.Fatalf("buffer len = %d, want cap %d", len(a.uiPending), maxBufferedUIEvents)
	}
	if a.uiPending[0].data != 5 {
		t.Fatalf("oldest events must be dropped first, got %v", a.uiPending[0].data)
	}
}

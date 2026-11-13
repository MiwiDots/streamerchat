package youtube

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miwi/streamerchat/internal/chat"
)

// emit must give up when ctx is done even if nobody reads and the buffer
// is full — otherwise the poll goroutine leaks on every teardown.
func TestEmitDoesNotBlockAfterCancel(t *testing.T) {
	c := NewInnerTubeClient("vid")
	ctx, cancel := context.WithCancel(context.Background())
	for i := 0; i < cap(c.messages); i++ {
		if !c.emit(ctx, chat.Message{}) {
			t.Fatal("emit into free buffer failed")
		}
	}
	cancel()
	done := make(chan bool, 1)
	go func() { done <- c.emit(ctx, chat.Message{}) }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("emit reported delivery into a full buffer after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("emit blocked after ctx cancel")
	}
}

func TestEmitErrNeverBlocks(t *testing.T) {
	c := NewInnerTubeClient("vid")
	done := make(chan struct{})
	go func() {
		for i := 0; i < cap(c.errors)+10; i++ {
			c.emitErr(errors.New("x"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("emitErr blocked on a full error buffer")
	}
}

func TestConnectWithRetryStopsOnCancel(t *testing.T) {
	c := NewInnerTubeClient("vid")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: must return immediately, no network wait loop
	done := make(chan error, 1)
	go func() { done <- c.ConnectWithRetry(ctx, nil) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ConnectWithRetry did not stop on cancelled ctx")
	}
}

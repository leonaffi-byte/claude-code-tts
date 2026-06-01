package relay

import (
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// SSEHub — London-school (mock-first) tests
//
// These tests define the expected contract for SSEHub before the real
// implementation exists. They are expected to FAIL until sse.go is fully
// implemented.
// ---------------------------------------------------------------------------

// TestSSEHub_NewHub_ZeroSubscribers verifies that a freshly created hub has no
// subscribers.
func TestSSEHub_NewHub_ZeroSubscribers(t *testing.T) {
	hub := NewSSEHub()
	if got := hub.Count(); got != 0 {
		t.Errorf("expected 0 subscribers on new hub, got %d", got)
	}
}

// TestSSEHub_Subscribe_CountBecomesOne verifies that Count() returns 1 after a
// single Subscribe() call.
func TestSSEHub_Subscribe_CountBecomesOne(t *testing.T) {
	hub := NewSSEHub()
	_, _, cancel := hub.Subscribe()
	defer cancel()

	if got := hub.Count(); got != 1 {
		t.Errorf("expected 1 subscriber after Subscribe(), got %d", got)
	}
}

// TestSSEHub_Subscribe_ReturnsNonNilChannelAndCancel verifies that Subscribe
// returns a non-empty ID, a non-nil channel, and a non-nil cancel function.
func TestSSEHub_Subscribe_ReturnsNonNilChannelAndCancel(t *testing.T) {
	hub := NewSSEHub()
	id, ch, cancel := hub.Subscribe()
	defer cancel()

	if id == "" {
		t.Error("expected non-empty subscriber ID from Subscribe()")
	}
	if ch == nil {
		t.Error("expected non-nil channel from Subscribe()")
	}
	if cancel == nil {
		t.Error("expected non-nil cancel func from Subscribe()")
	}
}

// TestSSEHub_Broadcast_DeliversFormattedSSEMessage verifies that Broadcast
// sends the correctly formatted SSE message to the subscriber's channel.
//
// The SSE wire format for event="new-clip", data=`{"id":"abc"}` must be:
//
//	"event: new-clip\ndata: {\"id\":\"abc\"}\n\n"
func TestSSEHub_Broadcast_DeliversFormattedSSEMessage(t *testing.T) {
	hub := NewSSEHub()
	_, ch, cancel := hub.Subscribe()
	defer cancel()

	hub.Broadcast("new-clip", `{"id":"abc"}`)

	select {
	case msg := <-ch:
		want := "event: new-clip\ndata: {\"id\":\"abc\"}\n\n"
		if msg != want {
			t.Errorf("SSE message format wrong:\n got  %q\nwant %q", msg, want)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timed out waiting for SSE message on subscriber channel")
	}
}

// TestSSEHub_Cancel_RemovesSubscriber verifies that calling the cancel function
// returned by Subscribe() removes the subscriber so that Count() drops to 0.
func TestSSEHub_Cancel_RemovesSubscriber(t *testing.T) {
	hub := NewSSEHub()
	_, _, cancel := hub.Subscribe()

	cancel()

	// Allow a brief moment for the removal to propagate if it is async.
	time.Sleep(10 * time.Millisecond)

	if got := hub.Count(); got != 0 {
		t.Errorf("expected 0 subscribers after cancel(), got %d", got)
	}
}

// TestSSEHub_BroadcastToZeroSubscribers_NoPanic verifies that broadcasting
// when there are no subscribers does not panic.
func TestSSEHub_BroadcastToZeroSubscribers_NoPanic(t *testing.T) {
	hub := NewSSEHub()
	// Must not panic.
	hub.Broadcast("new-clip", `{"id":"xyz"}`)
}

// TestSSEHub_Broadcast_DeliverToAllSubscribers verifies that a single
// Broadcast call delivers the message to every connected subscriber.
func TestSSEHub_Broadcast_DeliverToAllSubscribers(t *testing.T) {
	const subscriberCount = 3
	hub := NewSSEHub()

	channels := make([]<-chan string, subscriberCount)
	cancels := make([]func(), subscriberCount)
	for i := range subscriberCount {
		_, ch, cancel := hub.Subscribe()
		channels[i] = ch
		cancels[i] = cancel
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	hub.Broadcast("new-clip", `{"id":"multi"}`)

	for i, ch := range channels {
		select {
		case msg := <-ch:
			want := "event: new-clip\ndata: {\"id\":\"multi\"}\n\n"
			if msg != want {
				t.Errorf("subscriber %d: got %q, want %q", i, msg, want)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("subscriber %d: timed out waiting for SSE message", i)
		}
	}
}

// TestSSEHub_Subscribe_UniqueIDs verifies that two Subscribe calls return
// different IDs.
func TestSSEHub_Subscribe_UniqueIDs(t *testing.T) {
	hub := NewSSEHub()
	id1, _, cancel1 := hub.Subscribe()
	id2, _, cancel2 := hub.Subscribe()
	defer cancel1()
	defer cancel2()

	if id1 == id2 {
		t.Errorf("expected unique IDs from two Subscribe() calls, both returned %q", id1)
	}
}

// TestSSEHub_ConcurrentSubscribeBroadcast verifies that concurrent Subscribe
// and Broadcast calls do not cause a data race or panic. This test is run
// under the -race detector during CI.
func TestSSEHub_ConcurrentSubscribeBroadcast(t *testing.T) {
	hub := NewSSEHub()

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		got   []string
	)

	// Subscriber goroutine: subscribes and collects one message.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, ch, cancel := hub.Subscribe()
		defer cancel()
		select {
		case msg := <-ch:
			mu.Lock()
			got = append(got, msg)
			mu.Unlock()
		case <-time.After(200 * time.Millisecond):
			// It is acceptable not to receive if the broadcast races the subscribe.
		}
	}()

	// Give the goroutine a moment to subscribe before broadcasting.
	time.Sleep(5 * time.Millisecond)
	hub.Broadcast("new-clip", `{"id":"race"}`)
	wg.Wait()
	// No assertion on got — the test verifies absence of data races / panics.
}

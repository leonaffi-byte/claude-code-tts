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
		wg  sync.WaitGroup
		mu  sync.Mutex
		got []string
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

	// Concrete post-condition (in addition to -race): the subscriber goroutine
	// cancels on exit, so the hub must be empty, and at most one message may
	// have been collected (zero if the broadcast raced ahead of the subscribe).
	if c := hub.Count(); c != 0 {
		t.Errorf("expected 0 subscribers after the goroutine cancelled, got %d", c)
	}
	mu.Lock()
	if len(got) > 1 {
		t.Errorf("collected %d messages; a single subscriber+broadcast can yield at most 1", len(got))
	}
	mu.Unlock()
}

// ---------------------------------------------------------------------------
// Edge cases — gap-fill tests
// ---------------------------------------------------------------------------

// TestSSEHub_DoubleCancel_NoPanic verifies that calling the cancel function
// returned by Subscribe() a second time does not panic. The implementation
// must guard against the channel already being closed.
func TestSSEHub_DoubleCancel_NoPanic(t *testing.T) {
	hub := NewSSEHub()
	_, _, cancel := hub.Subscribe()

	cancel() // first call — removes subscriber and closes channel
	// Must not panic on second call.
	cancel()
}

// TestSSEHub_Broadcast_SlowSubscriber_ReturnsPromptly verifies that Broadcast
// returns quickly even when a subscriber's channel is full. We saturate the
// hub by broadcasting more messages than the channel capacity (4) without
// reading from the channel, then verify the next Broadcast completes within
// a generous 100 ms budget.
func TestSSEHub_Broadcast_SlowSubscriber_ReturnsPromptly(t *testing.T) {
	hub := NewSSEHub()
	_, _, cancel := hub.Subscribe()
	defer cancel()

	// Send enough broadcasts to fill the channel (capacity 4) without a reader.
	// The non-blocking select in Broadcast will drop messages on a full channel.
	// This ensures the channel is at capacity before the timed assertion below.
	for i := 0; i < 4; i++ {
		hub.Broadcast("new-clip", `{"id":"fill"}`)
	}

	// Now the subscriber channel is at capacity. Another Broadcast must not block.
	done := make(chan struct{})
	go func() {
		hub.Broadcast("new-clip", `{"id":"slow"}`)
		close(done)
	}()

	select {
	case <-done:
		// Broadcast returned promptly — pass.
	case <-time.After(100 * time.Millisecond):
		t.Error("Broadcast blocked for >100 ms on a full subscriber channel")
	}
}

// TestSSEHub_ConcurrentUnsubscribeBeforeBroadcast verifies that cancelling all
// subscribers and then calling Broadcast does not race or panic. This covers
// the common real-world scenario where clients disconnect just before a
// broadcast: after all cancels complete the hub is empty and Broadcast must be
// a no-op. Run under -race.
func TestSSEHub_ConcurrentUnsubscribeBeforeBroadcast(t *testing.T) {
	hub := NewSSEHub()

	const numSubscribers = 5
	var wg sync.WaitGroup
	for range numSubscribers {
		_, _, cancel := hub.Subscribe()
		wg.Add(1)
		go func() {
			defer wg.Done()
			cancel()
		}()
	}

	// Wait for all cancels to complete before broadcasting.
	wg.Wait()

	// After all subscribers have cancelled, Broadcast must not panic.
	hub.Broadcast("new-clip", `{"id":"after-cancel"}`)

	if got := hub.Count(); got != 0 {
		t.Errorf("expected 0 subscribers after all cancels, got %d", got)
	}
}

// TestSSEHub_ConcurrentMultipleBroadcasts verifies that multiple concurrent
// Broadcast calls do not cause a data race. Subscribers are registered before
// any broadcast starts and remain connected throughout. Run under -race.
func TestSSEHub_ConcurrentMultipleBroadcasts(t *testing.T) {
	hub := NewSSEHub()

	const numSubscribers = 3
	cancels := make([]func(), numSubscribers)
	for i := range numSubscribers {
		_, _, cancel := hub.Subscribe()
		cancels[i] = cancel
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hub.Broadcast("new-clip", `{"id":"parallel"}`)
		}()
	}
	wg.Wait()

	// Concrete post-condition (in addition to -race): all subscribers remain
	// connected (they are cancelled only by the deferred cleanup), so the count
	// must still equal numSubscribers after the concurrent broadcasts.
	if got := hub.Count(); got != numSubscribers {
		t.Errorf("expected %d subscribers after concurrent broadcasts, got %d", numSubscribers, got)
	}
}

// ---------------------------------------------------------------------------
// Subscriber cap tests
// ---------------------------------------------------------------------------

// TestSSEHub_MaxSubs_RejectsWhenFull verifies that Subscribe returns a nil
// channel once the hub has reached its maxSubs limit, and that Count does not
// exceed maxSubs.
func TestSSEHub_MaxSubs_RejectsWhenFull(t *testing.T) {
	hub := &SSEHub{
		subscribers: make(map[string]chan string),
		maxSubs:     3,
	}

	var cancels []func()
	for i := 0; i < 3; i++ {
		_, ch, cancel := hub.Subscribe()
		if ch == nil {
			t.Fatalf("subscription %d: expected non-nil channel below cap, got nil", i)
		}
		cancels = append(cancels, cancel)
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	// The 4th subscription must be rejected.
	_, ch, cancel := hub.Subscribe()
	defer cancel()
	if ch != nil {
		t.Error("expected nil channel when subscriber cap is reached, got non-nil")
	}

	if got := hub.Count(); got != 3 {
		t.Errorf("expected Count() == 3 at cap, got %d", got)
	}
}

// TestSSEHub_MaxSubs_Zero_NoLimit verifies that a maxSubs of 0 is treated as
// unlimited (no rejection).
func TestSSEHub_MaxSubs_Zero_NoLimit(t *testing.T) {
	hub := &SSEHub{
		subscribers: make(map[string]chan string),
		maxSubs:     0,
	}

	var cancels []func()
	for i := 0; i < 10; i++ {
		_, ch, cancel := hub.Subscribe()
		if ch == nil {
			t.Fatalf("subscription %d: expected non-nil channel with unlimited cap, got nil", i)
		}
		cancels = append(cancels, cancel)
	}
	for _, c := range cancels {
		c()
	}
}

package relay

// ---------------------------------------------------------------------------
// Presence tracking — London School TDD
//
// These tests verify the contract for the presence tracking subsystem.
// The PresenceTracker interface is the seam we test against. The SSEHub is the
// real implementation; in these unit tests we use the live hub directly to
// verify that subscribing/unsubscribing drives the tracker correctly.
// ---------------------------------------------------------------------------

import (
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// PresenceTracker interface contract — unit tests against SSEHub as tracker
//
// The SSEHub must satisfy the PresenceTracker interface so handler.go can
// call HasLiveListener() to decide whether to suppress push.
// ---------------------------------------------------------------------------

// TestSSEHub_HasLiveListener_FalseWhenNoSubscribers verifies that a hub with
// no subscribers reports HasLiveListener() == false. This is the base state
// that permits push delivery when the companion is not open.
func TestSSEHub_HasLiveListener_FalseWhenNoSubscribers(t *testing.T) {
	hub := NewSSEHub()

	// HasLiveListener must exist on SSEHub and return false when hub is empty.
	// This will fail until SSEHub implements the PresenceTracker interface.
	if hub.HasLiveListener() {
		t.Error("HasLiveListener() = true on empty hub; want false — push should be sent when no listener is connected")
	}
}

// TestSSEHub_HasLiveListener_TrueAfterSubscribe verifies that HasLiveListener()
// returns true once a client subscribes via SSE. This is the signal that the
// companion is open and the clip will auto-play via the event stream.
func TestSSEHub_HasLiveListener_TrueAfterSubscribe(t *testing.T) {
	hub := NewSSEHub()
	_, _, cancel := hub.Subscribe()
	defer cancel()

	if !hub.HasLiveListener() {
		t.Error("HasLiveListener() = false after Subscribe(); want true — push should be suppressed when a listener is connected")
	}
}

// TestSSEHub_HasLiveListener_FalseAfterAllSubscribersCancel verifies that
// HasLiveListener() returns false once every subscriber cancels. Once the
// companion closes the SSE connection, push delivery should resume.
func TestSSEHub_HasLiveListener_FalseAfterAllSubscribersCancel(t *testing.T) {
	hub := NewSSEHub()
	_, _, cancel1 := hub.Subscribe()
	_, _, cancel2 := hub.Subscribe()

	cancel1()
	cancel2()

	if hub.HasLiveListener() {
		t.Error("HasLiveListener() = true after all subscribers cancelled; want false — push should be sent when no listener remains")
	}
}

// TestSSEHub_HasLiveListener_TrueWithMixedSubscribers verifies that
// HasLiveListener() returns true as long as at least one subscriber remains,
// even after others have cancelled. Push suppression must stay active while
// any foreground listener is connected.
func TestSSEHub_HasLiveListener_TrueWithMixedSubscribers(t *testing.T) {
	hub := NewSSEHub()
	_, _, cancel1 := hub.Subscribe()
	_, _, cancel2 := hub.Subscribe()

	// Cancel one but keep the other.
	cancel1()

	if !hub.HasLiveListener() {
		t.Error("HasLiveListener() = false with one active subscriber remaining; want true — push must be suppressed while any listener is connected")
	}

	cancel2()
}

// TestSSEHub_HasLiveListener_ConcurrentSafety verifies that concurrent
// Subscribe/cancel calls do not race when HasLiveListener() is called
// simultaneously. Run with -race to detect data races.
func TestSSEHub_HasLiveListener_ConcurrentSafety(t *testing.T) {
	hub := NewSSEHub()

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, cancel := hub.Subscribe()
			// Immediately query presence while concurrent subscribes/cancels run.
			_ = hub.HasLiveListener()
			cancel()
		}()
	}
	wg.Wait()

	// After all goroutines finish, hub should be empty.
	if hub.HasLiveListener() {
		t.Error("HasLiveListener() = true after all goroutines cancelled; want false")
	}
}

// TestSSEHub_ImplementsPresenceTracker verifies that *SSEHub satisfies the
// PresenceTracker interface at compile time. This test fails to compile (and
// thus fails at 'go test') until SSEHub implements HasLiveListener().
func TestSSEHub_ImplementsPresenceTracker(t *testing.T) {
	hub := NewSSEHub()

	// Assign hub to a PresenceTracker interface variable.
	// This is a compile-time assertion: if SSEHub does not implement
	// PresenceTracker, the assignment fails to compile.
	var _ PresenceTracker = hub
	_ = hub // suppress unused variable warning
}

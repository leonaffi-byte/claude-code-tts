package relay

// ---------------------------------------------------------------------------
// Push suppression — London School TDD
//
// These tests verify the contract for presence-driven push suppression.
// The handler must suppress push when a live SSE listener is connected
// (the clip auto-plays via SSE), and send push when no listener is present.
//
// London School approach: we mock the PresenceTracker so these tests are
// pure unit tests of the handler's decision logic, isolated from the real
// SSEHub. The SSEHub's own PresenceTracker conformance is covered in
// presence_test.go. Full integration is tested in presence_gaps_test.go.
// ---------------------------------------------------------------------------

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// mockPresenceTracker — defines the contract the handler depends on
// ---------------------------------------------------------------------------

// mockPresenceTracker is a test double for PresenceTracker that allows tests
// to simulate a live or absent foreground listener without a real SSEHub.
type mockPresenceTracker struct {
	liveListener bool
}

func (m *mockPresenceTracker) HasLiveListener() bool {
	return m.liveListener
}

// ---------------------------------------------------------------------------
// Helper: build an ingest request
// ---------------------------------------------------------------------------

func newIngestRequest(t *testing.T, text string) *http.Request {
	t.Helper()
	body := bytes.NewBufferString(`{"text": "` + text + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ---------------------------------------------------------------------------
// Suppression tests
// ---------------------------------------------------------------------------

// TestHandler_PostIngest_SuppressesPush_WhenLiveListenerPresent verifies the
// core acceptance criterion: when a foreground SSE listener is connected, a
// successful ingest must NOT call pushSender.Send(). The clip already
// auto-plays via the SSE stream and a push would cause double delivery.
func TestHandler_PostIngest_SuppressesPush_WhenLiveListenerPresent(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}
	presence := &mockPresenceTracker{liveListener: true}

	// WithPresenceTracker does not exist yet — this will fail to compile
	// until handler.go implements it.
	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithPresenceTracker(presence)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIngestRequest(t, "hello presence"))

	if w.Code != http.StatusOK {
		t.Fatalf("POST /ingest: expected 200, got %d", w.Code)
	}

	// Push must be suppressed when a live listener is present. Settle briefly to
	// confirm no detached Send goroutine was dispatched.
	ps.assertNoSendCallsSettle(t)
}

// TestHandler_PostIngest_SendsPush_WhenNoLiveListener verifies the
// complementary acceptance criterion: when no foreground listener is
// connected, a successful ingest MUST call pushSender.Send() so the phone
// receives a background notification.
func TestHandler_PostIngest_SendsPush_WhenNoLiveListener(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}
	presence := &mockPresenceTracker{liveListener: false}

	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithPresenceTracker(presence)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIngestRequest(t, "hello push"))

	if w.Code != http.StatusOK {
		t.Fatalf("POST /ingest: expected 200, got %d", w.Code)
	}

	// Push must be sent when there is no live listener (Send runs async).
	ps.waitForSendCalls(t, 1)
}

// TestHandler_PostIngest_NoDoubleDelivery_ListenerConnected verifies the "no
// double delivery" acceptance criterion: a clip must never be both broadcast
// via SSE AND sent as a push notification in the same ingest cycle.
//
// When a live listener is present the handler broadcasts via SSE (auto-play)
// and must NOT push. We confirm that the hub receives the broadcast AND that
// the push sender receives no calls.
func TestHandler_PostIngest_NoDoubleDelivery_ListenerConnected(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}
	presence := &mockPresenceTracker{liveListener: true}

	_, ch, cancel := hub.Subscribe()
	defer cancel()

	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithPresenceTracker(presence)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIngestRequest(t, "no double delivery"))

	if w.Code != http.StatusOK {
		t.Fatalf("POST /ingest: expected 200, got %d", w.Code)
	}

	// SSE broadcast must still happen (the listener auto-plays it).
	select {
	case msg, ok := <-ch:
		if !ok {
			t.Fatal("subscriber channel closed before broadcast arrived")
		}
		if msg == "" {
			t.Error("expected non-empty SSE broadcast message, got empty string")
		}
	default:
		t.Error("no SSE broadcast received — SSE delivery must still happen when listener is present")
	}

	// Push must NOT have been sent (no double delivery). Settle to confirm no
	// detached Send goroutine fires.
	ps.assertNoSendCallsSettle(t)
}

// TestHandler_PostIngest_NilPresenceTracker_DefaultsToPushEnabled verifies
// backward compatibility: when no PresenceTracker is configured (nil), the
// handler behaves as before — it always sends push. This prevents a
// regression for callers that do not wire up the tracker.
func TestHandler_PostIngest_NilPresenceTracker_DefaultsToPushEnabled(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}

	// Deliberately no WithPresenceTracker call — tracker is nil.
	handler := NewHandler(synth, store, hub).WithPushSender(ps)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIngestRequest(t, "nil tracker fallback"))

	if w.Code != http.StatusOK {
		t.Fatalf("POST /ingest: expected 200, got %d", w.Code)
	}

	// Without a presence tracker, push must still be sent (legacy behaviour).
	ps.waitForSendCalls(t, 1)
}

// TestHandler_PostIngest_PresenceTransition_PushSentAfterListenerLeaves
// verifies that push suppression is evaluated per-clip at ingest time. When a
// listener was previously connected but has since cancelled (presence is now
// false), push must be sent for the new clip. This prevents a stale "listener
// was once here" from permanently suppressing push.
func TestHandler_PostIngest_PresenceTransition_PushSentAfterListenerLeaves(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}
	presence := &mockPresenceTracker{liveListener: false} // listener gone

	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithPresenceTracker(presence)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIngestRequest(t, "listener left"))

	if w.Code != http.StatusOK {
		t.Fatalf("POST /ingest: expected 200, got %d", w.Code)
	}

	// Presence tracker reports no live listener → push must be sent (async).
	ps.waitForSendCalls(t, 1)
}

// ---------------------------------------------------------------------------
// Table-driven: presence-driven suppression matrix
// ---------------------------------------------------------------------------

// TestHandler_PostIngest_PresenceSuppression_TableDriven is a table-driven
// test covering all combinations of (hasListener, hasPushSender) and asserting
// the expected push call count. It is the single authoritative specification
// for push-suppression behaviour.
func TestHandler_PostIngest_PresenceSuppression_TableDriven(t *testing.T) {
	tests := []struct {
		name          string
		hasListener   bool
		hasPushSender bool
		wantPushCalls int
	}{
		{
			name:          "listener present + push sender configured → suppress push",
			hasListener:   true,
			hasPushSender: true,
			wantPushCalls: 0,
		},
		{
			name:          "no listener + push sender configured → send push",
			hasListener:   false,
			hasPushSender: true,
			wantPushCalls: 1,
		},
		{
			name:          "listener present + no push sender → no push (nil safe)",
			hasListener:   true,
			hasPushSender: false,
			wantPushCalls: 0,
		},
		{
			name:          "no listener + no push sender → no push (nil safe)",
			hasListener:   false,
			hasPushSender: false,
			wantPushCalls: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synth := &mockSynthesizer{audioData: []byte("fake-mp3")}
			store := NewClipStore(10)
			hub := NewSSEHub()
			presence := &mockPresenceTracker{liveListener: tc.hasListener}

			handler := NewHandler(synth, store, hub).
				WithPresenceTracker(presence)

			var ps *mockPushSender
			if tc.hasPushSender {
				ps = &mockPushSender{}
				handler = handler.WithPushSender(ps)
			}

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, newIngestRequest(t, "table test"))

			if w.Code != http.StatusOK {
				t.Fatalf("POST /ingest: expected 200, got %d", w.Code)
			}

			if tc.hasPushSender {
				// Send runs on a detached goroutine. For a positive expectation
				// poll until it fires; for zero, settle to confirm it never does.
				if tc.wantPushCalls > 0 {
					ps.waitForSendCalls(t, tc.wantPushCalls)
				} else {
					ps.assertNoSendCallsSettle(t)
				}
			}
		})
	}
}

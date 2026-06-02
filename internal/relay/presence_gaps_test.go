package relay

// ---------------------------------------------------------------------------
// Presence-driven suppression — gap-fill tests (Phase 4)
//
// This file fills the gaps identified in the Phase 4 tester review:
//
//  1. Real SSEHub as PresenceTracker wired into Handler (integration seam)
//  2. Exactly-1 / exactly-0 listener boundary via the handler's real tracker
//  3. Rapid subscribe/unsubscribe followed by an ingest
//  4. Push sender error does not cause the handler to return a non-200 status
//  5. Concurrent ingests while listeners subscribe/unsubscribe (race detector)
//  6. Acceptance criterion: double-delivery is impossible via concurrent path
//  7. Per-ingest PresenceTracker consultation (state changes between calls)
// ---------------------------------------------------------------------------

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

// ---------------------------------------------------------------------------
// Goroutine-safe synthesizer — required for concurrent ingest tests.
//
// mockSynthesizer (handler_test.go) writes to `called` and `calledWith` on
// every Synthesize call, which races when multiple goroutines call ServeHTTP
// simultaneously. concurrentSafeSynth is read-only after construction.
// ---------------------------------------------------------------------------

type concurrentSafeSynth struct {
	data []byte
}

func (s *concurrentSafeSynth) Synthesize(_ string, _ tts.Voice) ([]byte, error) {
	return s.data, nil
}

// ---------------------------------------------------------------------------
// Goroutine-safe push sender — required for concurrent ingest tests.
//
// mockPushSender (companion_test.go) uses an unsynchronised slice, which races
// when multiple goroutines call Send simultaneously. concurrentMockPushSender
// protects its counter with a mutex.
// ---------------------------------------------------------------------------

type concurrentMockPushSender struct {
	mu    sync.Mutex
	calls int
}

func (c *concurrentMockPushSender) AddSubscription(_ PushSubscription) bool { return true }

func (c *concurrentMockPushSender) Send(_, _ string) error {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return nil
}

func (c *concurrentMockPushSender) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// ---------------------------------------------------------------------------
// Push sender that returns an error — used to verify non-fatal error handling.
// ---------------------------------------------------------------------------

type errPushSender struct {
	sendCalls int
}

func (e *errPushSender) AddSubscription(_ PushSubscription) bool { return true }

func (e *errPushSender) Send(_, _ string) error {
	e.sendCalls++
	return errors.New("push service unavailable")
}

// ---------------------------------------------------------------------------
// Mutable presence tracker — changes state between test steps.
// ---------------------------------------------------------------------------

// mutablePresenceTracker is a goroutine-safe PresenceTracker whose live state
// can be toggled between test steps to simulate a listener
// connecting/disconnecting between successive ingests.
type mutablePresenceTracker struct {
	mu   sync.RWMutex
	live bool
}

func (m *mutablePresenceTracker) HasLiveListener() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.live
}

func (m *mutablePresenceTracker) SetLive(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.live = v
}

// ---------------------------------------------------------------------------
// Gap 1 — Integration seam: real SSEHub wired as PresenceTracker into Handler
// ---------------------------------------------------------------------------

// TestHandler_PostIngest_RealSSEHub_WithSubscriber_SuppressesPush tests the
// full integration path: a real *SSEHub (not a mock) is injected as both the
// hub and the PresenceTracker. When a subscriber is connected before the
// ingest, push must be suppressed.
//
// This covers the concern raised in Phase 4 — "no single test exercises the
// full SSEHub → handler → push suppression chain."
func TestHandler_PostIngest_RealSSEHub_WithSubscriber_SuppressesPush(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("mp3-bytes")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}

	_, _, cancel := hub.Subscribe()
	defer cancel()

	// Hub is wired as both the SSE hub and the PresenceTracker — this matches
	// the production wiring in server.go: .WithPresenceTracker(hub).
	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithPresenceTracker(hub) // real SSEHub, not a mock

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIngestRequest(t, "integration: suppress"))

	if w.Code != http.StatusOK {
		t.Fatalf("POST /ingest: expected 200, got %d", w.Code)
	}
	if len(ps.sendCalls) != 0 {
		t.Errorf("push Send called %d time(s) with a real SSE subscriber; want 0 — double delivery via real hub",
			len(ps.sendCalls))
	}
}

// TestHandler_PostIngest_RealSSEHub_NoSubscriber_SendsPush verifies the
// complementary integration path: when no subscriber is connected to the real
// hub, push must be sent.
func TestHandler_PostIngest_RealSSEHub_NoSubscriber_SendsPush(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("mp3-bytes")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}

	// No subscribers — hub.HasLiveListener() == false.
	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithPresenceTracker(hub)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIngestRequest(t, "integration: send push"))

	if w.Code != http.StatusOK {
		t.Fatalf("POST /ingest: expected 200, got %d", w.Code)
	}
	if len(ps.sendCalls) != 1 {
		t.Errorf("push Send called %d time(s) with no SSE subscriber; want 1", len(ps.sendCalls))
	}
}

// ---------------------------------------------------------------------------
// Gap 2 — Boundary: exactly 1 listener, then cancel → push resumes
// ---------------------------------------------------------------------------

// TestHandler_PostIngest_RealSSEHub_ExactlyOneListener_ThenCancel_PushResumes
// verifies the boundary condition at exactly 1 listener. After that single
// listener cancels, the next ingest must send push (Count drops to 0).
func TestHandler_PostIngest_RealSSEHub_ExactlyOneListener_ThenCancel_PushResumes(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("mp3-bytes")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}

	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithPresenceTracker(hub)

	// === Ingest 1: exactly 1 subscriber connected — push suppressed ===
	_, _, cancel := hub.Subscribe()
	if hub.Count() != 1 {
		t.Fatalf("precondition: expected exactly 1 subscriber, got %d", hub.Count())
	}

	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, newIngestRequest(t, "with one listener"))
	if w1.Code != http.StatusOK {
		t.Fatalf("ingest 1: expected 200, got %d", w1.Code)
	}
	if len(ps.sendCalls) != 0 {
		t.Errorf("ingest 1: push called %d time(s) with exactly 1 listener; want 0", len(ps.sendCalls))
	}

	// === Cancel the single subscriber — hub count drops to 0 ===
	cancel()
	if hub.Count() != 0 {
		t.Fatalf("precondition: expected 0 subscribers after cancel, got %d", hub.Count())
	}

	// === Ingest 2: no listeners — push must be sent ===
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, newIngestRequest(t, "no listener now"))
	if w2.Code != http.StatusOK {
		t.Fatalf("ingest 2: expected 200, got %d", w2.Code)
	}
	if len(ps.sendCalls) != 1 {
		t.Errorf("ingest 2: push called %d time(s) after listener cancelled; want 1", len(ps.sendCalls))
	}
}

// TestHandler_PostIngest_RealSSEHub_ZeroListeners_PushSentEveryTime verifies
// that with a real hub at exactly 0 subscribers, every ingest sends push.
func TestHandler_PostIngest_RealSSEHub_ZeroListeners_PushSentEveryTime(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("mp3-bytes")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}

	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithPresenceTracker(hub)

	for i := range 3 {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, newIngestRequest(t, "no listeners"))
		if w.Code != http.StatusOK {
			t.Fatalf("ingest %d: expected 200, got %d", i, w.Code)
		}
	}
	if len(ps.sendCalls) != 3 {
		t.Errorf("push Send called %d time(s) for 3 ingests with 0 listeners; want 3", len(ps.sendCalls))
	}
}

// ---------------------------------------------------------------------------
// Gap 3 — Rapid subscribe/unsubscribe before ingest
// ---------------------------------------------------------------------------

// TestHandler_PostIngest_RapidSubscribeUnsubscribe_NoListener_SendsPush
// verifies that when many subscribe/cancel cycles complete before the ingest,
// and zero listeners remain, push is sent. The test exercises HasLiveListener()
// stability after a burst of state changes.
func TestHandler_PostIngest_RapidSubscribeUnsubscribe_NoListener_SendsPush(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("mp3-bytes")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}

	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithPresenceTracker(hub)

	// Rapid subscribe-then-cancel — all goroutines finish before ingest.
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, cancel := hub.Subscribe()
			cancel()
		}()
	}
	wg.Wait()

	// After all cancels, exactly 0 listeners remain.
	if hub.Count() != 0 {
		t.Fatalf("precondition: expected 0 subscribers after all cancels, got %d", hub.Count())
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIngestRequest(t, "after rapid churn"))
	if w.Code != http.StatusOK {
		t.Fatalf("POST /ingest: expected 200, got %d", w.Code)
	}
	if len(ps.sendCalls) != 1 {
		t.Errorf("push Send called %d time(s) after rapid churn with 0 listeners; want 1", len(ps.sendCalls))
	}
}

// ---------------------------------------------------------------------------
// Gap 4 — Push sender error does not cause the handler to return non-200
// ---------------------------------------------------------------------------

// TestHandler_PostIngest_PushSenderError_StillReturns200 verifies that when the
// push sender's Send() returns a non-nil error, the handler still responds with
// HTTP 200. Push failures must be non-fatal (fire-and-forget). The response to
// the TTS ingest caller must not be degraded by a push transport failure.
func TestHandler_PostIngest_PushSenderError_StillReturns200(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("mp3-bytes")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &errPushSender{}

	// No PresenceTracker so push is attempted (no suppression).
	handler := NewHandler(synth, store, hub).WithPushSender(ps)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIngestRequest(t, "push will fail"))

	if w.Code != http.StatusOK {
		t.Errorf("POST /ingest: expected 200 even when push fails, got %d", w.Code)
	}
	if ps.sendCalls != 1 {
		t.Errorf("expected push Send to be called once despite error, got %d", ps.sendCalls)
	}
}

// TestHandler_PostIngest_PushSenderError_WithListener_SuppressedNotSentAtAll
// verifies that when a live listener is present (suppression active), the error
// push sender is never called — even when it would fail. No error path from a
// deliberately avoided send should surface.
func TestHandler_PostIngest_PushSenderError_WithListener_SuppressedNotSentAtAll(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("mp3-bytes")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &errPushSender{}
	presence := &mockPresenceTracker{liveListener: true}

	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithPresenceTracker(presence)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, newIngestRequest(t, "suppressed, no error"))

	if w.Code != http.StatusOK {
		t.Errorf("POST /ingest: expected 200, got %d", w.Code)
	}
	if ps.sendCalls != 0 {
		t.Errorf("errPushSender was called %d time(s) despite suppression; want 0", ps.sendCalls)
	}
}

// ---------------------------------------------------------------------------
// Gap 5 — Concurrent ingests while listeners subscribe/unsubscribe
// ---------------------------------------------------------------------------

// TestHandler_PostIngest_ConcurrentIngests_ListenerChurn_NoRace verifies that
// firing multiple ingest requests concurrently while SSE listeners subscribe and
// cancel simultaneously does not produce data races or panics. The test does not
// assert a specific push count (the TOCTOU window makes it non-deterministic) —
// its purpose is to exercise the race detector.
func TestHandler_PostIngest_ConcurrentIngests_ListenerChurn_NoRace(t *testing.T) {
	// concurrentSafeSynth is required: multiple goroutines call ServeHTTP
	// simultaneously, so the synthesizer must not write to shared fields.
	synth := &concurrentSafeSynth{data: []byte("mp3-bytes")}
	store := NewClipStore(100)
	hub := NewSSEHub()
	concPS := &concurrentMockPushSender{}

	handler := NewHandler(synth, store, hub).
		WithPushSender(concPS).
		WithPresenceTracker(hub)

	var wg sync.WaitGroup

	// 10 goroutines fire ingest requests concurrently.
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, newIngestRequest(t, "concurrent"))
		}()
	}

	// 5 goroutines subscribe then immediately cancel (transient churn).
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, cancel := hub.Subscribe()
			cancel()
		}()
	}

	// 3 goroutines subscribe and hold (simulating a steady companion session).
	var (
		persistentMu      sync.Mutex
		persistentCancels []func()
	)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, cancel := hub.Subscribe()
			persistentMu.Lock()
			persistentCancels = append(persistentCancels, cancel)
			persistentMu.Unlock()
		}()
	}

	wg.Wait()

	// Clean up persistent subscribers after all ingests complete.
	persistentMu.Lock()
	for _, cancel := range persistentCancels {
		cancel()
	}
	persistentMu.Unlock()

	// Test passes if the -race detector is silent and no panic occurred.
	_ = concPS.Count()
}

// ---------------------------------------------------------------------------
// Gap 6 — Acceptance criterion: no double-delivery under concurrent ingests
// ---------------------------------------------------------------------------

// TestHandler_NoDoubleDelivery_ConcurrentIngest_ListenerPresent_NoPush verifies
// the "no clip is ever both auto-played and delivered as a push" acceptance
// criterion under a concurrent load scenario: multiple simultaneous ingests
// with a steady live listener must never produce push calls. All clips are
// broadcast via SSE; none must trigger push.
func TestHandler_NoDoubleDelivery_ConcurrentIngest_ListenerPresent_NoPush(t *testing.T) {
	// concurrentSafeSynth required: multiple goroutines call ServeHTTP.
	synth := &concurrentSafeSynth{data: []byte("mp3-bytes")}
	store := NewClipStore(50)
	hub := NewSSEHub()

	// Register a persistent subscriber before any ingest begins.
	_, subCh, cancel := hub.Subscribe()
	defer cancel()

	ps := &concurrentMockPushSender{}

	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithPresenceTracker(hub)

	const ingests = 8
	var wg sync.WaitGroup
	for range ingests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, newIngestRequest(t, "no-double-delivery"))
		}()
	}
	wg.Wait()

	// Drain any buffered SSE messages without blocking on future messages.
	drained := 0
drain:
	for {
		select {
		case _, ok := <-subCh:
			if !ok {
				break drain
			}
			drained++
		default:
			break drain
		}
	}

	// Push must never have been called — the listener was present for all ingests.
	if got := ps.Count(); got != 0 {
		t.Errorf("double delivery: push Send called %d time(s) while a live listener was always connected; want 0", got)
	}

	_ = drained // non-deterministic due to channel buffering; push count is the key assertion
}

// ---------------------------------------------------------------------------
// Gap 7 — PresenceTracker consulted on every ingest (per-call, not cached)
// ---------------------------------------------------------------------------

// TestHandler_PostIngest_PresenceTrackerConsultedPerIngest verifies that the
// handler re-queries the PresenceTracker on each ingest. Two successive ingests
// with a mutable tracker see different suppression outcomes as the tracker
// changes between calls. This guards against stale-cache bugs where an initial
// "listener present" state permanently suppresses push.
func TestHandler_PostIngest_PresenceTrackerConsultedPerIngest(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("mp3-bytes")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}

	// Start with a live listener — first ingest should suppress push.
	mpt := &mutablePresenceTracker{live: true}

	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithPresenceTracker(mpt)

	// === Ingest 1: listener present → push suppressed ===
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, newIngestRequest(t, "first"))
	if w1.Code != http.StatusOK {
		t.Fatalf("ingest 1: expected 200, got %d", w1.Code)
	}
	if len(ps.sendCalls) != 0 {
		t.Errorf("ingest 1: push called %d time(s); want 0 (listener present)", len(ps.sendCalls))
	}

	// Flip presence state — listener has gone away.
	mpt.SetLive(false)

	// === Ingest 2: no listener → push must be sent ===
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, newIngestRequest(t, "second"))
	if w2.Code != http.StatusOK {
		t.Fatalf("ingest 2: expected 200, got %d", w2.Code)
	}
	if len(ps.sendCalls) != 1 {
		t.Errorf("ingest 2: push called %d time(s); want 1 (no listener)", len(ps.sendCalls))
	}
}

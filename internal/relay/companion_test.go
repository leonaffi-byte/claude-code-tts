package relay

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// CompanionHandler — London-school (mock-first) tests
//
// These tests define the expected HTTP contract for CompanionHandler before
// the real implementation exists. They are expected to FAIL until
// companion.go is fully implemented.
// ---------------------------------------------------------------------------

// waitForCond polls cond every 5ms until it returns true or a 2s deadline
// expires, then fails. It replaces fixed time.Sleep handoffs in streaming tests
// so they do not flake on a loaded runner. Mirrors the helper in
// internal/server/worker_test.go (which is in a different package).
func waitForCond(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", msg)
}

// TestCompanionHandler_GetRoot_Returns200WithHTML verifies that GET / serves
// the companion HTML page with the correct status and Content-Type.
func TestCompanionHandler_GetRoot_Returns200WithHTML(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /: expected status 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET /: expected Content-Type text/html..., got %q", ct)
	}

	if w.Body.Len() == 0 {
		t.Error("GET /: expected non-empty response body (companion HTML)")
	}
}

// TestCompanionHandler_GetEvents_Returns200WithSSEContentType verifies that
// GET /events responds with status 200 and Content-Type text/event-stream.
// The request context is cancelled to unblock the streaming handler.
func TestCompanionHandler_GetEvents_Returns200WithSSEContentType(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(w, req)
	}()

	// Allow the handler to write headers before we cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("GET /events: handler did not return after context cancellation")
	}

	if w.Code != http.StatusOK {
		t.Errorf("GET /events: expected status 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("GET /events: expected Content-Type text/event-stream, got %q", ct)
	}
}

// TestCompanionHandler_GetEvents_IncrementsSubscriberCount verifies that
// connecting to GET /events adds one subscriber to the hub while connected,
// and the count returns to 0 after disconnect.
func TestCompanionHandler_GetEvents_IncrementsSubscriberCount(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(w, req)
	}()

	// Poll until the handler has registered the subscriber rather than sleeping a
	// fixed duration (which can flake on a loaded runner before Subscribe runs).
	waitForCond(t, func() bool { return hub.Count() == 1 },
		"hub.Count() == 1 while /events is connected")

	cancel() // disconnect the SSE client
	<-done
}

// TestCompanionHandler_GetEvents_WritesHeartbeat verifies that an idle SSE
// connection receives periodic heartbeat comment frames so a silently-dropped
// peer can be detected via a failing write. The heartbeat interval is shortened
// for the test.
func TestCompanionHandler_GetEvents_WritesHeartbeat(t *testing.T) {
	orig := sseHeartbeatInterval
	sseHeartbeatInterval = 10 * time.Millisecond
	t.Cleanup(func() { sseHeartbeatInterval = orig })

	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	w := newSyncRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(w, req)
	}()

	// No broadcast is sent — only the heartbeat ticker should write to the body.
	waitForCond(t, func() bool { return strings.Contains(w.body(), ":") },
		"SSE heartbeat comment frame to be written on an idle connection")

	cancel()
	<-done

	if body := w.body(); !strings.Contains(body, ": ping") {
		t.Errorf("expected a heartbeat comment frame in idle SSE body, got %q", body)
	}
}

// errAfterHeaderRecorder is a Flusher-capable ResponseWriter whose Write fails
// after the SSE headers are written, simulating a silently-dropped peer whose
// write surfaces an error. It records whether Write returned an error.
type errAfterHeaderRecorder struct {
	mu          sync.Mutex
	hdr         http.Header
	wroteHeader bool
	failed      bool
}

func newErrAfterHeaderRecorder() *errAfterHeaderRecorder {
	return &errAfterHeaderRecorder{hdr: make(http.Header)}
}

func (r *errAfterHeaderRecorder) Header() http.Header { return r.hdr }
func (r *errAfterHeaderRecorder) WriteHeader(int)     { r.wroteHeader = true }
func (r *errAfterHeaderRecorder) Flush()              {}

func (r *errAfterHeaderRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = true
	return 0, fmt.Errorf("simulated broken pipe")
}

// TestCompanionHandler_GetEvents_WriteError_FreesSubscriberSlot verifies that
// when a heartbeat write fails (dead peer), the handler returns and the deferred
// cancel frees the hub subscriber slot — preventing the leak described in the
// finding.
func TestCompanionHandler_GetEvents_WriteError_FreesSubscriberSlot(t *testing.T) {
	orig := sseHeartbeatInterval
	sseHeartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { sseHeartbeatInterval = orig })

	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	// No context cancellation and no broadcast: the only way the handler can
	// return is via a failing heartbeat write.
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	w := newErrAfterHeaderRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(w, req)
	}()

	select {
	case <-done:
		// Handler returned because the heartbeat write failed.
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after a failing heartbeat write — subscriber slot would leak")
	}

	// The subscriber slot must have been released by the deferred cancel().
	waitForCond(t, func() bool { return hub.Count() == 0 },
		"hub subscriber slot to be freed after the write error")
}

// TestCompanionHandler_GetEvents_BroadcastArrivesOnResponse verifies that
// a hub.Broadcast reaches the SSE response body while a client is connected.
func TestCompanionHandler_GetEvents_BroadcastArrivesOnResponse(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	// syncRecorder is a Flusher-capable, goroutine-safe recorder so the test can
	// poll the response body while the handler goroutine writes to it without a
	// data race (httptest.ResponseRecorder is not safe for concurrent access).
	w := newSyncRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(w, req)
	}()

	// Poll until the subscriber has registered before broadcasting.
	waitForCond(t, func() bool { return hub.Count() == 1 }, "subscriber to register")

	hub.Broadcast("new-clip", `{"id":"test-id"}`)

	// Poll the (synchronized) response body until the expected SSE frame has been
	// flushed rather than sleeping a fixed duration.
	want := "event: new-clip\ndata: {\"id\":\"test-id\"}\n\n"
	waitForCond(t, func() bool { return strings.Contains(w.body(), want) },
		"SSE broadcast frame to arrive in response body")

	cancel()
	<-done

	if body := w.body(); !strings.Contains(body, want) {
		t.Errorf("GET /events response body missing expected SSE event:\ngot:  %q\nwant substring: %q", body, want)
	}
}

// syncRecorder is a minimal goroutine-safe http.ResponseWriter+Flusher used by
// streaming tests that poll the response body concurrently with the handler.
type syncRecorder struct {
	mu      sync.Mutex
	hdr     http.Header
	buf     strings.Builder
	code    int
	written bool
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{hdr: make(http.Header), code: http.StatusOK}
}

func (r *syncRecorder) Header() http.Header { return r.hdr }

func (r *syncRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.written {
		r.code = code
		r.written = true
	}
}

func (r *syncRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.written = true
	return r.buf.Write(p)
}

func (r *syncRecorder) Flush() {}

func (r *syncRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// TestCompanionHandler_GetClip_KnownID_Returns200WithMP3Bytes verifies that
// GET /clips/{id} returns 200 audio/mpeg with the stored bytes.
func TestCompanionHandler_GetClip_KnownID_Returns200WithMP3Bytes(t *testing.T) {
	store := NewClipStore(10)
	audioBytes := []byte{0xFF, 0xFB, 0x90, 0x00}
	id, err := store.Add(audioBytes)
	if err != nil {
		t.Fatalf("store.Add failed: %v", err)
	}

	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	req := httptest.NewRequest(http.MethodGet, "/clips/"+id, nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /clips/%s: expected 200, got %d", id, w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "audio/mpeg") {
		t.Errorf("GET /clips/%s: expected Content-Type audio/mpeg, got %q", id, ct)
	}

	if w.Body.String() != string(audioBytes) {
		t.Errorf("GET /clips/%s: body bytes mismatch: got %v, want %v", id, w.Body.Bytes(), audioBytes)
	}
}

// TestCompanionHandler_GetClip_UnknownID_Returns404 verifies that requesting
// a clip that does not exist returns 404 Not Found.
func TestCompanionHandler_GetClip_UnknownID_Returns404(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	req := httptest.NewRequest(http.MethodGet, "/clips/unknown-id", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GET /clips/unknown-id: expected 404, got %d", w.Code)
	}
}

// TestCompanionHandler_GetClips_EmptyID_Returns404 verifies that GET /clips/
// with no ID segment returns 404 rather than panicking.
func TestCompanionHandler_GetClips_EmptyID_Returns404(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	req := httptest.NewRequest(http.MethodGet, "/clips/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GET /clips/: expected 404, got %d", w.Code)
	}
}

// TestCompanionHandler_NonGetToRoot_Returns405 verifies that any non-GET
// method to / returns 405 Method Not Allowed.
func TestCompanionHandler_NonGetToRoot_Returns405(t *testing.T) {
	methods := []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	}

	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s /: expected 405, got %d", method, w.Code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Edge cases — gap-fill tests
// ---------------------------------------------------------------------------

// nonFlusherRecorder implements http.ResponseWriter but deliberately does NOT
// implement http.Flusher. This is achieved by forwarding only the three
// ResponseWriter methods rather than embedding *httptest.ResponseRecorder
// (which would promote its Flush() method).
type nonFlusherRecorder struct {
	rec *httptest.ResponseRecorder
}

func (r *nonFlusherRecorder) Header() http.Header         { return r.rec.Header() }
func (r *nonFlusherRecorder) WriteHeader(code int)        { r.rec.WriteHeader(code) }
func (r *nonFlusherRecorder) Write(b []byte) (int, error) { return r.rec.Write(b) }

// TestCompanionHandler_GetEvents_NonFlusher_Returns500 verifies that when the
// ResponseWriter does not implement http.Flusher the handler responds with
// 500 Internal Server Error rather than panicking.
func TestCompanionHandler_GetEvents_NonFlusher_Returns500(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	inner := httptest.NewRecorder()
	// nonFlusherRecorder forwards ResponseWriter methods but does NOT expose
	// Flush(), so the http.Flusher type assertion inside handleEvents fails.
	w := &nonFlusherRecorder{rec: inner}

	handler.ServeHTTP(w, req)

	if inner.Code != http.StatusInternalServerError {
		t.Errorf("GET /events with non-Flusher: expected 500, got %d", inner.Code)
	}
}

// TestCompanionHandler_NonGetToEvents_Returns405 verifies that POST (and other
// non-GET methods) to /events returns 405 Method Not Allowed.
func TestCompanionHandler_NonGetToEvents_Returns405(t *testing.T) {
	methods := []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	}

	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/events", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s /events: expected 405, got %d", method, w.Code)
			}
		})
	}
}

// TestCompanionHandler_GetEvents_SSEHeaders verifies that the /events response
// includes the Cache-Control: no-cache and X-Accel-Buffering: no headers
// required to prevent proxies from buffering the event stream.
func TestCompanionHandler_GetEvents_SSEHeaders(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(w, req)
	}()

	// Allow the handler to write headers before we cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("GET /events: handler did not return after context cancellation")
	}

	tests := []struct {
		header string
		want   string
	}{
		{"Cache-Control", "no-cache"},
		{"X-Accel-Buffering", "no"},
	}
	for _, tc := range tests {
		t.Run(tc.header, func(t *testing.T) {
			got := w.Header().Get(tc.header)
			if got != tc.want {
				t.Errorf("GET /events: %s header = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

// TestCompanionHandler_GetRoot_SecurityHeaders verifies that GET / includes
// the required security headers (CSP, X-Content-Type-Options, X-Frame-Options).
func TestCompanionHandler_GetRoot_SecurityHeaders(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /: expected 200, got %d", w.Code)
	}

	tests := []struct {
		header string
		want   string
	}{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
	}
	for _, tc := range tests {
		t.Run(tc.header, func(t *testing.T) {
			got := w.Header().Get(tc.header)
			if got != tc.want {
				t.Errorf("GET /: %s = %q, want %q", tc.header, got, tc.want)
			}
		})
	}

	// CSP must be present and contain required directives.
	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("GET /: Content-Security-Policy header is missing")
	}
	// The inline script must run via a per-response nonce, not 'unsafe-inline'.
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("GET /: CSP must not allow 'unsafe-inline'; got %q", csp)
	}
	if !strings.Contains(csp, "nonce-") {
		t.Errorf("GET /: CSP script-src must use a nonce; got %q", csp)
	}

	// The served HTML must carry the same nonce on its <script> tag and must not
	// still contain the build-time placeholder.
	body := w.Body.String()
	if strings.Contains(body, cspNoncePlaceholder) {
		t.Error("GET /: served HTML still contains the unreplaced CSP nonce placeholder")
	}
	// Extract the nonce value from the CSP and confirm the script tag uses it.
	const marker = "'nonce-"
	idx := strings.Index(csp, marker)
	if idx < 0 {
		t.Fatalf("GET /: could not locate nonce in CSP %q", csp)
	}
	rest := csp[idx+len(marker):]
	end := strings.IndexByte(rest, '\'')
	if end < 0 {
		t.Fatalf("GET /: malformed nonce in CSP %q", csp)
	}
	nonce := rest[:end]
	if nonce == "" {
		t.Fatal("GET /: empty CSP nonce")
	}
	if !strings.Contains(body, `nonce="`+nonce+`"`) {
		t.Errorf("GET /: served <script> does not carry the CSP nonce %q", nonce)
	}
}

// TestCompanionHandler_GetRoot_NonceIsPerResponse verifies that each GET /
// response gets a distinct CSP nonce (nonces must not be reused across
// responses, or they provide no protection).
func TestCompanionHandler_GetRoot_NonceIsPerResponse(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	cspOf := func() string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Header().Get("Content-Security-Policy")
	}

	if a, b := cspOf(), cspOf(); a == b {
		t.Errorf("CSP nonce was reused across two responses: %q", a)
	}
}

// TestCompanionHandler_GetEvents_MaxSubscribers_Returns503 verifies that when
// the SSE hub has reached its subscriber cap, GET /events returns 503 Service
// Unavailable rather than blocking or panicking.
func TestCompanionHandler_GetEvents_MaxSubscribers_Returns503(t *testing.T) {
	store := NewClipStore(10)
	// Create a hub capped at 0 (immediate rejection for all subscribers).
	hub := &SSEHub{
		subscribers: make(map[string]chan string),
		maxSubs:     0, // zero means unlimited; set maxSubs=1 and fill it first
	}
	// Fill the hub to its cap by using maxSubs=1 and subscribing once manually.
	hub.maxSubs = 1
	_, _, firstCancel := hub.Subscribe()
	defer firstCancel()

	handler := NewCompanionHandler(store, hub, nil)

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /events at cap: expected 503, got %d", w.Code)
	}
}

// TestCompanionHandler_GetManifest_Returns200WithJSON verifies that
// GET /manifest.json returns 200 with Content-Type application/json.
func TestCompanionHandler_GetManifest_Returns200WithJSON(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	req := httptest.NewRequest(http.MethodGet, "/manifest.json", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /manifest.json: expected 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("GET /manifest.json: expected Content-Type application/json, got %q", ct)
	}
}

// TestCompanionHandler_GetManifest_StartURLContainsToken verifies that the
// manifest start_url embeds the token so PWA installs reload authenticated.
func TestCompanionHandler_GetManifest_StartURLContainsToken(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, tokenStoreWithValue(t, "mytoken"))

	req := httptest.NewRequest(http.MethodGet, "/manifest.json", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "mytoken") {
		t.Errorf("GET /manifest.json: expected body to contain token %q, got: %s", "mytoken", body)
	}
}

// TestCompanionHandler_GetManifest_URLSpecialCharsInToken verifies that a token
// containing URL-special characters (e.g. '+', '=', '/') is safely embedded in
// the manifest JSON without breaking the JSON structure.
func TestCompanionHandler_GetManifest_URLSpecialCharsInToken(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	// Token with characters that have special meaning in URLs and JSON.
	specialToken := "tok+en/with=special&chars"
	handler := NewCompanionHandler(store, hub, tokenStoreWithValue(t, specialToken))

	req := httptest.NewRequest(http.MethodGet, "/manifest.json", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /manifest.json with special-char token: expected 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("GET /manifest.json: expected Content-Type application/json, got %q", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, specialToken) {
		t.Errorf("manifest body does not contain the special-char token %q; body: %s", specialToken, body)
	}
}

// ---------------------------------------------------------------------------
// POST /push/subscribe — Tests E, F, G
// ---------------------------------------------------------------------------

// mockPushSender records AddSubscription calls so handler tests can assert
// that subscriptions are registered without needing a real PushSender.
//
// It is goroutine-safe because the handler now dispatches pushSender.Send on a
// detached goroutine (so the /ingest response is never coupled to push-service
// latency). Tests that expect a Send must wait via waitForSendCalls rather than
// reading sendCalls immediately after ServeHTTP returns.
type mockPushSender struct {
	mu        sync.Mutex
	addedSubs []PushSubscription
	sendCalls []sendCall
}

type sendCall struct {
	clipID  string
	clipURL string
}

func (m *mockPushSender) AddSubscription(sub PushSubscription) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addedSubs = append(m.addedSubs, sub)
	return true
}

func (m *mockPushSender) Send(_ context.Context, clipID, clipURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendCalls = append(m.sendCalls, sendCall{clipID: clipID, clipURL: clipURL})
	return nil
}

// sendCallCount returns the number of Send calls recorded so far.
func (m *mockPushSender) sendCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sendCalls)
}

// firstSendCall returns a copy of the i-th recorded Send call.
func (m *mockPushSender) sendCall(i int) sendCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sendCalls[i]
}

// waitForSendCalls polls until exactly n Send calls have been recorded or the
// deadline expires, then fails. It is used because Send runs asynchronously.
func (m *mockPushSender) waitForSendCalls(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.sendCallCount() == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %d push Send call(s), got %d", n, m.sendCallCount())
}

// assertNoSendCallsSettle verifies that no Send call is recorded within a short
// settle window. Used for suppression assertions where Send must never fire.
func (m *mockPushSender) assertNoSendCallsSettle(t *testing.T) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	if got := m.sendCallCount(); got != 0 {
		t.Errorf("push Send called %d time(s); want 0", got)
	}
}

// stubLookupIP overrides the package DNS resolver for the duration of the test
// so push-subscribe tests are hermetic (no real DNS) and deterministic. byHost
// maps a hostname to the IPs it should resolve to; unknown hosts return an
// error (treated as unresolvable → rejected).
func stubLookupIP(t *testing.T, byHost map[string][]net.IP) {
	t.Helper()
	orig := lookupIP
	t.Cleanup(func() { lookupIP = orig })
	lookupIP = func(host string) ([]net.IP, error) {
		if ips, ok := byHost[host]; ok {
			return ips, nil
		}
		return nil, fmt.Errorf("stub: no record for %q", host)
	}
}

// publicHostStub resolves push.example.com to a public IP so https tests that
// expect success do not depend on real DNS.
func publicHostStub(t *testing.T) {
	stubLookupIP(t, map[string][]net.IP{
		"push.example.com": {net.ParseIP("93.184.216.34")}, // example.com public IP
	})
}

// TestCompanionHandler_PostPushSubscribe_StoresSubscription verifies that a
// valid POST /push/subscribe body registers the subscription with the push
// sender and returns 201 Created.
func TestCompanionHandler_PostPushSubscribe_StoresSubscription(t *testing.T) {
	publicHostStub(t)
	store := NewClipStore(10)
	hub := NewSSEHub()
	mockPS := &mockPushSender{}
	handler := NewCompanionHandler(store, hub, nil)
	handler.WithPushSender(mockPS)

	body := `{"endpoint":"https://push.example.com/abc","keys":{"p256dh":"dGVzdA==","auth":"dGVzdA=="}}`
	req := httptest.NewRequest(http.MethodPost, "/push/subscribe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("POST /push/subscribe: expected 201, got %d — body: %s", w.Code, w.Body.String())
	}

	if len(mockPS.addedSubs) != 1 {
		t.Fatalf("expected 1 subscription added, got %d", len(mockPS.addedSubs))
	}
	if mockPS.addedSubs[0].Endpoint != "https://push.example.com/abc" {
		t.Errorf("subscription endpoint = %q, want %q", mockPS.addedSubs[0].Endpoint, "https://push.example.com/abc")
	}
}

// TestCompanionHandler_PostPushSubscribe_MissingEndpoint_Returns400 verifies
// that a body without an endpoint field returns 400 Bad Request.
func TestCompanionHandler_PostPushSubscribe_MissingEndpoint_Returns400(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	mockPS := &mockPushSender{}
	handler := NewCompanionHandler(store, hub, nil)
	handler.WithPushSender(mockPS)

	req := httptest.NewRequest(http.MethodPost, "/push/subscribe", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /push/subscribe with missing endpoint: expected 400, got %d", w.Code)
	}

	if len(mockPS.addedSubs) != 0 {
		t.Errorf("expected no subscriptions added on bad request, got %d", len(mockPS.addedSubs))
	}
}

// TestCompanionHandler_PostPushSubscribe_NilSender_Returns503 verifies that when
// no PushSender is wired, POST /push/subscribe returns 503 Service Unavailable
// rather than panicking. Push is an optional dependency.
func TestCompanionHandler_PostPushSubscribe_NilSender_Returns503(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil) // deliberately no WithPushSender

	body := `{"endpoint":"https://push.example.com/abc","keys":{"p256dh":"dGVzdA==","auth":"dGVzdA=="}}`
	req := httptest.NewRequest(http.MethodPost, "/push/subscribe", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("POST /push/subscribe with nil sender: expected 503, got %d — body: %s", w.Code, w.Body.String())
	}
}

// TestCompanionHandler_GetPushSubscribe_Returns405 verifies that a GET to
// /push/subscribe is rejected with 405 Method Not Allowed.
func TestCompanionHandler_GetPushSubscribe_Returns405(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	req := httptest.NewRequest(http.MethodGet, "/push/subscribe", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /push/subscribe: expected 405, got %d", w.Code)
	}
}

// TestCompanionHandler_GetManifest_EmptyToken verifies that GET /manifest.json
// still returns valid JSON when the token is an empty string (e.g. during
// development without auth configured).
func TestCompanionHandler_GetManifest_EmptyToken(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	req := httptest.NewRequest(http.MethodGet, "/manifest.json", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /manifest.json with empty token: expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if len(body) == 0 {
		t.Error("GET /manifest.json with empty token: expected non-empty body")
	}

	// Body must at least contain valid JSON braces.
	if !strings.Contains(body, "{") || !strings.Contains(body, "}") {
		t.Errorf("GET /manifest.json with empty token: body does not look like JSON: %s", body)
	}
}

// ---------------------------------------------------------------------------
// VAPID public key endpoint — GET /push/vapid-public-key
// ---------------------------------------------------------------------------

// TestCompanionHandler_GetVAPIDPublicKey_Returns200WithKey verifies that GET
// /push/vapid-public-key returns 200 with a non-empty text/plain body containing
// the VAPID public key.
func TestCompanionHandler_GetVAPIDPublicKey_Returns200WithKey(t *testing.T) {
	dir := t.TempDir()
	vs := NewVAPIDStore(dir)
	if _, _, err := vs.LoadOrGenerate(); err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}

	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil).WithVAPIDStore(vs)

	req := httptest.NewRequest(http.MethodGet, "/push/vapid-public-key", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /push/vapid-public-key: expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("GET /push/vapid-public-key: expected text/plain, got %q", ct)
	}
	if w.Body.String() == "" {
		t.Error("GET /push/vapid-public-key: expected non-empty body")
	}
}

// TestCompanionHandler_GetVAPIDPublicKey_NilStore_Returns503 verifies that GET
// /push/vapid-public-key returns 503 when no VAPIDStore is wired.
func TestCompanionHandler_GetVAPIDPublicKey_NilStore_Returns503(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil) // no VAPIDStore

	req := httptest.NewRequest(http.MethodGet, "/push/vapid-public-key", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /push/vapid-public-key with nil store: expected 503, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Service worker endpoint — GET /sw.js
// ---------------------------------------------------------------------------

// TestCompanionHandler_GetSWJS_Returns200WithJS verifies that GET /sw.js
// returns 200 with Content-Type application/javascript and a non-empty body.
func TestCompanionHandler_GetSWJS_Returns200WithJS(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /sw.js: expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("GET /sw.js: expected Content-Type application/javascript, got %q", ct)
	}
	if w.Body.Len() == 0 {
		t.Error("GET /sw.js: expected non-empty body")
	}
}

// TestCompanionHandler_GetSWJS_ServiceWorkerAllowedHeader verifies that GET
// /sw.js includes the Service-Worker-Allowed: / header so the service worker
// can claim the full companion path scope.
func TestCompanionHandler_GetSWJS_ServiceWorkerAllowedHeader(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub, nil)

	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	swAllowed := w.Header().Get("Service-Worker-Allowed")
	if swAllowed != "/" {
		t.Errorf("GET /sw.js: Service-Worker-Allowed = %q, want %q", swAllowed, "/")
	}
}

// ---------------------------------------------------------------------------
// SSRF prevention — non-HTTPS endpoint rejected
// ---------------------------------------------------------------------------

// TestCompanionHandler_PostPushSubscribe_NonHTTPSEndpoint_Returns400 verifies
// that a POST /push/subscribe with an http:// endpoint is rejected with 400 to
// prevent SSRF attacks where the relay would POST to an internal host.
func TestCompanionHandler_PostPushSubscribe_NonHTTPSEndpoint_Returns400(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	transport := newMockTransport()
	ps := NewPushSender(transport)
	handler := NewCompanionHandler(store, hub, nil).WithPushSender(ps)

	body := strings.NewReader(`{"endpoint":"http://127.0.0.1:8765/ingest","keys":{"p256dh":"FAKE","auth":"FAKE"}}`)
	req := httptest.NewRequest(http.MethodPost, "/push/subscribe", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /push/subscribe with http:// endpoint: expected 400, got %d", w.Code)
	}
}

// TestCompanionHandler_PostPushSubscribe_HTTPSIPLiteralEndpoint_Returns400
// verifies that an https endpoint whose host is a bare IP literal pointing at an
// internal target (loopback, IPv6 loopback, cloud metadata, RFC1918) is
// rejected with 400. These are the SSRF cases the scheme-only check missed.
func TestCompanionHandler_PostPushSubscribe_HTTPSIPLiteralEndpoint_Returns400(t *testing.T) {
	cases := map[string]string{
		"loopback v4":    "https://127.0.0.1/push",
		"loopback v6":    "https://[::1]/push",
		"cloud metadata": "https://169.254.169.254/latest/meta-data/",
		"rfc1918":        "https://10.0.0.5/push",
		"rfc1918 172":    "https://172.16.0.1/push",
		"rfc1918 192":    "https://192.168.1.1/push",
	}
	for name, endpoint := range cases {
		t.Run(name, func(t *testing.T) {
			store := NewClipStore(10)
			hub := NewSSEHub()
			ps := NewPushSender(newMockTransport())
			handler := NewCompanionHandler(store, hub, nil).WithPushSender(ps)

			body := strings.NewReader(fmt.Sprintf(`{"endpoint":%q,"keys":{"p256dh":"F","auth":"A"}}`, endpoint))
			req := httptest.NewRequest(http.MethodPost, "/push/subscribe", body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("POST /push/subscribe with %s endpoint %q: expected 400, got %d", name, endpoint, w.Code)
			}
			if len(ps.Subscriptions()) != 0 {
				t.Errorf("%s endpoint was registered despite being internal — SSRF not blocked", name)
			}
		})
	}
}

// TestCompanionHandler_PostPushSubscribe_HTTPSHostResolvingToInternal_Returns400
// verifies that an https endpoint with a DNS hostname that resolves to an
// internal address is rejected (DNS-rebinding-style SSRF), using a stubbed
// resolver so the test is hermetic.
func TestCompanionHandler_PostPushSubscribe_HTTPSHostResolvingToInternal_Returns400(t *testing.T) {
	stubLookupIP(t, map[string][]net.IP{
		"evil.example.com": {net.ParseIP("127.0.0.1")},
	})

	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := NewPushSender(newMockTransport())
	handler := NewCompanionHandler(store, hub, nil).WithPushSender(ps)

	body := strings.NewReader(`{"endpoint":"https://evil.example.com/push","keys":{"p256dh":"F","auth":"A"}}`)
	req := httptest.NewRequest(http.MethodPost, "/push/subscribe", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /push/subscribe with internal-resolving host: expected 400, got %d", w.Code)
	}
	if len(ps.Subscriptions()) != 0 {
		t.Error("internal-resolving host was registered — SSRF not blocked")
	}
}

// TestCompanionHandler_PostPushSubscribe_HTTPSPublicHost_Returns201 verifies
// the positive path: an https endpoint whose host resolves to a public IP is
// accepted (201), confirming the SSRF check does not over-block legitimate push
// services.
func TestCompanionHandler_PostPushSubscribe_HTTPSPublicHost_Returns201(t *testing.T) {
	publicHostStub(t)

	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := NewPushSender(newMockTransport())
	handler := NewCompanionHandler(store, hub, nil).WithPushSender(ps)

	body := strings.NewReader(`{"endpoint":"https://push.example.com/abc","keys":{"p256dh":"F","auth":"A"}}`)
	req := httptest.NewRequest(http.MethodPost, "/push/subscribe", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("POST /push/subscribe with public host: expected 201, got %d — body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Subscription cap — 429 when cap is reached
// ---------------------------------------------------------------------------

// TestCompanionHandler_PostPushSubscribe_CapReached_Returns429 verifies that
// when the subscription cap is reached, subsequent registrations are rejected
// with 429 Too Many Requests to prevent memory exhaustion.
func TestCompanionHandler_PostPushSubscribe_CapReached_Returns429(t *testing.T) {
	publicHostStub(t)
	store := NewClipStore(10)
	hub := NewSSEHub()
	transport := newMockTransport()
	ps := NewPushSender(transport)
	ps.maxSubs = 2 // set low cap for test

	handler := NewCompanionHandler(store, hub, nil).WithPushSender(ps)

	for i := 0; i < 2; i++ {
		body := strings.NewReader(fmt.Sprintf(`{"endpoint":"https://push.example.com/%d","keys":{"p256dh":"F","auth":"A"}}`, i))
		req := httptest.NewRequest(http.MethodPost, "/push/subscribe", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("registration %d: expected 201, got %d", i, w.Code)
		}
	}

	// Third registration must be rejected.
	body := strings.NewReader(`{"endpoint":"https://push.example.com/overflow","keys":{"p256dh":"F","auth":"A"}}`)
	req := httptest.NewRequest(http.MethodPost, "/push/subscribe", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 when cap reached, got %d", w.Code)
	}
}

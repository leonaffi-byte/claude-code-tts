package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestCompanionHandler_GetRoot_Returns200WithHTML verifies that GET / serves
// the companion HTML page with the correct status and Content-Type.
func TestCompanionHandler_GetRoot_Returns200WithHTML(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub)

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
	handler := NewCompanionHandler(store, hub)

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
	handler := NewCompanionHandler(store, hub)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(w, req)
	}()

	// Allow the handler a moment to register the subscriber.
	time.Sleep(20 * time.Millisecond)

	count := hub.Count()

	cancel() // disconnect the SSE client
	<-done

	if count != 1 {
		t.Errorf("expected hub.Count() == 1 while /events is connected, got %d", count)
	}
}

// TestCompanionHandler_GetEvents_BroadcastArrivesOnResponse verifies that
// a hub.Broadcast reaches the SSE response body while a client is connected.
func TestCompanionHandler_GetEvents_BroadcastArrivesOnResponse(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(w, req)
	}()

	// Wait for the subscriber to register.
	time.Sleep(20 * time.Millisecond)

	hub.Broadcast("new-clip", `{"id":"test-id"}`)

	// Wait briefly for the event to flush into the recorder.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	want := "event: new-clip\ndata: {\"id\":\"test-id\"}\n\n"
	if !strings.Contains(body, want) {
		t.Errorf("GET /events response body missing expected SSE event:\ngot:  %q\nwant substring: %q", body, want)
	}
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
	handler := NewCompanionHandler(store, hub)

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

	if string(w.Body.Bytes()) != string(audioBytes) {
		t.Errorf("GET /clips/%s: body bytes mismatch: got %v, want %v", id, w.Body.Bytes(), audioBytes)
	}
}

// TestCompanionHandler_GetClip_UnknownID_Returns404 verifies that requesting
// a clip that does not exist returns 404 Not Found.
func TestCompanionHandler_GetClip_UnknownID_Returns404(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewCompanionHandler(store, hub)

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
	handler := NewCompanionHandler(store, hub)

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
	handler := NewCompanionHandler(store, hub)

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

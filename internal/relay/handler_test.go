package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Mock synthesizer — defines the contract the handler depends on
// ---------------------------------------------------------------------------

type mockSynthesizer struct {
	audioData  []byte
	err        error
	called     bool
	calledWith string
}

func (m *mockSynthesizer) Synthesize(_ context.Context, text string) ([]byte, error) {
	m.called = true
	m.calledWith = text
	return m.audioData, m.err
}

// ---------------------------------------------------------------------------
// POST /ingest
// ---------------------------------------------------------------------------

func TestHandler_PostIngest_Returns200WithID(t *testing.T) {
	mock := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	handler := NewHandler(mock, store, NewSSEHub())

	body := bytes.NewBufferString(`{"text": "hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 but got %d", w.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	id, ok := resp["id"]
	if !ok || id == "" {
		t.Errorf("expected non-empty 'id' field in JSON response, got: %v", resp)
	}
}

func TestHandler_PostIngest_CallsSynthesizerWithProvidedText(t *testing.T) {
	mock := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	handler := NewHandler(mock, store, NewSSEHub())

	body := bytes.NewBufferString(`{"text": "synthesize this"}`)
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if !mock.called {
		t.Error("expected Synthesizer.Synthesize to be called but it was not")
	}
	if mock.calledWith != "synthesize this" {
		t.Errorf("Synthesize called with %q, want %q", mock.calledWith, "synthesize this")
	}
}

func TestHandler_PostIngest_SynthesisError_Returns500(t *testing.T) {
	mock := &mockSynthesizer{err: errors.New("openai unavailable")}
	store := NewClipStore(10)
	handler := NewHandler(mock, store, NewSSEHub())

	body := bytes.NewBufferString(`{"text": "hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Must not panic.
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500 on synthesis error but got %d", w.Code)
	}
}

func TestHandler_PostIngest_StoresClipReturnedBySynthesizer(t *testing.T) {
	expectedAudio := []byte{0xFF, 0xFB, 0x90, 0x64}
	mock := &mockSynthesizer{audioData: expectedAudio}
	store := NewClipStore(10)
	handler := NewHandler(mock, store, NewSSEHub())

	body := bytes.NewBufferString(`{"text": "store me"}`)
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 but got %d", w.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}

	id := resp["id"]
	got, ok := store.Get(id)
	if !ok {
		t.Fatalf("clip %q was not stored in the ClipStore after a successful ingest", id)
	}
	if string(got) != string(expectedAudio) {
		t.Errorf("stored bytes mismatch: got %v, want %v", got, expectedAudio)
	}
}

// ---------------------------------------------------------------------------
// GET /clips/{id}
// ---------------------------------------------------------------------------

func TestHandler_GetClip_ExistingID_Returns200WithMP3Bytes(t *testing.T) {
	expectedAudio := []byte{0xFF, 0xFB, 0x90, 0x00}
	store := NewClipStore(10)
	id, err := store.Add(expectedAudio)
	if err != nil {
		t.Fatalf("store.Add failed: %v", err)
	}

	mock := &mockSynthesizer{}
	handler := NewHandler(mock, store, NewSSEHub())

	req := httptest.NewRequest(http.MethodGet, "/clips/"+id, nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 but got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "audio/mpeg") {
		t.Errorf("expected Content-Type audio/mpeg but got %q", ct)
	}

	if w.Body.String() != string(expectedAudio) {
		t.Errorf("response body bytes mismatch: got %v, want %v", w.Body.Bytes(), expectedAudio)
	}
}

func TestHandler_GetClip_MissingID_Returns404(t *testing.T) {
	store := NewClipStore(10)
	mock := &mockSynthesizer{}
	handler := NewHandler(mock, store, NewSSEHub())

	req := httptest.NewRequest(http.MethodGet, "/clips/does-not-exist", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404 but got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Additional edge cases — POST /ingest
// ---------------------------------------------------------------------------

// TestHandler_PostIngest_MissingTextField verifies that a JSON body without a
// "text" key (or with an empty string) returns 400 Bad Request.
func TestHandler_PostIngest_MissingTextField_Returns400(t *testing.T) {
	mock := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	handler := NewHandler(mock, store, NewSSEHub())

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing text field but got %d", w.Code)
	}
	if mock.called {
		t.Error("Synthesizer must not be called when text field is absent")
	}
}

// TestHandler_PostIngest_EmptyBody verifies that an empty request body returns
// 400 Bad Request (JSON decoder reports an error).
func TestHandler_PostIngest_EmptyBody_Returns400(t *testing.T) {
	mock := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	handler := NewHandler(mock, store, NewSSEHub())

	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty body but got %d", w.Code)
	}
	if mock.called {
		t.Error("Synthesizer must not be called when body is empty")
	}
}

// TestHandler_PostIngest_WrongMethod_Returns405 verifies that sending a PUT to
// /ingest returns 405 Method Not Allowed.
func TestHandler_PostIngest_WrongMethod_Returns405(t *testing.T) {
	mock := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	handler := NewHandler(mock, store, NewSSEHub())

	body := bytes.NewBufferString(`{"text": "hello"}`)
	req := httptest.NewRequest(http.MethodPut, "/ingest", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for PUT /ingest but got %d", w.Code)
	}
	if mock.called {
		t.Error("Synthesizer must not be called for wrong method")
	}
}

// ---------------------------------------------------------------------------
// Additional edge cases — GET /clips/
// ---------------------------------------------------------------------------

// TestHandler_GetClip_EmptyID_Returns404 verifies that GET /clips/ (no ID after
// the slash) returns 404 rather than panicking or returning 200.
func TestHandler_GetClip_EmptyID_Returns404(t *testing.T) {
	store := NewClipStore(10)
	mock := &mockSynthesizer{}
	handler := NewHandler(mock, store, NewSSEHub())

	req := httptest.NewRequest(http.MethodGet, "/clips/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for GET /clips/ with empty ID but got %d", w.Code)
	}
}

// TestHandler_GetClip_WrongMethod_Returns405 verifies that POST /clips/{id}
// returns 405 Method Not Allowed.
func TestHandler_GetClip_WrongMethod_Returns405(t *testing.T) {
	store := NewClipStore(10)
	id, err := store.Add([]byte("audio"))
	if err != nil {
		t.Fatalf("store.Add failed: %v", err)
	}

	mock := &mockSynthesizer{}
	handler := NewHandler(mock, store, NewSSEHub())

	req := httptest.NewRequest(http.MethodPost, "/clips/"+id, nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST /clips/{id} but got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Unknown route
// ---------------------------------------------------------------------------

// TestHandler_UnknownRoute_Returns404 verifies that an unrecognised path
// returns 404 and does not route to any handler.
func TestHandler_UnknownRoute_Returns404(t *testing.T) {
	store := NewClipStore(10)
	mock := &mockSynthesizer{}
	handler := NewHandler(mock, store, NewSSEHub())

	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown route but got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Hub integration
// ---------------------------------------------------------------------------

// TestHandler_PostIngest_BroadcastsToHub verifies that a successful POST
// /ingest sends a "new-clip" SSE event to any subscriber on the hub.
func TestHandler_PostIngest_BroadcastsToHub(t *testing.T) {
	mock := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewHandler(mock, store, hub)

	_, ch, cancel := hub.Subscribe()
	defer cancel()

	body := bytes.NewBufferString(`{"text": "broadcast me"}`)
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /ingest: expected 200, got %d", w.Code)
	}

	select {
	case msg := <-ch:
		if !strings.Contains(msg, "event: new-clip\n") {
			t.Errorf("hub broadcast message missing expected SSE event prefix:\ngot: %q", msg)
		}
		if !strings.Contains(msg, `"id"`) {
			t.Errorf("hub broadcast message missing 'id' field:\ngot: %q", msg)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timed out waiting for hub broadcast after POST /ingest")
	}
}

// ---------------------------------------------------------------------------
// POST /rotate-token
// ---------------------------------------------------------------------------

func TestHandler_PostRotateToken_Returns200WithNewToken(t *testing.T) {
	dir := t.TempDir()
	ts := NewTokenStore(filepath.Join(dir, "token"))
	synth := &mockSynthesizer{}
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewHandler(synth, store, hub).WithTokenStore(ts)

	req := httptest.NewRequest(http.MethodPost, "/rotate-token", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST /rotate-token: expected 200, got %d", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if resp["token"] == "" {
		t.Error("expected non-empty token in response")
	}
}

func TestHandler_PostRotateToken_WrongMethod_Returns405(t *testing.T) {
	dir := t.TempDir()
	ts := NewTokenStore(filepath.Join(dir, "token"))
	synth := &mockSynthesizer{}
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewHandler(synth, store, hub).WithTokenStore(ts)

	req := httptest.NewRequest(http.MethodGet, "/rotate-token", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /rotate-token: expected 405, got %d", w.Code)
	}
}

// TestHandler_PostRotateToken_NilTokenStore_Returns500 verifies that calling
// POST /rotate-token when the handler was constructed with a nil TokenStore
// returns 500 rather than panicking. NewHandler documents that ts may be nil.
func TestHandler_PostRotateToken_NilTokenStore_Returns500(t *testing.T) {
	synth := &mockSynthesizer{}
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewHandler(synth, store, hub) // no TokenStore wired

	req := httptest.NewRequest(http.MethodPost, "/rotate-token", nil)
	w := httptest.NewRecorder()

	// Must not panic.
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("POST /rotate-token with nil TokenStore: expected 500, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Push sender integration — Tests H and I
// ---------------------------------------------------------------------------

// TestHandler_PostIngest_CallsPushSenderWithNewClipID verifies that after a
// successful ingest the handler calls the push sender's Send method with the
// newly minted clip ID. This is the trigger that delivers a background push
// notification to subscribed phones.
func TestHandler_PostIngest_CallsPushSenderWithNewClipID(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}

	handler := NewHandler(synth, store, hub).WithPushSender(ps)

	body := bytes.NewBufferString(`{"text": "hello push"}`)
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /ingest: expected 200, got %d", w.Code)
	}

	// Retrieve the clip ID from the response so we can verify the push call.
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	id := resp["id"]
	if id == "" {
		t.Fatal("response JSON has no 'id' field")
	}

	// Send runs on a detached goroutine; wait for it before inspecting the call.
	ps.waitForSendCalls(t, 1)
	if got := ps.sendCall(0).clipID; got != id {
		t.Errorf("push Send called with clipID %q, want %q", got, id)
	}
}

// TestHandler_PostIngest_ClipURLInPushPayload_StartsWithBaseURL verifies that
// when a clipBaseURL is configured, the push sender receives a clipURL that
// begins with "<clipBaseURL>/clips/" followed by the clip ID. This ensures
// the service worker can reconstruct the fetch URL from the push payload.
func TestHandler_PostIngest_ClipURLInPushPayload_StartsWithBaseURL(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	ps := &mockPushSender{}

	const baseURL = "https://relay.example.com"
	handler := NewHandler(synth, store, hub).
		WithPushSender(ps).
		WithClipBaseURL(baseURL)

	body := bytes.NewBufferString(`{"text": "check clip url"}`)
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /ingest: expected 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	id := resp["id"]
	if id == "" {
		t.Fatal("response JSON has no 'id' field")
	}

	// Send runs on a detached goroutine; wait for it before inspecting the call.
	ps.waitForSendCalls(t, 1)

	wantURL := baseURL + "/clips/" + id
	gotURL := ps.sendCall(0).clipURL
	if gotURL != wantURL {
		t.Errorf("push clipURL = %q, want %q", gotURL, wantURL)
	}
}

// TestHandler_PostIngest_NilPushSender_DoesNotPanic verifies that when no push
// sender is configured (nil), a successful ingest still returns 200 without
// panicking. Push is an optional dependency.
func TestHandler_PostIngest_NilPushSender_DoesNotPanic(t *testing.T) {
	synth := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	hub := NewSSEHub()

	// Deliberately no WithPushSender call — push sender is nil.
	handler := NewHandler(synth, store, hub)

	body := bytes.NewBufferString(`{"text": "hello, no push"}`)
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Must not panic.
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST /ingest with nil push sender: expected 200, got %d", w.Code)
	}
}

// TestHandler_PostIngest_BroadcastsNewClipEventToHub verifies that after a
// successful synthesis and store, the Handler broadcasts a "new-clip" SSE event
// to the hub. This ensures companion clients receive real-time notifications.
func TestHandler_PostIngest_BroadcastsNewClipEventToHub(t *testing.T) {
	mock := &mockSynthesizer{audioData: []byte("fake-mp3")}
	store := NewClipStore(10)
	hub := NewSSEHub()
	handler := NewHandler(mock, store, hub)

	// Subscribe before the ingest so we receive the broadcast.
	_, ch, cancel := hub.Subscribe()
	defer cancel()

	body := bytes.NewBufferString(`{"text": "broadcast me"}`)
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from ingest, got %d", w.Code)
	}

	// Drain the channel; the broadcast is non-blocking so it may be buffered.
	select {
	case msg, ok := <-ch:
		if !ok {
			t.Fatal("subscriber channel was closed before receiving a broadcast")
		}
		if !strings.Contains(msg, "new-clip") {
			t.Errorf("broadcast message %q does not contain expected event %q", msg, "new-clip")
		}
	default:
		t.Error("hub received no broadcast after successful ingest")
	}
}

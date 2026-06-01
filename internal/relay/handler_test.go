package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/tts"
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

func (m *mockSynthesizer) Synthesize(text string, voice tts.Voice) ([]byte, error) {
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

	if string(w.Body.Bytes()) != string(expectedAudio) {
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
// Hub integration — NewHandler wires hub to POST /ingest
// ---------------------------------------------------------------------------

// TestHandler_PostIngest_BroadcastsToHub verifies that a successful POST
// /ingest via NewHandler sends a "new-clip" SSE event to any
// subscriber on the hub. The test subscribes to the hub, issues the request,
// then waits up to 100 ms for the message to arrive on the subscriber channel.
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

	// The ingest handler broadcasts synchronously before returning, so the
	// message should already be in the buffered channel. Use a 100 ms timeout
	// as a safety net.
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

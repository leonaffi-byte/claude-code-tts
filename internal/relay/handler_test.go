package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	handler := NewHandler(mock, store)

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
	handler := NewHandler(mock, store)

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
	handler := NewHandler(mock, store)

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
	handler := NewHandler(mock, store)

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
	handler := NewHandler(mock, store)

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
	handler := NewHandler(mock, store)

	req := httptest.NewRequest(http.MethodGet, "/clips/does-not-exist", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404 but got %d", w.Code)
	}
}

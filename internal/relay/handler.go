package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

// Handler wires an HTTP mux for the relay endpoints.
// It depends on a Synthesizer and a ClipStore, both injected at construction
// time so tests can supply mocks without touching real network or disk.
// The optional hub field, when non-nil, receives a broadcast after each
// successful ingest so SSE subscribers are notified immediately.
type Handler struct {
	synth Synthesizer
	store *ClipStore
	hub   *SSEHub // nil if not wired; broadcast is skipped when nil
}

// NewHandler creates an HTTP handler backed by the given Synthesizer and store.
// No SSE hub is wired; use NewHandlerWithHub when SSE broadcasts are needed.
func NewHandler(synth Synthesizer, store *ClipStore) *Handler {
	return &Handler{synth: synth, store: store}
}

// NewHandlerWithHub creates an HTTP handler backed by the given Synthesizer,
// store, and SSEHub. After each successful ingest the handler broadcasts a
// "new-clip" event to all hub subscribers.
func NewHandlerWithHub(synth Synthesizer, store *ClipStore, hub *SSEHub) *Handler {
	return &Handler{synth: synth, store: store, hub: hub}
}

// ServeHTTP implements http.Handler by routing requests to the correct handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/ingest":
		h.handleIngest(w, r)
	case strings.HasPrefix(r.URL.Path, "/clips/"):
		h.handleClip(w, r)
	default:
		http.NotFound(w, r)
	}
}

// ingestRequest is the JSON body expected by POST /ingest.
type ingestRequest struct {
	Text string `json:"text"`
}

// handleIngest processes POST /ingest: synthesizes text and stores the clip.
func (h *Handler) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<16) // 64 KB limit

	var req ingestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if req.Text == "" {
		http.Error(w, "text field is required and must not be empty", http.StatusBadRequest)
		return
	}

	audioBytes, err := h.synth.Synthesize(req.Text, tts.VoiceAlloy)
	if err != nil {
		logging.Error("synthesis failed: %v", err)
		http.Error(w, "synthesis failed", http.StatusInternalServerError)
		return
	}

	id, err := h.store.Add(audioBytes)
	if err != nil {
		logging.Error("failed to store clip: %v", err)
		http.Error(w, "failed to store clip", http.StatusInternalServerError)
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("new-clip", fmt.Sprintf(`{"id":"%s"}`, id))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id}) //nolint:errcheck
}

// handleClip processes GET /clips/{id}: retrieves and streams a stored MP3 clip.
func (h *Handler) handleClip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/clips/")
	serveClip(w, h.store, id)
}

// serveClip writes the stored clip identified by id to w, or 404 if not found.
// An empty id is treated as not found. Method checking is the caller's responsibility.
func serveClip(w http.ResponseWriter, store *ClipStore, id string) {
	if id == "" {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}

	data, ok := store.Get(id)
	if !ok {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Write(data) //nolint:errcheck
}

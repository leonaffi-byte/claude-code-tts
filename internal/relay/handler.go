package relay

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

// Handler wires an HTTP mux for the relay endpoints.
// It depends on a Synthesizer, ClipStore, SSEHub, and optional TokenStore, all
// injected at construction time so tests can supply mocks without touching real
// network or disk.
type Handler struct {
	synth     Synthesizer
	store     *ClipStore
	hub       *SSEHub     // nil-safe; broadcast is skipped when nil
	ts        *TokenStore // nil-safe; rotation disabled when nil
	qrPrinter func(string) error
}

// NewHandler creates an HTTP handler backed by the given Synthesizer, store, and hub.
func NewHandler(synth Synthesizer, store *ClipStore, hub *SSEHub) *Handler {
	return &Handler{synth: synth, store: store, hub: hub}
}

// ServeHTTP implements http.Handler by routing requests to the correct handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/ingest":
		h.handleIngest(w, r)
	case r.URL.Path == "/rotate-token":
		h.handleRotateToken(w, r)
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
		payload, _ := json.Marshal(map[string]string{"id": id})
		h.hub.Broadcast("new-clip", string(payload))
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

// handleRotateToken processes POST /rotate-token: rotates the auth token.
func (h *Handler) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.ts == nil {
		http.Error(w, "token rotation not configured", http.StatusInternalServerError)
		return
	}

	token, err := h.ts.Rotate()
	if err != nil {
		http.Error(w, "rotation failed", http.StatusInternalServerError)
		return
	}

	if h.qrPrinter != nil {
		if err := h.qrPrinter(token); err != nil {
			logging.Error("QR print failed after rotation: %v", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": token}) //nolint:errcheck
}

// WithTokenStore attaches a TokenStore to this handler, enabling POST /rotate-token.
func (h *Handler) WithTokenStore(ts *TokenStore) *Handler {
	h.ts = ts
	return h
}

// WithQRPrinter attaches a callback that is called with the new token after rotation.
func (h *Handler) WithQRPrinter(fn func(string) error) *Handler {
	h.qrPrinter = fn
	return h
}

// serveClip writes the stored clip identified by id to w, or 404 if not found.
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

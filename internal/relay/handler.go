package relay

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
)

// Handler wires an HTTP mux for the relay endpoints.
// It depends on a Synthesizer, ClipStore, SSEHub, and optional TokenStore, all
// injected at construction time so tests can supply mocks without touching real
// network or disk.
type Handler struct {
	synth           Synthesizer
	store           *ClipStore
	hub             *SSEHub     // nil-safe; broadcast is skipped when nil
	ts              *TokenStore // nil-safe; rotation disabled when nil
	qrPrinter       func(string) error
	pushSender      PushSenderIface // nil-safe; push is skipped when nil
	clipBaseURL     string          // base URL prepended to clip IDs in push payloads
	presenceTracker PresenceTracker // nil-safe; when nil, push is never suppressed
}

// NewHandler creates an HTTP handler backed by the given Synthesizer, store, and hub.
func NewHandler(synth Synthesizer, store *ClipStore, hub *SSEHub) *Handler {
	return &Handler{synth: synth, store: store, hub: hub}
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

// WithPushSender attaches an optional push sender that is called after each
// successful ingest to deliver a background Web Push notification.
// Passing nil disables push (useful in tests that don't need push).
func (h *Handler) WithPushSender(ps PushSenderIface) *Handler {
	h.pushSender = ps
	return h
}

// WithClipBaseURL sets the base URL prepended to clip IDs in push payloads
// (e.g. "https://relay.example.com"). When empty, clipURL in the push payload
// is also empty; the service worker degrades gracefully.
func (h *Handler) WithClipBaseURL(url string) *Handler {
	h.clipBaseURL = url
	return h
}

// WithPresenceTracker attaches a PresenceTracker that is consulted on each
// ingest to decide whether to suppress the Web Push notification. When the
// tracker reports a live foreground listener, push is suppressed (the clip
// already auto-plays via SSE). When nil, push is never suppressed (legacy
// behaviour).
func (h *Handler) WithPresenceTracker(pt PresenceTracker) *Handler {
	h.presenceTracker = pt
	return h
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

	audioBytes, err := h.synth.Synthesize(r.Context(), req.Text)
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

	if h.pushSender != nil {
		// Suppress push when a foreground SSE listener is connected — the clip
		// already auto-plays in the companion. Send push only when no live
		// listener is present (AFK / companion not open).
		if h.presenceTracker == nil || !h.presenceTracker.HasLiveListener() {
			clipURL := ""
			if h.clipBaseURL != "" {
				clipURL = h.clipBaseURL + "/clips/" + id
			}
			if sendErr := h.pushSender.Send(id, clipURL); sendErr != nil {
				logging.Error("push send failed: %v", sendErr)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id}) //nolint:errcheck
}

// handleRotateToken processes POST /rotate-token: generates a new token and
// returns it as JSON. The old token is immediately invalidated.
// Returns 500 when no TokenStore was provided (ts is nil).
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
		if printErr := h.qrPrinter(token); printErr != nil {
			logging.Error("QR print after rotation failed: %v", printErr)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": token}) //nolint:errcheck
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

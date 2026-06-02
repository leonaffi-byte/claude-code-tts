package relay

import (
	_ "embed"
	"fmt"
	"net/http"
	"strings"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
)

//go:embed web/companion.html
var companionHTML []byte

// CompanionHandler serves the public companion page, SSE event stream, and
// clip proxy. It is intended to run on a separate public-facing port.
type CompanionHandler struct {
	store *ClipStore
	hub   *SSEHub
	ts    *TokenStore
}

// NewCompanionHandler creates a CompanionHandler backed by the given ClipStore,
// SSEHub, and token store. The token store is consulted on each manifest
// request so that token rotation is reflected immediately in the PWA start_url.
func NewCompanionHandler(store *ClipStore, hub *SSEHub, ts *TokenStore) *CompanionHandler {
	return &CompanionHandler{store: store, hub: hub, ts: ts}
}

// ServeHTTP routes requests to the companion page, SSE stream, or clip proxy.
func (h *CompanionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/":
		h.handleRoot(w, r)
	case r.URL.Path == "/events":
		h.handleEvents(w, r)
	case r.URL.Path == "/manifest.json":
		h.serveManifest(w, r)
	case strings.HasPrefix(r.URL.Path, "/clips/"):
		h.handleClip(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleRoot serves the embedded companion HTML page.
func (h *CompanionHandler) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(http.StatusOK)
	w.Write(companionHTML) //nolint:errcheck
}

// handleEvents streams SSE new-clip events to the client until the client
// disconnects or the request context is cancelled.
func (h *CompanionHandler) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		logging.Error("streaming unsupported: ResponseWriter does not implement http.Flusher")
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	_, ch, cancel := h.hub.Subscribe()
	if ch == nil {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, msg) //nolint:errcheck
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleClip retrieves a stored clip by ID and streams its bytes to the client.
func (h *CompanionHandler) handleClip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/clips/")
	serveClip(w, h.store, id)
}

// serveManifest generates and writes a PWA manifest JSON with the token
// embedded in the start_url so installed PWAs reload authenticated.
func (h *CompanionHandler) serveManifest(w http.ResponseWriter, r *http.Request) {
	token := ""
	if h.ts != nil {
		token = h.ts.Current()
	}
	startURL := "/" + token + "/"
	manifest := fmt.Sprintf(`{
  "name": "TTS Companion",
  "short_name": "TTS",
  "start_url": %q,
  "display": "standalone",
  "background_color": "#000000",
  "theme_color": "#000000",
  "icons": []
}`, startURL)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, manifest) //nolint:errcheck
}

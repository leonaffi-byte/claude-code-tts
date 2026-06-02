package relay

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
)

//go:embed web/companion.html
var companionHTML []byte

//go:embed web/sw.js
var serviceWorkerJS []byte

// CompanionHandler serves the public companion page, SSE event stream, clip
// proxy, push subscription endpoint, VAPID public key endpoint, and service
// worker. It is intended to run on a separate public-facing port.
type CompanionHandler struct {
	store      *ClipStore
	hub        *SSEHub
	ts         *TokenStore
	pushSender PushSenderIface // nil-safe; subscription endpoint returns 503 when nil
	vapidStore *VAPIDStore     // nil-safe; public-key endpoint returns 503 when nil
}

// NewCompanionHandler creates a CompanionHandler backed by the given ClipStore,
// SSEHub, and token store. The token store is consulted on each manifest
// request so that token rotation is reflected immediately in the PWA start_url.
func NewCompanionHandler(store *ClipStore, hub *SSEHub, ts *TokenStore) *CompanionHandler {
	return &CompanionHandler{store: store, hub: hub, ts: ts}
}

// WithPushSender attaches an optional push sender so the companion can accept
// Web Push subscription registrations via POST /push/subscribe.
func (h *CompanionHandler) WithPushSender(ps PushSenderIface) *CompanionHandler {
	h.pushSender = ps
	return h
}

// WithVAPIDStore attaches an optional VAPID store so the companion can serve
// the VAPID public key via GET /push/vapid-public-key.
func (h *CompanionHandler) WithVAPIDStore(vs *VAPIDStore) *CompanionHandler {
	h.vapidStore = vs
	return h
}

// ServeHTTP routes requests to the companion page, SSE stream, clip proxy,
// push subscription, VAPID key, or service worker endpoints.
func (h *CompanionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/":
		h.handleRoot(w, r)
	case r.URL.Path == "/events":
		h.handleEvents(w, r)
	case r.URL.Path == "/manifest.json":
		h.serveManifest(w, r)
	case r.URL.Path == "/push/subscribe":
		h.handlePushSubscribe(w, r)
	case r.URL.Path == "/push/vapid-public-key":
		h.handleVAPIDPublicKey(w, r)
	case r.URL.Path == "/sw.js":
		h.handleSWJS(w, r)
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

// handlePushSubscribe processes POST /push/subscribe: stores the Web Push
// subscription from the phone so future clips trigger a background push.
// Returns 405 for non-POST, 503 when no PushSender is wired, 400 for invalid
// JSON or missing endpoint, and 200 on success.
func (h *CompanionHandler) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.pushSender == nil {
		http.Error(w, "push not configured", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4<<10) // 4 KB limit
	var sub PushSubscription
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if sub.Endpoint == "" {
		http.Error(w, "endpoint field is required", http.StatusBadRequest)
		return
	}

	h.pushSender.AddSubscription(sub)
	w.WriteHeader(http.StatusOK)
}

// handleVAPIDPublicKey processes GET /push/vapid-public-key: returns the VAPID
// public key as plain text for use in PushManager.subscribe(). Returns 503
// when no VAPIDStore is wired.
func (h *CompanionHandler) handleVAPIDPublicKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.vapidStore == nil {
		http.Error(w, "VAPID not configured", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, h.vapidStore.PublicKey()) //nolint:errcheck
}

// handleSWJS serves the embedded service worker script with the scope header
// required for it to intercept push events on the companion's token-scoped path.
func (h *CompanionHandler) handleSWJS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Service-Worker-Allowed: / grants the SW scope over the entire origin path
	// after the auth middleware has already stripped the token prefix.
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Service-Worker-Allowed", "/")
	w.WriteHeader(http.StatusOK)
	w.Write(serviceWorkerJS) //nolint:errcheck
}

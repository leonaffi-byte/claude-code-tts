package relay

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
)

// cspNoncePlaceholder is the token in the embedded companion HTML that is
// replaced per-response with a fresh CSP nonce so the inline <script> can run
// under script-src 'self' 'nonce-…' without enabling 'unsafe-inline'.
const cspNoncePlaceholder = "__CSP_NONCE__"

// sseHeartbeatInterval controls how often a comment frame is written on an idle
// SSE connection. A failed write detects a silently-dropped peer (e.g. a phone
// that slept or a NAT idle timeout) so the goroutine returns and the hub
// subscriber slot is released. Without this, a half-open connection produces no
// write between broadcasts, r.Context() is never cancelled, and the slot leaks.
//
// It is a package variable (not a const) so tests can shorten it.
var sseHeartbeatInterval = 20 * time.Second

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

	// Generate a per-response nonce so the inline <script> can run under a strict
	// CSP without 'unsafe-inline'. An attacker who injects markup cannot guess
	// the nonce, so injected inline scripts will not execute.
	nonce, err := newCSPNonce()
	if err != nil {
		logging.Error("companion: failed to generate CSP nonce: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	body := bytes.ReplaceAll(companionHTML, []byte(cspNoncePlaceholder), []byte(nonce))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy",
		fmt.Sprintf("default-src 'self'; script-src 'self' 'nonce-%s'", nonce))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(http.StatusOK)
	w.Write(body) //nolint:errcheck
}

// newCSPNonce returns a base64-encoded 128-bit random nonce suitable for a
// Content-Security-Policy script-src 'nonce-…' source expression.
func newCSPNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
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

	// Periodic heartbeat: writing a comment frame forces a TCP write so a
	// silently-dropped peer surfaces as a write error, letting us return and
	// free the subscriber slot via the deferred cancel().
	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, err := fmt.Fprint(w, msg); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			// SSE comment frame — ignored by clients, but detects dead peers.
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
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
// JSON, missing endpoint, or non-HTTPS endpoint (SSRF prevention), 429 when
// the subscription cap is reached, and 201 on success.
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

	// Reject non-HTTPS endpoints to prevent SSRF.
	u, err := url.Parse(sub.Endpoint)
	if err != nil || u.Scheme != "https" {
		http.Error(w, "endpoint must be an https URL", http.StatusBadRequest)
		return
	}
	// Defence-in-depth against SSRF: reject endpoints that resolve to internal
	// hosts (loopback, RFC1918 private, link-local / cloud metadata) so a
	// registrant cannot make the relay POST to an internal service. IP-literal
	// hosts are rejected outright since legitimate push services use DNS names.
	if err := validatePublicHost(u.Hostname()); err != nil {
		http.Error(w, "endpoint host is not allowed", http.StatusBadRequest)
		return
	}

	if !h.pushSender.AddSubscription(sub) {
		http.Error(w, "subscription limit reached", http.StatusTooManyRequests)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// lookupIP resolves a hostname to its IP addresses. It is a package variable so
// tests can stub DNS resolution and stay hermetic (offline-deterministic).
var lookupIP = net.LookupIP

// validatePublicHost rejects an endpoint host that would let the relay reach an
// internal network (SSRF). It refuses bare IP-literal hosts (legitimate push
// services use DNS names) and resolves DNS names, rejecting any host that maps
// to a loopback, private, or link-local/metadata address.
func validatePublicHost(host string) error {
	if host == "" {
		return fmt.Errorf("empty host")
	}
	// Reject IP-literal hosts outright — real push services use hostnames, and
	// an IP literal is the classic way to point straight at an internal target
	// (e.g. 127.0.0.1, ::1, 169.254.169.254).
	if ip := net.ParseIP(host); ip != nil {
		return fmt.Errorf("ip-literal host not allowed")
	}
	addrs, err := lookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("host %q resolved to no addresses", host)
	}
	for _, ip := range addrs {
		if isInternalIP(ip) {
			return fmt.Errorf("host %q resolves to internal address %s", host, ip)
		}
	}
	return nil
}

// isInternalIP reports whether ip is loopback, private (RFC1918 / ULA),
// link-local (incl. the 169.254.169.254 cloud metadata range), or otherwise
// non-routable on the public internet.
func isInternalIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
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

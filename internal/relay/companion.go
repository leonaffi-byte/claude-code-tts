package relay

import (
	_ "embed"
	"net/http"
)

//go:embed web/companion.html
var companionHTML []byte

// CompanionHandler serves the public companion page, SSE event stream, and
// clip proxy. It is intended to run on a separate public-facing port.
type CompanionHandler struct {
	store *ClipStore
	hub   *SSEHub
}

// NewCompanionHandler creates a CompanionHandler backed by the given ClipStore
// and SSEHub.
func NewCompanionHandler(store *ClipStore, hub *SSEHub) *CompanionHandler {
	return &CompanionHandler{store: store, hub: hub}
}

// ServeHTTP routes requests to the companion page, SSE stream, or clip proxy.
func (h *CompanionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {}

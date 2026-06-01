package relay

import "net/http"

// Handler wires an HTTP mux for the relay endpoints.
// It depends on a Synthesizer and a ClipStore, both injected at construction
// time so tests can supply mocks without touching real network or disk.
type Handler struct {
	synth Synthesizer
	store *ClipStore
}

// NewHandler creates an HTTP handler backed by the given Synthesizer and store.
func NewHandler(synth Synthesizer, store *ClipStore) *Handler {
	return &Handler{synth: synth, store: store}
}

// ServeHTTP implements http.Handler by routing requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// stub — routing not yet implemented
	http.NotFound(w, r)
}

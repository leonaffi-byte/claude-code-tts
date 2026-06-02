package relay

import (
	"context"
	"net/http"
	"time"
)

// Server wraps the ingest http.Server and ClipStore so both can be shut down cleanly.
type Server struct {
	httpServer *http.Server
	store      *ClipStore
	handler    *Handler
}

// NewServer wires together a Handler and http.Server using the provided store,
// hub, and token store. addr is the listen address (e.g. "127.0.0.1:8765"); the
// caller is responsible for using a loopback address and creating the store and
// hub so they can be shared with the CompanionHandler. ts may be nil if token
// rotation is not required.
func NewServer(addr string, synth Synthesizer, store *ClipStore, hub *SSEHub) (*Server, error) {
	handler := NewHandler(synth, store, hub).
		WithPresenceTracker(hub)

	mux := http.NewServeMux()
	mux.Handle("/ingest", handler)
	mux.Handle("/clips/", handler)
	mux.Handle("/rotate-token", handler)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	return &Server{httpServer: srv, store: store, handler: handler}, nil
}

// Handler returns the underlying Handler, allowing post-construction wiring
// (e.g. attaching a TokenStore or QR printer).
func (s *Server) Handler() *Handler {
	return s.handler
}

// Start begins accepting connections. It blocks until the server is closed.
// http.ErrServerClosed is returned on graceful shutdown and should be treated
// as a normal exit condition by callers.
func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully drains in-flight requests within the deadline imposed by
// ctx, then frees clip-store resources.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.httpServer.Shutdown(ctx)
	s.store.Close() //nolint:errcheck
	return err
}

// PublicServer serves the companion page on the public-facing port,
// with token-based auth middleware wrapping the CompanionHandler.
type PublicServer struct {
	httpServer *http.Server
	handler    http.Handler
}

// NewPublicServer creates a PublicServer. Call Serve() to begin listening.
// ts is the live TokenStore so the auth middleware always validates against the
// current token even after a rotation.
func NewPublicServer(ts *TokenStore, companion *CompanionHandler) *PublicServer {
	h := NewAuthMiddleware(ts, companion)
	return &PublicServer{handler: h}
}

// Handler returns the http.Handler (useful for testing without binding a port).
func (s *PublicServer) Handler() http.Handler {
	return s.handler
}

// Serve starts the public server on addr. It blocks until the server closes.
// WriteTimeout is intentionally 0 (disabled) because the /events endpoint is a
// long-lived SSE stream; a finite write timeout would silently kill companion
// connections after the deadline.
func (s *PublicServer) Serve(addr string) error {
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      0, // SSE connections are long-lived; no write deadline
		IdleTimeout:       120 * time.Second,
	}
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the public server.
func (s *PublicServer) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

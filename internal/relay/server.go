package relay

import (
	"context"
	"net/http"
)

// Server wraps an http.Server and the ClipStore so both can be shut down cleanly.
type Server struct {
	httpServer *http.Server
	store      *ClipStore
}

// NewServer wires together a ClipStore, Handler, and http.Server.
// addr is the listen address (e.g. "127.0.0.1:8765"); the caller is responsible
// for using a loopback address. maxClips <= 0 defaults to 10.
func NewServer(addr string, synth Synthesizer, maxClips int) (*Server, error) {
	store := NewClipStore(maxClips)
	handler := NewHandler(synth, store)

	mux := http.NewServeMux()
	mux.HandleFunc("/ingest", handler.handleIngest)
	mux.HandleFunc("/clips/", handler.handleClip)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return &Server{httpServer: srv, store: store}, nil
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

package relay

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Server wraps the private ingest http.Server, the public companion http.Server,
// and the ClipStore so all three can be shut down cleanly together.
type Server struct {
	privateServer *http.Server
	publicServer  *http.Server
	store         *ClipStore
}

// NewServer wires the private ingest listener and the public companion listener.
// privateAddr should be a loopback address (e.g. "127.0.0.1:8765").
// publicAddr binds on all interfaces (e.g. ":8766") so companion pages on the LAN can connect.
// maxClips <= 0 defaults to 10.
func NewServer(privateAddr string, publicAddr string, synth Synthesizer, maxClips int) (*Server, error) {
	store := NewClipStore(maxClips)
	hub := NewSSEHub()

	// Private ingest listener (loopback)
	handler := NewHandlerWithHub(synth, store, hub)
	privateMux := http.NewServeMux()
	privateMux.Handle("/ingest", handler)
	privateMux.Handle("/clips/", handler)
	privateHTTP := &http.Server{
		Addr:              privateAddr,
		Handler:           privateMux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Public companion listener (all interfaces)
	companion := NewCompanionHandler(store, hub)
	publicMux := http.NewServeMux()
	publicMux.Handle("/", companion)
	publicHTTP := &http.Server{
		Addr:              publicAddr,
		Handler:           publicMux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      0, // SSE connections are long-lived; no write deadline
		IdleTimeout:       120 * time.Second,
	}

	return &Server{
		privateServer: privateHTTP,
		publicServer:  publicHTTP,
		store:         store,
	}, nil
}

// Start begins accepting connections on both the private and public listeners.
// It blocks until one of the servers closes, then waits for the other.
// http.ErrServerClosed is treated as a normal exit condition (graceful shutdown).
func (s *Server) Start() error {
	errCh := make(chan error, 2)
	go func() { errCh <- s.privateServer.ListenAndServe() }()
	go func() { errCh <- s.publicServer.ListenAndServe() }()
	err := <-errCh
	if err == http.ErrServerClosed {
		err = <-errCh
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown gracefully drains in-flight requests on both servers concurrently
// within the deadline imposed by ctx, then frees clip-store resources.
func (s *Server) Shutdown(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = s.privateServer.Shutdown(ctx) }()
	go func() { defer wg.Done(); errs[1] = s.publicServer.Shutdown(ctx) }()
	wg.Wait()
	s.store.Close() //nolint:errcheck
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// PublicServer serves the companion page on the public-facing port,
// with token-based auth middleware wrapping the CompanionHandler.
// Used when the caller manages listeners independently (e.g. in tests).
type PublicServer struct {
	httpServer *http.Server
	handler    http.Handler
}

// NewPublicServer creates a PublicServer. Call Serve() to begin listening.
func NewPublicServer(token string, companion *CompanionHandler) *PublicServer {
	h := NewAuthMiddleware(token, companion)
	return &PublicServer{handler: h}
}

// Handler returns the http.Handler (useful for testing without binding a port).
func (s *PublicServer) Handler() http.Handler {
	return s.handler
}

// Serve starts the public server on addr. It blocks until the server closes.
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

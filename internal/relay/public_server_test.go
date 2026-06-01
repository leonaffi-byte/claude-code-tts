package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPublicServer_ValidToken_Returns200 verifies that a request with the
// correct token prefix reaches the companion and returns 200.
func TestPublicServer_ValidToken_Returns200(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	companion := NewCompanionHandler(store, hub, "testtoken")
	ps := NewPublicServer("testtoken", companion)

	req := httptest.NewRequest(http.MethodGet, "/testtoken/", nil)
	w := httptest.NewRecorder()
	ps.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// TestPublicServer_WrongToken_Returns404 verifies that a request with the
// wrong token returns 404.
func TestPublicServer_WrongToken_Returns404(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	companion := NewCompanionHandler(store, hub, "testtoken")
	ps := NewPublicServer("testtoken", companion)

	req := httptest.NewRequest(http.MethodGet, "/wrongtoken/", nil)
	w := httptest.NewRecorder()
	ps.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// TestPublicServer_MissingToken_Returns404 verifies that a bare request
// with no token returns 404.
func TestPublicServer_MissingToken_Returns404(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	companion := NewCompanionHandler(store, hub, "testtoken")
	ps := NewPublicServer("testtoken", companion)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	ps.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// TestPublicServer_Shutdown_NeverStarted_ReturnsNil verifies that calling
// Shutdown on a PublicServer that was never started (httpServer is nil)
// returns nil cleanly without panicking.
func TestPublicServer_Shutdown_NeverStarted_ReturnsNil(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	companion := NewCompanionHandler(store, hub, "testtoken")
	ps := NewPublicServer("testtoken", companion)

	// ps.Serve() was never called — httpServer is nil.
	err := ps.Shutdown(t.Context())
	if err != nil {
		t.Errorf("Shutdown on never-started server: expected nil error, got %v", err)
	}
}

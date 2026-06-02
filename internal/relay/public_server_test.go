package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// tokenStoreWithValue creates a TokenStore backed by a temp file seeded with
// the given token value, simulating a loaded (not fresh-generated) store.
func tokenStoreWithValue(t *testing.T, token string) *TokenStore {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatalf("failed to seed token file: %v", err)
	}
	ts := NewTokenStore(path)
	if _, err := ts.LoadOrGenerate(); err != nil {
		t.Fatalf("LoadOrGenerate failed: %v", err)
	}
	return ts
}

// TestPublicServer_ValidToken_Returns200 verifies that a request with the
// correct token prefix reaches the companion and returns 200.
func TestPublicServer_ValidToken_Returns200(t *testing.T) {
	store := NewClipStore(10)
	hub := NewSSEHub()
	ts := tokenStoreWithValue(t, "testtoken")
	companion := NewCompanionHandler(store, hub, ts)
	ps := NewPublicServer(ts, companion)

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
	ts := tokenStoreWithValue(t, "testtoken")
	companion := NewCompanionHandler(store, hub, ts)
	ps := NewPublicServer(ts, companion)

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
	ts := tokenStoreWithValue(t, "testtoken")
	companion := NewCompanionHandler(store, hub, ts)
	ps := NewPublicServer(ts, companion)

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
	ts := tokenStoreWithValue(t, "testtoken")
	companion := NewCompanionHandler(store, hub, ts)
	ps := NewPublicServer(ts, companion)

	// ps.Serve() was never called — httpServer is nil.
	err := ps.Shutdown(context.Background())
	if err != nil {
		t.Errorf("Shutdown on never-started server: expected nil error, got %v", err)
	}
}

package relay

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Auth middleware — London-school (mock-first) tests
//
// These tests define the expected HTTP contract for NewAuthMiddleware and
// TokenStore before any implementation exists. They MUST fail until
// auth.go is fully implemented.
// ---------------------------------------------------------------------------

// handlerSpy records whether the inner handler was called and what path it saw.
type handlerSpy struct {
	called   bool
	seenPath string
}

func (s *handlerSpy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.called = true
	s.seenPath = r.URL.Path
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Auth middleware
// ---------------------------------------------------------------------------

// TestAuthMiddleware_ValidToken_ForwardsRequest verifies that a request whose
// path begins with the correct token prefix reaches the inner handler and
// receives 200.
func TestAuthMiddleware_ValidToken_ForwardsRequest(t *testing.T) {
	const token = "abc123"
	inner := &handlerSpy{}
	mw := NewAuthMiddleware(token, inner)

	req := httptest.NewRequest(http.MethodGet, "/"+token+"/", nil)
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, req)

	if !inner.called {
		t.Error("inner handler was not called for a valid token prefix")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for valid token, got %d", w.Code)
	}
}

// TestAuthMiddleware_MissingToken_Returns404 verifies that a request to the
// root path (no token prefix at all) is rejected with 404.
func TestAuthMiddleware_MissingToken_Returns404(t *testing.T) {
	const token = "abc123"
	inner := &handlerSpy{}
	mw := NewAuthMiddleware(token, inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, req)

	if inner.called {
		t.Error("inner handler must not be called when the token prefix is absent")
	}
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing token, got %d", w.Code)
	}
}

// TestAuthMiddleware_WrongToken_Returns404 verifies that a request carrying the
// wrong token prefix is rejected with 404.
func TestAuthMiddleware_WrongToken_Returns404(t *testing.T) {
	const token = "abc123"
	inner := &handlerSpy{}
	mw := NewAuthMiddleware(token, inner)

	req := httptest.NewRequest(http.MethodGet, "/wrongtoken/", nil)
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, req)

	if inner.called {
		t.Error("inner handler must not be called for a wrong token")
	}
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for wrong token, got %d", w.Code)
	}
}

// TestAuthMiddleware_ValidToken_StripsPrefix verifies that the token prefix is
// removed from the URL path before the inner handler sees the request, so
// /<token>/events becomes /events.
func TestAuthMiddleware_ValidToken_StripsPrefix(t *testing.T) {
	const token = "abc123"
	inner := &handlerSpy{}
	mw := NewAuthMiddleware(token, inner)

	req := httptest.NewRequest(http.MethodGet, "/"+token+"/events", nil)
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, req)

	if !inner.called {
		t.Fatal("inner handler was not called for a valid token prefix")
	}
	if inner.seenPath != "/events" {
		t.Errorf("inner handler saw path %q, want %q", inner.seenPath, "/events")
	}
}

// ---------------------------------------------------------------------------
// TokenStore
// ---------------------------------------------------------------------------

// TestTokenStore_LoadOrGenerate_CreatesFileIfMissing verifies that when no
// token file exists LoadOrGenerate creates one and returns a non-empty token.
func TestTokenStore_LoadOrGenerate_CreatesFileIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")

	store := NewTokenStore(path)
	token, err := store.LoadOrGenerate()

	if err != nil {
		t.Fatalf("LoadOrGenerate returned unexpected error: %v", err)
	}
	if token == "" {
		t.Error("LoadOrGenerate returned an empty token")
	}

	// File must now exist on disk.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		t.Error("LoadOrGenerate did not create the token file")
	}
}

// TestTokenStore_LoadOrGenerate_ReturnsSameTokenOnReload verifies that when a
// token file already contains a token, LoadOrGenerate returns that exact value
// rather than generating a new one.
func TestTokenStore_LoadOrGenerate_ReturnsSameTokenOnReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")

	const knownToken = "my-persistent-token"
	if err := os.WriteFile(path, []byte(knownToken), 0o600); err != nil {
		t.Fatalf("failed to write token file: %v", err)
	}

	store := NewTokenStore(path)
	token, err := store.LoadOrGenerate()

	if err != nil {
		t.Fatalf("LoadOrGenerate returned unexpected error: %v", err)
	}
	if token != knownToken {
		t.Errorf("LoadOrGenerate returned %q, want %q", token, knownToken)
	}
}

// TestTokenStore_Rotate_ChangesToken verifies that after Rotate() the next call
// to LoadOrGenerate returns a different, non-empty token.
func TestTokenStore_Rotate_ChangesToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")

	store := NewTokenStore(path)

	original, err := store.LoadOrGenerate()
	if err != nil {
		t.Fatalf("initial LoadOrGenerate failed: %v", err)
	}

	rotated, err := store.Rotate()
	if err != nil {
		t.Fatalf("Rotate returned unexpected error: %v", err)
	}

	if rotated == "" {
		t.Error("Rotate returned an empty token")
	}
	if rotated == original {
		t.Errorf("Rotate did not change the token: still %q", rotated)
	}

	// A fresh store pointing at the same file must see the rotated token.
	store2 := NewTokenStore(path)
	reloaded, err := store2.LoadOrGenerate()
	if err != nil {
		t.Fatalf("LoadOrGenerate after Rotate failed: %v", err)
	}
	if reloaded != rotated {
		t.Errorf("reloaded token %q does not match rotated token %q", reloaded, rotated)
	}
}

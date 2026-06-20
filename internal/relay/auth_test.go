package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	mw := NewAuthMiddleware(tokenStoreWithValue(t, token), inner)

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
	mw := NewAuthMiddleware(tokenStoreWithValue(t, token), inner)

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
	mw := NewAuthMiddleware(tokenStoreWithValue(t, token), inner)

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
	mw := NewAuthMiddleware(tokenStoreWithValue(t, token), inner)

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

// TestTokenStore_LoadOrGenerate_EmptyFileGeneratesNewToken verifies that when
// the token file exists but is empty, LoadOrGenerate treats it as missing and
// generates a fresh token, persisting it back to disk.
func TestTokenStore_LoadOrGenerate_EmptyFileGeneratesNewToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")

	// Create an empty file (zero bytes).
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("failed to create empty token file: %v", err)
	}

	store := NewTokenStore(path)
	token, err := store.LoadOrGenerate()

	if err != nil {
		t.Fatalf("LoadOrGenerate returned unexpected error: %v", err)
	}
	if token == "" {
		t.Error("LoadOrGenerate returned an empty token for an empty file")
	}

	// Confirm the new token was persisted.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("could not read token file after LoadOrGenerate: %v", readErr)
	}
	if string(data) != token {
		t.Errorf("persisted token %q does not match returned token %q", string(data), token)
	}
}

// TestTokenStore_LoadOrGenerate_WhitespaceOnlyFileGeneratesNewToken verifies
// that a file containing only whitespace (spaces, newlines) is treated as
// missing and causes a new token to be generated.
func TestTokenStore_LoadOrGenerate_WhitespaceOnlyFileGeneratesNewToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")

	if err := os.WriteFile(path, []byte("   \n\t  \n"), 0o600); err != nil {
		t.Fatalf("failed to create whitespace token file: %v", err)
	}

	store := NewTokenStore(path)
	token, err := store.LoadOrGenerate()

	if err != nil {
		t.Fatalf("LoadOrGenerate returned unexpected error: %v", err)
	}
	if token == "" {
		t.Error("LoadOrGenerate returned an empty token for a whitespace-only file")
	}
	if len(token) < 8 {
		t.Errorf("generated token %q looks too short to be a real token", token)
	}
}

// TestTokenStore_Rotate_WithoutPriorLoadOrGenerate verifies that Rotate()
// succeeds even when LoadOrGenerate has never been called, i.e. there is no
// pre-existing token file.
func TestTokenStore_Rotate_WithoutPriorLoadOrGenerate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")

	store := NewTokenStore(path)
	token, err := store.Rotate()

	if err != nil {
		t.Fatalf("Rotate (cold start) returned unexpected error: %v", err)
	}
	if token == "" {
		t.Error("Rotate returned an empty token on a cold start")
	}

	// The token file must now exist with the returned value.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("token file not found after cold-start Rotate: %v", readErr)
	}
	if string(data) != token {
		t.Errorf("persisted token %q does not match Rotate return value %q", string(data), token)
	}
}

// TestAuthMiddleware_ExactTokenPath_ForwardsAsRoot verifies that a request to
// /<token> (exact match, no trailing slash) is forwarded to the inner handler
// with path "/" rather than being rejected.
func TestAuthMiddleware_ExactTokenPath_ForwardsAsRoot(t *testing.T) {
	const token = "abc123"
	inner := &handlerSpy{}
	mw := NewAuthMiddleware(tokenStoreWithValue(t, token), inner)

	req := httptest.NewRequest(http.MethodGet, "/"+token, nil)
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, req)

	if !inner.called {
		t.Error("inner handler was not called for exact token path (no trailing slash)")
	}
	if inner.seenPath != "/" {
		t.Errorf("inner handler saw path %q, want %q", inner.seenPath, "/")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for exact token path, got %d", w.Code)
	}
}

// TestAuthMiddleware_NestedPath_ForwardsCorrectly verifies that a deeply nested
// path like /<token>/foo/bar is forwarded as /foo/bar to the inner handler.
func TestAuthMiddleware_NestedPath_ForwardsCorrectly(t *testing.T) {
	const token = "abc123"
	inner := &handlerSpy{}
	mw := NewAuthMiddleware(tokenStoreWithValue(t, token), inner)

	req := httptest.NewRequest(http.MethodGet, "/"+token+"/foo/bar", nil)
	w := httptest.NewRecorder()

	mw.ServeHTTP(w, req)

	if !inner.called {
		t.Fatal("inner handler was not called for nested token path")
	}
	if inner.seenPath != "/foo/bar" {
		t.Errorf("inner handler saw path %q, want %q", inner.seenPath, "/foo/bar")
	}
}

// TestAuthMiddleware_RepeatedFailures_SameIP_EventuallyThrottled verifies the
// per-IP defence-in-depth rate limiter: after a burst of failed (wrong-token)
// attempts from a single client IP the middleware starts returning 429 instead
// of 404, throttling unauthenticated request flooding.
func TestAuthMiddleware_RepeatedFailures_SameIP_EventuallyThrottled(t *testing.T) {
	inner := &handlerSpy{}
	mw := NewAuthMiddleware(tokenStoreWithValue(t, "correct-token"), inner)

	throttled := false
	// Burst is authFailureBurst (20); send comfortably more to spend the bucket.
	for i := 0; i < authFailureBurst+10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/wrong-token/events", nil)
		req.RemoteAddr = "203.0.113.7:5555" // same IP for every attempt
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, req)
		switch w.Code {
		case http.StatusNotFound:
			// allowed-but-rejected attempt
		case http.StatusTooManyRequests:
			throttled = true
		default:
			t.Fatalf("attempt %d: unexpected status %d", i, w.Code)
		}
	}
	if !throttled {
		t.Errorf("expected repeated failures from one IP to be throttled with 429 after %d attempts", authFailureBurst)
	}
	if inner.called {
		t.Error("inner handler must never be called for wrong-token attempts")
	}
}

// TestAuthMiddleware_ValidTokenNotThrottled verifies that the failure limiter
// never blocks legitimate requests: a client presenting the correct token is
// always forwarded with 200, regardless of how many requests it makes.
func TestAuthMiddleware_ValidTokenNotThrottled(t *testing.T) {
	const token = "correct-token"
	inner := &handlerSpy{}
	mw := NewAuthMiddleware(tokenStoreWithValue(t, token), inner)

	for i := 0; i < authFailureBurst+50; i++ {
		req := httptest.NewRequest(http.MethodGet, "/"+token+"/events", nil)
		req.RemoteAddr = "203.0.113.8:5555"
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("valid-token attempt %d: expected 200, got %d", i, w.Code)
		}
	}
}

// TestAuthMiddleware_FailureThrottleIsPerIP verifies that throttling one abusive
// IP does not affect a different client IP.
func TestAuthMiddleware_FailureThrottleIsPerIP(t *testing.T) {
	mw := NewAuthMiddleware(tokenStoreWithValue(t, "correct-token"), &handlerSpy{})

	// Exhaust the bucket for the abusive IP.
	for i := 0; i < authFailureBurst+10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/wrong/events", nil)
		req.RemoteAddr = "203.0.113.9:1111"
		mw.ServeHTTP(httptest.NewRecorder(), req)
	}

	// A different IP making its first failed attempt must get a clean 404, not 429.
	req := httptest.NewRequest(http.MethodGet, "/wrong/events", nil)
	req.RemoteAddr = "203.0.113.10:2222"
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("fresh IP: expected 404 (not throttled by another IP), got %d", w.Code)
	}
}

// okHandler is a minimal inner handler that always returns 200. Unlike
// handlerSpy it is goroutine-safe (no shared mutable state) for use in the
// concurrent rotation test.
type okHandler struct{}

func (okHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// TestAuthMiddleware_ConcurrentRotateVsAuthRead verifies the security-critical
// concurrent path: while one goroutine hammers Rotate(), many goroutines call
// ServeHTTP using the current token. Every response must be a clean 200
// (matched the live token) or 404 (the token rotated out from under it) — never
// a panic or torn read. Run under -race. (Each request uses a distinct client
// IP so the per-IP failure rate limiter never trips and responses stay strictly
// 200/404.)
func TestAuthMiddleware_ConcurrentRotateVsAuthRead(t *testing.T) {
	dir := t.TempDir()
	ts := NewTokenStore(filepath.Join(dir, "token"))
	if _, err := ts.LoadOrGenerate(); err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}
	mw := NewAuthMiddleware(ts, okHandler{})

	var (
		readersWG sync.WaitGroup
		rotatorWG sync.WaitGroup
		stop      atomic.Bool
		ipSeq     atomic.Int64
		badCodes  atomic.Int64
	)

	// Rotator goroutine: continuously rotate the token until stop is set.
	rotatorWG.Add(1)
	go func() {
		defer rotatorWG.Done()
		for !stop.Load() {
			if _, err := ts.Rotate(); err != nil {
				t.Errorf("Rotate failed: %v", err)
				return
			}
		}
	}()

	// Reader goroutines: repeatedly authenticate with the (possibly stale) token.
	const readers = 8
	const perReader = 200
	for range readers {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for i := 0; i < perReader; i++ {
				token := ts.Current() // may be stale by the time the request runs
				req := httptest.NewRequest(http.MethodGet, "/"+token+"/events", nil)
				// Unique client IP per request so the failure limiter never trips.
				req.RemoteAddr = fmt.Sprintf("198.51.100.%d:1234", ipSeq.Add(1)%250+1)
				w := httptest.NewRecorder()
				mw.ServeHTTP(w, req)
				if w.Code != http.StatusOK && w.Code != http.StatusNotFound {
					badCodes.Add(1)
				}
			}
		}()
	}

	readersWG.Wait()
	stop.Store(true)
	rotatorWG.Wait()

	if n := badCodes.Load(); n != 0 {
		t.Errorf("got %d response(s) that were neither 200 nor 404 during concurrent rotation", n)
	}
}

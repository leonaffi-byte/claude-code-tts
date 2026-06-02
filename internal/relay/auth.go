package relay

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// NewAuthMiddleware returns an http.Handler that validates the token prefix on
// every incoming request. Requests whose path begins with /<token>/ have the
// prefix stripped before being forwarded to next. All other requests receive
// 404 Not Found.
//
// ts is consulted on every request so that token rotation takes effect
// immediately without restarting the server.
func NewAuthMiddleware(ts *TokenStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ts.Current()
		prefix := "/" + token
		if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
			http.NotFound(w, r)
			return
		}
		stripped := strings.TrimPrefix(r.URL.Path, prefix)
		if stripped == "" {
			stripped = "/"
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = stripped
		// Clear RawPath so downstream handlers use the decoded Path only;
		// leaving RawPath set would expose the un-stripped percent-encoded form.
		r2.URL.RawPath = ""
		next.ServeHTTP(w, r2)
	})
}

// TokenStore persists and retrieves a bearer token from a file, enabling the
// token to survive relay restarts.
type TokenStore struct {
	path    string
	mu      sync.RWMutex
	current string
}

// NewTokenStore creates a TokenStore backed by the file at path.
func NewTokenStore(path string) *TokenStore {
	return &TokenStore{path: path}
}

// Current returns the token currently active in memory. It returns an empty
// string before LoadOrGenerate or Rotate has been called.
func (s *TokenStore) Current() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// LoadOrGenerate returns the token stored in the file. When the file does not
// exist it generates a new random token, persists it, and returns it.
func (s *TokenStore) LoadOrGenerate() (string, error) {
	data, err := os.ReadFile(s.path)
	if err == nil && len(strings.TrimSpace(string(data))) > 0 {
		token := strings.TrimSpace(string(data))
		s.mu.Lock()
		s.current = token
		s.mu.Unlock()
		return token, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	token := uuid.New().String()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(s.path, []byte(token), 0o600); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.current = token
	s.mu.Unlock()
	return token, nil
}

// Rotate generates a fresh token, overwrites the file, and returns the new
// token. The previous token is immediately invalidated in memory so the live
// auth middleware rejects the old token on the next request.
func (s *TokenStore) Rotate() (string, error) {
	token := uuid.New().String()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(s.path, []byte(token), 0o600); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.current = token
	s.mu.Unlock()
	return token, nil
}

package relay

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// NewAuthMiddleware returns an http.Handler that validates the token prefix on
// every incoming request. Requests whose path begins with /<token>/ have the
// prefix stripped before being forwarded to next. All other requests receive
// 404 Not Found.
func NewAuthMiddleware(token string, next http.Handler) http.Handler {
	prefix := "/" + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		next.ServeHTTP(w, r2)
	})
}

// TokenStore persists and retrieves a bearer token from a file, enabling the
// token to survive relay restarts.
type TokenStore struct {
	path string
}

// NewTokenStore creates a TokenStore backed by the file at path.
func NewTokenStore(path string) *TokenStore {
	return &TokenStore{path: path}
}

// LoadOrGenerate returns the token stored in the file. When the file does not
// exist it generates a new random token, persists it, and returns it.
func (s *TokenStore) LoadOrGenerate() (string, error) {
	data, err := os.ReadFile(s.path)
	if err == nil && len(strings.TrimSpace(string(data))) > 0 {
		return strings.TrimSpace(string(data)), nil
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
	return token, nil
}

// Rotate generates a fresh token, overwrites the file, and returns the new
// token. The previous token is immediately invalidated.
func (s *TokenStore) Rotate() (string, error) {
	token := uuid.New().String()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(s.path, []byte(token), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

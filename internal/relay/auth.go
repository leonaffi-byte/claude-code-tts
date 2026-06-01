package relay

import "net/http"

// NewAuthMiddleware returns an http.Handler that validates the token prefix on
// every incoming request. Requests whose path begins with /<token>/ have the
// prefix stripped before being forwarded to next. All other requests receive
// 404 Not Found.
//
// The concrete implementation lives here once the TDD cycle completes.
func NewAuthMiddleware(token string, next http.Handler) http.Handler {
	// Stub — not yet implemented. Tests must fail here.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
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
	// Stub — not yet implemented. Tests must fail here.
	return "", nil
}

// Rotate generates a fresh token, overwrites the file, and returns the new
// token. The previous token is immediately invalidated.
func (s *TokenStore) Rotate() (string, error) {
	// Stub — not yet implemented. Tests must fail here.
	return "", nil
}

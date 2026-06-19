package relay

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// authFailureDelay is a small constant delay added to every failed auth
	// attempt. It removes residual timing signal and slows brute-force probing
	// without meaningfully affecting legitimate (always-succeeding) clients.
	authFailureDelay = 100 * time.Millisecond

	// authFailureBurst is the number of failed auth attempts a single client IP
	// may make before being throttled.
	authFailureBurst = 20

	// authFailureRefill is how often one failed-attempt token is replenished per
	// client IP, capping sustained failure throughput after the burst is spent.
	authFailureRefill = time.Second

	// authBucketTTL is how long a bucket must be idle before it is evicted. After
	// this long with no failures a bucket has fully refilled and is therefore
	// indistinguishable from a fresh one, so dropping it changes no behaviour.
	authBucketTTL = authFailureBurst * authFailureRefill

	// authSweepInterval bounds how often the limiter scans for evictable buckets;
	// authMaxBuckets forces a scan regardless so a flood of unique client IPs
	// cannot grow the map without bound between scheduled sweeps.
	authSweepInterval = time.Minute
	authMaxBuckets    = 4096
)

// NewAuthMiddleware returns an http.Handler that validates the token prefix on
// every incoming request. Requests whose path begins with /<token>/ have the
// prefix stripped before being forwarded to next. All other requests receive
// 404 Not Found.
//
// ts is consulted on every request so that token rotation takes effect
// immediately without restarting the server.
func NewAuthMiddleware(ts *TokenStore, next http.Handler) http.Handler {
	limiter := newAuthFailureLimiter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ts.Current()
		seg, rest := firstSegment(r.URL.Path)

		// Compare the first path segment against the token in constant time so
		// the response time does not leak how many leading bytes matched. The
		// length check is non-secret and may short-circuit; ConstantTimeCompare
		// itself requires equal-length inputs to be meaningful.
		if len(seg) != len(token) || subtle.ConstantTimeCompare([]byte(seg), []byte(token)) != 1 {
			// Throttle repeated failed attempts from the same client to provide
			// defence-in-depth against unauthenticated request flooding. A small
			// constant delay further removes any residual timing signal.
			if !limiter.allow(clientIP(r)) {
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			time.Sleep(authFailureDelay)
			http.NotFound(w, r)
			return
		}

		stripped := rest
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

// firstSegment splits a URL path of the form "/<seg>" or "/<seg>/<rest>" into
// the first segment (without slashes) and the remaining path (with its leading
// slash, e.g. "/rest"). For "/<seg>" exactly, rest is "". For paths that do not
// start with "/" or have an empty first segment, seg is "" so the constant-time
// token comparison fails cleanly.
func firstSegment(path string) (seg, rest string) {
	if !strings.HasPrefix(path, "/") {
		return "", path
	}
	trimmed := path[1:]
	if i := strings.IndexByte(trimmed, '/'); i >= 0 {
		return trimmed[:i], trimmed[i:]
	}
	return trimmed, ""
}

// clientIP extracts the client IP from the request's RemoteAddr, falling back to
// the raw RemoteAddr when it has no port. It intentionally ignores forwarded
// headers, which are attacker-controlled on a directly-exposed listener.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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

// authFailureLimiter is a lightweight per-IP token-bucket rate limiter for
// failed auth attempts. It uses only the standard library (no external rate
// package) and is safe for concurrent use. Each client IP starts with
// authFailureBurst tokens; a token is consumed per failed attempt and one is
// refilled every authFailureRefill. Successful auth never touches the limiter,
// so legitimate clients are unaffected.
type authFailureLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*authBucket
	lastSweep time.Time
}

type authBucket struct {
	tokens float64
	last   time.Time
}

func newAuthFailureLimiter() *authFailureLimiter {
	return &authFailureLimiter{buckets: make(map[string]*authBucket)}
}

// allow reports whether a failed attempt from ip may proceed (true) or should
// be throttled (false). It is called only on the auth-failure path.
func (l *authFailureLimiter) allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweepLocked(now)

	b, ok := l.buckets[ip]
	if !ok {
		// First failure for this IP: seed a full bucket and consume one token.
		l.buckets[ip] = &authBucket{tokens: authFailureBurst - 1, last: now}
		return true
	}

	// Refill proportionally to elapsed time, capped at the burst size.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * (1.0 / authFailureRefill.Seconds())
	if b.tokens > authFailureBurst {
		b.tokens = authFailureBurst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweepLocked evicts buckets that have been idle for at least authBucketTTL,
// bounding the map against client-IP churn (an attacker rotating source IPs
// could otherwise grow it without limit). An evicted bucket has fully refilled,
// so its removal is behaviourally identical to leaving it. The scan runs at most
// once per authSweepInterval, but is forced whenever the map exceeds
// authMaxBuckets. Callers must hold l.mu.
func (l *authFailureLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < authSweepInterval && len(l.buckets) <= authMaxBuckets {
		return
	}
	l.lastSweep = now
	for ip, b := range l.buckets {
		if now.Sub(b.last) >= authBucketTTL {
			delete(l.buckets, ip)
		}
	}
}

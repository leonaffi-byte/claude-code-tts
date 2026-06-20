package relay

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// TestSafeDialContext_RejectsInternalIP verifies the delivery-time SSRF guard:
// a host that resolves to an internal address is refused at dial time, closing
// the DNS-rebinding window that subscribe-time validation alone cannot.
func TestSafeDialContext_RejectsInternalIP(t *testing.T) {
	stubLookupIP(t, map[string][]net.IP{
		"rebind.example.com": {net.ParseIP("127.0.0.1")},
	})

	conn, err := safeDialContext(context.Background(), "tcp", "rebind.example.com:443")
	if conn != nil {
		conn.Close() //nolint:errcheck
		t.Fatal("expected no connection to an internal-resolving host")
	}
	if err == nil || !strings.Contains(err.Error(), "internal") {
		t.Fatalf("expected an internal-address refusal error, got %v", err)
	}
}

// TestSafeDialContext_RejectsIPLiteralInternal verifies that an IP-literal host
// pointing straight at an internal address is rejected without any DNS lookup.
func TestSafeDialContext_RejectsIPLiteralInternal(t *testing.T) {
	conn, err := safeDialContext(context.Background(), "tcp", "169.254.169.254:80")
	if conn != nil {
		conn.Close() //nolint:errcheck
		t.Fatal("expected no connection to the link-local metadata address")
	}
	if err == nil || !strings.Contains(err.Error(), "internal") {
		t.Fatalf("expected an internal-address refusal error, got %v", err)
	}
}

// TestAuthFailureLimiter_EvictsIdleBuckets verifies the limiter map does not grow
// without bound: a bucket idle for longer than authBucketTTL is evicted on the
// next sweep, so client-IP churn cannot leak memory.
func TestAuthFailureLimiter_EvictsIdleBuckets(t *testing.T) {
	l := newAuthFailureLimiter()

	// Seed a stale bucket and force the next allow() to run a sweep.
	l.buckets["10.0.0.1"] = &authBucket{tokens: authFailureBurst, last: time.Now().Add(-2 * authBucketTTL)}
	l.lastSweep = time.Now().Add(-2 * authSweepInterval)

	if !l.allow("10.0.0.2") { // a fresh IP; should always be allowed
		t.Fatal("first failure from a new IP should be allowed")
	}

	if _, ok := l.buckets["10.0.0.1"]; ok {
		t.Error("idle bucket should have been evicted by the sweep")
	}
	if _, ok := l.buckets["10.0.0.2"]; !ok {
		t.Error("active bucket for the current IP should be retained")
	}
}

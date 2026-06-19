package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/ybouhjira/claude-code-tts/internal/logging"
)

// pushTransportTimeout bounds each outbound HTTPS call to a push service so a
// slow or unreachable endpoint cannot stall the whole fan-out indefinitely.
const pushTransportTimeout = 10 * time.Second

// PushTransport is the outbound transport used by PushSender to deliver a Web
// Push notification to a single subscription endpoint. The implementation is
// injected at construction time so tests can substitute a mock without making
// real HTTPS calls.
type PushTransport interface {
	// Send delivers a push notification to the given subscription. It returns
	// the HTTP status code received from the push service and any transport-
	// level error. A 410 status means the subscription has expired. The context
	// bounds the outbound call so a slow endpoint can be aborted.
	Send(ctx context.Context, sub PushSubscription, payload []byte) (statusCode int, err error)
}

// PushSubscription holds the endpoint and key material for a single Web Push
// subscriber as defined by the Push API specification.
type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

const defaultMaxSubscriptions = 100

// PushSender stores subscriptions and delivers push notifications via an
// injected PushTransport. It is safe for concurrent use.
//
// A nil PushSender is valid; callers that only have an optional push sender
// should check for nil before calling Send.
type PushSender struct {
	mu            sync.Mutex
	transport     PushTransport
	subscriptions []PushSubscription
	maxSubs       int
}

// NewPushSender creates a PushSender backed by the given transport.
func NewPushSender(t PushTransport) *PushSender {
	return &PushSender{transport: t, maxSubs: defaultMaxSubscriptions}
}

// AddSubscription registers a new subscription. Returns false when the cap is
// reached so the caller can respond with 429 Too Many Requests.
//
// Subscriptions are de-duplicated by Endpoint: re-subscribing with an endpoint
// that already exists replaces the stored entry (refreshing its key material)
// instead of appending a duplicate. This prevents the cap from filling with
// repeat registrations from the same client (e.g. PWA reinstalls).
func (ps *PushSender) AddSubscription(sub PushSubscription) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for i, existing := range ps.subscriptions {
		if existing.Endpoint == sub.Endpoint {
			ps.subscriptions[i] = sub
			return true
		}
	}
	if ps.maxSubs > 0 && len(ps.subscriptions) >= ps.maxSubs {
		return false
	}
	ps.subscriptions = append(ps.subscriptions, sub)
	return true
}

// Subscriptions returns a copy of the current subscription list.
func (ps *PushSender) Subscriptions() []PushSubscription {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	result := make([]PushSubscription, len(ps.subscriptions))
	copy(result, ps.subscriptions)
	return result
}

// Send delivers a notification containing clipID and clipURL to all registered
// subscriptions. Subscriptions that respond with HTTP 410 are pruned immediately
// and never retried. Send always returns nil — errors are non-fatal so that a
// failed push never blocks the ingest pipeline.
//
// ctx bounds the fan-out: callers that want the ingest response to return
// immediately should dispatch Send on a goroutine with a detached,
// deadline-bearing context (e.g. context.WithTimeout(context.Background(), ...)).
// Each per-subscription transport call also stops early if ctx is cancelled.
func (ps *PushSender) Send(ctx context.Context, clipID, clipURL string) error {
	payload, err := json.Marshal(map[string]string{
		"clipId":  clipID,
		"clipURL": clipURL,
	})
	if err != nil {
		logging.Error("push: failed to marshal payload: %v", err)
		return nil
	}

	ps.mu.Lock()
	subs := make([]PushSubscription, len(ps.subscriptions))
	copy(subs, ps.subscriptions)
	ps.mu.Unlock()

	// Collect endpoints that returned 410 Gone.
	var gone []string
	for _, sub := range subs {
		// Abort the remaining fan-out if the caller's context is done.
		if err := ctx.Err(); err != nil {
			logging.Error("push: fan-out aborted before completion: %v", err)
			break
		}
		code, sendErr := ps.transport.Send(ctx, sub, payload)
		if sendErr != nil {
			logging.Error("push: transport error for subscription %q: %v", sub.Endpoint, sendErr)
			continue
		}
		if code == http.StatusGone {
			logging.Error("push: subscription %q returned 410 Gone — pruning", sub.Endpoint)
			gone = append(gone, sub.Endpoint)
		}
	}

	// Prune 410 endpoints from the live slice without dropping any that were
	// added concurrently during the transport calls above.
	if len(gone) > 0 {
		goneSet := make(map[string]bool, len(gone))
		for _, ep := range gone {
			goneSet[ep] = true
		}
		ps.mu.Lock()
		filtered := ps.subscriptions[:0]
		for _, s := range ps.subscriptions {
			if !goneSet[s.Endpoint] {
				filtered = append(filtered, s)
			}
		}
		ps.subscriptions = filtered
		ps.mu.Unlock()
	}

	return nil
}

// PushSenderIface is the injectable interface for the push-sender dependency
// used by Handler and CompanionHandler. This keeps both handlers decoupled from
// the concrete PushSender so tests can supply a mock.
type PushSenderIface interface {
	AddSubscription(sub PushSubscription) bool
	Send(ctx context.Context, clipID, clipURL string) error
}

// WebPushTransport delivers Web Push notifications using the VAPID protocol.
// It holds a VAPIDStore reference so it can access keys without storing them
// as plain strings in memory longer than necessary.
type WebPushTransport struct {
	vapid *VAPIDStore
}

// NewWebPushTransport creates a WebPushTransport backed by the given VAPIDStore.
func NewWebPushTransport(vapid *VAPIDStore) *WebPushTransport {
	return &WebPushTransport{vapid: vapid}
}

// Send delivers payload to sub using VAPID-authenticated Web Push.
// It returns the HTTP status code from the push service (e.g. 201, 410).
//
// The call is bounded by both ctx and an http.Client timeout so a slow or
// unreachable push service cannot block the caller indefinitely.
func (t *WebPushTransport) Send(ctx context.Context, sub PushSubscription, payload []byte) (int, error) {
	s := &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys: webpush.Keys{
			P256dh: sub.Keys.P256DH,
			Auth:   sub.Keys.Auth,
		},
	}

	// Guard the actual TCP connection (not just the subscribe-time hostname) so a
	// DNS name that resolved public at subscribe time but flips to an internal
	// address at delivery time (DNS rebinding) cannot be used for SSRF. The dialer
	// validates the resolved IP and connects to it directly, leaving no
	// resolve-then-reconnect TOCTOU window.
	client := &http.Client{
		Timeout:   pushTransportTimeout,
		Transport: &http.Transport{DialContext: safeDialContext},
	}

	resp, err := webpush.SendNotificationWithContext(ctx, payload, s, &webpush.Options{
		HTTPClient:      client,
		VAPIDPublicKey:  t.vapid.PublicKey(),
		VAPIDPrivateKey: t.vapid.PrivateKey(),
		TTL:             60,
	})
	if err != nil {
		return 0, err
	}
	resp.Body.Close() //nolint:errcheck
	return resp.StatusCode, nil
}

// safeDialContext resolves the target host, refuses to connect if it maps to an
// internal address (loopback/private/link-local/metadata), and dials a validated
// IP directly. Dialing the resolved IP — rather than handing the hostname back to
// the stdlib to re-resolve — closes the DNS-rebinding TOCTOU that subscribe-time
// validation alone cannot. It shares lookupIP and isInternalIP with the
// subscribe-time SSRF guard so both apply identical policy.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	var candidates []net.IP
	if ip := net.ParseIP(host); ip != nil {
		candidates = []net.IP{ip}
	} else if candidates, err = lookupIP(host); err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}

	var dialIP net.IP
	for _, ip := range candidates {
		if isInternalIP(ip) {
			return nil, fmt.Errorf("refusing to connect to internal address %s (host %q)", ip, host)
		}
		if dialIP == nil {
			dialIP = ip
		}
	}
	if dialIP == nil {
		return nil, fmt.Errorf("host %q resolved to no addresses", host)
	}

	d := net.Dialer{Timeout: pushTransportTimeout}
	return d.DialContext(ctx, network, net.JoinHostPort(dialIP.String(), port))
}

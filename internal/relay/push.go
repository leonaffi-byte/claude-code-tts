package relay

import (
	"encoding/json"
	"net/http"
	"sync"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/ybouhjira/claude-code-tts/internal/logging"
)

// PushTransport is the outbound transport used by PushSender to deliver a Web
// Push notification to a single subscription endpoint. The implementation is
// injected at construction time so tests can substitute a mock without making
// real HTTPS calls.
type PushTransport interface {
	// Send delivers a push notification to the given subscription. It returns
	// the HTTP status code received from the push service and any transport-
	// level error. A 410 status means the subscription has expired.
	Send(sub PushSubscription, payload []byte) (statusCode int, err error)
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

// PushSender stores subscriptions and delivers push notifications via an
// injected PushTransport. It is safe for concurrent use.
//
// A nil PushSender is valid; callers that only have an optional push sender
// should check for nil before calling Send.
type PushSender struct {
	mu            sync.Mutex
	transport     PushTransport
	subscriptions []PushSubscription
}

// NewPushSender creates a PushSender backed by the given transport.
func NewPushSender(t PushTransport) *PushSender {
	return &PushSender{transport: t}
}

// AddSubscription registers a new subscription with the sender.
func (ps *PushSender) AddSubscription(sub PushSubscription) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.subscriptions = append(ps.subscriptions, sub)
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
func (ps *PushSender) Send(clipID, clipURL string) error {
	payload, err := json.Marshal(map[string]string{
		"clipId":  clipID,
		"clipUrl": clipURL,
	})
	if err != nil {
		logging.Error("push: failed to marshal payload: %v", err)
		return nil
	}

	ps.mu.Lock()
	subs := make([]PushSubscription, len(ps.subscriptions))
	copy(subs, ps.subscriptions)
	ps.mu.Unlock()

	var keep []PushSubscription
	for _, sub := range subs {
		code, sendErr := ps.transport.Send(sub, payload)
		if sendErr != nil {
			logging.Error("push: transport error for subscription %q: %v", sub.Endpoint, sendErr)
			keep = append(keep, sub)
			continue
		}
		if code == http.StatusGone {
			logging.Error("push: subscription %q returned 410 Gone — pruning", sub.Endpoint)
			continue
		}
		keep = append(keep, sub)
	}

	ps.mu.Lock()
	ps.subscriptions = keep
	ps.mu.Unlock()
	return nil
}

// PushSenderIface is the injectable interface for the push-sender dependency
// used by Handler and CompanionHandler. This keeps both handlers decoupled from
// the concrete PushSender so tests can supply a mock.
type PushSenderIface interface {
	AddSubscription(sub PushSubscription)
	Send(clipID, clipURL string) error
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
func (t *WebPushTransport) Send(sub PushSubscription, payload []byte) (int, error) {
	s := &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys: webpush.Keys{
			P256dh: sub.Keys.P256DH,
			Auth:   sub.Keys.Auth,
		},
	}

	resp, err := webpush.SendNotification(payload, s, &webpush.Options{
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

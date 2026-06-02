package relay

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
	transport     PushTransport
	subscriptions []PushSubscription
}

// NewPushSender creates a PushSender backed by the given transport.
func NewPushSender(t PushTransport) *PushSender {
	return &PushSender{transport: t}
}

// AddSubscription registers a new subscription with the sender.
func (ps *PushSender) AddSubscription(sub PushSubscription) {
	// unimplemented
}

// Subscriptions returns a copy of the current subscription list.
func (ps *PushSender) Subscriptions() []PushSubscription {
	return nil
}

// Send delivers a notification containing clipID and clipURL to all registered
// subscriptions. Subscriptions that respond with HTTP 410 are pruned.
func (ps *PushSender) Send(clipID, clipURL string) error {
	return nil
}

// VAPIDStore persists and loads a VAPID keypair from a directory on disk.
// The private key is written to <dir>/vapid-private.pem and the public key to
// <dir>/vapid-public.txt. On first call the keys are generated; subsequent
// calls read them back from disk.
type VAPIDStore struct {
	dir string
}

// NewVAPIDStore creates a VAPIDStore that keeps its keypair files under dir.
func NewVAPIDStore(dir string) *VAPIDStore {
	return &VAPIDStore{dir: dir}
}

// LoadOrGenerate returns the VAPID private and public keys. On first call with
// a fresh directory the keys are generated and persisted; on subsequent calls
// the existing files are read back unchanged.
func (vs *VAPIDStore) LoadOrGenerate() (privateKey, publicKey string, err error) {
	return "", "", nil
}

// PushSenderIface is the injectable interface for the push-sender dependency
// used by Handler and CompanionHandler. This keeps both handlers decoupled from
// the concrete PushSender so tests can supply a mock.
type PushSenderIface interface {
	AddSubscription(sub PushSubscription)
	Send(clipID, clipURL string) error
}

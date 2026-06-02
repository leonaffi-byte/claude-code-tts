package relay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Mock PushTransport — defines the contract PushSender depends on
// ---------------------------------------------------------------------------

// mockTransport records calls made to Send and allows per-subscription status
// code overrides to simulate 410/500 responses from a real push service.
type mockTransport struct {
	calls      []transportCall
	statusByEP map[string]int // endpoint → status code override; default 201
}

type transportCall struct {
	sub     PushSubscription
	payload []byte
}

// newMockTransport creates a mock transport that returns 201 by default.
func newMockTransport() *mockTransport {
	return &mockTransport{statusByEP: make(map[string]int)}
}

// returnStatus registers a status code to be returned for the given endpoint.
func (m *mockTransport) returnStatus(endpoint string, code int) {
	m.statusByEP[endpoint] = code
}

func (m *mockTransport) Send(sub PushSubscription, payload []byte) (int, error) {
	m.calls = append(m.calls, transportCall{sub: sub, payload: payload})
	if code, ok := m.statusByEP[sub.Endpoint]; ok {
		return code, nil
	}
	return 201, nil
}

// callCount returns the number of Send calls made to the given endpoint.
func (m *mockTransport) callCount(endpoint string) int {
	n := 0
	for _, c := range m.calls {
		if c.sub.Endpoint == endpoint {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Helper — build a PushSubscription with the given endpoint
// ---------------------------------------------------------------------------

func pushSub(endpoint string) PushSubscription {
	var s PushSubscription
	s.Endpoint = endpoint
	s.Keys.P256DH = "dGVzdHB1YmxpY2tleQ==" // "testpublickey" base64
	s.Keys.Auth = "dGVzdGF1dGg="             // "testauth" base64
	return s
}

// ---------------------------------------------------------------------------
// Test A: sends a notification when a clip is ready
// ---------------------------------------------------------------------------

// TestPushSender_Send_CallsTransportWithPayloadContainingClipIDAndURL verifies
// that Send delivers exactly one transport call whose JSON payload contains
// both the clip ID and the clip URL.
func TestPushSender_Send_CallsTransportWithPayloadContainingClipIDAndURL(t *testing.T) {
	transport := newMockTransport()
	sender := NewPushSender(transport)
	sender.AddSubscription(pushSub("https://example.com/push/abc"))

	const clipID = "clip-001"
	const clipURL = "https://relay.example.com/clips/clip-001"

	if err := sender.Send(clipID, clipURL); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	if len(transport.calls) != 1 {
		t.Fatalf("expected 1 transport call, got %d", len(transport.calls))
	}

	payload := transport.calls[0].payload
	var body map[string]interface{}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not valid JSON: %v — raw: %s", err, payload)
	}

	if body["clipId"] != clipID {
		t.Errorf("payload.clipId = %v, want %q", body["clipId"], clipID)
	}
	if body["clipURL"] != clipURL {
		t.Errorf("payload.clipURL = %v, want %q", body["clipURL"], clipURL)
	}
}

// ---------------------------------------------------------------------------
// Test B: 410 prunes the subscription
// ---------------------------------------------------------------------------

// TestPushSender_Send_410Response_PrunesSubscription verifies that when the
// transport returns HTTP 410 for a subscription, that subscription is removed
// and never passed to the transport on subsequent Send calls.
func TestPushSender_Send_410Response_PrunesSubscription(t *testing.T) {
	transport := newMockTransport()
	const ep = "https://push.example.com/gone"
	transport.returnStatus(ep, 410)

	sender := NewPushSender(transport)
	sender.AddSubscription(pushSub(ep))

	// First Send — transport returns 410.
	_ = sender.Send("clip-001", "https://relay.example.com/clips/clip-001")

	// The transport must have been called on the first Send (subscription existed).
	if transport.callCount(ep) != 1 {
		t.Errorf("expected transport called once on first Send, got %d — subscription may not have been registered",
			transport.callCount(ep))
	}

	// The subscription must have been pruned.
	subs := sender.Subscriptions()
	for _, s := range subs {
		if s.Endpoint == ep {
			t.Errorf("subscription %q was not pruned after 410 response", ep)
		}
	}

	// Reset call log and do a second Send — transport must NOT be called again.
	transport.calls = nil
	_ = sender.Send("clip-002", "https://relay.example.com/clips/clip-002")

	if transport.callCount(ep) != 0 {
		t.Errorf("pruned subscription was delivered to on a second Send — expected 0 calls, got %d",
			transport.callCount(ep))
	}
}

// ---------------------------------------------------------------------------
// Test C: non-410 HTTP errors do not prune
// ---------------------------------------------------------------------------

// TestPushSender_Send_500Response_RetainsSubscription verifies that a 500
// response from the transport is treated as a transient error — the subscription
// is kept and will be retried on the next Send call.
func TestPushSender_Send_500Response_RetainsSubscription(t *testing.T) {
	transport := newMockTransport()
	const ep = "https://push.example.com/error"
	transport.returnStatus(ep, 500)

	sender := NewPushSender(transport)
	sender.AddSubscription(pushSub(ep))

	_ = sender.Send("clip-001", "https://relay.example.com/clips/clip-001")

	subs := sender.Subscriptions()
	found := false
	for _, s := range subs {
		if s.Endpoint == ep {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("subscription %q was incorrectly pruned after a 500 response (should only prune on 410)", ep)
	}
}

// ---------------------------------------------------------------------------
// Test D: multiple subscriptions — 410 prunes only that one
// ---------------------------------------------------------------------------

// TestPushSender_Send_410OnFirstSub_PrunesOnlyFirst verifies that when two
// subscriptions are registered and the first returns 410, only the first is
// removed while the second still receives the notification.
func TestPushSender_Send_410OnFirstSub_PrunesOnlyFirst(t *testing.T) {
	transport := newMockTransport()
	const ep1 = "https://push.example.com/first-gone"
	const ep2 = "https://push.example.com/second-ok"
	transport.returnStatus(ep1, 410)

	sender := NewPushSender(transport)
	sender.AddSubscription(pushSub(ep1))
	sender.AddSubscription(pushSub(ep2))

	_ = sender.Send("clip-001", "https://relay.example.com/clips/clip-001")

	// ep2 must still have been called (it received the push).
	if transport.callCount(ep2) != 1 {
		t.Errorf("expected ep2 to be called once, got %d", transport.callCount(ep2))
	}

	// ep1 must be pruned.
	subs := sender.Subscriptions()
	for _, s := range subs {
		if s.Endpoint == ep1 {
			t.Errorf("subscription %q was not pruned after 410 response", ep1)
		}
	}

	// ep2 must still be in the list.
	found2 := false
	for _, s := range subs {
		if s.Endpoint == ep2 {
			found2 = true
			break
		}
	}
	if !found2 {
		t.Errorf("subscription %q was incorrectly removed — only 410 subscriptions should be pruned", ep2)
	}
}

// ---------------------------------------------------------------------------
// Test E: Send with no subscriptions returns nil
// ---------------------------------------------------------------------------

// TestPushSender_Send_NoSubscriptions_ReturnsNil verifies that calling Send on a
// PushSender with no registered subscriptions returns nil and does not invoke
// the transport at all.
func TestPushSender_Send_NoSubscriptions_ReturnsNil(t *testing.T) {
	transport := newMockTransport()
	sender := NewPushSender(transport)

	err := sender.Send("clip-001", "https://relay.example.com/clips/clip-001")
	if err != nil {
		t.Errorf("Send with no subscriptions returned unexpected error: %v", err)
	}
	if len(transport.calls) != 0 {
		t.Errorf("expected 0 transport calls with no subscriptions, got %d", len(transport.calls))
	}
}

// ---------------------------------------------------------------------------
// Test F: AddSubscription after Send is visible on the next Send
// ---------------------------------------------------------------------------

// TestPushSender_AddSubscription_AfterSend_IsIncludedInNextSend verifies that
// registering a new subscription between two Send calls means the subscription
// receives the second notification (not just the first). This guards against
// implementations that snapshot the subscriber list at construction time.
func TestPushSender_AddSubscription_AfterSend_IsIncludedInNextSend(t *testing.T) {
	transport := newMockTransport()
	sender := NewPushSender(transport)

	const epFirst = "https://push.example.com/first"
	const epLate = "https://push.example.com/late"

	sender.AddSubscription(pushSub(epFirst))

	// First Send — only epFirst should be called.
	_ = sender.Send("clip-001", "https://relay.example.com/clips/clip-001")

	if transport.callCount(epFirst) != 1 {
		t.Errorf("first Send: expected 1 call to epFirst, got %d", transport.callCount(epFirst))
	}
	if transport.callCount(epLate) != 0 {
		t.Errorf("first Send: expected 0 calls to epLate (not yet added), got %d", transport.callCount(epLate))
	}

	// Register epLate between the two Sends.
	sender.AddSubscription(pushSub(epLate))

	// Second Send — both subscriptions must be called.
	_ = sender.Send("clip-002", "https://relay.example.com/clips/clip-002")

	if transport.callCount(epFirst) != 2 {
		t.Errorf("second Send: expected 2 total calls to epFirst, got %d", transport.callCount(epFirst))
	}
	if transport.callCount(epLate) != 1 {
		t.Errorf("second Send: expected 1 call to epLate, got %d", transport.callCount(epLate))
	}
}

// ---------------------------------------------------------------------------
// Test G: Multiple 410 responses in one Send prune all of them
// ---------------------------------------------------------------------------

// TestPushSender_Send_Multiple410s_PrunesAll verifies that when three
// subscriptions are registered and two of them return 410, both are pruned
// while the third is retained. This guards against implementations that only
// prune the first 410 and leave subsequent ones in the list.
func TestPushSender_Send_Multiple410s_PrunesAll(t *testing.T) {
	transport := newMockTransport()
	const ep1 = "https://push.example.com/gone-1"
	const ep2 = "https://push.example.com/ok"
	const ep3 = "https://push.example.com/gone-2"
	transport.returnStatus(ep1, 410)
	transport.returnStatus(ep3, 410)

	sender := NewPushSender(transport)
	sender.AddSubscription(pushSub(ep1))
	sender.AddSubscription(pushSub(ep2))
	sender.AddSubscription(pushSub(ep3))

	_ = sender.Send("clip-001", "https://relay.example.com/clips/clip-001")

	subs := sender.Subscriptions()

	// ep2 must still be present.
	foundOK := false
	for _, s := range subs {
		if s.Endpoint == ep2 {
			foundOK = true
		}
		if s.Endpoint == ep1 {
			t.Errorf("subscription %q was not pruned after 410 response", ep1)
		}
		if s.Endpoint == ep3 {
			t.Errorf("subscription %q was not pruned after 410 response", ep3)
		}
	}
	if !foundOK {
		t.Errorf("subscription %q was incorrectly removed — only 410 subscriptions should be pruned", ep2)
	}
}

// ---------------------------------------------------------------------------
// Test H: Concurrent sends do not race (goroutine-safety)
// ---------------------------------------------------------------------------

// safeMockTransport is a goroutine-safe PushTransport used only in the
// concurrent test. It uses its own mutex so multiple goroutines may call Send
// simultaneously without a data race inside the mock itself.
type safeMockTransport struct {
	mu    sync.Mutex
	calls int
}

func (s *safeMockTransport) Send(_ PushSubscription, _ []byte) (int, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return 201, nil
}

func (s *safeMockTransport) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestPushSender_ConcurrentSends_NoDataRace verifies that concurrent calls to
// Send and AddSubscription do not produce data races. It uses a goroutine-safe
// mock transport (safeMockTransport) so the race detector only fires if the
// PushSender itself has unsynchronised access. The test also asserts that every
// goroutine's Send call delivers the pre-seeded subscription at least once.
func TestPushSender_ConcurrentSends_NoDataRace(t *testing.T) {
	transport := &safeMockTransport{}
	sender := NewPushSender(transport)

	// Pre-load one subscription so every Send delivers at least one notification.
	sender.AddSubscription(pushSub("https://push.example.com/base"))

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(n int) {
			defer wg.Done()
			// Interleave AddSubscription and Send from multiple goroutines.
			sender.AddSubscription(pushSub("https://push.example.com/concurrent-" + string(rune('a'+n))))
			_ = sender.Send("clip-concurrent", "https://relay.example.com/clips/concurrent")
		}(i)
	}

	wg.Wait()

	// Each goroutine called Send once; with at least 1 subscription alive,
	// the total call count must be at least goroutines.
	if got := transport.callCount(); got < goroutines {
		t.Errorf("expected at least %d transport calls, got %d", goroutines, got)
	}
	// If we reach here without the race detector firing, goroutine safety holds.
}

// ---------------------------------------------------------------------------
// Test J: VAPIDStore creates keypair on first call
// ---------------------------------------------------------------------------

// TestVAPIDStore_LoadOrGenerate_CreatesKeypairOnFirstCall verifies that when the
// VAPID store directory is empty, LoadOrGenerate generates and returns non-empty
// private and public keys, and persists them to disk.
func TestVAPIDStore_LoadOrGenerate_CreatesKeypairOnFirstCall(t *testing.T) {
	dir := t.TempDir()
	store := NewVAPIDStore(dir)

	priv, pub, err := store.LoadOrGenerate()
	if err != nil {
		t.Fatalf("LoadOrGenerate returned unexpected error: %v", err)
	}
	if priv == "" {
		t.Error("LoadOrGenerate returned empty private key on first call")
	}
	if pub == "" {
		t.Error("LoadOrGenerate returned empty public key on first call")
	}

	// Key files must now exist on disk.
	privPath := filepath.Join(dir, "vapid-private.pem")
	pubPath := filepath.Join(dir, "vapid-public.txt")

	if _, err := os.Stat(privPath); os.IsNotExist(err) {
		t.Errorf("private key file %q was not created by LoadOrGenerate", privPath)
	}
	if _, err := os.Stat(pubPath); os.IsNotExist(err) {
		t.Errorf("public key file %q was not created by LoadOrGenerate", pubPath)
	}
}

// ---------------------------------------------------------------------------
// Test K: VAPIDStore reuses an existing keypair
// ---------------------------------------------------------------------------

// TestVAPIDStore_LoadOrGenerate_ReusesExistingKeypair verifies that when key
// files already exist on disk, LoadOrGenerate returns the same values without
// regenerating them. This ensures the VAPID public key (shared with push
// services) remains stable across relay restarts.
func TestVAPIDStore_LoadOrGenerate_ReusesExistingKeypair(t *testing.T) {
	dir := t.TempDir()
	store := NewVAPIDStore(dir)

	// First call generates and persists.
	priv1, pub1, err := store.LoadOrGenerate()
	if err != nil {
		t.Fatalf("first LoadOrGenerate failed: %v", err)
	}

	// Keys from the first call must be non-empty — otherwise we can't
	// distinguish "reused the same empty value" from "generated and reloaded".
	if priv1 == "" {
		t.Fatal("first LoadOrGenerate returned empty private key — cannot verify reuse")
	}
	if pub1 == "" {
		t.Fatal("first LoadOrGenerate returned empty public key — cannot verify reuse")
	}

	// Second call (fresh store, same dir) must return the same keys.
	store2 := NewVAPIDStore(dir)
	priv2, pub2, err := store2.LoadOrGenerate()
	if err != nil {
		t.Fatalf("second LoadOrGenerate failed: %v", err)
	}

	if priv2 != priv1 {
		t.Errorf("private key changed between calls: first %q, second %q", priv1, priv2)
	}
	if pub2 != pub1 {
		t.Errorf("public key changed between calls: first %q, second %q", pub1, pub2)
	}
}

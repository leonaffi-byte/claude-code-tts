package relay

import (
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// SSEHub manages Server-Sent Event subscriber channels. It is safe for
// concurrent use from multiple goroutines.
type SSEHub struct {
	mu          sync.RWMutex
	subscribers map[string]chan string
	maxSubs     int // maximum concurrent subscribers; <= 0 means unlimited
}

// NewSSEHub creates a new SSEHub with a default maximum of 100 concurrent
// subscribers. Pass a value <= 0 to disable the limit (not recommended in
// production, as it allows unbounded resource consumption).
func NewSSEHub() *SSEHub {
	return &SSEHub{
		subscribers: make(map[string]chan string),
		maxSubs:     100,
	}
}

// Subscribe registers a new subscriber and returns a unique ID, a read-only
// channel that receives formatted SSE messages, and a cancel function that
// removes the subscriber and closes the channel.
//
// The returned channel is buffered with capacity 4 to prevent the broadcaster
// from stalling on a slow client.
func (h *SSEHub) Subscribe() (id string, ch <-chan string, cancel func()) {
	h.mu.Lock()
	if h.maxSubs > 0 && len(h.subscribers) >= h.maxSubs {
		h.mu.Unlock()
		return "", nil, func() {}
	}

	id = uuid.New().String()
	buffered := make(chan string, 4)
	h.subscribers[id] = buffered
	h.mu.Unlock()

	cancel = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subscribers[id]; ok {
			delete(h.subscribers, id)
			close(buffered)
		}
	}

	return id, buffered, cancel
}

// Broadcast formats event and data as an SSE message and sends it to all
// active subscribers without blocking on slow clients.
//
// The frame format is: "event: <event>\ndata: <data>\n\n"
// Sending to a full channel is skipped (non-blocking select) to prevent
// the broadcaster from stalling on a slow client.
//
// The read-lock is held throughout the sends. The sends are non-blocking
// (select with default), so cancel() is only delayed by the time to drain
// the loop — no deadlock, no send-to-closed-channel race.
func (h *SSEHub) Broadcast(event string, data string) {
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, ch := range h.subscribers {
		select {
		case ch <- msg:
		default:
			// Skip slow/full subscriber — do not block broadcaster.
		}
	}
}

// Count returns the number of currently active subscribers.
func (h *SSEHub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers)
}

// HasLiveListener implements PresenceTracker. It returns true if at least one
// SSE subscriber is currently connected. This is used by Handler to decide
// whether to suppress a Web Push notification after broadcasting via SSE.
func (h *SSEHub) HasLiveListener() bool {
	return h.Count() > 0
}

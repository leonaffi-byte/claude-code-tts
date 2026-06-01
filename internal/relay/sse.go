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
}

// NewSSEHub creates a new, empty SSEHub ready to accept subscribers.
func NewSSEHub() *SSEHub {
	return &SSEHub{
		subscribers: make(map[string]chan string),
	}
}

// Subscribe registers a new subscriber and returns a unique ID, a read-only
// channel that receives formatted SSE messages, and a cancel function that
// removes the subscriber and closes the channel.
//
// The returned channel is buffered with capacity 4 to prevent the broadcaster
// from stalling on a slow client.
func (h *SSEHub) Subscribe() (id string, ch <-chan string, cancel func()) {
	id = uuid.New().String()
	buffered := make(chan string, 4)

	h.mu.Lock()
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
func (h *SSEHub) Broadcast(event string, data string) {
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)

	// Copy subscriber channels under a read lock to avoid holding the lock
	// during channel sends, which prevents deadlock with cancel().
	h.mu.RLock()
	channels := make([]chan string, 0, len(h.subscribers))
	for _, ch := range h.subscribers {
		channels = append(channels, ch)
	}
	h.mu.RUnlock()

	for _, ch := range channels {
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

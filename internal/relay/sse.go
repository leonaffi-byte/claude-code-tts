package relay

// SSEHub manages Server-Sent Event subscriber channels. It is safe for
// concurrent use from multiple goroutines.
type SSEHub struct{}

// NewSSEHub creates a new, empty SSEHub ready to accept subscribers.
func NewSSEHub() *SSEHub {
	return &SSEHub{}
}

// Subscribe registers a new subscriber and returns a unique ID, a read-only
// channel that receives formatted SSE messages, and a cancel function that
// removes the subscriber and closes the channel.
func (h *SSEHub) Subscribe() (id string, ch <-chan string, cancel func()) {
	return "", nil, func() {}
}

// Broadcast formats event and data as an SSE message and sends it to all
// active subscribers without blocking on slow clients.
func (h *SSEHub) Broadcast(event string, data string) {}

// Count returns the number of currently active subscribers.
func (h *SSEHub) Count() int { return 0 }

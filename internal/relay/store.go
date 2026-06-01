package relay

import (
	"sync"

	"github.com/google/uuid"
)

// ClipStore holds a bounded number of in-memory audio clips, evicting the
// oldest when the capacity is exceeded. It is safe for concurrent use.
type ClipStore struct {
	mu       sync.Mutex
	capacity int
	clips    map[string][]byte
	order    []string
}

// NewClipStore creates a ClipStore that retains at most capacity clips.
func NewClipStore(capacity int) *ClipStore {
	if capacity <= 0 {
		capacity = 10
	}
	return &ClipStore{
		capacity: capacity,
		clips:    make(map[string][]byte),
	}
}

// Add stores audioData and returns a unique ID for the clip.
// When the store is at capacity the oldest clip is evicted first.
func (s *ClipStore) Add(audioData []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := uuid.New().String()

	if len(s.order) >= s.capacity {
		oldest := s.order[0]
		delete(s.clips, oldest)
		s.order = s.order[1:]
	}

	s.clips[id] = audioData
	s.order = append(s.order, id)

	return id, nil
}

// Get retrieves a clip by ID. Returns the audio bytes and true when found,
// nil and false when the ID is unknown or has been evicted.
func (s *ClipStore) Get(id string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, ok := s.clips[id]
	if !ok {
		return nil, false
	}
	return data, true
}

// Close is a no-op for the in-memory store but satisfies the lifecycle contract.
func (s *ClipStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for k := range s.clips {
		delete(s.clips, k)
	}
	s.order = nil
	return nil
}


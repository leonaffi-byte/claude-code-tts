package relay

// ClipStore holds a bounded number of in-memory audio clips, evicting the
// oldest when the capacity is exceeded. It is safe for concurrent use.
type ClipStore struct {
	capacity int
	clips    map[string][]byte
	order    []string
}

// NewClipStore creates a ClipStore that retains at most capacity clips.
func NewClipStore(capacity int) *ClipStore {
	return &ClipStore{
		capacity: capacity,
		clips:    make(map[string][]byte),
	}
}

// Add stores audioData and returns a unique ID for the clip.
// When the store is at capacity the oldest clip is evicted first.
func (s *ClipStore) Add(audioData []byte) (string, error) {
	// stub — logic not yet implemented
	return "", nil
}

// Get retrieves a clip by ID. Returns the audio bytes and true when found,
// nil and false when the ID is unknown or has been evicted.
func (s *ClipStore) Get(id string) ([]byte, bool) {
	// stub — logic not yet implemented
	return nil, false
}

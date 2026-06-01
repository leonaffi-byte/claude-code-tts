package relay

import (
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// ClipStore — Add / Get / eviction / concurrency
// ---------------------------------------------------------------------------

func TestClipStore_Add_ReturnsNonEmptyID(t *testing.T) {
	store := NewClipStore(10)

	id, err := store.Add([]byte("audio"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == "" {
		t.Error("expected a non-empty ID but got empty string")
	}
}

func TestClipStore_Add_ReturnsDifferentIDsForEachCall(t *testing.T) {
	store := NewClipStore(10)

	id1, err := store.Add([]byte("audio1"))
	if err != nil {
		t.Fatalf("unexpected error on first Add: %v", err)
	}
	id2, err := store.Add([]byte("audio2"))
	if err != nil {
		t.Fatalf("unexpected error on second Add: %v", err)
	}

	if id1 == id2 {
		t.Errorf("expected unique IDs but both were %q", id1)
	}
}

func TestClipStore_Get_ReturnsStoredBytes(t *testing.T) {
	store := NewClipStore(10)
	audio := []byte{0xFF, 0xFB, 0x90, 0x00}

	id, err := store.Add(audio)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := store.Get(id)
	if !ok {
		t.Fatalf("expected to find clip %q but Get returned false", id)
	}
	if string(got) != string(audio) {
		t.Errorf("bytes mismatch: got %v, want %v", got, audio)
	}
}

func TestClipStore_Get_ReturnsFalseForUnknownID(t *testing.T) {
	store := NewClipStore(10)

	_, ok := store.Get("does-not-exist")
	if ok {
		t.Error("expected Get to return false for an unknown ID")
	}
}

func TestClipStore_Evicts_OldestWhenAtCapacity(t *testing.T) {
	const capacity = 3
	store := NewClipStore(capacity)

	// Fill the store to capacity.
	ids := make([]string, capacity)
	for i := 0; i < capacity; i++ {
		id, err := store.Add([]byte{byte(i)})
		if err != nil {
			t.Fatalf("Add %d failed: %v", i, err)
		}
		ids[i] = id
	}

	// Adding one more must evict the oldest (ids[0]).
	newID, err := store.Add([]byte{99})
	if err != nil {
		t.Fatalf("Add beyond capacity failed: %v", err)
	}

	// The evicted clip must be gone.
	_, stillExists := store.Get(ids[0])
	if stillExists {
		t.Errorf("expected oldest clip %q to be evicted but it is still retrievable", ids[0])
	}

	// The remaining older clips and the new one must all be present.
	for _, id := range append(ids[1:], newID) {
		if _, ok := store.Get(id); !ok {
			t.Errorf("expected clip %q to be present after eviction but Get returned false", id)
		}
	}
}

func TestClipStore_Evicts_SequentiallyOldestFirst(t *testing.T) {
	const capacity = 3
	store := NewClipStore(capacity)

	// Add capacity+2 clips; the first two should be evicted.
	ids := make([]string, capacity+2)
	for i := 0; i < capacity+2; i++ {
		id, err := store.Add([]byte{byte(i)})
		if err != nil {
			t.Fatalf("Add %d failed: %v", i, err)
		}
		ids[i] = id
	}

	// ids[0] and ids[1] should be evicted.
	for _, evicted := range ids[:2] {
		if _, ok := store.Get(evicted); ok {
			t.Errorf("expected clip %q to be evicted but it is still retrievable", evicted)
		}
	}

	// ids[2], ids[3], ids[4] should be present.
	for _, present := range ids[2:] {
		if _, ok := store.Get(present); !ok {
			t.Errorf("expected clip %q to be present but Get returned false", present)
		}
	}
}

func TestClipStore_ConcurrentAdd_DoesNotPanic(t *testing.T) {
	store := NewClipStore(50)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			_, err := store.Add([]byte{byte(n)})
			if err != nil {
				t.Errorf("concurrent Add returned error: %v", err)
			}
		}(i)
	}

	wg.Wait()
	// If we reach here without a panic the test passes.
}

// ---------------------------------------------------------------------------
// Additional edge cases
// ---------------------------------------------------------------------------

// TestClipStore_ExactCapacity_NoEviction verifies that filling the store to
// exactly its capacity does not evict any clip and that the order slice length
// equals capacity.
func TestClipStore_ExactCapacity_NoEviction(t *testing.T) {
	const capacity = 5
	store := NewClipStore(capacity)

	ids := make([]string, capacity)
	for i := 0; i < capacity; i++ {
		id, err := store.Add([]byte{byte(i)})
		if err != nil {
			t.Fatalf("Add %d failed: %v", i, err)
		}
		ids[i] = id
	}

	// All clips must still be retrievable — nothing should be evicted.
	for i, id := range ids {
		if _, ok := store.Get(id); !ok {
			t.Errorf("clip %d (%q) was unexpectedly evicted at exact capacity", i, id)
		}
	}

	// order slice length must equal capacity.
	store.mu.Lock()
	orderLen := len(store.order)
	store.mu.Unlock()

	if orderLen != capacity {
		t.Errorf("expected order length %d but got %d", capacity, orderLen)
	}
}

// TestClipStore_OneOverCapacity_OldestEvicted adds exactly capacity+1 clips and
// verifies: the oldest is evicted, the rest are present, and len(order) == capacity.
func TestClipStore_OneOverCapacity_OldestEvicted(t *testing.T) {
	const capacity = 4
	store := NewClipStore(capacity)

	ids := make([]string, capacity+1)
	for i := 0; i <= capacity; i++ {
		id, err := store.Add([]byte{byte(i)})
		if err != nil {
			t.Fatalf("Add %d failed: %v", i, err)
		}
		ids[i] = id
	}

	// Oldest (ids[0]) must be evicted.
	if _, ok := store.Get(ids[0]); ok {
		t.Errorf("oldest clip %q should have been evicted but was still found", ids[0])
	}

	// The remaining capacity clips must be present.
	for _, id := range ids[1:] {
		if _, ok := store.Get(id); !ok {
			t.Errorf("clip %q should be present after one-over-capacity add but was not found", id)
		}
	}

	// order length must remain at capacity.
	store.mu.Lock()
	orderLen := len(store.order)
	store.mu.Unlock()

	if orderLen != capacity {
		t.Errorf("expected order length %d after eviction but got %d", capacity, orderLen)
	}
}

// TestClipStore_GetFromEmpty verifies that Get on a freshly created store
// returns (nil, false) without panicking.
func TestClipStore_GetFromEmpty(t *testing.T) {
	store := NewClipStore(10)

	data, ok := store.Get("any-id")
	if ok {
		t.Error("expected ok=false from empty store but got true")
	}
	if data != nil {
		t.Errorf("expected nil data from empty store but got %v", data)
	}
}

// TestClipStore_Add_SameContentGetsDifferentIDs asserts that two Add calls with
// identical byte slices produce distinct UUIDs (UUIDs are content-independent).
func TestClipStore_Add_SameContentGetsDifferentIDs(t *testing.T) {
	store := NewClipStore(10)

	payload := []byte("identical audio content")
	id1, err := store.Add(payload)
	if err != nil {
		t.Fatalf("first Add failed: %v", err)
	}
	id2, err := store.Add(payload)
	if err != nil {
		t.Fatalf("second Add failed: %v", err)
	}

	if id1 == id2 {
		t.Errorf("expected distinct IDs for identical content but both were %q", id1)
	}
}

// TestClipStore_Close_ClearsAllClips verifies that after Close(), previously
// stored clips are no longer retrievable.
func TestClipStore_Close_ClearsAllClips(t *testing.T) {
	store := NewClipStore(10)

	id, err := store.Add([]byte("some audio"))
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Confirm the clip is present before Close.
	if _, ok := store.Get(id); !ok {
		t.Fatalf("clip %q should be present before Close but was not found", id)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close returned unexpected error: %v", err)
	}

	data, ok := store.Get(id)
	if ok {
		t.Errorf("expected Get to return false after Close but got true")
	}
	if data != nil {
		t.Errorf("expected nil data after Close but got %v", data)
	}
}

// TestClipStore_ConcurrentAdd_10Goroutines_RaceDetector stresses the store with
// 10 concurrent goroutines and verifies all IDs are non-empty and unique.
// Run with -race to catch data races.
func TestClipStore_ConcurrentAdd_10Goroutines_RaceDetector(t *testing.T) {
	const goroutines = 10
	store := NewClipStore(goroutines * 2)

	ids := make([]string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			id, err := store.Add([]byte{byte(n)})
			if err != nil {
				t.Errorf("goroutine %d: Add returned error: %v", n, err)
				return
			}
			ids[n] = id
		}(i)
	}

	wg.Wait()

	// All IDs must be non-empty.
	for i, id := range ids {
		if id == "" {
			t.Errorf("goroutine %d produced an empty ID", i)
		}
	}

	// All IDs must be unique.
	seen := make(map[string]int, goroutines)
	for i, id := range ids {
		if prev, exists := seen[id]; exists {
			t.Errorf("goroutines %d and %d produced the same ID %q", prev, i, id)
		}
		seen[id] = i
	}
}

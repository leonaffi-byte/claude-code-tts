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

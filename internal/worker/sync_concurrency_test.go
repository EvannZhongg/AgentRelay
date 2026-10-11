package worker

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSyncStoreSerializesOnlyTheSameSession(t *testing.T) {
	store := newSyncStore(t.TempDir())
	first := &syncState{id: "first", stage: filepath.Join(store.workspace, "first")}
	second := &syncState{id: "second", stage: filepath.Join(store.workspace, "second")}
	first.expires.Store(time.Now().Add(time.Minute).UnixNano())
	second.expires.Store(time.Now().Add(time.Minute).UnixNano())
	store.values[first.id] = first
	store.values[second.id] = second

	lockedFirst := store.acquire(first.id)
	if lockedFirst == nil {
		t.Fatal("failed to acquire first session")
	}
	defer lockedFirst.mu.Unlock()

	acquired := make(chan *syncState, 1)
	go func() {
		session := store.acquire(second.id)
		acquired <- session
	}()
	select {
	case session := <-acquired:
		if session == nil {
			t.Fatal("failed to acquire second session")
		}
		session.mu.Unlock()
	case <-time.After(time.Second):
		t.Fatal("independent sync session was blocked")
	}
}

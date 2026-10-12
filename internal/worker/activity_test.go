package worker

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"AgentRelay/internal/protocol"
)

func TestActivityUsesProcessCompletionAndSyncExpiry(t *testing.T) {
	s, err := New(Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer func() {
		// Avoid process cleanup of these synthetic records, including assertion failure.
		s.tasks.values = map[string]*protocol.Task{}
		s.sessions.values = map[string]*sessionState{}
		s.syncs.values = map[string]*syncState{}
	}()
	s.tasks.values["cancelled"] = &protocol.Task{ID: "cancelled", Status: "cancelled"}
	s.sessions.values["shell"] = &sessionState{task: protocol.Session{ID: "shell", Status: "running"}}
	active, expired := &syncState{}, &syncState{}
	active.expires.Store(time.Now().Add(time.Minute).UnixNano())
	expired.expires.Store(time.Now().Add(-time.Minute).UnixNano())
	s.syncs.values["active"], s.syncs.values["expired"] = active, expired
	s.requests.Store(1)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/activity", nil))
	var a protocol.Activity
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Requests != 1 || a.Tasks != 1 || a.Sessions != 1 || a.Syncs != 1 || !a.Busy() {
		t.Fatalf("activity: %+v", a)
	}
}

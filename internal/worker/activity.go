package worker

import (
	"net/http"
	"time"

	"AgentRelay/internal/protocol"
)

// Private adapter endpoint, never routed through the device operation API.
func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a := protocol.Activity{Requests: s.requests.Load()}
	for _, task := range s.tasks.list() {
		// Cancellation is asynchronous: status alone does not prove process exit.
		if task.FinishedAt == nil {
			a.Tasks++
		}
	}
	for _, session := range s.sessions.list() {
		if session.FinishedAt == nil {
			a.Sessions++
		}
	}
	s.syncs.mu.Lock()
	for _, session := range s.syncs.values {
		if session.expires.Load() > time.Now().UnixNano() {
			a.Syncs++
		}
	}
	s.syncs.mu.Unlock()
	writeJSON(w, http.StatusOK, a)
}

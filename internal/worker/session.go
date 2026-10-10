package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"AgentRelay/internal/pathutil"
	"AgentRelay/internal/protocol"
)

const sessionOutputLimit = outputLimit

type sessionState struct {
	mu      sync.RWMutex
	inputMu sync.Mutex
	task    protocol.Session
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	closed  bool
	done    chan struct{}
}

type sessionStore struct {
	mu     sync.RWMutex
	next   uint64
	values map[string]*sessionState
}

func newSessionStore() *sessionStore { return &sessionStore{values: map[string]*sessionState{}} }

func (s *Server) sessionsRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req protocol.SessionRequest
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		session, err := s.sessions.start(s.cfg.Workspace, req)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errStoreCapacity) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusCreated, session)
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.sessions.list())
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) sessionByID(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/sessions/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, errors.New("session id is required"))
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "input" && r.Method == http.MethodPost {
		var input protocol.SessionInput
		if err := readJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.sessions.write(id, input.Input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, s.sessions.getOrError(id))
		return
	}
	if len(parts) != 1 {
		methodNotAllowed(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		session, ok := s.sessions.get(id)
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("session not found"))
			return
		}
		writeJSON(w, http.StatusOK, session)
	case http.MethodDelete:
		if !s.sessions.close(id) {
			writeError(w, http.StatusNotFound, errors.New("session not found"))
			return
		}
		writeJSON(w, http.StatusOK, s.sessions.getOrError(id))
	default:
		methodNotAllowed(w)
	}
}

func (s *sessionStore) start(workspace string, req protocol.SessionRequest) (protocol.Session, error) {
	workDir, err := pathutil.ResolveUnderRoot(workspace, req.WorkingDir)
	if err != nil {
		return protocol.Session{}, err
	}
	if info, statErr := os.Stat(workDir); statErr != nil || !info.IsDir() {
		return protocol.Session{}, fmt.Errorf("working directory does not exist: %s", req.WorkingDir)
	}
	cmd := shellCommand(context.Background(), req.Command, true)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for key, value := range req.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	s.mu.Lock()
	s.pruneLocked(time.Now(), storeRecordLimit-1)
	if len(s.values) >= storeRecordLimit {
		s.mu.Unlock()
		return protocol.Session{}, fmt.Errorf("%w; close existing sessions", errStoreCapacity)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		s.mu.Unlock()
		return protocol.Session{}, err
	}
	id := fmt.Sprintf("session-%d", atomic.AddUint64(&s.next, 1))
	state := &sessionState{task: protocol.Session{ID: id, Status: "running", Command: req.Command, StartedAt: time.Now()}, cmd: cmd, stdin: stdin, done: make(chan struct{})}
	cmd.Stdout = sessionOutput{state: state}
	cmd.Stderr = sessionOutput{state: state, stderr: true}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		_ = stdin.Close()
		return protocol.Session{}, err
	}
	s.values[id] = state
	s.mu.Unlock()
	if shellPreamble != "" || strings.TrimSpace(req.Command) != "" {
		// Serialize the initial command before accepting subsequent input.
		state.inputMu.Lock()
		go func() {
			defer state.inputMu.Unlock()
			_, _ = io.WriteString(stdin, shellPreamble+"\n"+req.Command+"\n\n")
		}()
	}
	go func() {
		defer close(state.done)
		err := cmd.Wait()
		_ = stdin.Close()
		now := time.Now()
		state.mu.Lock()
		defer state.mu.Unlock()
		state.task.FinishedAt = &now
		if state.closed {
			return
		}
		state.task.Status = "completed"
		code := 0
		state.task.ExitCode = &code
		if err != nil {
			state.task.Status = "failed"
			state.task.Error = err.Error()
			if exitErr, ok := err.(*exec.ExitError); ok {
				code := exitErr.ExitCode()
				state.task.ExitCode = &code
			}
		}
	}()
	return state.snapshot(), nil
}

type sessionOutput struct {
	state  *sessionState
	stderr bool
}

func (output sessionOutput) Write(data []byte) (int, error) {
	state := output.state
	state.mu.Lock()
	defer state.mu.Unlock()
	target := &state.task.Stdout
	if output.stderr {
		target = &state.task.Stderr
	}
	remaining := sessionOutputLimit - len(*target)
	if len(data) > remaining {
		state.task.OutputTruncated = true
	}
	*target += string(data[:min(len(data), remaining)])
	return len(data), nil
}

func (s *sessionStore) write(id, input string) error {
	state, ok := s.state(id)
	if !ok {
		return errors.New("session not found")
	}
	state.inputMu.Lock()
	defer state.inputMu.Unlock()
	state.mu.RLock()
	running := !state.closed && state.task.Status == "running"
	state.mu.RUnlock()
	if !running {
		return errors.New("session is not running")
	}
	_, err := io.WriteString(state.stdin, input)
	return err
}
func (s *sessionStore) close(id string) bool {
	state, ok := s.state(id)
	if !ok {
		return false
	}
	state.mu.Lock()
	shouldClose := !state.closed && state.task.Status == "running"
	if shouldClose {
		state.closed = true
		state.task.Status = "closed"
		now := time.Now()
		state.task.FinishedAt = &now
	}
	state.mu.Unlock()
	if shouldClose {
		// Closing stdin must not wait for a blocked input writer's lock.
		_ = state.stdin.Close()
		_ = killProcessTree(state.cmd)
	}
	select {
	case <-state.done:
	case <-time.After(3 * time.Second):
	}
	return true
}
func (s *sessionStore) state(id string) (*sessionState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now(), storeRecordLimit)
	value, ok := s.values[id]
	return value, ok
}
func (s *sessionStore) get(id string) (protocol.Session, bool) {
	state, ok := s.state(id)
	if !ok {
		return protocol.Session{}, false
	}
	return state.snapshot(), true
}
func (s *sessionStore) getOrError(id string) protocol.Session { value, _ := s.get(id); return value }
func (s *sessionStore) list() []protocol.Session {
	s.mu.Lock()
	s.pruneLocked(time.Now(), storeRecordLimit)
	values := make([]*sessionState, 0, len(s.values))
	for _, state := range s.values {
		values = append(values, state)
	}
	s.mu.Unlock()
	result := make([]protocol.Session, 0, len(values))
	for _, state := range values {
		result = append(result, state.snapshot())
	}
	return result
}

func (s *sessionStore) pruneLocked(now time.Time, limit int) {
	var records []finishedRecord
	for id, state := range s.values {
		select {
		case <-state.done:
			if session := state.snapshot(); session.FinishedAt != nil {
				records = append(records, finishedRecord{id, *session.FinishedAt})
			}
		default:
		}
	}
	for _, id := range expiredRecords(records, len(s.values), limit, now) {
		delete(s.values, id)
	}
}
func (state *sessionState) snapshot() protocol.Session {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.task
}

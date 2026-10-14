package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"DevFreight/internal/desktop"
	"DevFreight/internal/pathutil"
	"DevFreight/internal/protocol"
)

type Config struct {
	Desktop       desktop.Backend
	Workspace     string
	WorkspaceMode string
	Name          string
	DeviceID      string
}

type Server struct {
	requests  atomic.Int64
	desktop   *desktop.Service
	cfg       Config
	startedAt time.Time
	tasks     *taskStore
	sessions  *sessionStore
	syncs     *syncStore
}

func New(cfg Config) (*Server, error) {
	if cfg.Workspace == "" {
		return nil, errors.New("workspace is required")
	}
	if cfg.WorkspaceMode == "" {
		cfg.WorkspaceMode = protocol.WorkspaceLocalSync
	}
	if cfg.WorkspaceMode != protocol.WorkspaceLocalSync && cfg.WorkspaceMode != protocol.WorkspaceRemote {
		return nil, errors.New("workspace mode must be local_sync or remote")
	}
	root, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	if cfg.Name == "" {
		cfg.Name, _ = os.Hostname()
	}
	cfg.Workspace = root
	d := desktop.New()
	if cfg.Desktop != nil {
		d = desktop.NewWithBackend(cfg.Desktop)
	}
	return &Server{cfg: cfg, desktop: d, startedAt: time.Now(), tasks: newTaskStore(), sessions: newSessionStore(), syncs: newSyncStore(root)}, nil
}

func (s *Server) Device() protocol.Device {
	host, _ := os.Hostname()
	capability := desktop.Capability()
	if s.cfg.Desktop != nil {
		if info, err := s.cfg.Desktop.Info(); err == nil {
			capability = &info.DesktopCapability
		}
	}
	return protocol.Device{
		ID: s.cfg.DeviceID, Name: s.cfg.Name, OS: runtime.GOOS, Workspace: s.cfg.Workspace, WorkspaceMode: s.cfg.WorkspaceMode,
		Architecture: runtime.GOARCH, Capabilities: protocol.Capabilities{Desktop: capability, Exec: true, Files: true, Sync: true, Tasks: true, Sessions: true, Shells: []string{nativeShell}, MaxPayload: 32 << 20},
		Metadata: map[string]string{"hostname": host, "workspace": s.cfg.Workspace}, LastSeen: time.Now(), Online: true,
	}
}

func (s *Server) Close() {
	s.syncs.mu.Lock()
	syncSessions := make([]*syncState, 0, len(s.syncs.values))
	for _, session := range s.syncs.values {
		syncSessions = append(syncSessions, session)
	}
	s.syncs.values = map[string]*syncState{}
	s.syncs.mu.Unlock()
	for _, session := range syncSessions {
		session.mu.Lock()
		_ = os.RemoveAll(session.stage)
		session.mu.Unlock()
	}
	for _, task := range s.tasks.list() {
		s.tasks.cancel(task.ID)
	}
	var closing sync.WaitGroup
	for _, session := range s.sessions.list() {
		closing.Add(1)
		go func(id string) { defer closing.Done(); s.sessions.close(id) }(session.ID)
	}
	closing.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.tasks.mu.RLock()
		running := len(s.tasks.cancels)
		s.tasks.mu.RUnlock()
		if running == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/activity", s.activity)
	mux.HandleFunc("/v1/health", s.health)
	mux.HandleFunc("/v1/device", s.device)
	mux.HandleFunc("/v1/desktop", s.desktopInfo)
	mux.HandleFunc("/v1/desktop/capture", s.desktopCapture)
	mux.HandleFunc("/v1/desktop/input", s.desktopInput)
	mux.HandleFunc("/v1/exec", s.exec)
	mux.HandleFunc("/v1/files/sync/start", s.syncStart)
	mux.HandleFunc("/v1/files/sync/", s.syncSession)
	mux.HandleFunc("/v1/files/read", s.readFile)
	mux.HandleFunc("/v1/tasks", s.tasksRoot)
	mux.HandleFunc("/v1/tasks/", s.taskByID)
	mux.HandleFunc("/v1/sessions", s.sessionsRoot)
	mux.HandleFunc("/v1/sessions/", s.sessionByID)
	return withJSONHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/activity" {
			s.requests.Add(1)
			defer s.requests.Add(-1)
		}
		mux.ServeHTTP(w, r)
	}))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, protocol.Health{Status: "ok", APIVersion: protocol.APIVersion, UptimeMS: time.Since(s.startedAt).Milliseconds()})
}

func (s *Server) device(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, s.Device())
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req protocol.ExecRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.run(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) run(parent context.Context, req protocol.ExecRequest) (*protocol.ExecResponse, error) {
	if strings.TrimSpace(req.Command) == "" {
		return nil, errors.New("command is required")
	}
	if req.TimeoutMS < 0 || req.TimeoutMS > (1<<63-1)/int64(time.Millisecond) {
		return nil, errors.New("timeout_ms is outside the supported duration range")
	}
	workDir, err := pathutil.ResolveUnderRoot(s.cfg.Workspace, req.WorkingDir)
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Stat(workDir); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("working directory does not exist: %s", req.WorkingDir)
	}
	timeout := 30 * time.Minute
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	start := time.Now()
	cmd := shellCommand(ctx, req.Command, false)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = workDir
	cmd.Env = cmd.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr limitedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	timedOut := ctx.Err() == context.DeadlineExceeded
	exitCode := 0
	if runErr != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	if runErr != nil && !timedOut && ctx.Err() == nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return nil, runErr
		}
	}
	return &protocol.ExecResponse{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode, DurationMS: time.Since(start).Milliseconds(), TimedOut: timedOut, OutputTruncated: stdout.truncated || stderr.truncated}, nil
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	path, err := pathutil.ResolveUnderRoot(s.cfg.Workspace, r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.ReadResponse{Path: r.URL.Query().Get("path"), ContentBase64: encodeBase64(data), Bytes: int64(len(data))})
}

func (s *Server) tasksRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req protocol.TaskRequest
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		task, err := s.tasks.start(req.ExecRequest, func(ctx context.Context, req protocol.ExecRequest) (*protocol.ExecResponse, error) {
			return s.run(ctx, req)
		})
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusAccepted, task)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.tasks.list())
		return
	}
	methodNotAllowed(w)
}

func (s *Server) taskByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
	if strings.HasSuffix(id, "/retrieve") && r.Method == http.MethodPost {
		task, err := s.tasks.retrieve(strings.TrimSuffix(id, "/retrieve"))
		if err != nil {
			status := http.StatusConflict
			if errors.Is(err, errTaskNotFound) {
				status = http.StatusNotFound
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, task)
		return
	}
	if r.Method == http.MethodGet {
		task, ok := s.tasks.get(id)
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("task not found"))
			return
		}
		writeJSON(w, http.StatusOK, task)
		return
	}
	if r.Method == http.MethodDelete {
		if !s.tasks.cancel(id) {
			writeError(w, http.StatusNotFound, errors.New("task not found"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	methodNotAllowed(w)
}

type taskStore struct {
	mu      sync.RWMutex
	next    uint64
	values  map[string]*protocol.Task
	cancels map[string]context.CancelFunc
}

func newTaskStore() *taskStore {
	return &taskStore{values: map[string]*protocol.Task{}, cancels: map[string]context.CancelFunc{}}
}
func (t *taskStore) start(req protocol.ExecRequest, run func(context.Context, protocol.ExecRequest) (*protocol.ExecResponse, error)) (*protocol.Task, error) {
	id := fmt.Sprintf("task-%d", atomic.AddUint64(&t.next, 1))
	task := &protocol.Task{ID: id, Status: "running", Command: req.Command, StartedAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.pruneLocked(time.Now(), storeRecordLimit-1)
	if len(t.values) >= storeRecordLimit {
		t.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("%w; cancel existing tasks", errStoreCapacity)
	}
	t.values[id] = task
	t.cancels[id] = cancel
	initial := *task
	t.mu.Unlock()
	go func() {
		defer cancel()
		result, err := run(ctx, req)
		now := time.Now()
		t.mu.Lock()
		defer t.mu.Unlock()
		defer t.pruneLocked(now, storeRecordLimit)
		delete(t.cancels, id)
		task.FinishedAt = &now
		task.Result = result
		if task.Status == "cancelled" {
			return
		}
		if err != nil {
			task.Status = "failed"
			task.Error = err.Error()
		} else {
			task.Status = "completed"
			if result != nil && (result.ExitCode != 0 || result.TimedOut) {
				task.Status = "failed"
				if result.TimedOut {
					task.Error = "execution timed out"
				} else {
					task.Error = fmt.Sprintf("command exited with code %d", result.ExitCode)
				}
			}
		}
	}()
	return &initial, nil
}
func (t *taskStore) get(id string) (*protocol.Task, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(time.Now(), storeRecordLimit)
	v, ok := t.values[id]
	if !ok {
		return nil, false
	}
	copy := *v
	return &copy, true
}
func (t *taskStore) list() []*protocol.Task {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(time.Now(), storeRecordLimit)
	out := make([]*protocol.Task, 0, len(t.values))
	for _, v := range t.values {
		copy := *v
		out = append(out, &copy)
	}
	return out
}
func (t *taskStore) cancel(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(time.Now(), storeRecordLimit)
	task, ok := t.values[id]
	if !ok {
		return false
	}
	if cancel, exists := t.cancels[id]; exists {
		cancel()
		task.Status = "cancelled"
	}
	return true
}

var errTaskNotFound = errors.New("task not found")

func (t *taskStore) retrieve(id string) (*protocol.Task, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(time.Now(), storeRecordLimit)
	task, ok := t.values[id]
	if !ok {
		return nil, errTaskNotFound
	}
	if task.FinishedAt == nil {
		return nil, errors.New("task is still running; use get to poll before retrieve")
	}
	copy := *task
	delete(t.values, id)
	return &copy, nil
}

func (t *taskStore) pruneLocked(now time.Time, limit int) {
	var records []finishedRecord
	for id, task := range t.values {
		if task.FinishedAt != nil {
			records = append(records, finishedRecord{id, *task.FinishedAt})
		}
	}
	for _, id := range expiredRecords(records, len(t.values), limit, now) {
		delete(t.values, id)
	}
}

func withJSONHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func readJSON(r *http.Request, dst any) error {
	r.Body = io.NopCloser(io.LimitReader(r.Body, 64<<20))
	return json.NewDecoder(r.Body).Decode(dst)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, protocol.ErrorResponse{Error: err.Error()})
}
func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}
func encodeBase64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

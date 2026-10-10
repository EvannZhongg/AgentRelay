package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

	"AgentRelay/internal/pathutil"
	"AgentRelay/internal/protocol"
)

type Config struct {
	ListenAddr string
	Workspace  string
	Name       string
}

type Server struct {
	cfg       Config
	startedAt time.Time
	tasks     *taskStore
	sessions  *sessionStore
}

func New(cfg Config) (*Server, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8765"
	}
	if cfg.Workspace == "" {
		return nil, errors.New("workspace is required")
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
	return &Server{cfg: Config{ListenAddr: cfg.ListenAddr, Workspace: root, Name: cfg.Name}, startedAt: time.Now(), tasks: newTaskStore(), sessions: newSessionStore()}, nil
}

func (s *Server) Device() protocol.Device {
	host, _ := os.Hostname()
	sum := sha256.Sum256([]byte(host + "|" + s.cfg.Workspace))
	return protocol.Device{
		ID: runtime.GOOS + "-" + hex.EncodeToString(sum[:])[:16], Name: s.cfg.Name, OS: runtime.GOOS,
		Architecture: runtime.GOARCH, Capabilities: protocol.Capabilities{Exec: true, Files: true, Tasks: true, Sessions: true, Shells: []string{nativeShell}, MaxPayload: 32 << 20},
		Metadata: map[string]string{"hostname": host, "workspace": s.cfg.Workspace}, LastSeen: time.Now(), Online: true,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.health)
	mux.HandleFunc("/v1/device", s.device)
	mux.HandleFunc("/v1/exec", s.exec)
	mux.HandleFunc("/v1/files/sync", s.syncFiles)
	mux.HandleFunc("/v1/files/read", s.readFile)
	mux.HandleFunc("/v1/tasks", s.tasksRoot)
	mux.HandleFunc("/v1/tasks/", s.taskByID)
	mux.HandleFunc("/v1/sessions", s.sessionsRoot)
	mux.HandleFunc("/v1/sessions/", s.sessionByID)
	return withJSONHeaders(mux)
}

func (s *Server) ListenAndServe() error { return http.ListenAndServe(s.cfg.ListenAddr, s.Handler()) }

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
	cmd.Env = os.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
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
	return &protocol.ExecResponse{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode, DurationMS: time.Since(start).Milliseconds(), TimedOut: timedOut}, nil
}

func (s *Server) syncFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req protocol.SyncRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	root, err := pathutil.ResolveUnderRoot(s.cfg.Workspace, req.Root)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var result protocol.SyncResponse
	for _, file := range req.Files {
		path, pathErr := pathutil.ResolveUnderRoot(root, file.Path)
		if pathErr != nil {
			writeError(w, http.StatusBadRequest, pathErr)
			return
		}
		data, decodeErr := decodeBase64(file.ContentBase64)
		if decodeErr != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("%s: %w", file.Path, decodeErr))
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		result.Written = append(result.Written, file.Path)
		result.Bytes += int64(len(data))
	}
	writeJSON(w, http.StatusOK, result)
}

func decodeBase64(v string) ([]byte, error) { return base64.StdEncoding.DecodeString(v) }

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
		task := s.tasks.start(req.ExecRequest, func(ctx context.Context, req protocol.ExecRequest) (*protocol.ExecResponse, error) {
			return s.run(ctx, req)
		})
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
func (t *taskStore) start(req protocol.ExecRequest, run func(context.Context, protocol.ExecRequest) (*protocol.ExecResponse, error)) *protocol.Task {
	id := fmt.Sprintf("task-%d", atomic.AddUint64(&t.next, 1))
	task := &protocol.Task{ID: id, Status: "running", Command: req.Command, StartedAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
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
			}
		}
	}()
	return &initial
}
func (t *taskStore) get(id string) (*protocol.Task, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	v, ok := t.values[id]
	if !ok {
		return nil, false
	}
	copy := *v
	return &copy, true
}
func (t *taskStore) list() []*protocol.Task {
	t.mu.RLock()
	defer t.mu.RUnlock()
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

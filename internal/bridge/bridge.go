package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"AgentRelay/internal/protocol"
)

type Config struct{ ListenAddr string }
type Server struct {
	cfg     Config
	mu      sync.RWMutex
	devices map[string]protocol.Device
	client  *http.Client
}

func New(cfg Config) *Server {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8764"
	}
	return &Server{cfg: cfg, devices: map[string]protocol.Device{}, client: &http.Client{Timeout: 30 * time.Minute}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.health)
	mux.HandleFunc("/v1/devices", s.devicesRoot)
	mux.HandleFunc("/v1/devices/", s.deviceRoute)
	return withJSONHeaders(mux)
}
func (s *Server) ListenAndServe() error { return http.ListenAndServe(s.cfg.ListenAddr, s.Handler()) }

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, protocol.Health{Status: "ok", APIVersion: protocol.APIVersion})
}

type registerRequest struct {
	WorkerURL string `json:"worker_url"`
	Name      string `json:"name,omitempty"`
}

func (s *Server) devicesRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		out := make([]protocol.Device, 0, len(s.devices))
		for _, d := range s.devices {
			out = append(out, d)
		}
		s.mu.RUnlock()
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var req registerRequest
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		d, err := s.register(r.Context(), req)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusCreated, d)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) register(ctx context.Context, req registerRequest) (protocol.Device, error) {
	u := strings.TrimRight(strings.TrimSpace(req.WorkerURL), "/")
	if u == "" {
		return protocol.Device{}, errors.New("worker_url is required")
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return protocol.Device{}, errors.New("worker_url must be an absolute HTTP URL")
	}
	var d protocol.Device
	if err := s.call(ctx, u, "/v1/device", http.MethodGet, nil, &d); err != nil {
		return d, err
	}
	d.WorkerURL = u
	if req.Name != "" {
		d.Name = req.Name
	}
	d.LastSeen = time.Now()
	s.mu.Lock()
	s.devices[d.ID] = d
	s.mu.Unlock()
	return d, nil
}

func (s *Server) call(ctx context.Context, base, endpoint, method string, body io.Reader, out any) error {
	status, data, err := s.proxy(ctx, base, endpoint, method, body, "")
	if err != nil {
		return err
	}
	if status >= 300 {
		var e protocol.ErrorResponse
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("worker returned HTTP %d", status)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (s *Server) deviceRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/devices/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, errors.New("device id is required"))
		return
	}
	id := parts[0]
	s.mu.RLock()
	d, ok := s.devices[id]
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("device not found"))
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		s.mu.Lock()
		delete(s.devices, id)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) < 2 {
		methodNotAllowed(w)
		return
	}
	endpoint := "/v1/" + strings.Join(parts[1:], "/")
	status, body, err := s.proxy(r.Context(), d.WorkerURL, endpoint, r.Method, r.Body, r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) proxy(ctx context.Context, base, endpoint, method string, body io.Reader, rawQuery string) (int, []byte, error) {
	target := strings.TrimRight(base, "/") + endpoint
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func NewClient(base string) *Client {
	return &Client{BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 30 * time.Minute}}
}
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e protocol.ErrorResponse
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
func (c *Client) Devices(ctx context.Context) ([]protocol.Device, error) {
	var out []protocol.Device
	return out, c.Do(ctx, http.MethodGet, "/v1/devices", nil, &out)
}
func (c *Client) AddDevice(ctx context.Context, workerURL, name string) (protocol.Device, error) {
	var out protocol.Device
	err := c.Do(ctx, http.MethodPost, "/v1/devices", registerRequest{WorkerURL: workerURL, Name: name}, &out)
	return out, err
}
func (c *Client) Exec(ctx context.Context, id string, req protocol.ExecRequest) (protocol.ExecResponse, error) {
	var out protocol.ExecResponse
	err := c.Do(ctx, http.MethodPost, "/v1/devices/"+url.PathEscape(id)+"/exec", req, &out)
	return out, err
}
func (c *Client) Sync(ctx context.Context, id string, req protocol.SyncRequest) (protocol.SyncResponse, error) {
	var out protocol.SyncResponse
	err := c.Do(ctx, http.MethodPost, "/v1/devices/"+url.PathEscape(id)+"/files/sync", req, &out)
	return out, err
}
func (c *Client) StartTask(ctx context.Context, id string, req protocol.TaskRequest) (protocol.Task, error) {
	var out protocol.Task
	err := c.Do(ctx, http.MethodPost, "/v1/devices/"+url.PathEscape(id)+"/tasks", req, &out)
	return out, err
}
func (c *Client) GetTask(ctx context.Context, id, taskID string) (protocol.Task, error) {
	var out protocol.Task
	err := c.Do(ctx, http.MethodGet, "/v1/devices/"+url.PathEscape(id)+"/tasks/"+url.PathEscape(taskID), nil, &out)
	return out, err
}

func withJSONHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
func readJSON(r *http.Request, dst any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(dst)
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

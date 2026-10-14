package controller

import (
	"DevFreight/internal/connection"
	"DevFreight/internal/protocol"
	"DevFreight/internal/state"
	"DevFreight/internal/transport"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const syncMappingRetention = time.Hour

type Config struct {
	ListenAddr, StatePath string
	Adapters              connection.Registry
}
type record struct {
	Device protocol.Device `json:"device"`
}
type connectionRecord struct {
	Connection protocol.Connection `json:"connection"`
	TokenHash  string              `json:"token_hash,omitempty"`
}
type registry struct {
	Schema      int                         `json:"schema"`
	Identity    transport.Identity          `json:"identity"`
	Devices     map[string]record           `json:"devices"`
	Connections map[string]connectionRecord `json:"connections"`
}
type Server struct {
	cfg          Config
	mu           sync.RWMutex
	store        registry
	workerPeers  map[string]*transport.Peer
	adapters     connection.Registry
	calls        map[string]map[string]context.CancelFunc
	pairings     map[string]*pendingPairing
	syncMappings map[syncMappingKey]syncMapping
	operations   map[string]int
	changing     map[string]bool
	syncStarts   map[string]int
}

type syncMappingKey struct {
	deviceID string
	syncID   string
}

type syncMapping struct {
	connectionID string
	sourcePath   string
	root         string
	expiresAt    time.Time
}

type pendingPairing struct {
	request     protocol.PairingRequest
	status      string
	credentials *protocol.Credentials
}

func New(cfg Config) (*Server, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8764"
	}
	if cfg.StatePath == "" {
		cfg.StatePath = state.Path("controller")
	}
	s := &Server{cfg: cfg, workerPeers: map[string]*transport.Peer{}, calls: map[string]map[string]context.CancelFunc{}, pairings: map[string]*pendingPairing{}, syncMappings: map[syncMappingKey]syncMapping{}}
	s.adapters = connection.Defaults()
	for kind, adapter := range cfg.Adapters {
		s.adapters[kind] = adapter
	}
	if err := state.Load(cfg.StatePath, &s.store); err != nil {
		return nil, fmt.Errorf("load controller state: %w", err)
	}
	if s.store.Schema != 4 && (s.store.Schema != 0 || s.store.Identity.Certificate != "" || len(s.store.Devices) != 0 || len(s.store.Connections) != 0) {
		return nil, errors.New("unsupported controller state schema; initialize a new state file")
	}
	s.store.Schema = 4
	for _, rec := range s.store.Devices {
		if rec.Device.Handle == "" {
			return nil, errors.New("invalid device state: missing handle")
		}
	}
	if s.store.Connections == nil {
		s.store.Connections = map[string]connectionRecord{}
	}
	if s.store.Devices == nil {
		s.store.Devices = map[string]record{}
	}
	if err := s.validateRegistry(); err != nil {
		return nil, err
	}
	if s.store.Identity.Certificate == "" {
		identity, err := transport.NewIdentity()
		if err != nil {
			return nil, err
		}
		s.store.Identity = identity
		if err = s.save(); err != nil {
			return nil, err
		}
	}
	if _, err := s.store.Identity.TLS(); err != nil {
		return nil, err
	}
	for id, rec := range s.store.Devices {
		rec.Device.Online = false
		s.store.Devices[id] = rec
	}
	for id, rec := range s.store.Connections {
		rec.Connection.Online = false
		s.store.Connections[id] = rec
	}
	for id := range s.store.Devices {
		s.refreshDeviceLocked(id)
	}
	return s, nil
}
func (s *Server) save() error {
	snapshot := s.store
	snapshot.Devices = make(map[string]record, len(s.store.Devices))
	for id, r := range s.store.Devices {
		// Device records persist identity and the unique binding; connection projections
		// are reconstructed from the connection registry.
		d := protocol.Device{ID: r.Device.ID, Handle: r.Device.Handle, Name: r.Device.Name, Authorized: r.Device.Authorized, DesktopAccess: r.Device.DesktopAccess, ConnectionID: r.Device.ConnectionID}
		snapshot.Devices[id] = record{Device: d}
	}
	return state.Save(s.cfg.StatePath, snapshot)
}
func (s *Server) Fingerprint() string             { return s.store.Identity.Fingerprint() }
func (s *Server) TLSConfig() (*tls.Config, error) { return s.store.Identity.TLS() }
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		writeJSON(w, 200, protocol.Health{Status: "ok", APIVersion: protocol.APIVersion})
	})
	mux.HandleFunc("/v1/pair", s.pair)
	mux.HandleFunc("/v1/pair/", s.pairStatus)
	mux.HandleFunc("/v1/connect", s.connect)
	mux.HandleFunc("/ui", serveUI)
	mux.HandleFunc("/ui/", serveUI)
	admin := http.NewServeMux()
	admin.HandleFunc("/v1/controller", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		writeJSON(w, 200, map[string]string{"fingerprint": s.Fingerprint()})
	})
	admin.HandleFunc("/v1/pair-requests", s.pairRequests)
	admin.HandleFunc("/v1/pair-requests/", s.pairRequestAction)
	admin.HandleFunc("/v1/devices", s.devicesRoot)
	admin.HandleFunc("/v1/devices/", s.deviceRoute)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() || !sameOrigin(r) {
			writeError(w, 403, errors.New("controller API is local only"))
			return
		}
		admin.ServeHTTP(w, r)
	}))
	return mux
}
func (s *Server) ListenAndServe() error {
	cfg, err := s.TLSConfig()
	if err != nil {
		return err
	}
	server := &http.Server{Addr: s.cfg.ListenAddr, Handler: s.Handler(), TLSConfig: cfg, ReadHeaderTimeout: 10 * time.Second}
	return server.ListenAndServeTLS("", "")
}
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.workerPeers {
		p.Close()
	}
	for cid := range s.calls {
		s.cancelConnectionLocked(cid)
	}
}
func readJSON(r *http.Request, dst any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 32<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, protocol.ErrorResponse{Error: err.Error()})
}
func methodNotAllowed(w http.ResponseWriter) { writeError(w, 405, errors.New("method not allowed")) }

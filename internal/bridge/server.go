package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"AgentRelay/internal/backend"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"
	"AgentRelay/internal/transport"
	"github.com/gorilla/websocket"
)

const syncMappingRetention = time.Hour

type Config struct{ ListenAddr, StatePath string }
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
	connections  map[string]*transport.Peer
	sshCalls     map[string]map[string]context.CancelFunc
	pairings     map[string]*pendingPairing
	syncMappings map[syncMappingKey]syncMapping
	syncStarts   map[string]int
}

type syncMappingKey struct {
	deviceID string
	syncID   string
}

type syncMapping struct {
	sourcePath string
	root       string
	expiresAt  time.Time
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
		cfg.StatePath = state.Path("bridge")
	}
	s := &Server{cfg: cfg, connections: map[string]*transport.Peer{}, sshCalls: map[string]map[string]context.CancelFunc{}, pairings: map[string]*pendingPairing{}, syncMappings: map[syncMappingKey]syncMapping{}}
	if err := state.Load(cfg.StatePath, &s.store); err != nil {
		return nil, fmt.Errorf("load bridge state: %w", err)
	}
	if (s.store.Identity.Certificate != "" || len(s.store.Devices) != 0 || len(s.store.Connections) != 0) && s.store.Schema != 2 {
		return nil, errors.New("unsupported bridge state schema; initialize a new state file")
	}
	s.store.Schema = 2
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
	return s, nil
}
func (s *Server) save() error                     { return state.Save(s.cfg.StatePath, s.store) }
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
	for _, p := range s.connections {
		p.Close()
	}
	for cid := range s.sshCalls {
		s.cancelConnectionLocked(cid)
	}
}
func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		methodNotAllowed(w)
		return
	}
	var req protocol.PairRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err)
		return
	}
	if req.Device.Workspace == "" || !validAlias(req.Device.Name) {
		writeError(w, 400, errors.New("device alias and workspace are required; alias cannot contain path separators"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.cleanupPairingsLocked(now)
	id := transport.Token()
	verification, err := verificationCode()
	if err != nil {
		writeError(w, 500, err)
		return
	}
	pending := &pendingPairing{request: protocol.PairingRequest{ID: id, Device: req.Device, Verification: verification, Fingerprint: s.Fingerprint(), ExpiresAt: now.Add(5 * time.Minute)}, status: "pending"}
	s.pairings[id] = pending
	writeJSON(w, http.StatusAccepted, pending.request)
}

func verificationCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	value := fmt.Sprintf("%06d", n.Int64())
	return value[:3] + " " + value[3:], nil
}

func (s *Server) cleanupPairingsLocked(now time.Time) {
	for id, pending := range s.pairings {
		if !pending.request.ExpiresAt.After(now) {
			delete(s.pairings, id)
		}
	}
}

func (s *Server) pairStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/pair/"), "/")
	s.mu.Lock()
	s.cleanupPairingsLocked(time.Now())
	pending, ok := s.pairings[id]
	if !ok {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, errors.New("pairing request not found or expired"))
		return
	}
	status := protocol.PairingStatus{PairingRequest: pending.request, Status: pending.status, Credentials: pending.credentials}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) pairRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupPairingsLocked(time.Now())
	out := make([]protocol.PairingRequest, 0)
	for _, pending := range s.pairings {
		if pending.status == "pending" {
			out = append(out, pending.request)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) pairRequestAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/pair-requests/"), "/"), "/")
	if len(parts) != 2 || (parts[1] != "approve" && parts[1] != "reject") {
		writeError(w, http.StatusNotFound, errors.New("unknown pairing request action"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupPairingsLocked(time.Now())
	pending, ok := s.pairings[parts[0]]
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("pairing request not found or expired"))
		return
	}
	if pending.status != "pending" {
		writeError(w, http.StatusConflict, errors.New("pairing request already decided"))
		return
	}
	if parts[1] == "reject" {
		pending.status = "rejected"
		writeJSON(w, http.StatusOK, protocol.PairingStatus{PairingRequest: pending.request, Status: pending.status})
		return
	}
	var attach struct {
		DeviceID string `json:"device_id"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := readJSON(r, &attach); err != nil && err != io.EOF {
			writeError(w, 400, err)
			return
		}
	}
	id, token, connectionID := transport.Token(), transport.Token(), transport.Token()
	d := pending.request.Device
	if attach.DeviceID != "" {
		existing, ok := s.store.Devices[attach.DeviceID]
		if !ok || !existing.Device.Authorized || existing.Device.Workspace != d.Workspace {
			writeError(w, 400, errors.New("target device must be authorized and use the same workspace"))
			return
		}
		id = attach.DeviceID
		d = existing.Device
	} else {
		d.ID = id
		d.Handle = s.newDeviceHandleLocked()
		if strings.EqualFold(strings.TrimSpace(d.Name), d.Handle) || !s.aliasAvailableLocked(d.Name, "") {
			writeError(w, http.StatusConflict, errors.New("device alias is already in use"))
			return
		}
		d.Authorized = true
		d.DesktopAccess = "granted"
		d.Online = false
		d.LastSeen = time.Time{}
		d.Mappings = map[string]string{}
		d.ActiveConnection = connectionID
	}
	credentials := &protocol.Credentials{DeviceID: id, ConnectionID: connectionID, Token: token, Workspace: d.Workspace, Fingerprint: s.Fingerprint()}
	previous, existed := s.store.Devices[id]
	s.store.Devices[id] = record{Device: d}
	s.store.Connections[connectionID] = connectionRecord{Connection: protocol.Connection{ID: connectionID, DeviceID: id, Kind: "worker", Capabilities: pending.request.Device.Capabilities}, TokenHash: tokenHash(token)}
	if err := s.save(); err != nil {
		if existed {
			s.store.Devices[id] = previous
		} else {
			delete(s.store.Devices, id)
		}
		delete(s.store.Connections, connectionID)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	pending.status = "approved"
	pending.credentials = credentials
	writeJSON(w, http.StatusOK, protocol.PairingStatus{PairingRequest: pending.request, Status: pending.status, Credentials: credentials})
}
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || r.Header.Get("Origin") != "" {
		writeError(w, 403, errors.New("invalid worker connection"))
		return
	}
	id := r.Header.Get("X-AgentRelay-Device")
	connectionID := r.Header.Get("X-AgentRelay-Connection")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	rec, ok := s.store.Devices[id]
	connection, connected := s.store.Connections[connectionID]
	if !ok || !connected || connection.Connection.DeviceID != id || connection.Connection.Kind != "worker" || !rec.Device.Authorized || subtle.ConstantTimeCompare([]byte(connection.TokenHash), []byte(tokenHash(token))) != 1 {
		s.mu.Unlock()
		writeError(w, 401, errors.New("device is not authorized; pair again"))
		return
	}
	conn, err := (&websocket.Upgrader{HandshakeTimeout: 10 * time.Second}).Upgrade(w, r, nil)
	if err != nil {
		s.mu.Unlock()
		return
	}
	var peer *transport.Peer
	ready := make(chan struct{})
	// Worker registration and Bridge execution are RPC calls on the same connection.
	peer = transport.NewPeer(conn, func(ctx context.Context, req transport.Request) transport.Response {
		<-ready
		if req.Method != "POST" || req.Path != "/register" {
			return transport.Response{Status: 404}
		}
		var d protocol.Device
		if json.Unmarshal(req.Body, &d) != nil {
			return transport.Response{Status: 400}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		current, exists := s.store.Devices[id]
		if !exists || s.connections[connectionID] != peer || !current.Device.Authorized || d.ID != id || d.Workspace != current.Device.Workspace {
			return transport.Response{Status: 403}
		}
		previous := current
		current.Device.OS = d.OS
		current.Device.Architecture = d.Architecture
		connection := s.store.Connections[connectionID]
		oldConnection := connection
		connection.Connection.Capabilities = d.Capabilities
		connection.Connection.Online = true
		s.store.Connections[connectionID] = connection
		current.Device.Metadata = d.Metadata
		current.Device.LastSeen = time.Now()
		if current.Device.ActiveConnection == connectionID {
			current.Device.Capabilities = d.Capabilities
			current.Device.Online = true
		}
		s.store.Devices[id] = current
		if err := s.save(); err != nil {
			s.store.Devices[id] = previous
			s.store.Connections[connectionID] = oldConnection
			return transport.Response{Status: 500}
		}
		return transport.Response{Status: 200}
	})
	old := s.connections[connectionID]
	s.connections[connectionID] = peer
	connection.Connection.Online = false
	s.store.Connections[connectionID] = connection
	s.refreshDeviceLocked(id)
	close(ready)
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	defer peer.Close()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-peer.Done():
	case <-timer.C:
		s.mu.RLock()
		online := s.store.Connections[connectionID].Connection.Online
		s.mu.RUnlock()
		if online {
			<-peer.Done()
		}
	}
	s.mu.Lock()
	if s.connections[connectionID] == peer {
		delete(s.connections, connectionID)
		connection = s.store.Connections[connectionID]
		connection.Connection.Online = false
		s.store.Connections[connectionID] = connection
		s.refreshDeviceLocked(id)
	}
	s.mu.Unlock()
}
func (s *Server) devicesRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		s.createDevice(w, r)
		return
	}
	if r.Method != "GET" {
		methodNotAllowed(w)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]protocol.Device, 0, len(s.store.Devices))
	for _, rec := range s.store.Devices {
		out = append(out, s.deviceViewLocked(rec.Device))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, 200, out)
}

type DeviceUpdate struct {
	ActiveConnection *string           `json:"active_connection,omitempty"`
	Name             *string           `json:"name,omitempty"`
	Authorized       *bool             `json:"authorized,omitempty"`
	DesktopAccess    *string           `json:"desktop_access,omitempty"`
	Mappings         map[string]string `json:"workspace_mappings,omitempty"`
}

func validRemote(value string) bool {
	value = strings.ReplaceAll(value, "\\", "/")
	if strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func validAlias(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\r\n\t") {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
func (s *Server) deviceRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/devices/"), "/"), "/")
	selector := parts[0]
	s.mu.RLock()
	devices := make([]protocol.Device, 0, len(s.store.Devices))
	for _, item := range s.store.Devices {
		devices = append(devices, item.Device)
	}
	id, resolveErr := ResolveDeviceSelector(devices, selector)
	rec, ok := s.store.Devices[id]
	connection := s.store.Connections[rec.Device.ActiveConnection].Connection
	peer := s.connections[connection.ID]
	s.mu.RUnlock()
	if resolveErr != nil || !ok {
		if resolveErr != nil {
			writeError(w, 404, resolveErr)
		} else {
			writeError(w, 404, errors.New("device not found"))
		}
		return
	}
	if len(parts) == 1 {
		s.updateDevice(w, r, id)
		return
	}
	if parts[1] == "connections" {
		s.connectionRoute(w, r, id, parts[2:])
		return
	}
	if !rec.Device.Authorized {
		writeError(w, 403, errors.New("device authorization revoked"))
		return
	}
	endpoint := "/v1/" + strings.Join(parts[1:], "/")
	if !allowed(endpoint, r.Method) {
		writeError(w, 404, errors.New("unknown device operation"))
		return
	}
	capability := backend.Capability(endpoint)
	if capability != "" && !backend.Supported(connection.Capabilities, capability) {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("device does not provide %s capability", capability))
		return
	}
	if !connection.Online || (connection.Kind == "worker" && peer == nil) {
		writeError(w, 503, errors.New("device offline; test its connection or reconnect"))
		return
	}
	if strings.HasPrefix(endpoint, "/v1/desktop") && rec.Device.DesktopAccess == "revoked" {
		writeError(w, http.StatusForbidden, errors.New("desktop control permission is revoked"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, (32<<20)+1))
	if err != nil || len(data) > 32<<20 {
		writeError(w, 413, errors.New("RPC payload exceeds 32 MiB"))
		return
	}
	isSyncStart := endpoint == "/v1/files/sync/start" && r.Method == http.MethodPost
	var pendingMapping syncMapping
	if isSyncStart {
		if err := s.beginSyncStart(id, connection.ID); err != nil {
			writeError(w, 409, err)
			return
		}
		defer s.endSyncStart(id)
		data, pendingMapping, err = s.prepareSyncStart(id, data)
		if err != nil {
			writeError(w, 400, err)
			return
		}
	}
	syncID, syncOperation, isSyncSession := parseSyncOperation(endpoint)
	isSyncCommit := isSyncSession && syncOperation == "commit" && r.Method == http.MethodPost
	isSyncAbort := isSyncSession && syncOperation == "" && r.Method == http.MethodDelete
	var commitMapping syncMapping
	if isSyncCommit {
		var exists bool
		commitMapping, exists = s.lookupSyncMapping(id, syncID)
		if !exists {
			writeError(w, http.StatusConflict, errors.New("sync session mapping not found or expired"))
			return
		}
		if err := s.persistSyncMapping(id, commitMapping); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if endpoint == "/v1/exec" || (r.Method == "POST" && (endpoint == "/v1/tasks" || endpoint == "/v1/sessions")) {
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) == nil {
			var dir string
			_ = json.Unmarshal(fields["working_dir"], &dir)
			if mapped, found := mappedDir(rec.Device.Mappings, dir); found {
				fields["working_dir"], _ = json.Marshal(mapped)
				data, _ = json.Marshal(fields)
			}
		}
	}
	if r.URL.RawQuery != "" {
		endpoint += "?" + r.URL.RawQuery
	}
	ctx, cancel := context.WithTimeout(r.Context(), 31*time.Minute)
	defer cancel()
	var call backend.Call
	if connection.Kind == "ssh" && connection.SSH != nil {
		call = func(ctx context.Context, req transport.Request) (transport.Response, error) {
			return s.callSSH(ctx, connection, req)
		}
	} else if peer != nil {
		call = peer.Call
	}
	if call == nil {
		writeError(w, 503, errors.New("connection is unavailable"))
		return
	}
	var resp transport.Response
	if capability == "" {
		if strings.HasPrefix(endpoint, "/v1/device") {
			resp.Body, _ = json.Marshal(s.agentDevice(rec.Device))
			resp.Status = 200
		} else {
			resp.Body, _ = json.Marshal(protocol.Health{Status: "ok", APIVersion: protocol.APIVersion})
			resp.Status = 200
		}
	} else {
		resp, err = backend.FromRPC(call, connection.Capabilities).Dispatch(ctx, transport.Request{Method: r.Method, Path: endpoint, Body: data})
	}
	if err != nil {
		var failure *backend.Error
		if errors.As(err, &failure) {
			writeError(w, failure.Status, err)
			return
		}
		var syntax *json.SyntaxError
		var fields *json.UnmarshalTypeError
		if errors.As(err, &syntax) || errors.As(err, &fields) {
			writeError(w, 400, err)
			return
		}
		if connection.Kind == "ssh" && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.markConnectionFailed(connection, err)
		}
		writeError(w, 502, err)
		return
	}
	if isSyncStart && resp.Status < http.StatusMultipleChoices {
		var started protocol.SyncStartResponse
		if err := json.Unmarshal(resp.Body, &started); err != nil || started.ID == "" || started.Root != pendingMapping.root {
			writeError(w, http.StatusBadGateway, errors.New("worker returned an invalid sync session"))
			return
		}
		pendingMapping.expiresAt = time.Now().Add(syncMappingRetention)
		s.rememberSyncMapping(id, started.ID, pendingMapping)
	}
	if isSyncCommit && resp.Status < http.StatusMultipleChoices {
		s.forgetSyncMapping(id, syncID)
	}
	if isSyncAbort && resp.Status < http.StatusMultipleChoices {
		s.forgetSyncMapping(id, syncID)
	}
	if isSyncSession && !isSyncCommit && !isSyncAbort && resp.Status < http.StatusMultipleChoices {
		s.refreshSyncMapping(id, syncID)
	}
	if strings.Split(endpoint, "?")[0] == "/v1/desktop/capture" && resp.Status == http.StatusOK {
		resp.Body, err = s.captureArtifact(data, resp.Body)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}
func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != "PATCH" && r.Method != "DELETE" {
		methodNotAllowed(w)
		return
	}
	var update DeviceUpdate
	if r.Method == "PATCH" {
		if err := readJSON(r, &update); err != nil {
			writeError(w, 400, err)
			return
		}
		if update.Name != nil && !validAlias(*update.Name) {
			writeError(w, 400, errors.New("name is required and cannot contain path separators"))
			return
		}
		for local, remote := range update.Mappings {
			if !filepath.IsAbs(local) || !validRemote(remote) {
				writeError(w, 400, errors.New("mapping needs absolute local path and workspace-relative remote path"))
				return
			}
		}
	}
	s.mu.Lock()
	old, exists := s.store.Devices[id]
	if !exists {
		s.mu.Unlock()
		writeError(w, 404, errors.New("device not found"))
		return
	}
	rec := old
	oldConnections := make(map[string]connectionRecord)
	for cid, c := range s.store.Connections {
		if c.Connection.DeviceID == id {
			oldConnections[cid] = c
		}
	}
	if r.Method == "DELETE" {
		delete(s.store.Devices, id)
		for cid := range oldConnections {
			delete(s.store.Connections, cid)
		}
	} else {
		if update.ActiveConnection != nil {
			c, ok := s.store.Connections[*update.ActiveConnection]
			if !ok || c.Connection.DeviceID != id {
				s.mu.Unlock()
				writeError(w, 400, errors.New("connection does not belong to this device"))
				return
			}
			if *update.ActiveConnection != rec.Device.ActiveConnection && s.hasSyncMappingsLocked(id) {
				s.mu.Unlock()
				writeError(w, 409, errors.New("abort or finish active syncs before switching connections"))
				return
			}
			rec.Device.ActiveConnection = *update.ActiveConnection
			rec.Device.Capabilities = c.Connection.Capabilities
			rec.Device.Online = c.Connection.Online && rec.Device.Authorized
		}
		if update.Name != nil {
			if !s.aliasAvailableLocked(*update.Name, id) {
				s.mu.Unlock()
				writeError(w, http.StatusConflict, errors.New("device alias is already in use"))
				return
			}
			rec.Device.Name = *update.Name
		}
		if update.Authorized != nil {
			rec.Device.Authorized = *update.Authorized
		}
		if update.DesktopAccess != nil {
			access := strings.ToLower(strings.TrimSpace(*update.DesktopAccess))
			if access != "granted" && access != "revoked" {
				s.mu.Unlock()
				writeError(w, 400, errors.New("desktop_access must be granted or revoked"))
				return
			}
			rec.Device.DesktopAccess = access
		}
		if update.Mappings != nil {
			rec.Device.Mappings = update.Mappings
		}
		if !rec.Device.Authorized {
			rec.Device.Online = false
		}
		s.store.Devices[id] = rec
	}
	if err := s.save(); err != nil {
		s.store.Devices[id] = old
		for cid, c := range oldConnections {
			s.store.Connections[cid] = c
		}
		s.mu.Unlock()
		writeError(w, 500, err)
		return
	}
	if r.Method == "DELETE" || (update.Authorized != nil && !*update.Authorized) {
		s.clearDeviceSyncMappingsLocked(id)
	}
	if r.Method == "DELETE" || !rec.Device.Authorized {
		for cid, c := range oldConnections {
			s.cancelConnectionLocked(cid)
			if peer := s.connections[cid]; peer != nil {
				peer.Close()
			}
			delete(s.connections, cid)
			if r.Method != "DELETE" {
				c.Connection.Online = false
				s.store.Connections[cid] = c
			}
		}
	}
	s.mu.Unlock()
	if r.Method == "DELETE" {
		w.WriteHeader(204)
	} else {
		writeJSON(w, 200, rec.Device)
	}
}

func (s *Server) aliasAvailableLocked(name, exceptID string) bool {
	name = strings.TrimSpace(name)
	for id, rec := range s.store.Devices {
		if strings.EqualFold(strings.TrimSpace(rec.Device.Handle), name) {
			return false
		}
		if id != exceptID && strings.EqualFold(strings.TrimSpace(rec.Device.Name), name) {
			return false
		}
	}
	return name != ""
}

func (s *Server) newDeviceHandleLocked() string {
	for {
		token := transport.Token()
		handle := "dev-" + token[:8]
		used := false
		for _, rec := range s.store.Devices {
			if strings.EqualFold(rec.Device.Handle, handle) {
				used = true
				break
			}
		}
		if !used {
			return handle
		}
	}
}
func allowed(endpoint, method string) bool {
	switch endpoint {
	case "/v1/health", "/v1/device", "/v1/files/read", "/v1/desktop":
		return method == "GET"
	case "/v1/exec", "/v1/files/sync/start", "/v1/desktop/capture", "/v1/desktop/input":
		return method == "POST"
	case "/v1/tasks", "/v1/sessions":
		return method == "POST" || method == "GET"
	}
	parts := strings.Split(strings.TrimPrefix(endpoint, "/v1/"), "/")
	if len(parts) >= 3 && parts[0] == "files" && parts[1] == "sync" {
		if len(parts) == 3 {
			return method == "DELETE"
		}
		return len(parts) == 4 && method == "POST" && (parts[3] == "manifest" || parts[3] == "plan" || parts[3] == "file" || parts[3] == "commit")
	}
	if len(parts) < 2 || parts[1] == "" || (parts[0] != "tasks" && parts[0] != "sessions") {
		return false
	}
	if len(parts) == 2 {
		return method == "GET" || method == "DELETE"
	}
	return len(parts) == 3 && method == "POST" && ((parts[0] == "tasks" && parts[2] == "retrieve") || (parts[0] == "sessions" && parts[2] == "input"))
}
func mappedDir(mappings map[string]string, dir string) (string, bool) {
	if !filepath.IsAbs(dir) {
		return "", false
	}
	best, result := "", ""
	for local, remote := range mappings {
		rel, err := filepath.Rel(local, dir)
		if err == nil && validRemote(rel) && len(local) > len(best) {
			best = local
			result = path.Join(strings.ReplaceAll(remote, "\\", "/"), filepath.ToSlash(rel))
		}
	}
	return result, best != ""
}
func (s *Server) prepareSyncStart(id string, data []byte) ([]byte, syncMapping, error) {
	var req protocol.SyncStartRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, syncMapping{}, err
	}
	if !filepath.IsAbs(req.SourcePath) {
		return nil, syncMapping{}, errors.New("source_path must be an absolute controller path")
	}
	local, err := filepath.Abs(req.SourcePath)
	if err != nil {
		return nil, syncMapping{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.store.Devices[id]
	if !ok {
		return nil, syncMapping{}, errors.New("device not found")
	}
	if req.Root == "" {
		if mapped, ok := rec.Device.Mappings[local]; ok {
			req.Root = mapped
		} else {
			sum := sha256.Sum256([]byte(local))
			req.Root = "projects/" + hex.EncodeToString(sum[:8])
		}
	}
	if !validRemote(req.Root) {
		return nil, syncMapping{}, errors.New("remote root must be workspace-relative")
	}
	mapping := syncMapping{sourcePath: local, root: req.Root}
	req.SourcePath = ""
	forwarded, err := json.Marshal(req)
	return forwarded, mapping, err
}

func parseSyncOperation(endpoint string) (string, string, bool) {
	const prefix = "/v1/files/sync/"
	if !strings.HasPrefix(endpoint, prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(endpoint, prefix), "/"), "/")
	if len(parts) == 1 && parts[0] == "start" {
		return "", "", false
	}
	if len(parts) == 1 && parts[0] != "" {
		return parts[0], "", true
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1], true
	}
	return "", "", false
}

func (s *Server) rememberSyncMapping(deviceID, syncID string, mapping syncMapping) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupSyncMappingsLocked(time.Now())
	if s.syncMappings == nil {
		s.syncMappings = map[syncMappingKey]syncMapping{}
	}
	s.syncMappings[syncMappingKey{deviceID: deviceID, syncID: syncID}] = mapping
}

func (s *Server) forgetSyncMapping(deviceID, syncID string) {
	s.mu.Lock()
	s.cleanupSyncMappingsLocked(time.Now())
	delete(s.syncMappings, syncMappingKey{deviceID: deviceID, syncID: syncID})
	s.mu.Unlock()
}

func (s *Server) refreshSyncMapping(deviceID, syncID string) {
	s.mu.Lock()
	s.cleanupSyncMappingsLocked(time.Now())
	key := syncMappingKey{deviceID: deviceID, syncID: syncID}
	mapping, exists := s.syncMappings[key]
	if exists {
		mapping.expiresAt = time.Now().Add(syncMappingRetention)
		s.syncMappings[key] = mapping
	}
	s.mu.Unlock()
}

func (s *Server) lookupSyncMapping(deviceID, syncID string) (syncMapping, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupSyncMappingsLocked(time.Now())
	mapping, exists := s.syncMappings[syncMappingKey{deviceID: deviceID, syncID: syncID}]
	return mapping, exists
}

func (s *Server) cleanupSyncMappingsLocked(now time.Time) {
	for key, mapping := range s.syncMappings {
		if !mapping.expiresAt.IsZero() && now.After(mapping.expiresAt) {
			delete(s.syncMappings, key)
		}
	}
}

func (s *Server) clearDeviceSyncMappingsLocked(deviceID string) {
	for key := range s.syncMappings {
		if key.deviceID == deviceID {
			delete(s.syncMappings, key)
		}
	}
}

func (s *Server) persistSyncMapping(id string, mapping syncMapping) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.store.Devices[id]
	if !ok {
		return errors.New("device not found")
	}
	previous := rec
	mappings := make(map[string]string, len(rec.Device.Mappings)+1)
	for key, value := range rec.Device.Mappings {
		mappings[key] = value
	}
	mappings[mapping.sourcePath] = mapping.root
	rec.Device.Mappings = mappings
	s.store.Devices[id] = rec
	if err := s.save(); err != nil {
		s.store.Devices[id] = previous
		return err
	}
	return nil
}
func readJSON(r *http.Request, dst any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(dst)
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

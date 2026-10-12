package controller

import (
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/transport"
	"AgentRelay/internal/workspace"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"
)

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
	proposed := pending.request.Device
	policy := protocol.WorkspacePolicy{Root: proposed.Workspace, Mode: proposed.WorkspaceMode}
	if policy.Mode == "" {
		policy.Mode = protocol.WorkspaceLocalSync
	}
	if err := workspace.Validate(policy); err != nil {
		writeError(w, 400, err)
		return
	}
	d := proposed
	if attach.DeviceID != "" {
		existing, ok := s.store.Devices[attach.DeviceID]
		if !ok || !existing.Device.Authorized {
			writeError(w, 400, errors.New("target device must be authorized"))
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
	credentials := &protocol.Credentials{Schema: 3, DeviceID: id, ConnectionID: connectionID, Token: token, Workspace: policy.Root, Fingerprint: s.Fingerprint()}
	previous, existed := s.store.Devices[id]
	s.store.Devices[id] = record{Device: d}
	s.store.Connections[connectionID] = connectionRecord{Connection: protocol.Connection{ID: connectionID, DeviceID: id, Kind: "worker", Revision: transport.Token(), Workspace: policy, OS: proposed.OS, Architecture: proposed.Architecture, Capabilities: proposed.Capabilities}, TokenHash: tokenHash(token)}
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
	s.refreshDeviceLocked(id)
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
	var peer *transport.Peer
	ready := make(chan struct{})
	// Worker registration and Controller execution are RPC calls on the same connection.
	peer, err := transport.AcceptWorker(w, r, func(ctx context.Context, req transport.Request) transport.Response {
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
		if !exists || s.workerPeers[connectionID] != peer || !current.Device.Authorized || d.ID != id || d.Workspace != s.store.Connections[connectionID].Connection.Workspace.Root {
			return transport.Response{Status: 403}
		}
		previous := current
		connection := s.store.Connections[connectionID]
		oldConnection := connection
		connection.Connection.OS = d.OS
		connection.Connection.Architecture = d.Architecture
		connection.Connection.Metadata = d.Metadata
		connection.Connection.LastSeen = time.Now()
		connection.Connection.Capabilities = d.Capabilities
		connection.Connection.Online = true
		connection.Connection.LastError = ""
		s.store.Connections[connectionID] = connection
		s.refreshDeviceLocked(id)
		if err := s.save(); err != nil {
			s.store.Devices[id] = previous
			s.store.Connections[connectionID] = oldConnection
			return transport.Response{Status: 500}
		}
		return transport.Response{Status: 200}
	})
	if err != nil {
		s.mu.Unlock()
		return
	}
	old := s.workerPeers[connectionID]
	s.workerPeers[connectionID] = peer
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
	if s.workerPeers[connectionID] == peer {
		delete(s.workerPeers, connectionID)
		connection = s.store.Connections[connectionID]
		connection.Connection.Online = false
		s.store.Connections[connectionID] = connection
		s.refreshDeviceLocked(id)
	}
	s.mu.Unlock()
}

package controller

import (
	"context"
	"errors"
	"net/http"
	"time"

	"AgentRelay/internal/backend"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/transport"
)

// Reserve the device before probing persistent remote activity. New operations
// cannot start between the activity check and the registry commit.
func (s *Server) beginConnectionChange(ctx context.Context, id string) (connectionRecord, error) {
	s.mu.Lock()
	d, exists := s.store.Devices[id]
	c, ok := s.store.Connections[d.Device.ConnectionID]
	if !exists || !ok || !d.Device.Authorized {
		s.mu.Unlock()
		return connectionRecord{}, errors.New("device must exist and be authorized")
	}
	if s.changing[id] || s.operations[id] != 0 || len(s.calls[c.Connection.ID]) != 0 || s.hasSyncMappingsLocked(id) {
		s.mu.Unlock()
		return connectionRecord{}, errors.New("finish active operations and syncs before changing connection")
	}
	if s.changing == nil {
		s.changing = map[string]bool{}
	}
	s.changing[id] = true
	adapter := s.adapters[c.Connection.Kind]
	var live backend.Call
	if peer := s.workerPeers[c.Connection.ID]; peer != nil {
		live = peer.Call
	}
	s.mu.Unlock()
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var err error
	if adapter == nil {
		err = errors.New("connection adapter unavailable")
	} else {
		err = adapter.CheckIdle(probeCtx, c.Connection, live)
	}
	if err != nil {
		s.endConnectionChange(id)
		return connectionRecord{}, err
	}
	return c, nil
}

func (s *Server) endConnectionChange(id string) {
	s.mu.Lock()
	delete(s.changing, id)
	s.mu.Unlock()
}

func (s *Server) connectionUnchangedLocked(id string, c protocol.Connection) bool {
	d, exists := s.store.Devices[id]
	current, ok := s.store.Connections[c.ID]
	return exists && ok && d.Device.Authorized && d.Device.ConnectionID == c.ID && current.Connection.Revision == c.Revision
}

func (s *Server) replaceConnection(w http.ResponseWriter, r *http.Request, id string) {
	var in protocol.ConnectionReplacement
	if err := readJSON(r, &in); err != nil {
		writeError(w, 400, err)
		return
	}
	if (in.Connection == nil) == (in.PairingID == "") {
		writeError(w, 400, errors.New("provide exactly one connection specification or Worker pairing_id"))
		return
	}
	if in.Connection != nil {
		if in.Connection.Kind == "worker" {
			writeError(w, 400, errors.New("Worker replacement requires pairing_id"))
			return
		}
		if err := s.validateSpec(*in.Connection); err != nil {
			writeError(w, 400, err)
			return
		}
	}
	old, err := s.beginConnectionChange(r.Context(), id)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	defer s.endConnectionChange(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connectionUnchangedLocked(id, old.Connection) {
		writeError(w, 409, errors.New("device connection changed during replacement"))
		return
	}
	old = s.store.Connections[old.Connection.ID]
	var next connectionRecord
	var pending *pendingPairing
	var credentials *protocol.Credentials
	if in.PairingID != "" {
		s.cleanupPairingsLocked(time.Now())
		pending = s.pairings[in.PairingID]
		if pending == nil || pending.status != "pending" {
			writeError(w, 409, errors.New("Worker pairing request is missing, expired or already decided"))
			return
		}
		next, credentials, err = s.pairedConnection(id, pending.request.Device)
		if err != nil {
			writeError(w, 400, err)
			return
		}
	} else {
		next.Connection = s.newConnection(id, *in.Connection)
	}
	previous := s.store.Devices[id]
	updated := previous
	updated.Device.ConnectionID = next.Connection.ID
	s.store.Devices[id] = updated
	delete(s.store.Connections, old.Connection.ID)
	s.store.Connections[next.Connection.ID] = next
	s.refreshDeviceLocked(id)
	if err := s.save(); err != nil {
		s.store.Devices[id] = previous
		delete(s.store.Connections, next.Connection.ID)
		s.store.Connections[old.Connection.ID] = old
		writeError(w, 500, err)
		return
	}
	// Invalidate the old token and close its peer only after durable replacement.
	if peer := s.workerPeers[old.Connection.ID]; peer != nil {
		peer.Close()
	}
	delete(s.workerPeers, old.Connection.ID)
	s.clearDeviceSyncMappingsLocked(id)
	if pending != nil {
		pending.status = "approved"
		pending.credentials = credentials
	}
	writeJSON(w, 200, s.deviceViewLocked(s.store.Devices[id].Device))
}

func (s *Server) pairedConnection(id string, proposed protocol.Device) (connectionRecord, *protocol.Credentials, error) {
	policy := protocol.WorkspacePolicy{Root: proposed.Workspace, Mode: proposed.WorkspaceMode}
	if policy.Mode == "" {
		policy.Mode = protocol.WorkspaceLocalSync
	}
	spec := protocol.ConnectionSpec{Kind: "worker", Workspace: policy}
	if err := s.validateSpec(spec); err != nil {
		return connectionRecord{}, nil, err
	}
	c := s.newConnection(id, spec)
	c.OS, c.Architecture, c.Capabilities = proposed.OS, proposed.Architecture, proposed.Capabilities
	token := transport.Token()
	credentials := &protocol.Credentials{Schema: 3, DeviceID: id, ConnectionID: c.ID, Token: token, Workspace: policy.Root, Fingerprint: s.Fingerprint()}
	return connectionRecord{Connection: c, TokenHash: tokenHash(token)}, credentials, nil
}

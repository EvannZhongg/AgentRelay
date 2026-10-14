package controller

import (
	"DevFreight/internal/backend"
	"DevFreight/internal/protocol"
	"DevFreight/internal/transport"
	"DevFreight/internal/workspace"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

func (s *Server) cancelConnectionLocked(cid string) {
	for _, cancel := range s.calls[cid] {
		cancel()
	}
}
func (s *Server) callConnection(ctx context.Context, c protocol.Connection, req transport.Request) (transport.Response, error) {
	s.mu.Lock()
	current, ok := s.store.Connections[c.ID]
	d, exists := s.store.Devices[c.DeviceID]
	if !ok || !exists || !d.Device.Authorized || d.Device.ConnectionID != c.ID || current.Connection.Revision != c.Revision {
		s.mu.Unlock()
		return transport.Response{}, &backend.Error{Status: 403, Message: "connection authorization or configuration changed"}
	}
	if s.changing[c.DeviceID] {
		s.mu.Unlock()
		return transport.Response{}, &backend.Error{Status: 409, Message: "device connection is being changed"}
	}
	adapter := s.adapters[c.Kind]
	if adapter == nil {
		s.mu.Unlock()
		return transport.Response{}, &backend.Error{Status: 501, Message: "connection adapter unavailable"}
	}
	var live backend.Call
	if peer := s.workerPeers[c.ID]; peer != nil {
		live = peer.Call
	}
	call, bindErr := adapter.Bind(c, live)
	if bindErr != nil {
		s.mu.Unlock()
		return transport.Response{}, bindErr
	}
	callCtx, cancel := context.WithCancel(ctx)
	callID := transport.Token()
	if s.calls == nil {
		s.calls = map[string]map[string]context.CancelFunc{}
	}
	if s.calls[c.ID] == nil {
		s.calls[c.ID] = map[string]context.CancelFunc{}
	}
	s.calls[c.ID][callID] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.calls[c.ID], callID)
		if len(s.calls[c.ID]) == 0 {
			delete(s.calls, c.ID)
		}
		s.mu.Unlock()
	}()
	return call(callCtx, req)
}

func (s *Server) markConnectionFailed(c protocol.Connection, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.store.Connections[c.ID]
	if !ok || current.Connection.Revision != c.Revision {
		return
	}
	current.Connection.Online = false
	current.Connection.LastError = err.Error()
	s.store.Connections[c.ID] = current
	s.refreshDeviceLocked(c.DeviceID)
}

func (s *Server) refreshDeviceLocked(id string) {
	r, ok := s.store.Devices[id]
	if !ok {
		return
	}
	c := s.store.Connections[r.Device.ConnectionID].Connection
	r.Device.Workspace = c.Workspace.Root
	r.Device.WorkspaceMode = c.Workspace.Mode
	r.Device.Mappings = c.Workspace.Mappings
	r.Device.OS = c.OS
	r.Device.Architecture = c.Architecture
	r.Device.Metadata = c.Metadata
	r.Device.LastSeen = c.LastSeen
	r.Device.Capabilities = c.Capabilities
	if c.Workspace.Mode == protocol.WorkspaceRemote {
		r.Device.Capabilities.Sync = false
	}
	r.Device.Online = c.Online && r.Device.Authorized
	s.store.Devices[id] = r
}
func (s *Server) deviceViewLocked(d protocol.Device) protocol.Device {
	c := s.store.Connections[d.ConnectionID].Connection
	d.Connection = &c
	return d
}
func (s *Server) hasSyncMappingsLocked(id string) bool {
	if s.syncStarts[id] > 0 {
		return true
	}
	s.cleanupSyncMappingsLocked(time.Now())
	for key := range s.syncMappings {
		if key.deviceID == id {
			return true
		}
	}
	return false
}

func (s *Server) beginSyncStart(id, cid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.store.Devices[id]
	if !ok || !d.Device.Authorized || d.Device.ConnectionID != cid || s.changing[id] {
		return errors.New("device connection changed before sync started")
	}
	if s.syncStarts == nil {
		s.syncStarts = map[string]int{}
	}
	s.syncStarts[id]++
	return nil
}
func (s *Server) endSyncStart(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncStarts[id]--
	if s.syncStarts[id] <= 0 {
		delete(s.syncStarts, id)
	}
}

func (s *Server) validateSpec(in protocol.ConnectionSpec) error {
	if err := workspace.Validate(in.Workspace); err != nil {
		return err
	}
	adapter := s.adapters[in.Kind]
	if adapter == nil {
		return errors.New("unknown connection adapter")
	}
	return adapter.Validate(in)
}
func (s *Server) newConnection(id string, in protocol.ConnectionSpec) protocol.Connection {
	return protocol.Connection{ID: transport.Token(), Revision: transport.Token(), DeviceID: id, Kind: in.Kind, SSH: in.SSH, Options: in.Options, Workspace: in.Workspace, Capabilities: s.adapters[in.Kind].Capabilities()}
}
func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
	var in protocol.DeviceCreate
	if err := readJSON(r, &in); err != nil {
		writeError(w, 400, err)
		return
	}
	if !validAlias(in.Name) {
		writeError(w, 400, errors.New("device alias is required"))
		return
	}
	if in.Connection.Kind == "worker" {
		writeError(w, 400, errors.New("Worker connections require approved pairing"))
		return
	}
	if err := s.validateSpec(in.Connection); err != nil {
		writeError(w, 400, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.aliasAvailableLocked(in.Name, "") {
		writeError(w, 409, errors.New("device alias is already in use"))
		return
	}
	id := transport.Token()
	c := s.newConnection(id, in.Connection)
	d := protocol.Device{ID: id, Handle: s.newDeviceHandleLocked(), Name: in.Name, Authorized: true, DesktopAccess: "granted", ConnectionID: c.ID}
	s.store.Devices[id] = record{Device: d}
	s.store.Connections[c.ID] = connectionRecord{Connection: c}
	s.refreshDeviceLocked(id)
	if err := s.save(); err != nil {
		delete(s.store.Devices, id)
		delete(s.store.Connections, c.ID)
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, s.deviceViewLocked(s.store.Devices[id].Device))
}

// Connection management belongs to the loopback controller UI/API, not MCP.
func (s *Server) connectionRoute(w http.ResponseWriter, r *http.Request, id string, parts []string) {
	if len(parts) == 1 && parts[0] == "replace" && r.Method == "POST" {
		s.replaceConnection(w, r, id)
		return
	}
	s.mu.RLock()
	d := s.store.Devices[id].Device
	c := s.store.Connections[d.ConnectionID]
	s.mu.RUnlock()
	if len(parts) == 1 && parts[0] == "test" && r.Method == "POST" {
		s.testConnection(w, r, d, c)
		return
	}
	if len(parts) != 0 {
		writeError(w, 404, errors.New("unknown connection operation"))
		return
	}
	if r.Method != "PATCH" {
		methodNotAllowed(w)
		return
	}
	var in protocol.ConnectionSpec
	if err := readJSON(r, &in); err != nil {
		writeError(w, 400, err)
		return
	}
	if in.Kind != c.Connection.Kind {
		writeError(w, 400, errors.New("use replace to change connection method"))
		return
	}
	if in.Kind == "worker" && in.Workspace.Root != c.Connection.Workspace.Root {
		writeError(w, 400, errors.New("Worker workspace root changes require replacement with approved pairing"))
		return
	}
	if err := s.validateSpec(in); err != nil {
		writeError(w, 400, err)
		return
	}
	old, err := s.beginConnectionChange(r.Context(), id)
	if err != nil {
		writeError(w, 409, err)
		return
	}
	defer s.endConnectionChange(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if old.Connection.Revision != c.Connection.Revision || !s.connectionUnchangedLocked(id, old.Connection) {
		writeError(w, 409, errors.New("connection changed while applying configuration"))
		return
	}
	previousDevice := s.store.Devices[id]
	// Registration/disconnection may update status during the activity probe.
	old = s.store.Connections[old.Connection.ID]
	updated := old
	updated.Connection.SSH = in.SSH
	updated.Connection.Options = in.Options
	updated.Connection.Workspace = in.Workspace
	updated.Connection.Revision = transport.Token()
	if in.Kind != "worker" {
		updated.Connection.Online = false
		updated.Connection.OS = ""
		updated.Connection.Architecture = ""
		updated.Connection.Metadata = nil
		updated.Connection.LastSeen = time.Time{}
		updated.Connection.Capabilities = s.adapters[in.Kind].Capabilities()
	}
	updated.Connection.LastError = ""
	s.store.Connections[old.Connection.ID] = updated
	s.refreshDeviceLocked(id)
	if err := s.save(); err != nil {
		s.store.Connections[old.Connection.ID] = old
		s.store.Devices[id] = previousDevice
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, updated.Connection)
}

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request, d protocol.Device, c connectionRecord) {
	if !d.Authorized {
		writeError(w, 403, errors.New("device authorization revoked"))
		return
	}
	adapter := s.adapters[c.Connection.Kind]
	if adapter == nil {
		writeError(w, 501, errors.New("connection adapter unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	resp, err := s.callConnection(ctx, c.Connection, adapter.Probe())
	var probed protocol.Device
	if err == nil {
		if resp.Status != 200 {
			var failure protocol.ErrorResponse
			_ = json.Unmarshal(resp.Body, &failure)
			err = errors.New(failure.Error)
		} else {
			err = json.Unmarshal(resp.Body, &probed)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.store.Connections[c.Connection.ID]
	device, exists := s.store.Devices[d.ID]
	if !ok || !exists || !device.Device.Authorized || current.Connection.Revision != c.Connection.Revision {
		writeError(w, 409, errors.New("connection changed during test"))
		return
	}
	updated := current
	previousDevice := device
	updated.Connection.Online = err == nil
	updated.Connection.LastError = ""
	if err != nil {
		updated.Connection.LastError = err.Error()
	} else {
		updated.Connection.OS = probed.OS
		updated.Connection.Architecture = probed.Architecture
		updated.Connection.Metadata = probed.Metadata
		updated.Connection.LastSeen = time.Now()
		updated.Connection.Capabilities = probed.Capabilities

	}
	s.store.Connections[c.Connection.ID] = updated
	s.refreshDeviceLocked(d.ID)
	if saveErr := s.save(); saveErr != nil {
		s.store.Connections[c.Connection.ID] = current
		s.store.Devices[d.ID] = previousDevice
		writeError(w, 500, saveErr)
		return
	}
	if err != nil {
		writeError(w, 502, err)
	} else {
		writeJSON(w, 200, updated.Connection)
	}
}

package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"AgentRelay/internal/backend"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/transport"
)

func sshCapabilities() protocol.Capabilities {
	return protocol.Capabilities{Exec: true, Files: true, Shells: []string{"sh"}, MaxPayload: 32 << 20}
}

func (s *Server) cancelConnectionLocked(cid string) {
	for _, cancel := range s.sshCalls[cid] {
		cancel()
	}
}
func (s *Server) callSSH(ctx context.Context, c protocol.Connection, req transport.Request) (transport.Response, error) {
	s.mu.Lock()
	current, ok := s.store.Connections[c.ID]
	d, exists := s.store.Devices[c.DeviceID]
	if !ok || !exists || !d.Device.Authorized || current.Connection.SSH != c.SSH || c.SSH == nil {
		s.mu.Unlock()
		return transport.Response{}, &backend.Error{Status: 403, Message: "connection authorization or configuration changed"}
	}
	callCtx, cancel := context.WithCancel(ctx)
	callID := transport.Token()
	if s.sshCalls == nil {
		s.sshCalls = map[string]map[string]context.CancelFunc{}
	}
	if s.sshCalls[c.ID] == nil {
		s.sshCalls[c.ID] = map[string]context.CancelFunc{}
	}
	s.sshCalls[c.ID][callID] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.sshCalls[c.ID], callID)
		if len(s.sshCalls[c.ID]) == 0 {
			delete(s.sshCalls, c.ID)
		}
		s.mu.Unlock()
	}()
	return backend.SSHCaller(*c.SSH, d.Device.Workspace)(callCtx, req)
}

func (s *Server) markConnectionFailed(c protocol.Connection, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.store.Connections[c.ID]
	if !ok || current.Connection.SSH != c.SSH {
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
	c := s.store.Connections[r.Device.ActiveConnection].Connection
	r.Device.Capabilities = c.Capabilities
	r.Device.Online = c.Online && r.Device.Authorized
	s.store.Devices[id] = r
}
func (s *Server) deviceViewLocked(d protocol.Device) protocol.Device {
	d.Connections = []protocol.Connection{}
	for _, r := range s.store.Connections {
		if r.Connection.DeviceID == d.ID {
			d.Connections = append(d.Connections, r.Connection)
		}
	}
	sort.Slice(d.Connections, func(i, j int) bool { return d.Connections[i].ID < d.Connections[j].ID })
	return d
}
func (s *Server) agentDevice(d protocol.Device) protocol.Device {
	d.ActiveConnection = ""
	d.Connections = nil
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
	if !ok || !d.Device.Authorized || d.Device.ActiveConnection != cid {
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

func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
	var in protocol.DeviceCreate
	if err := readJSON(r, &in); err != nil {
		writeError(w, 400, err)
		return
	}
	if !validAlias(in.Name) || in.SSH == nil {
		writeError(w, 400, errors.New("name, workspace and SSH configuration are required"))
		return
	}
	if err := transport.ValidateSSH(*in.SSH, in.Workspace); err != nil {
		writeError(w, 400, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.aliasAvailableLocked(in.Name, "") {
		writeError(w, 409, errors.New("device alias is already in use"))
		return
	}
	id, cid := transport.Token(), transport.Token()
	d := protocol.Device{ID: id, Handle: s.newDeviceHandleLocked(), Name: in.Name, Workspace: in.Workspace, Authorized: true, DesktopAccess: "granted", ActiveConnection: cid, Capabilities: sshCapabilities()}
	s.store.Devices[id] = record{Device: d}
	s.store.Connections[cid] = connectionRecord{Connection: protocol.Connection{ID: cid, DeviceID: id, Kind: "ssh", SSH: in.SSH, Capabilities: sshCapabilities()}}
	if err := s.save(); err != nil {
		delete(s.store.Devices, id)
		delete(s.store.Connections, cid)
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, s.deviceViewLocked(d))
}

// Connection management belongs to the loopback controller UI/API, not MCP.
func (s *Server) connectionRoute(w http.ResponseWriter, r *http.Request, id string, parts []string) {
	if len(parts) == 0 {
		if r.Method != "POST" {
			methodNotAllowed(w)
			return
		}
		var in protocol.SSHConfig
		if err := readJSON(r, &in); err != nil {
			writeError(w, 400, err)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		d, ok := s.store.Devices[id]
		if !ok {
			writeError(w, 404, errors.New("device not found"))
			return
		}
		if err := transport.ValidateSSH(in, d.Device.Workspace); err != nil {
			writeError(w, 400, err)
			return
		}
		cid := transport.Token()
		c := connectionRecord{Connection: protocol.Connection{ID: cid, DeviceID: id, Kind: "ssh", SSH: &in, Capabilities: sshCapabilities()}}
		s.store.Connections[cid] = c
		if err := s.save(); err != nil {
			delete(s.store.Connections, cid)
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 201, c.Connection)
		return
	}
	cid := parts[0]
	s.mu.RLock()
	c, ok := s.store.Connections[cid]
	d, exists := s.store.Devices[id]
	s.mu.RUnlock()
	if !ok || !exists || c.Connection.DeviceID != id {
		writeError(w, 404, errors.New("connection not found"))
		return
	}
	if len(parts) == 2 && parts[1] == "test" && r.Method == "POST" {
		s.testConnection(w, r, d.Device, c)
		return
	}
	if len(parts) != 1 {
		writeError(w, 404, errors.New("unknown connection operation"))
		return
	}
	if r.Method != "DELETE" && r.Method != "PATCH" {
		methodNotAllowed(w)
		return
	}
	var in protocol.SSHConfig
	if r.Method == "PATCH" {
		if c.Connection.Kind != "ssh" {
			writeError(w, 400, errors.New("Worker connections are configured by pairing"))
			return
		}
		if err := readJSON(r, &in); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := transport.ValidateSSH(in, d.Device.Workspace); err != nil {
			writeError(w, 400, err)
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok = s.store.Connections[cid]
	d, exists = s.store.Devices[id]
	if !ok || !exists || c.Connection.DeviceID != id {
		writeError(w, 404, errors.New("connection not found"))
		return
	}
	if d.Device.ActiveConnection == cid && s.hasSyncMappingsLocked(id) {
		writeError(w, 409, errors.New("abort or finish active syncs before changing this connection"))
		return
	}
	if r.Method == "DELETE" {
		delete(s.store.Connections, cid)
		if d.Device.ActiveConnection == cid {
			updated := d
			updated.Device.ActiveConnection = ""
			s.store.Devices[id] = updated
		}
	} else {
		updated := c
		updated.Connection.SSH = &in
		updated.Connection.Online = false
		updated.Connection.LastError = ""
		s.store.Connections[cid] = updated
	}
	s.refreshDeviceLocked(id)
	if err := s.save(); err != nil {
		s.store.Connections[cid] = c
		s.store.Devices[id] = d
		writeError(w, 500, err)
		return
	}
	s.cancelConnectionLocked(cid)
	if r.Method == "DELETE" {
		if peer := s.connections[cid]; peer != nil {
			peer.Close()
		}
		delete(s.connections, cid)
		w.WriteHeader(204)
	} else {
		writeJSON(w, 200, s.store.Connections[cid].Connection)
	}
}

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request, d protocol.Device, c connectionRecord) {
	if !d.Authorized {
		writeError(w, 403, errors.New("device authorization revoked"))
		return
	}
	if c.Connection.Kind == "worker" {
		if !c.Connection.Online {
			writeError(w, 503, errors.New("Worker is offline"))
		} else {
			writeJSON(w, 200, c.Connection)
		}
		return
	}
	if c.Connection.SSH == nil {
		writeError(w, 400, errors.New("SSH configuration missing"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	resp, err := s.callSSH(ctx, c.Connection, transport.Request{Method: "GET", Path: "/probe"})
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
	if !ok || !exists || !device.Device.Authorized || current.Connection.SSH != c.Connection.SSH {
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
		updated.Connection.Capabilities = probed.Capabilities
		if device.Device.ActiveConnection == c.Connection.ID {
			device.Device.OS = probed.OS
			device.Device.Architecture = probed.Architecture
			device.Device.LastSeen = time.Now()
			s.store.Devices[d.ID] = device
		}
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

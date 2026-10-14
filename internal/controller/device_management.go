package controller

import (
	"DevFreight/internal/transport"
	"errors"
	"net/http"
	"strings"
)

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

	}
	s.mu.Lock()
	old, exists := s.store.Devices[id]
	if !exists {
		s.mu.Unlock()
		writeError(w, 404, errors.New("device not found"))
		return
	}
	rec := old
	if s.changing[id] {
		s.mu.Unlock()
		writeError(w, 409, errors.New("device connection is being changed"))
		return
	}
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
		if !rec.Device.Authorized {
			rec.Device.Online = false
		}
		s.store.Devices[id] = rec
		s.refreshDeviceLocked(id)
		rec = s.store.Devices[id]
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
			if peer := s.workerPeers[cid]; peer != nil {
				peer.Close()
			}
			delete(s.workerPeers, cid)
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

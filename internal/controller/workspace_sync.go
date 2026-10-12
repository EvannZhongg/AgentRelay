package controller

import (
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/workspace"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

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
	policy := s.store.Connections[rec.Device.ConnectionID].Connection.Workspace
	if policy.Mode != protocol.WorkspaceLocalSync {
		return nil, syncMapping{}, errors.New("sync requires local_sync workspace mode")
	}
	if req.Root == "" {
		if mapped, ok := policy.Mappings[local]; ok {
			req.Root = mapped
		} else {
			sum := sha256.Sum256([]byte(local))
			req.Root = "projects/" + hex.EncodeToString(sum[:8])
		}
	}
	if !workspace.Relative(req.Root) {
		return nil, syncMapping{}, errors.New("remote root must be workspace-relative")
	}
	mapping := syncMapping{connectionID: rec.Device.ConnectionID, sourcePath: local, root: req.Root}
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
	if !rec.Device.Authorized || rec.Device.ConnectionID != mapping.connectionID {
		return errors.New("connection authorization or selection changed during sync")
	}
	previous := rec
	cid := rec.Device.ConnectionID
	previousConnection := s.store.Connections[cid]
	connection := previousConnection
	mappings := make(map[string]string, len(connection.Connection.Workspace.Mappings)+1)
	for key, value := range connection.Connection.Workspace.Mappings {
		mappings[key] = value
	}
	mappings[mapping.sourcePath] = mapping.root
	rec.Device.Mappings = mappings
	connection.Connection.Workspace.Mappings = mappings
	s.store.Connections[cid] = connection
	s.store.Devices[id] = rec
	if err := s.save(); err != nil {
		s.store.Devices[id] = previous
		s.store.Connections[cid] = previousConnection
		return err
	}
	return nil
}

package controller

import (
	"DevFreight/internal/backend"
	"DevFreight/internal/protocol"
	"DevFreight/internal/transport"
	"DevFreight/internal/workspace"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

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
	connection := s.store.Connections[rec.Device.ConnectionID].Connection
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
	if parts[1] == "connection" {
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
	if capability != "" && !backend.Supported(rec.Device.Capabilities, capability) {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("device does not provide %s capability", capability))
		return
	}
	if !connection.Online {
		writeError(w, 503, errors.New("device offline"))
		return
	}
	if strings.HasPrefix(endpoint, "/v1/desktop") && rec.Device.DesktopAccess == "revoked" {
		writeError(w, http.StatusForbidden, errors.New("desktop control permission is revoked"))
		return
	}
	// Keep the operation reserved through response processing and sync bookkeeping.
	s.mu.Lock()
	if !s.connectionUnchangedLocked(id, connection) || s.changing[id] {
		s.mu.Unlock()
		writeError(w, 409, errors.New("device connection changed or is being changed"))
		return
	}
	if s.operations == nil {
		s.operations = map[string]int{}
	}
	s.operations[id]++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.operations[id]--
		if s.operations[id] == 0 {
			delete(s.operations, id)
		}
		s.mu.Unlock()
	}()
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
	}
	if endpoint == "/v1/exec" || (r.Method == "POST" && (endpoint == "/v1/tasks" || endpoint == "/v1/sessions")) {
		data, err = resolveWorkingDir(connection.Workspace, data)
		if err != nil {
			writeError(w, 400, err)
			return
		}
	}
	if endpoint == "/v1/files/read" {
		query := r.URL.Query()
		resolved, resolveErr := workspace.Resolve(connection.Workspace, query.Get("path"), "remote")
		if resolveErr != nil {
			writeError(w, 400, resolveErr)
			return
		}
		query.Set("path", resolved)
		r.URL.RawQuery = query.Encode()
	}
	if r.URL.RawQuery != "" {
		endpoint += "?" + r.URL.RawQuery
	}
	ctx, cancel := context.WithTimeout(r.Context(), 31*time.Minute)
	defer cancel()
	call := func(ctx context.Context, req transport.Request) (transport.Response, error) {
		return s.callConnection(ctx, connection, req)
	}
	var resp transport.Response
	if capability == "" {
		if strings.HasPrefix(endpoint, "/v1/device") {
			resp.Body, _ = json.Marshal(AgentDeviceView(rec.Device))
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
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.markConnectionFailed(connection, err)
		}
		// Transport diagnostics may contain private hosts, users or key paths.
		// Keep them in the management view, outside the Agent operation surface.
		message := "device operation failed; ask the user to check device availability"
		if errors.Is(err, context.Canceled) {
			message = "device operation cancelled"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			message = "device operation timed out"
		}
		writeError(w, 502, errors.New(message))
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
		if err := s.persistSyncMapping(id, commitMapping); err != nil {
			writeError(w, 500, fmt.Errorf("remote sync committed but controller mapping could not be saved: %w", err))
			return
		}
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

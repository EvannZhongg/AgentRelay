package controller

import (
	"AgentRelay/internal/protocol"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncMappingIsBoundToStartedSession(t *testing.T) {
	deviceID := "device-1"
	server := &Server{
		cfg:          Config{StatePath: filepath.Join(t.TempDir(), "controller.json")},
		store:        registry{Connections: map[string]connectionRecord{"connection-1": {Connection: protocol.Connection{ID: "connection-1", DeviceID: deviceID, Workspace: protocol.WorkspacePolicy{Mode: protocol.WorkspaceLocalSync, Root: t.TempDir()}}}}, Devices: map[string]record{deviceID: {Device: protocol.Device{ID: deviceID, Authorized: true, ConnectionID: "connection-1", Mappings: map[string]string{}}}}},
		syncMappings: map[syncMappingKey]syncMapping{},
	}
	source := t.TempDir()
	request, mapping, err := server.prepareSyncStart(deviceID, mustJSON(t, protocol.SyncStartRequest{SourcePath: source, Root: "expected", Mode: protocol.SyncModeUpdate}))
	if err != nil {
		t.Fatal(err)
	}
	var forwarded protocol.SyncStartRequest
	if err := json.Unmarshal(request, &forwarded); err != nil {
		t.Fatal(err)
	}
	if forwarded.SourcePath != "" || forwarded.Root != "expected" {
		t.Fatalf("forwarded start = %+v", forwarded)
	}

	mapping.expiresAt = time.Now().Add(time.Minute)
	server.rememberSyncMapping(deviceID, "sync-1", mapping)
	if _, exists := server.lookupSyncMapping(deviceID, "wrong-session"); exists {
		t.Fatal("unregistered sync session was found")
	}
	bound, exists := server.lookupSyncMapping(deviceID, "sync-1")
	if !exists {
		t.Fatal("registered sync session was not found")
	}
	if err := server.persistSyncMapping(deviceID, bound); err != nil {
		t.Fatal(err)
	}
	if got := server.store.Devices[deviceID].Device.Mappings[source]; got != "expected" {
		t.Fatalf("saved mapping = %q", got)
	}
	if len(server.syncMappings) != 1 {
		t.Fatalf("pending mapping was removed before worker commit: %+v", server.syncMappings)
	}
	server.forgetSyncMapping(deviceID, "sync-1")
	if _, _, ok := parseSyncOperation("/v1/files/sync/start"); ok {
		t.Fatal("sync start was parsed as a session operation")
	}

	mapping.expiresAt = time.Now().Add(time.Minute)
	server.rememberSyncMapping(deviceID, "sync-2", mapping)
	recorder := httptest.NewRecorder()
	updateRequest := httptest.NewRequest(http.MethodPatch, "/v1/devices/"+deviceID, bytes.NewBufferString(`{"authorized":false}`))
	server.updateDevice(recorder, updateRequest, deviceID)
	if recorder.Code != http.StatusOK || len(server.syncMappings) != 0 {
		t.Fatalf("revoke status = %d, mappings = %+v", recorder.Code, server.syncMappings)
	}
}

func TestSyncMappingMaintenanceRemovesExpiredEntries(t *testing.T) {
	server := &Server{syncMappings: map[syncMappingKey]syncMapping{
		{deviceID: "old", syncID: "old"}:       {expiresAt: time.Now().Add(-time.Minute)},
		{deviceID: "active", syncID: "active"}: {expiresAt: time.Now().Add(time.Minute)},
	}}
	server.refreshSyncMapping("active", "active")
	if len(server.syncMappings) != 1 {
		t.Fatalf("refresh did not clean expired mappings: %+v", server.syncMappings)
	}
	server.syncMappings[syncMappingKey{deviceID: "old", syncID: "old"}] = syncMapping{expiresAt: time.Now().Add(-time.Minute)}
	server.forgetSyncMapping("missing", "missing")
	if len(server.syncMappings) != 1 {
		t.Fatalf("forget did not clean expired mappings: %+v", server.syncMappings)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

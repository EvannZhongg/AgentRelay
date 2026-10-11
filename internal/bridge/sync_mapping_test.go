package bridge

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"AgentRelay/internal/protocol"
)

func TestSyncMappingIsBoundToStartedSession(t *testing.T) {
	deviceID := "device-1"
	server := &Server{
		cfg:          Config{StatePath: filepath.Join(t.TempDir(), "bridge.json")},
		store:        registry{Devices: map[string]record{deviceID: {Device: protocol.Device{ID: deviceID, Mappings: map[string]string{}}}}},
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
	if err := server.persistSyncMapping(deviceID, "wrong-session"); err == nil {
		t.Fatal("unregistered sync session was accepted")
	}
	if err := server.persistSyncMapping(deviceID, "sync-1"); err != nil {
		t.Fatal(err)
	}
	if got := server.store.Devices[deviceID].Device.Mappings[source]; got != "expected" {
		t.Fatalf("saved mapping = %q", got)
	}
	if len(server.syncMappings) != 0 {
		t.Fatalf("completed mapping was retained: %+v", server.syncMappings)
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

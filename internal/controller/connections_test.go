package controller

import (
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func adminRequest(t *testing.T, s *Server, method, path string, in any) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func newConnectionServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Config{StatePath: filepath.Join(t.TempDir(), "controller.json")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func createSSHDevice(t *testing.T, s *Server, name string) protocol.Device {
	t.Helper()
	w := adminRequest(t, s, "POST", "/v1/devices", protocol.DeviceCreate{Name: name, Connection: protocol.ConnectionSpec{Kind: "ssh", Workspace: protocol.WorkspacePolicy{Root: "/tmp/workspace", Mode: protocol.WorkspaceRemote}, SSH: &protocol.SSHConfig{Host: "localhost", User: "test", Port: 22}}})
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var d protocol.Device
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	return d
}
func TestConnectionSelectionCapabilitiesAndPersistence(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "test")
	if len(d.Connections) != 1 || d.ActiveConnection != d.Connections[0].ID || d.Capabilities.Sync || d.Online {
		t.Fatalf("initial: %+v", d)
	}
	cid := d.ActiveConnection
	if w := adminRequest(t, s, "POST", "/v1/devices/"+d.ID+"/tasks", protocol.TaskRequest{}); w.Code != 501 {
		t.Fatalf("unsupported: %d %s", w.Code, w.Body.String())
	}
	if w := adminRequest(t, s, "POST", "/v1/devices/"+d.ID+"/exec", protocol.ExecRequest{Command: "echo test"}); w.Code != 503 {
		t.Fatalf("untested SSH: %d", w.Code)
	}
	other := createSSHDevice(t, s, "other")
	if w := adminRequest(t, s, "PATCH", "/v1/devices/"+d.ID, DeviceUpdate{ActiveConnection: &other.ActiveConnection}); w.Code != 400 {
		t.Fatal("accepted another device's connection")
	}
	connection := s.store.Connections[cid]
	connection.Connection.Online = true
	s.store.Connections[cid] = connection
	s.refreshDeviceLocked(d.ID)
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restored := restarted.store.Devices[d.ID].Device; restored.Handle != d.Handle || restored.ActiveConnection != cid || restored.Online {
		t.Fatalf("restored: %+v", restored)
	}
	if restarted.store.Connections[cid].Connection.Online {
		t.Fatal("restored stale connection status")
	}
	if w := adminRequest(t, s, "DELETE", "/v1/devices/"+d.ID+"/connections/"+cid, nil); w.Code != 204 {
		t.Fatalf("delete connection: %d", w.Code)
	}
	if device := s.store.Devices[d.ID].Device; device.ActiveConnection != "" || device.Handle != d.Handle || device.Capabilities.Exec {
		t.Fatalf("identity after disconnect: %+v", device)
	}
	if w := adminRequest(t, s, "DELETE", "/v1/devices/"+other.ID, nil); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	for _, c := range s.store.Connections {
		if c.Connection.DeviceID == other.ID {
			t.Fatal("orphaned connection")
		}
	}
}
func TestWorkerPairingAttachesToOneLogicalDevice(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "one-computer")
	w := adminRequest(t, s, "POST", "/v1/pair", protocol.PairRequest{Device: protocol.Device{Name: "worker-alias", Workspace: d.Workspace, Capabilities: protocol.Capabilities{Exec: true, Files: true, Sync: true, Tasks: true, Sessions: true}}})
	var p protocol.PairingRequest
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	w = adminRequest(t, s, "POST", "/v1/pair-requests/"+p.ID+"/approve", map[string]string{"device_id": d.ID})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var status protocol.PairingStatus
	_ = json.Unmarshal(w.Body.Bytes(), &status)
	if len(s.store.Devices) != 1 || status.Credentials.DeviceID != d.ID || status.Credentials.ConnectionID == d.ActiveConnection {
		t.Fatalf("attachment: %+v", status)
	}
	workerID := status.Credentials.ConnectionID
	w = adminRequest(t, s, "PATCH", "/v1/devices/"+d.ID, DeviceUpdate{ActiveConnection: &workerID})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	current := s.store.Devices[d.ID].Device
	if current.Handle != d.Handle || !current.Capabilities.Tasks || current.Name != d.Name {
		t.Fatalf("selected: %+v", current)
	}
	if s.store.Connections[workerID].TokenHash == "" {
		t.Fatal("worker token missing from connection")
	}
	w = adminRequest(t, s, "GET", "/v1/devices", nil)
	if strings.Contains(w.Body.String(), status.Credentials.Token) || strings.Contains(w.Body.String(), "token_hash") {
		t.Fatal("credentials leaked")
	}
	if len(s.deviceViewLocked(current).Connections) != 2 {
		t.Fatal("two transports were not retained")
	}
	no := false
	w = adminRequest(t, s, "PATCH", "/v1/devices/"+d.ID, DeviceUpdate{Authorized: &no})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = adminRequest(t, s, "POST", "/v1/devices/"+d.ID+"/connections/"+d.ActiveConnection+"/test", nil); w.Code != 403 {
		t.Fatal("revoked SSH test accepted")
	}
}
func TestRejectOldStateWithoutMigration(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "controller.json")
	if err := state.Save(filename, map[string]any{"devices": map[string]any{"old": map[string]any{"device": protocol.Device{ID: "old", Handle: "dev-old"}, "token_hash": "legacy"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{StatePath: filename}); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("old state: %v", err)
	}
}
func TestSwitchConnectionRejectsActiveSync(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "test")
	w := adminRequest(t, s, "POST", "/v1/devices/"+d.ID+"/connections", protocol.ConnectionSpec{Kind: "ssh", Workspace: protocol.WorkspacePolicy{Root: "/other-repo", Mode: protocol.WorkspaceRemote}, SSH: &protocol.SSHConfig{Host: "other", Port: 22, User: "test"}})
	var c protocol.Connection
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if err := s.beginSyncStart(d.ID, d.ActiveConnection); err != nil {
		t.Fatal(err)
	}
	w = adminRequest(t, s, "PATCH", "/v1/devices/"+d.ID, DeviceUpdate{ActiveConnection: &c.ID})
	if w.Code != 409 {
		t.Fatal("switched while sync start was in flight")
	}
	s.endSyncStart(d.ID)
	s.rememberSyncMapping(d.ID, "active", syncMapping{})
	w = adminRequest(t, s, "PATCH", "/v1/devices/"+d.ID, DeviceUpdate{ActiveConnection: &c.ID})
	if w.Code != 409 {
		t.Fatalf("switch during sync: %d", w.Code)
	}
	s.forgetSyncMapping(d.ID, "active")
	w = adminRequest(t, s, "PATCH", "/v1/devices/"+d.ID, DeviceUpdate{ActiveConnection: &c.ID})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

func TestRevocationCancelsEverySSHCall(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.calls[d.ActiveConnection] = map[string]context.CancelFunc{"in-flight": cancel}
	no := false
	w := adminRequest(t, s, "PATCH", "/v1/devices/"+d.ID, DeviceUpdate{Authorized: &no})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if ctx.Err() == nil {
		t.Fatal("in-flight SSH request retained access after revocation")
	}
}

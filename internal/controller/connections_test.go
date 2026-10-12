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
func replacementSpec(root string) protocol.ConnectionSpec {
	return protocol.ConnectionSpec{Kind: "ssh", Workspace: protocol.WorkspacePolicy{Root: root, Mode: protocol.WorkspaceRemote}, SSH: &protocol.SSHConfig{Host: "other", User: "test", Port: 22}}
}
func replaceSSH(t *testing.T, s *Server, d protocol.Device) *httptest.ResponseRecorder {
	t.Helper()
	spec := replacementSpec("/other-repo")
	return adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/connection/replace", protocol.ConnectionReplacement{Connection: &spec})
}
func TestOneConnectionReplacementAndPersistence(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "test")
	if d.Connection == nil || d.ConnectionID != d.Connection.ID || d.Capabilities.Sync || d.Online {
		t.Fatalf("initial: %+v", d)
	}
	for _, op := range []string{"tasks", "desktop/capture", "desktop/input"} {
		if w := adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/"+op, struct{}{}); w.Code != 501 {
			t.Fatalf("unsupported %s: %d %s", op, w.Code, w.Body.String())
		}
	}
	if w := adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/exec", protocol.ExecRequest{Command: "echo test"}); w.Code != 503 {
		t.Fatal(w.Body.String())
	}
	w := replaceSSH(t, s, d)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var changed protocol.Device
	_ = json.Unmarshal(w.Body.Bytes(), &changed)
	if changed.Handle != d.Handle || changed.Name != d.Name || changed.ID != d.ID || changed.ConnectionID == d.ConnectionID || len(s.store.Connections) != 1 || changed.Workspace != "/other-repo" {
		t.Fatalf("replacement: %+v", changed)
	}
	if _, ok := s.store.Connections[d.ConnectionID]; ok {
		t.Fatal("old connection retained")
	}
	restarted, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if got := restarted.store.Devices[d.ID].Device; got.Handle != d.Handle || got.ConnectionID != changed.ConnectionID || got.Online {
		t.Fatalf("restored: %+v", got)
	}
	for _, request := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/connections", replacementSpec("/extra")},
		{"PATCH", "", map[string]string{"active_connection": d.ConnectionID}},
		{"PATCH", "", map[string]string{"connection_id": d.ConnectionID}},
		{"DELETE", "/connection", nil},
		{"DELETE", "/connections/" + changed.ConnectionID, nil},
	} {
		if w := adminRequest(t, s, request.method, "/v1/devices/"+d.Handle+request.path, request.body); w.Code < 400 {
			t.Fatalf("old/multi-connection interface accepted: %s %s", request.method, request.path)
		}
	}
	if w := adminRequest(t, s, "DELETE", "/v1/devices/"+d.Handle, nil); w.Code != 204 || len(s.store.Connections) != 0 {
		t.Fatal("device deletion left an orphan connection")
	}
}
func TestWorkerReplacementKeepsIdentityAndConsumesPairing(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "one-computer")
	w := adminRequest(t, s, "POST", "/v1/pair", protocol.PairRequest{Device: protocol.Device{Name: "worker-alias", Workspace: d.Workspace, Capabilities: protocol.Capabilities{Exec: true, Files: true, Sync: true, Tasks: true, Sessions: true}}})
	var p protocol.PairingRequest
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if w := adminRequest(t, s, "POST", "/v1/pair-requests/"+p.ID+"/approve", map[string]string{"device_id": d.ID}); w.Code != 400 {
		t.Fatal("approval accepted attachment")
	}
	w = adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/connection/replace", protocol.ConnectionReplacement{PairingID: p.ID})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	current := s.store.Devices[d.ID].Device
	if current.Handle != d.Handle || current.Name != d.Name || !current.Capabilities.Tasks || len(s.store.Connections) != 1 || current.ConnectionID == d.ConnectionID {
		t.Fatalf("changed: %+v", current)
	}
	if s.store.Connections[current.ConnectionID].TokenHash == "" || s.pairings[p.ID].credentials.ConnectionID != current.ConnectionID {
		t.Fatal("worker credentials not bound")
	}
	if strings.Contains(w.Body.String(), "token") {
		t.Fatal("management response leaked credentials")
	}
	if w := adminRequest(t, s, "POST", "/v1/pair-requests/"+p.ID+"/approve", nil); w.Code != 409 {
		t.Fatal("pairing replay accepted")
	}
	// Persistent Worker activity cannot be verified while disconnected.
	if w := replaceSSH(t, s, current); w.Code != 409 {
		t.Fatal("switched an offline Worker without checking activity")
	}
}
func TestRejectOldStateWithoutMigration(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "controller.json")
	if err := state.Save(filename, map[string]any{"schema": 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{StatePath: filename}); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("old state: %v", err)
	}
}
func TestChangeRejectsActiveSyncAndRequests(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "test")
	if err := s.beginSyncStart(d.ID, d.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if w := replaceSSH(t, s, d); w.Code != 409 {
		t.Fatal("switched during sync start")
	}
	s.endSyncStart(d.ID)
	s.rememberSyncMapping(d.ID, "active", syncMapping{})
	if w := replaceSSH(t, s, d); w.Code != 409 {
		t.Fatal("switched during sync")
	}
	s.forgetSyncMapping(d.ID, "active")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.calls[d.ConnectionID] = map[string]context.CancelFunc{"in-flight": cancel}
	if w := replaceSSH(t, s, d); w.Code != 409 || ctx.Err() != nil {
		t.Fatal("switch cancelled an active call")
	}
	delete(s.calls, d.ConnectionID)
	s.operations = map[string]int{d.ID: 1}
	if w := replaceSSH(t, s, d); w.Code != 409 {
		t.Fatal("switched during operation bookkeeping")
	}
	delete(s.operations, d.ID)
	if w := replaceSSH(t, s, d); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestReplacementSaveFailurePreservesOldConnection(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "test")
	s.cfg.StatePath = t.TempDir() // Rename onto a directory must fail.
	if w := replaceSSH(t, s, d); w.Code != 500 {
		t.Fatalf("save failure: %d %s", w.Code, w.Body.String())
	}
	if got := s.store.Devices[d.ID].Device; got.ConnectionID != d.ConnectionID || got.Workspace != d.Workspace || len(s.store.Connections) != 1 || s.changing[d.ID] {
		t.Fatalf("rollback: %+v", got)
	}
}
func TestRegistryRejectsUnboundOrAdditionalConnections(t *testing.T) {
	for _, invalid := range []string{"missing", "extra"} {
		t.Run(invalid, func(t *testing.T) {
			s := newConnectionServer(t)
			d := createSSHDevice(t, s, "test")
			if invalid == "missing" {
				delete(s.store.Connections, d.ConnectionID)
			} else {
				c := s.newConnection(d.ID, replacementSpec("/extra"))
				s.store.Connections[c.ID] = connectionRecord{Connection: c}
			}
			if err := s.save(); err != nil {
				t.Fatal(err)
			}
			if _, err := New(s.cfg); err == nil {
				t.Fatal("invalid cardinality accepted")
			}
		})
	}
}
func TestRevocationCancelsEverySSHCall(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.calls[d.ConnectionID] = map[string]context.CancelFunc{"in-flight": cancel}
	no := false
	if w := adminRequest(t, s, "PATCH", "/v1/devices/"+d.ID, DeviceUpdate{Authorized: &no}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if ctx.Err() == nil {
		t.Fatal("call retained access after revocation")
	}
}

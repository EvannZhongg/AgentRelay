package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"AgentRelay/internal/backend"
	"AgentRelay/internal/connection"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"
	"AgentRelay/internal/transport"
)

type memoryAdapter struct{ call backend.Call }

func (memoryAdapter) Validate(protocol.ConnectionSpec) error { return nil }
func (memoryAdapter) Capabilities() protocol.Capabilities {
	return protocol.Capabilities{Exec: true, Files: true}
}
func (memoryAdapter) Probe() transport.Request {
	return transport.Request{Method: "GET", Path: "/probe"}
}
func (m memoryAdapter) Bind(protocol.Connection, backend.Call) (backend.Call, error) {
	return m.call, nil
}

func TestFutureTransportUsesUnifiedOperationsWithoutSync(t *testing.T) {
	var forwarded protocol.ExecRequest
	adapter := memoryAdapter{call: func(ctx context.Context, r transport.Request) (transport.Response, error) {
		var body any
		if r.Path == "/probe" {
			body = protocol.Device{OS: "linux", Architecture: "arm64", Capabilities: protocol.Capabilities{Exec: true, Files: true}}
		} else {
			if r.Path != "/v1/exec" {
				return transport.Response{}, errors.New("wrong operation")
			}
			if err := json.Unmarshal(r.Body, &forwarded); err != nil {
				return transport.Response{}, err
			}
			body = protocol.ExecResponse{Stdout: "executed", ExitCode: 0}
		}
		data, _ := json.Marshal(body)
		return transport.Response{Status: 200, Body: data}, nil
	}}
	s, err := New(Config{StatePath: filepath.Join(t.TempDir(), "controller.json"), Adapters: connection.Registry{"future": adapter}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in := protocol.DeviceCreate{Name: "future-device", Connection: protocol.ConnectionSpec{Kind: "future", Workspace: protocol.WorkspacePolicy{Root: "/srv/repo", Mode: protocol.WorkspaceRemote}}}
	w := adminRequest(t, s, "POST", "/v1/devices", in)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var d protocol.Device
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	w = adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/connections/"+d.ActiveConnection+"/test", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, selector := range []string{d.Handle, d.Name} {
		w = adminRequest(t, s, "POST", "/v1/devices/"+selector+"/exec", protocol.ExecRequest{Command: "pwd", WorkingDir: "/srv/repo/src"})
		if w.Code != 200 || forwarded.WorkingDir != "src" {
			t.Fatalf("unified execution: %d %+v %s", w.Code, forwarded, w.Body.String())
		}
	}
	w = adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/exec", protocol.ExecRequest{Command: "pwd", WorkingDir: "/srv/repo/src", WorkingDirKind: "local"})
	if w.Code != 400 {
		t.Fatalf("remote source accepted local dir: %d", w.Code)
	}
}

func TestWorkspacePolicyIsConnectionOwnedAndPersisted(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "one-device")
	first := s.store.Connections[d.ActiveConnection].Connection
	second := protocol.ConnectionSpec{Kind: "ssh", SSH: first.SSH, Workspace: protocol.WorkspacePolicy{Mode: protocol.WorkspaceLocalSync, Root: "/another-root", Mappings: map[string]string{t.TempDir(): "copy"}}}
	w := adminRequest(t, s, "POST", "/v1/devices/"+d.ID+"/connections", second)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var c protocol.Connection
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	w = adminRequest(t, s, "PATCH", "/v1/devices/"+d.ID, DeviceUpdate{ActiveConnection: &c.ID})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	view := s.store.Devices[d.ID].Device
	if view.Workspace != "/another-root" || view.WorkspaceMode != protocol.WorkspaceLocalSync || len(view.Mappings) != 1 || view.Handle != d.Handle {
		t.Fatalf("selected policy: %+v", view)
	}
	if s.store.Connections[first.ID].Connection.Workspace.Mode != protocol.WorkspaceRemote {
		t.Fatal("overwrote first connection")
	}
	var saved registry
	if err := state.Load(s.cfg.StatePath, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Devices[d.ID].Device.Workspace != "" || len(saved.Devices[d.ID].Device.Mappings) != 0 {
		t.Fatal("workspace persisted as device identity")
	}
	restarted, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if got := restarted.store.Devices[d.ID].Device; got.Workspace != "/another-root" || len(got.Mappings) != 1 {
		t.Fatalf("projection not restored: %+v", got)
	}
	w = adminRequest(t, s, "PATCH", "/v1/devices/"+d.ID, DeviceUpdate{ActiveConnection: &first.ID})
	if w.Code != 200 || len(s.store.Devices[d.ID].Device.Mappings) != 0 {
		t.Fatal("local sync mappings leaked into remote mode")
	}
}

func TestWorkerCanAttachWithIndependentRootAndMode(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "one-computer")
	w := adminRequest(t, s, "POST", "/v1/pair", protocol.PairRequest{Device: protocol.Device{Name: "native-worker", OS: "windows", Workspace: `C:\native-root`, WorkspaceMode: protocol.WorkspaceRemote, Capabilities: protocol.Capabilities{Exec: true, Sync: true}}})
	var p protocol.PairingRequest
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	w = adminRequest(t, s, "POST", "/v1/pair-requests/"+p.ID+"/approve", map[string]string{"device_id": d.ID})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var approved protocol.PairingStatus
	_ = json.Unmarshal(w.Body.Bytes(), &approved)
	cid := approved.Credentials.ConnectionID
	w = adminRequest(t, s, "PATCH", "/v1/devices/"+d.ID, DeviceUpdate{ActiveConnection: &cid})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if got := s.store.Devices[d.ID].Device; got.Workspace != `C:\native-root` || got.WorkspaceMode != protocol.WorkspaceRemote || got.Capabilities.Sync {
		t.Fatalf("worker policy: %+v", got)
	}
	w = adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/files/sync/start", protocol.SyncStartRequest{SourcePath: t.TempDir()})
	if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "sync") {
		t.Fatal("remote source allowed local sync")
	}
}

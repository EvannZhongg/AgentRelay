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

type memoryAdapter struct {
	call backend.Call
	idle func(context.Context) error
}

func (m memoryAdapter) CheckIdle(ctx context.Context, _ protocol.Connection, _ backend.Call) error {
	if m.idle != nil {
		return m.idle(ctx)
	}
	return nil
}

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
	w = adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/connection/test", nil)
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

func TestWorkspacePolicyReplacementIsPersisted(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "one-device")
	first := s.store.Connections[d.ConnectionID].Connection
	second := protocol.ConnectionSpec{Kind: "ssh", SSH: first.SSH, Workspace: protocol.WorkspacePolicy{Mode: protocol.WorkspaceLocalSync, Root: "/another-root", Mappings: map[string]string{t.TempDir(): "copy"}}}
	w := adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/connection/replace", protocol.ConnectionReplacement{Connection: &second})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	view := s.store.Devices[d.ID].Device
	if view.Workspace != "/another-root" || view.WorkspaceMode != protocol.WorkspaceLocalSync || len(view.Mappings) != 1 || view.Handle != d.Handle {
		t.Fatalf("policy: %+v", view)
	}
	if _, ok := s.store.Connections[first.ID]; ok {
		t.Fatal("old policy retained")
	}
	var saved registry
	if err := state.Load(s.cfg.StatePath, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Devices[d.ID].Device.Workspace != "" || len(saved.Devices[d.ID].Device.Mappings) != 0 {
		t.Fatal("workspace persisted as identity")
	}
	restarted, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if got := restarted.store.Devices[d.ID].Device; got.Workspace != "/another-root" || len(got.Mappings) != 1 {
		t.Fatalf("restored: %+v", got)
	}
	remote := replacementSpec("/remote")
	if w := adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/connection/replace", protocol.ConnectionReplacement{Connection: &remote}); w.Code != 200 || len(s.store.Devices[d.ID].Device.Mappings) != 0 {
		t.Fatal("old mappings survived replacement")
	}
}
func TestWorkerReplacementUsesItsOwnRootAndMode(t *testing.T) {
	s := newConnectionServer(t)
	d := createSSHDevice(t, s, "one-computer")
	w := adminRequest(t, s, "POST", "/v1/pair", protocol.PairRequest{Device: protocol.Device{Name: "native-worker", OS: "windows", Workspace: `C:\native-root`, WorkspaceMode: protocol.WorkspaceRemote, Capabilities: protocol.Capabilities{Exec: true, Sync: true}}})
	var p protocol.PairingRequest
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	w = adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/connection/replace", protocol.ConnectionReplacement{PairingID: p.ID})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if got := s.store.Devices[d.ID].Device; got.Workspace != `C:\native-root` || got.WorkspaceMode != protocol.WorkspaceRemote || got.Capabilities.Sync {
		t.Fatalf("worker policy: %+v", got)
	}
	w = adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/files/sync/start", protocol.SyncStartRequest{SourcePath: t.TempDir()})
	if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "sync") {
		t.Fatal("remote allowed local sync")
	}
}
func TestChangingConnectionReservesDeviceUntilCommit(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	adapter := memoryAdapter{idle: func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	s, err := New(Config{StatePath: filepath.Join(t.TempDir(), "controller.json"), Adapters: connection.Registry{"memory": adapter}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := adminRequest(t, s, "POST", "/v1/devices", protocol.DeviceCreate{Name: "memory", Connection: protocol.ConnectionSpec{Kind: "memory", Workspace: protocol.WorkspacePolicy{Mode: protocol.WorkspaceRemote, Root: "/repo"}}})
	var d protocol.Device
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	c := s.store.Connections[d.ConnectionID]
	c.Connection.Online = true
	s.store.Connections[d.ConnectionID] = c
	s.refreshDeviceLocked(d.ID)
	done := make(chan int, 1)
	go func() { done <- replaceSSH(t, s, d).Code }()
	<-entered
	for _, op := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/exec", protocol.ExecRequest{Command: "pwd"}},
		{"POST", "/connection/replace", protocol.ConnectionReplacement{Connection: ptrSpec(replacementSpec("/other"))}},
		{"PATCH", "", DeviceUpdate{Name: ptrString("renamed")}},
		{"DELETE", "", nil},
	} {
		if w := adminRequest(t, s, op.method, "/v1/devices/"+d.Handle+op.path, op.body); w.Code != 409 {
			t.Fatalf("reservation failed: %s %d %s", op.path, w.Code, w.Body.String())
		}
	}
	close(release)
	if code := <-done; code != 200 {
		t.Fatalf("replacement: %d", code)
	}
}
func ptrSpec(s protocol.ConnectionSpec) *protocol.ConnectionSpec { return &s }
func ptrString(s string) *string                                 { return &s }

func TestAgentOperationsHideTransportDiagnostics(t *testing.T) {
	adapter := memoryAdapter{call: func(context.Context, transport.Request) (transport.Response, error) {
		return transport.Response{}, errors.New("private-host private-user private-key")
	}}
	s, err := New(Config{StatePath: filepath.Join(t.TempDir(), "controller.json"), Adapters: connection.Registry{"private": adapter}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := adminRequest(t, s, "POST", "/v1/devices", protocol.DeviceCreate{Name: "test", Connection: protocol.ConnectionSpec{Kind: "private", Workspace: protocol.WorkspacePolicy{Mode: protocol.WorkspaceRemote, Root: "/repo"}}})
	var d protocol.Device
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	c := s.store.Connections[d.ConnectionID]
	c.Connection.Online = true
	s.store.Connections[d.ConnectionID] = c
	s.refreshDeviceLocked(d.ID)
	w = adminRequest(t, s, "POST", "/v1/devices/"+d.Handle+"/exec", protocol.ExecRequest{Command: "pwd"})
	if w.Code != 502 || strings.Contains(w.Body.String(), "private-") {
		t.Fatalf("private diagnostics leaked: %d %s", w.Code, w.Body.String())
	}
	if got := s.store.Connections[d.ConnectionID].Connection.LastError; !strings.Contains(got, "private-host") {
		t.Fatal("management diagnostics lost")
	}
}

package controller_test

import (
	"AgentRelay/internal/controller"
	"AgentRelay/internal/filesync"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"
	"AgentRelay/internal/testshell"
	"AgentRelay/internal/testutil"
	"AgentRelay/internal/worker"
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func restartController(t *testing.T, relay *testutil.Relay) (*controller.Server, *httptest.Server, *controller.Client) {
	t.Helper()
	address := relay.HTTP.Listener.Addr().String()
	relay.Controller.Close()
	relay.HTTP.Close()
	b, err := controller.New(controller.Config{StatePath: relay.StatePath})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(b.Handler())
	_ = server.Listener.Close()
	server.Listener = listener
	server.TLS, err = b.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	t.Cleanup(b.Close)
	return b, server, controller.NewClient(server.URL)
}

func TestControllerRestartRejectsCommitBeforeWorkerAppliesIt(t *testing.T) {
	relay := testutil.New(t, "")
	ctx := context.Background()
	devicePath := "/v1/devices/" + relay.Device.ID
	var started protocol.SyncStartResponse
	if err := relay.Client.Do(ctx, "POST", devicePath+"/files/sync/start", protocol.SyncStartRequest{
		SourcePath: t.TempDir(),
		Root:       "restart-guard",
		Mode:       protocol.SyncModeUpdate,
	}, &started); err != nil {
		t.Fatal(err)
	}
	if err := relay.Client.Do(ctx, "POST", devicePath+"/files/sync/"+started.ID+"/manifest", protocol.SyncManifestRequest{Final: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := relay.Client.Do(ctx, "POST", devicePath+"/files/sync/"+started.ID+"/plan", struct{}{}, nil); err != nil {
		t.Fatal(err)
	}

	_, _, client := restartController(t, relay)
	peer, err := relay.Worker.Connect(ctx, relay.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(peer.Close)
	commitPath := devicePath + "/files/sync/" + started.ID + "/commit"
	if err := client.Do(ctx, "POST", commitPath, struct{}{}, nil); err == nil || !strings.Contains(err.Error(), "mapping not found or expired") {
		t.Fatalf("commit after controller restart = %v", err)
	}
	if err := client.Do(ctx, "DELETE", devicePath+"/files/sync/"+started.ID, nil, nil); err != nil {
		t.Fatalf("worker session was committed before mapping validation: %v", err)
	}
}

func TestControllerRestartPreservesIdentityMappingsAndWorkerProcesses(t *testing.T) {
	relay := testutil.New(t, "")
	ctx := context.Background()
	alias := "my-build-machine"
	if _, err := relay.Client.UpdateDevice(ctx, relay.Device.ID, controller.DeviceUpdate{Name: &alias}); err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	if err := os.MkdirAll(filepath.Join(local, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "sub", "input.txt"), []byte("persisted-map"), 0600); err != nil {
		t.Fatal(err)
	}
	synced, err := relay.Client.Sync(ctx, relay.Device.ID, filesync.Options{SourcePath: local, Mode: protocol.SyncModeUpdate, UseGitIgnore: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(synced.Root, "projects/") {
		t.Fatalf("automatic root: %q", synced.Root)
	}
	task, err := relay.Client.StartTask(ctx, relay.Device.ID, protocol.TaskRequest{ExecRequest: protocol.ExecRequest{Command: testshell.Native("Start-Sleep -Milliseconds 1500; Write-Output survived", "sleep 1.5; printf '%s\\n' survived")}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := relay.Client.CreateSession(ctx, relay.Device.ID, protocol.SessionRequest{WorkingDir: local, Command: testshell.Native("$relayValue = 'retained'", "relayValue='retained'")})
	if err != nil {
		t.Fatal(err)
	}
	b, _, client := restartController(t, relay)
	if b.Fingerprint() != relay.Controller.Fingerprint() {
		t.Fatal("controller identity changed")
	}
	devices, err := client.Devices(ctx)
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices: %+v, %v", devices, err)
	}
	d := devices[0]
	if d.ID != relay.Device.ID || d.Name != alias || !d.Authorized || d.Online || d.Workspace != relay.Credentials.Workspace || d.Mappings[local] != synced.Root {
		t.Fatalf("restored: %+v", d)
	}
	var saved protocol.Credentials
	if err := state.Load(state.Path("worker"), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.DeviceID != d.ID || saved.Token != relay.Credentials.Token {
		t.Fatal("worker credentials not persisted")
	}
	peer, err := relay.Worker.Connect(ctx, saved)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(peer.Close)
	result, err := client.Exec(ctx, d.ID, protocol.ExecRequest{WorkingDir: filepath.Join(local, "sub"), Command: testshell.Native("Get-Content input.txt", "cat input.txt")})
	if err != nil || !strings.Contains(result.Stdout, "persisted-map") {
		t.Fatalf("mapped execution: %+v, %v", result, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err = client.GetTask(ctx, d.ID, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if task.FinishedAt != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if task.Result == nil || !strings.Contains(task.Result.Stdout, "survived") {
		t.Fatalf("task lost across controller restart: %+v", task)
	}
	if _, err = client.WriteSession(ctx, d.ID, session.ID, testshell.Native("Write-Output $relayValue\r\n", "printf '%s\\n' \"$relayValue\"\n")); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		session, err = client.GetSession(ctx, d.ID, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(session.Stdout, "retained") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(session.Stdout, "retained") {
		t.Fatal("session state lost")
	}
	if _, err = client.CloseSession(ctx, d.ID, session.ID); err != nil {
		t.Fatal(err)
	}
	peer.Close()
	fresh, err := worker.New(worker.Config{Workspace: saved.Workspace, DeviceID: saved.DeviceID})
	if err != nil {
		t.Fatal(err)
	}
	peer, err = fresh.Connect(ctx, saved)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(peer.Close)
	if _, err = client.GetTask(ctx, d.ID, task.ID); err == nil {
		t.Fatal("fresh Worker retained an old process record")
	}
	var current map[string]any
	if err = client.Do(ctx, "GET", "/v1/devices/"+d.ID+"/device", nil, &current); err != nil || current["handle"] != d.Handle {
		t.Fatalf("Worker restart identity: %+v, %v", current, err)
	}
	devices, err = client.Devices(ctx)
	if err != nil || len(devices) != 1 || devices[0].Name != alias {
		t.Fatalf("alias overwritten: %+v, %v", devices, err)
	}
}

func TestPairingAuthenticationRevocationAndRemoval(t *testing.T) {
	relay := testutil.New(t, "")
	ctx := context.Background()
	bad := relay.Credentials
	bad.Token = "incorrect"
	if _, err := relay.Worker.Connect(ctx, bad); !errors.Is(err, worker.ErrUnauthorized) {
		t.Fatalf("invalid token accepted: %v", err)
	}
	other, err := worker.New(worker.Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	requestCh := make(chan protocol.PairingRequest, 1)
	resultCh := make(chan error, 1)
	otherState := filepath.Join(t.TempDir(), "worker.json")
	go func() {
		_, pairErr := other.Pair(ctx, relay.HTTP.URL, relay.Controller.Fingerprint(), otherState, func(request protocol.PairingRequest) { requestCh <- request })
		resultCh <- pairErr
	}()
	pairRequest := <-requestCh
	if _, err = relay.Client.DecidePairing(ctx, pairRequest.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = <-resultCh; err != nil {
		t.Fatal(err)
	}
	unauthorized := false
	if _, err = relay.Client.UpdateDevice(ctx, relay.Device.ID, controller.DeviceUpdate{Authorized: &unauthorized}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-relay.Peer.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("revoked connection left open")
	}
	if _, err = relay.Worker.Connect(ctx, relay.Credentials); !errors.Is(err, worker.ErrUnauthorized) {
		t.Fatalf("revoked credentials accepted: %v", err)
	}
	if _, err = relay.Client.Exec(ctx, relay.Device.ID, protocol.ExecRequest{Command: testshell.Native("Write-Output forbidden", "printf '%s\\n' forbidden")}); err == nil {
		t.Fatal("revoked execution accepted")
	}
	_, _, client := restartController(t, relay)
	devices, err := client.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if d.ID == relay.Device.ID && d.Authorized {
			t.Fatal("revocation not persisted")
		}
	}
	if err = client.Do(ctx, "DELETE", "/v1/devices/"+relay.Device.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	reloaded, err := controller.New(controller.Config{StatePath: relay.StatePath})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/v1/devices", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	reloaded.Handler().ServeHTTP(response, request)
	if strings.Contains(response.Body.String(), relay.Device.ID) {
		t.Fatal("removed device restored")
	}
}

func TestWorkerReconnectsAfterControllerRestart(t *testing.T) {
	relay := testutil.New(t, "")
	relay.Peer.Close()
	testutil.WaitOffline(t, relay.Client, relay.Device.ID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- relay.Worker.Run(ctx, relay.Credentials, nil) }()
	waitOnline := func(client *controller.Client) {
		t.Helper()
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			devices, err := client.Devices(context.Background())
			if err == nil && len(devices) == 1 && devices[0].Online {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("Worker did not reconnect")
	}
	waitOnline(relay.Client)
	_, _, client := restartController(t, relay)
	waitOnline(client)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestControllerAPIRejectsRemoteCallersAndBrowserOrigins(t *testing.T) {
	relay := testutil.New(t, "")
	for _, remote := range []string{"192.168.1.50:1234", "127.0.0.1:1234"} {
		req := httptest.NewRequest("GET", "/v1/pair-requests", nil)
		req.RemoteAddr = remote
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		if strings.HasPrefix(remote, "127.") {
			req.Header.Set("Origin", "https://untrusted.example")
		}
		out := httptest.NewRecorder()
		relay.Controller.Handler().ServeHTTP(out, req)
		if out.Code != 403 {
			t.Fatalf("remote controller access: %d", out.Code)
		}
	}
	if err := relay.Client.Do(context.Background(), "POST", "/v1/devices", map[string]string{"worker_url": "http://unused:8765"}, nil); err == nil {
		t.Fatal("legacy registration API remains enabled")
	}
}

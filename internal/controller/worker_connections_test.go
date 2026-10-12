package controller_test

import (
	"AgentRelay/internal/controller"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/testshell"
	"AgentRelay/internal/testutil"
	"AgentRelay/internal/worker"
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sshReplacement() protocol.ConnectionReplacement {
	return protocol.ConnectionReplacement{Connection: &protocol.ConnectionSpec{Kind: "ssh", SSH: &protocol.SSHConfig{Host: "localhost", Port: 22, User: "test"}, Workspace: protocol.WorkspacePolicy{Mode: protocol.WorkspaceRemote, Root: "/repo"}}}
}
func TestWorkerChangeRejectsTasksSessionsSyncsAndOffline(t *testing.T) {
	relay := testutil.New(t, "")
	ctx := context.Background()
	reject := func(label string) {
		t.Helper()
		if _, err := relay.Client.ReplaceConnection(ctx, relay.Device.Handle, sshReplacement()); err == nil {
			t.Fatalf("changed during %s", label)
		}
		ds, err := relay.Client.Devices(ctx)
		if err != nil || len(ds) != 1 || ds[0].ConnectionID != relay.Credentials.ConnectionID {
			t.Fatalf("failed change modified identity: %+v %v", ds, err)
		}
	}
	task, err := relay.Client.StartTask(ctx, relay.Device.Handle, protocol.TaskRequest{ExecRequest: protocol.ExecRequest{Command: testshell.Native("Start-Sleep -Seconds 30", "sleep 30")}})
	if err != nil {
		t.Fatal(err)
	}
	reject("running task")
	if err := relay.Client.CancelTask(ctx, relay.Device.Handle, task.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := relay.Client.GetTask(ctx, relay.Device.Handle, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.FinishedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	session, err := relay.Client.CreateSession(ctx, relay.Device.Handle, protocol.SessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	reject("shell session")
	if _, err := relay.Client.CloseSession(ctx, relay.Device.Handle, session.ID); err != nil {
		t.Fatal(err)
	}
	var sync protocol.SyncStartResponse
	if err := relay.Client.Do(ctx, "POST", "/v1/devices/"+relay.Device.Handle+"/files/sync/start", protocol.SyncStartRequest{SourcePath: t.TempDir(), Root: "copy", Mode: protocol.SyncModeUpdate}, &sync); err != nil {
		t.Fatal(err)
	}
	reject("sync")
	if err := relay.Client.Do(ctx, "DELETE", "/v1/devices/"+relay.Device.Handle+"/files/sync/"+sync.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	relay.Peer.Close()
	testutil.WaitOffline(t, relay.Client, relay.Device.ID)
	reject("offline Worker")
	peer, err := relay.Worker.Connect(ctx, relay.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	changed, err := relay.Client.ReplaceConnection(ctx, relay.Device.Handle, sshReplacement())
	if err != nil {
		t.Fatal(err)
	}
	if changed.Handle != relay.Device.Handle || changed.Name != relay.Device.Name || changed.Connection.Kind != "ssh" || changed.Capabilities.Desktop != nil || changed.Capabilities.Tasks {
		t.Fatalf("changed: %+v", changed)
	}
	if _, err := relay.Worker.Connect(ctx, relay.Credentials); !errors.Is(err, worker.ErrUnauthorized) {
		t.Fatalf("old credentials still valid: %v", err)
	}
	if _, err := relay.Client.Exec(ctx, relay.Device.Handle, protocol.ExecRequest{Command: "echo no-fallback"}); err == nil {
		t.Fatal("fell back to old Worker")
	}
}

func TestWorkerReplacementKeepsExactlyOneDeviceAndInvalidatesOldPeer(t *testing.T) {
	relay := testutil.New(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	second, err := worker.New(worker.Config{Workspace: t.TempDir(), Name: "replacement-worker"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	requests := make(chan protocol.PairingRequest, 1)
	type result struct {
		creds protocol.Credentials
		err   error
	}
	results := make(chan result, 1)
	go func() {
		c, err := second.Pair(ctx, relay.HTTP.URL, relay.Controller.Fingerprint(), filepath.Join(t.TempDir(), "second.json"), func(p protocol.PairingRequest) { requests <- p })
		results <- result{c, err}
	}()
	p := <-requests
	d, err := relay.Client.ReplaceConnection(ctx, relay.Device.Handle, protocol.ConnectionReplacement{PairingID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	res := <-results
	if res.err != nil {
		t.Fatal(res.err)
	}
	select {
	case <-relay.Peer.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("old peer remained open")
	}
	if _, err := relay.Worker.Connect(ctx, relay.Credentials); !errors.Is(err, worker.ErrUnauthorized) {
		t.Fatalf("old credentials: %v", err)
	}
	peer, err := second.Connect(ctx, res.creds)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	ds, err := relay.Client.Devices(ctx)
	if err != nil || len(ds) != 1 || ds[0].Handle != relay.Device.Handle || ds[0].Name != relay.Device.Name || ds[0].ConnectionID != d.ConnectionID || ds[0].ConnectionID == relay.Credentials.ConnectionID || ds[0].Workspace != res.creds.Workspace {
		t.Fatalf("replacement: %+v %v", ds, err)
	}
	out, err := relay.Client.Exec(ctx, relay.Device.Handle, protocol.ExecRequest{Command: testshell.Native("Write-Output replacement-worker", "printf '%s\n' replacement-worker")})
	if err != nil || !strings.Contains(out.Stdout, "replacement-worker") {
		t.Fatalf("execution: %+v %v", out, err)
	}
	if _, err := relay.Client.ReplaceConnection(ctx, relay.Device.Handle, protocol.ConnectionReplacement{PairingID: p.ID}); err == nil {
		t.Fatal("pairing replay accepted")
	}
}

func TestRestartChecksWorkerActivityWithoutControllerMappings(t *testing.T) {
	relay := testutil.New(t, "")
	ctx := context.Background()
	// Create remote sync state that the restarted Controller does not remember.
	var sync protocol.SyncStartResponse
	if err := relay.Client.Do(ctx, "POST", "/v1/devices/"+relay.Device.Handle+"/files/sync/start", protocol.SyncStartRequest{SourcePath: t.TempDir(), Root: "copy", Mode: protocol.SyncModeUpdate}, &sync); err != nil {
		t.Fatal(err)
	}
	relay.Controller.Close()
	restarted, err := controller.New(controller.Config{StatePath: relay.StatePath})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	httpServer := httptest.NewUnstartedServer(restarted.Handler())
	httpServer.TLS, err = restarted.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	httpServer.StartTLS()
	defer httpServer.Close()
	credentials := relay.Credentials
	credentials.ControllerURL = httpServer.URL
	peer, err := relay.Worker.Connect(ctx, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	client := controller.NewClient(httpServer.URL)
	if _, err := client.ReplaceConnection(ctx, relay.Device.Handle, sshReplacement()); err == nil {
		t.Fatal("forgotten remote sync did not block change")
	}
	if err := client.Do(ctx, "DELETE", "/v1/devices/"+relay.Device.Handle+"/files/sync/"+sync.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReplaceConnection(ctx, relay.Device.Handle, sshReplacement()); err != nil {
		t.Fatal(err)
	}
}

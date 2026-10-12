package bridge_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"AgentRelay/internal/bridge"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/testutil"
	"AgentRelay/internal/worker"
)

func TestMultipleWorkerConnectionsKeepOneDeviceAndRouteExplicitly(t *testing.T) {
	relay := testutil.New(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	second, err := worker.New(worker.Config{Workspace: relay.Credentials.Workspace, Name: "second-connection"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	requests := make(chan protocol.PairingRequest, 1)
	credentials := make(chan protocol.Credentials, 1)
	errorsCh := make(chan error, 1)
	go func() {
		c, err := second.Pair(ctx, relay.HTTP.URL, relay.Bridge.Fingerprint(), filepath.Join(t.TempDir(), "second.json"), func(p protocol.PairingRequest) { requests <- p })
		credentials <- c
		errorsCh <- err
	}()
	p := <-requests
	if _, err := relay.Client.AttachPairing(ctx, p.ID, relay.Device.ID); err != nil {
		t.Fatal(err)
	}
	creds := <-credentials
	if err := <-errorsCh; err != nil {
		t.Fatal(err)
	}
	peer, err := second.Connect(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	ds, err := relay.Client.Devices(ctx)
	if err != nil || len(ds) != 1 || len(ds[0].Connections) != 2 || ds[0].Handle != relay.Device.Handle || ds[0].ActiveConnection != relay.Credentials.ConnectionID || !ds[0].Online {
		t.Fatalf("attached devices: %+v %v", ds, err)
	}
	bad := relay.Credentials
	bad.ConnectionID = creds.ConnectionID
	if _, err := relay.Worker.Connect(ctx, bad); !errors.Is(err, worker.ErrUnauthorized) {
		t.Fatalf("cross-connection credentials: %v", err)
	}
	if _, err := relay.Client.UpdateDevice(ctx, relay.Device.Handle, bridge.DeviceUpdate{ActiveConnection: &creds.ConnectionID}); err != nil {
		t.Fatal(err)
	}
	result, err := relay.Client.Exec(ctx, relay.Device.Handle, protocol.ExecRequest{Command: "Write-Output selected-worker"})
	if err != nil || !strings.Contains(result.Stdout, "selected-worker") {
		t.Fatalf("selected RPC: %+v %v", result, err)
	}
	peer.Close()
	testutil.WaitOffline(t, relay.Client, relay.Device.ID)
	if _, err := relay.Client.Exec(ctx, relay.Device.Handle, protocol.ExecRequest{Command: "Write-Output no-fallback"}); err == nil {
		t.Fatal("implicitly fell back to another live connection")
	}
	if _, err := relay.Client.UpdateDevice(ctx, relay.Device.Handle, bridge.DeviceUpdate{ActiveConnection: &relay.Credentials.ConnectionID}); err != nil {
		t.Fatal(err)
	}
	result, err = relay.Client.Exec(ctx, relay.Device.Handle, protocol.ExecRequest{Command: "Write-Output first-worker"})
	if err != nil || !strings.Contains(result.Stdout, "first-worker") {
		t.Fatalf("restored RPC: %+v %v", result, err)
	}
}

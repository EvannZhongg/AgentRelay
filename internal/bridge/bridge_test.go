package bridge

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"

	"AgentRelay/internal/protocol"
	"AgentRelay/internal/worker"
)

func TestBridgeRegistersAndProxiesWorker(t *testing.T) {
	workerServer, err := worker.New(worker.Config{Workspace: t.TempDir(), Name: "proxy-test"})
	if err != nil {
		t.Fatal(err)
	}
	ws := httptest.NewServer(workerServer.Handler())
	defer ws.Close()
	bridgeServer := New(Config{})
	bs := httptest.NewServer(bridgeServer.Handler())
	defer bs.Close()
	client := NewClient(bs.URL)
	device, err := client.AddDevice(context.Background(), ws.URL, "proxy-test")
	if err != nil {
		t.Fatal(err)
	}
	if device.ID == "" {
		t.Fatal("device ID is empty")
	}
	devices, err := client.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("devices = %d", len(devices))
	}
	result, err := client.Exec(context.Background(), device.ID, protocol.ExecRequest{Command: "Write-Output proxy-ok"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Stdout, "proxy-ok") {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	_, err = client.Sync(context.Background(), device.ID, protocol.SyncRequest{Files: []protocol.FileEntry{{Path: "a.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("a"))}}})
	if err != nil {
		t.Fatal(err)
	}
}

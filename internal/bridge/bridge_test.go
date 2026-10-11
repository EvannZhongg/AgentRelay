package bridge_test

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"AgentRelay/internal/filesync"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/testutil"
)

func TestBridgePairsAndCallsOutboundWorker(t *testing.T) {
	relay := testutil.New(t, "")
	client, device := relay.Client, relay.Device
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
	if !devices[0].Online {
		t.Fatal("registered worker is offline")
	}
	result, err := client.Exec(context.Background(), device.ID, protocol.ExecRequest{Command: "Write-Output proxy-ok"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Stdout, "proxy-ok") {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	synced, err := client.Sync(context.Background(), device.ID, filesync.Options{SourcePath: source, Root: "project", Mode: protocol.SyncModeUpdate, UseGitIgnore: true})
	if err != nil {
		t.Fatal(err)
	}
	file, err := client.ReadFile(context.Background(), device.ID, synced.Root+"/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if file.ContentBase64 != base64.StdEncoding.EncodeToString([]byte("a")) {
		t.Fatalf("unexpected pulled file: %+v", file)
	}
	task, err := client.StartTask(context.Background(), device.ID, protocol.TaskRequest{ExecRequest: protocol.ExecRequest{Command: "Write-Output task-ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID == "" {
		t.Fatal("task ID is empty")
	}
	tasks, err := client.ListTasks(context.Background(), device.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks = %#v, error = %v", tasks, err)
	}
	session, err := client.CreateSession(context.Background(), device.ID, protocol.SessionRequest{})
	if err != nil || session.ID == "" {
		t.Fatalf("session = %#v, error = %v", session, err)
	}
	defer func() { _, _ = client.CloseSession(context.Background(), device.ID, session.ID) }()
	if _, err := client.WriteSession(context.Background(), device.ID, session.ID, "Write-Output bridge-session-ok\r\n"); err != nil {
		t.Fatal(err)
	}
	var current protocol.Session
	for i := 0; i < 200; i++ {
		current, err = client.GetSession(context.Background(), device.ID, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(current.Stdout, "bridge-session-ok") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !strings.Contains(current.Stdout, "bridge-session-ok") {
		t.Fatalf("session output = %q", current.Stdout)
	}
	if _, err := client.CloseSession(context.Background(), device.ID, session.ID); err != nil {
		t.Fatal(err)
	}
	relay.Peer.Close()
	testutil.WaitOffline(t, client, device.ID)
	devices, err = client.Devices(context.Background())
	if err != nil || len(devices) != 1 || devices[0].Online {
		t.Fatalf("offline devices = %+v, error = %v", devices, err)
	}
}

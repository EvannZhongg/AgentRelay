package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"AgentRelay/internal/protocol"
	"AgentRelay/internal/testshell"
	"AgentRelay/internal/testutil"
)

func TestAgentDeviceListingOmitsConnectionConfiguration(t *testing.T) {
	d := protocol.Device{ID: "private-device", Handle: "dev-example", Name: "computer", ConnectionID: "private-connection", Capabilities: protocol.Capabilities{Exec: true}, Connection: &protocol.Connection{ID: "private-connection", Kind: "ssh", SSH: &protocol.SSHConfig{Host: "private-host", User: "private-user", IdentityFile: "private-key-file"}}}
	data, err := json.Marshal(agentDeviceList([]protocol.Device{d}))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-device", "private-connection", "private-host", "private-user", "private-key-file", "connection_id", "connection", "ssh", "worker"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("agent learned connection configuration: %s", data)
		}
	}
	if !strings.Contains(string(data), `"desktop":false`) {
		t.Fatal("unsupported Desktop must be explicit")
	}
	d.Capabilities.Desktop = &protocol.DesktopCapability{Supported: true, Backend: "private-backend"}
	data, _ = json.Marshal(agentDeviceList([]protocol.Device{d}))
	if !strings.Contains(string(data), `"desktop":true`) || strings.Contains(string(data), "private-backend") {
		t.Fatal("Desktop must expose availability, not adapter details")
	}
	for _, definition := range toolDefinitions() {
		if strings.Contains(definition["name"].(string), "connection") {
			t.Fatal("connection management exposed as an Agent tool")
		}
	}
	if !strings.Contains(string(data), "dev-example") || !strings.Contains(string(data), "capabilities") {
		t.Fatal("agent identity or capabilities missing")
	}
}

func TestMCPExecutesRemoteRepositoryWithoutSync(t *testing.T) {
	relay := testutil.New(t, "")
	ctx := context.Background()
	c := *relay.Device.Connection
	policy := c.Workspace
	policy.Mode = protocol.WorkspaceRemote
	if _, err := relay.Client.UpdateConnection(ctx, relay.Device.Handle, protocol.ConnectionSpec{Kind: c.Kind, Workspace: policy}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(policy.Root, "existing-repo")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
		result, err := handle(ctx, relay.Client, rpcRequest{Method: "tools/call", Params: params})
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]any)
	}
	listed := call("device_list", map[string]any{})
	text := listed["content"].([]any)[0].(map[string]any)["text"].(string)
	var devices []map[string]any
	if err := json.Unmarshal([]byte(text), &devices); err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0]["workspace_mode"] != protocol.WorkspaceRemote || devices[0]["capabilities"].(map[string]any)["sync"] != false {
		t.Fatalf("remote discovery: %s", text)
	}
	for _, selector := range []string{relay.Device.Handle, relay.Device.Name} {
		result := call("device_exec", map[string]any{"device_id": selector, "working_dir": dir, "working_dir_kind": "remote", "command": testshell.Native("(Get-Location).Path", "pwd")})
		if result["isError"] == true {
			t.Fatalf("remote exec: %+v", result)
		}
		text = result["content"].([]any)[0].(map[string]any)["text"].(string)
		var execution protocol.ExecResponse
		if err := json.Unmarshal([]byte(text), &execution); err != nil {
			t.Fatal(err)
		}
		if execution.ExitCode != 0 || !testutil.OutputIsPath(t, execution.Stdout, dir) {
			t.Fatalf("unsynced repository: %+v", execution)
		}
	}
}

package main

import (
	"DevFreight/internal/testshell"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"DevFreight/internal/controller"
	"DevFreight/internal/protocol"
	"DevFreight/internal/testutil"
)

func TestMCPInitializeNegotiatesSupportedVersion(t *testing.T) {
	params, _ := json.Marshal(map[string]string{"protocolVersion": "2025-03-26"})
	result, err := handle(context.Background(), controller.NewClient("http://127.0.0.1:1"), rpcRequest{Method: "initialize", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	got := result.(map[string]any)["protocolVersion"]
	if got != "2025-03-26" {
		t.Fatalf("protocol version = %v", got)
	}
}

func TestMCPStdioFramingAndErrors(t *testing.T) {
	input := strings.NewReader("{\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":9007199254740993,"method":"ping"}` + "\n" +
		`{"jsonrpc":"2.0","id":"unknown","method":"missing"}` + "\n" +
		`{"jsonrpc":"1.0","id":1,"method":"ping"}` + "\n" +
		`[]` + "\n")
	var output bytes.Buffer
	if err := serve(controller.NewClient("http://127.0.0.1:1"), input, &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("responses = %s", output.String())
	}
	if !strings.Contains(lines[0], `"id":null`) || !strings.Contains(lines[0], `"code":-32700`) {
		t.Fatal(lines[0])
	}
	if !strings.Contains(lines[1], `"id":9007199254740993`) {
		t.Fatal(lines[1])
	}
	if !strings.Contains(lines[2], `"code":-32601`) {
		t.Fatal(lines[2])
	}
	if !strings.Contains(lines[3], `"code":-32600`) {
		t.Fatal(lines[3])
	}
	if !strings.Contains(lines[4], `"code":-32600`) {
		t.Fatal(lines[4])
	}
}

func TestMCPDevelopmentWorkflow(t *testing.T) {
	relay := testutil.New(t, "")
	client, device := relay.Client, relay.Device
	ctx := context.Background()
	call := func(name string, args map[string]any, out any) {
		t.Helper()
		args["device_id"] = device.ID
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
		result, err := handle(ctx, client, rpcRequest{Method: "tools/call", Params: params})
		if err != nil {
			t.Fatal(err)
		}
		value := result.(map[string]any)
		text := value["content"].([]any)[0].(map[string]any)["text"].(string)
		if value["isError"] == true {
			t.Fatalf("%s: %s", name, text)
		}
		if out != nil {
			if err := json.Unmarshal([]byte(text), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var devices []map[string]any
	call("device_list", map[string]any{}, &devices)
	if len(devices) != 1 || devices[0]["handle"] == "" || devices[0]["authorized"] != true {
		t.Fatalf("devices = %+v", devices)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "input.txt"), []byte("workflow-payload"), 0600); err != nil {
		t.Fatal(err)
	}
	call("device_sync", map[string]any{"source_path": source, "root": "project"}, nil)
	var task protocol.Task
	call("device_task", map[string]any{"action": "start", "working_dir": "project", "command": testshell.Native("Get-Content input.txt | Set-Content report.txt; Write-Output $env:AR_TASK_TEST", "cp input.txt report.txt; printf '%s\\n' \"$AR_TASK_TEST\""), "env": map[string]string{"AR_TASK_TEST": "task-env-ok"}, "timeout_ms": 5000}, &task)
	deadline := time.Now().Add(5 * time.Second)
	for task.FinishedAt == nil && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		call("device_task", map[string]any{"action": "get", "task_id": task.ID}, &task)
	}
	if task.Status != "completed" || task.Result == nil || !strings.Contains(task.Result.Stdout, "task-env-ok") {
		t.Fatalf("task = %+v", task)
	}
	var tasks []protocol.Task
	call("device_task", map[string]any{"action": "list"}, &tasks)
	if len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("tasks = %+v", tasks)
	}
	call("device_task", map[string]any{"action": "retrieve", "task_id": task.ID}, &task)
	if task.Result == nil || task.Status != "completed" {
		t.Fatalf("retrieved = %+v", task)
	}
	if _, err := client.GetTask(ctx, device.ID, task.ID); err == nil {
		t.Fatal("retrieved task was retained")
	}
	call("device_task", map[string]any{"action": "list"}, &tasks)
	if len(tasks) != 0 {
		t.Fatalf("retrieved task still listed: %+v", tasks)
	}
	var files []protocol.ReadResponse
	call("device_pull", map[string]any{"path": "project/report.txt"}, &files)
	if len(files) != 1 {
		t.Fatalf("files = %+v", files)
	}
	data, err := base64.StdEncoding.DecodeString(files[0].ContentBase64)
	if err != nil || strings.TrimSpace(string(data)) != "workflow-payload" {
		t.Fatalf("pulled = %q, error = %v", data, err)
	}
	task = protocol.Task{}
	call("device_task", map[string]any{"action": "start", "command": testshell.Native("Start-Sleep -Seconds 30", "sleep 30")}, &task)
	call("device_task", map[string]any{"action": "cancel", "task_id": task.ID}, &task)
	if task.Status != "cancelled" {
		t.Fatalf("cancel = %+v", task)
	}
	deadline = time.Now().Add(5 * time.Second)
	for task.FinishedAt == nil && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		call("device_task", map[string]any{"action": "get", "task_id": task.ID}, &task)
	}
	if task.FinishedAt == nil {
		t.Fatal("cancelled process was not reaped")
	}
	var execution protocol.ExecResponse
	call("device_exec", map[string]any{"command": testshell.Native("Write-Output '\u4e2d\u6587'", "printf '%s\\n' '中文'")}, &execution)
	if strings.TrimSpace(execution.Stdout) != "\u4e2d\u6587" {
		t.Fatalf("UTF-8 stdout = %q", execution.Stdout)
	}
	call("device_exec", map[string]any{"command": testshell.Native("Start-Sleep -Seconds 30", "sleep 30"), "timeout_ms": 100}, &execution)
	if !execution.TimedOut {
		t.Fatalf("timeout = %+v", execution)
	}
	var session protocol.Session
	call("device_session", map[string]any{"action": "create", "working_dir": "project", "command": testshell.Native("$relayValue = 'preserved'", "relayValue='preserved'"), "env": map[string]string{"AR_SESSION_TEST": "session-env-ok"}}, &session)
	defer func() { _, _ = client.CloseSession(ctx, device.ID, session.ID) }()
	call("device_session", map[string]any{"action": "send", "session_id": session.ID, "input": testshell.Native("Write-Output ($relayValue + ':' + $env:AR_SESSION_TEST); Write-Output '\u4e2d\u6587'; Get-Content input.txt\r\n", "printf '%s\\n' \"$relayValue:$AR_SESSION_TEST\"; printf '%s\\n' '中文'; cat input.txt\n")}, &session)
	deadline = time.Now().Add(5 * time.Second)
	for !strings.Contains(session.Stdout, "workflow-payload") && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		call("device_session", map[string]any{"action": "get", "session_id": session.ID}, &session)
	}
	if !strings.Contains(session.Stdout, "preserved:session-env-ok") || !strings.Contains(session.Stdout, "workflow-payload") || !strings.Contains(session.Stdout, "\u4e2d\u6587") {
		t.Fatalf("session = %+v", session)
	}
	var sessions []protocol.Session
	call("device_session", map[string]any{"action": "list"}, &sessions)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v", sessions)
	}
	call("device_session", map[string]any{"action": "close", "session_id": session.ID}, &session)
	if session.Status != "closed" || session.FinishedAt == nil {
		t.Fatalf("closed = %+v", session)
	}
	relay.Peer.Close()
	testutil.WaitOffline(t, client, device.ID)
	call("device_list", map[string]any{}, &devices)
	if len(devices) != 1 || devices[0]["handle"] == "" || devices[0]["online"] != false {
		t.Fatalf("offline worker missing from catalog: %+v", devices)
	}
}

func TestMCPInvalidToolArguments(t *testing.T) {
	client := controller.NewClient("http://127.0.0.1:1")
	for _, arguments := range []string{
		`{"device_id":"test","command":"echo test","env":{"X":42}}`,
		`{"device_id":"test","command":"echo test","timeout_ms":9223372036854775807}`,
		`{"device_id":"test","command":"echo test","timeout_ms":-1}`,
	} {
		result, err := handle(context.Background(), client, rpcRequest{Method: "tools/call", Params: json.RawMessage(`{"name":"device_exec","arguments":` + arguments + `}`)})
		if err != nil || result.(map[string]any)["isError"] != true {
			t.Fatalf("invalid arguments: result = %+v, error = %v", result, err)
		}
	}
}

func TestMCPPing(t *testing.T) {
	result, err := handle(context.Background(), controller.NewClient("http://127.0.0.1:1"), rpcRequest{Method: "ping"})
	if err != nil || result == nil {
		t.Fatalf("ping result = %#v, error = %v", result, err)
	}
}

func TestMCPToolDefinitionsExposeDeviceCapabilities(t *testing.T) {
	data, err := json.Marshal(toolDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, name := range []string{"device_list", "device_exec", "device_sync", "device_pull", "device_task", "device_session"} {
		if !strings.Contains(text, `"name":"`+name+`"`) {
			t.Fatalf("missing tool %q in %s", name, text)
		}
	}
	if !strings.Contains(text, `"timeout_ms"`) || !strings.Contains(text, `"env"`) {
		t.Fatalf("exec schema does not expose env and timeout_ms: %s", text)
	}
	for _, field := range []string{`"mode"`, `"exclude"`, `"use_gitignore"`, `"dry_run"`} {
		if !strings.Contains(text, field) {
			t.Fatalf("sync schema does not expose %s: %s", field, text)
		}
	}
}

func TestMCPDeviceExecPassesEnvAndTimeout(t *testing.T) {
	relay := testutil.New(t, "")
	client, device := relay.Client, relay.Device
	params, _ := json.Marshal(map[string]any{"name": "device_exec", "arguments": map[string]any{"device_id": device.ID, "command": testshell.Native("Write-Output $env:AR_MCP_TEST", "printf '%s\\n' \"$AR_MCP_TEST\""), "env": map[string]string{"AR_MCP_TEST": "passed"}, "timeout_ms": 5000}})
	result, err := callTool(context.Background(), client, params)
	if err != nil {
		t.Fatal(err)
	}
	text := result.(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "passed") {
		t.Fatalf("device_exec result = %s", text)
	}
}

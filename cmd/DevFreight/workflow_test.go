package main

import (
	"DevFreight/internal/testshell"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"DevFreight/internal/controller"
	"DevFreight/internal/protocol"
	"DevFreight/internal/testutil"
)

func captureCommand(t *testing.T, command func([]string) error, args []string) ([]byte, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = original }()
	output := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(r); _ = r.Close(); output <- data }()
	err = command(args)
	_ = w.Close()
	return <-output, err
}

func cliFixture(t *testing.T) ([]string, *controller.Client, string, string) {
	t.Helper()
	root := t.TempDir()
	relay := testutil.New(t, root)
	return []string{"--controller", relay.HTTP.URL, "--device", relay.Device.ID}, relay.Client, relay.Device.ID, root
}

func TestCLITaskWorkflow(t *testing.T) {
	common, c, device, root := cliFixture(t)
	if err := os.Mkdir(filepath.Join(root, "project"), 0755); err != nil {
		t.Fatal(err)
	}
	call := func(action string, out any, args ...string) {
		t.Helper()
		all := append([]string{action}, common...)
		data, err := captureCommand(t, taskCommand, append(all, args...))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(task *protocol.Task) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for task.FinishedAt == nil && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
			call("get", task, "--id", task.ID)
		}
		if task.FinishedAt == nil {
			t.Fatal("task did not finish")
		}
	}
	var task protocol.Task
	call("start", &task, "--dir", "project", "--env", "AR_CLI_TEST=first", "--env", "AR_CLI_TEST=value=ok", "--timeout-ms", "5000", testshell.Native("Write-Output $env:AR_CLI_TEST; (Get-Location).Path", "printf '%s\\n' \"$AR_CLI_TEST\"; pwd"))
	t.Cleanup(func() { _ = c.CancelTask(context.Background(), device, task.ID) })
	wait(&task)
	if task.Status != "completed" || task.Result == nil || !strings.Contains(task.Result.Stdout, "value=ok") || !testutil.OutputContainsPath(t, task.Result.Stdout, filepath.Join(root, "project")) {
		t.Fatalf("task = %+v", task)
	}
	var tasks []protocol.Task
	call("list", &tasks)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	call("retrieve", &task, "--id", task.ID)
	if task.Result == nil {
		t.Fatal("missing retrieved result")
	}
	if _, err := c.GetTask(context.Background(), device, task.ID); err == nil {
		t.Fatal("retrieved record retained")
	}
	call("list", &tasks)
	if len(tasks) != 0 {
		t.Fatal("retrieved record listed")
	}
	for _, tc := range []struct {
		command   string
		timeout   string
		errorText string
	}{
		{"exit 7", "5000", "command exited with code 7"},
		{testshell.Native("Start-Sleep -Seconds 30", "sleep 30"), "100", "execution timed out"},
	} {
		task = protocol.Task{}
		call("start", &task, "--timeout-ms", tc.timeout, tc.command)
		wait(&task)
		if task.Status != "failed" || task.Error != tc.errorText {
			t.Fatalf("failed task = %+v", task)
		}
		call("retrieve", &task, "--id", task.ID)
	}
	task = protocol.Task{}
	call("start", &task, testshell.Native("Start-Sleep -Seconds 30", "sleep 30"))
	call("cancel", &task, "--id", task.ID)
	if task.Status != "cancelled" {
		t.Fatalf("cancel = %+v", task)
	}
	wait(&task)
	call("retrieve", &task, "--id", task.ID)
}

func TestCLIIncrementalSync(t *testing.T) {
	common, _, _, root := cliFixture(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "input.txt"), []byte("cli-sync"), 0600); err != nil {
		t.Fatal(err)
	}
	args := append(common, "--source", source, "--remote", "project", "--dry-run")
	data, err := captureCommand(t, syncCommand, args)
	if err != nil {
		t.Fatal(err)
	}
	var result protocol.SyncResponse
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Uploaded != 1 {
		t.Fatalf("dry run = %+v", result)
	}
	args = append(common, "--source", source, "--remote", "project", "--mode", "mirror")
	data, err = captureCommand(t, syncCommand, args)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "project", "input.txt"))
	if err != nil || string(content) != "cli-sync" || result.Uploaded != 1 {
		t.Fatalf("sync result = %+v, content = %q, error = %v", result, content, err)
	}
}

func TestCLISessionAndPull(t *testing.T) {
	common, c, device, root := cliFixture(t)
	if err := os.Mkdir(filepath.Join(root, "project"), 0755); err != nil {
		t.Fatal(err)
	}
	call := func(command func([]string) error, args []string, out any) {
		t.Helper()
		data, err := captureCommand(t, command, args)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
	}
	var session protocol.Session
	args := append([]string{"create"}, common...)
	call(sessionCommand, append(args, "--dir", "project", "--env", "AR_CLI_SESSION=env-ok", testshell.Native("$relay = 'retained'", "relay='retained'")), &session)
	t.Cleanup(func() { _, _ = c.CloseSession(context.Background(), device, session.ID) })
	inputFile := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(inputFile, []byte(testshell.Native("Write-Output ($relay + ':' + $env:AR_CLI_SESSION); (Get-Location).Path\r\n", "printf '%s\\n' \"$relay:$AR_CLI_SESSION\"; pwd\n")), 0600); err != nil {
		t.Fatal(err)
	}
	args = append([]string{"send"}, common...)
	call(sessionCommand, append(args, "--id", session.ID, "--input-file", inputFile), &session)
	args = append([]string{"send"}, common...)
	call(sessionCommand, append(args, "--id", session.ID, "--input", testshell.Native("Write-Output 'direct-input'\r\n", "printf '%s\\n' 'direct-input'\n")), &session)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(session.Stdout, "direct-input") && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		args = append([]string{"get"}, common...)
		call(sessionCommand, append(args, "--id", session.ID), &session)
	}
	if !strings.Contains(session.Stdout, "retained:env-ok") || !strings.Contains(session.Stdout, "direct-input") || !testutil.OutputContainsPath(t, session.Stdout, filepath.Join(root, "project")) {
		t.Fatalf("session = %+v", session)
	}
	var sessions []protocol.Session
	call(sessionCommand, append([]string{"list"}, common...), &sessions)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v", sessions)
	}
	args = append([]string{"close"}, common...)
	call(sessionCommand, append(args, "--id", session.ID), &session)
	if session.Status != "closed" || session.FinishedAt == nil {
		t.Fatalf("closed = %+v", session)
	}
	payload := []byte{0, 255, 128, 13, 10, 42}
	if err := os.WriteFile(filepath.Join(root, "project", "report.bin"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	var files []protocol.ReadResponse
	call(pullCommand, append(common, "--path", "project/report.bin"), &files)
	if len(files) != 1 {
		t.Fatalf("files = %+v", files)
	}
	data, err := base64.StdEncoding.DecodeString(files[0].ContentBase64)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("base64 pull = %v, %v", data, err)
	}
	out := t.TempDir()
	var saved []struct {
		LocalPath string `json:"local_path"`
		Bytes     int    `json:"bytes"`
	}
	call(pullCommand, append(common, "--out", out, "project/report.bin"), &saved)
	data, err = os.ReadFile(filepath.Join(out, "project", "report.bin"))
	if err != nil || !bytes.Equal(data, payload) || len(saved) != 1 || saved[0].Bytes != len(payload) {
		t.Fatalf("local pull = %v, %v, %+v", data, err, saved)
	}
}

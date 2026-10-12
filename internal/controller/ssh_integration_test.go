package controller_test

import (
	"AgentRelay/internal/controller"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in real OpenSSH test. Supply an isolated test workspace and key files.
func TestSSHIntegration(t *testing.T) {
	raw := os.Getenv("AGENTRELAY_TEST_SSH")
	if raw == "" {
		t.Skip("set AGENTRELAY_TEST_SSH to DeviceCreate JSON for a real SSH target")
	}
	var in protocol.DeviceCreate
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTRELAY_HOME", t.TempDir())
	s, err := controller.New(controller.Config{StatePath: state.Path("controller")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := httptest.NewUnstartedServer(s.Handler())
	h.TLS, err = s.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	h.StartTLS()
	defer h.Close()
	c, ctx := controller.NewClient(h.URL), context.Background()
	d, err := c.CreateDevice(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.TestConnection(ctx, d.ID, d.ActiveConnection); err != nil {
		t.Fatal(err)
	}
	result, err := c.Exec(ctx, d.Handle, protocol.ExecRequest{Command: "printf '%s' \"$RELAY_TEST\"; printf error >&2; exit 7", Env: map[string]string{"RELAY_TEST": "SSH 'quoted' value"}})
	if err != nil || result.ExitCode != 7 || result.Stdout != "SSH 'quoted' value" || result.Stderr != "error" {
		t.Fatalf("exec: %+v %v", result, err)
	}
	result, err = c.Exec(ctx, d.Handle, protocol.ExecRequest{Command: "sleep 10", TimeoutMS: 100})
	if err != nil || !result.TimedOut {
		t.Fatalf("timeout: %+v %v", result, err)
	}
	_, err = c.Exec(ctx, d.Handle, protocol.ExecRequest{Command: "printf file-result > relay-test.txt"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := c.ReadFile(ctx, d.Handle, "relay-test.txt")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := base64.StdEncoding.DecodeString(file.ContentBase64)
	if string(data) != "file-result" {
		t.Fatalf("read: %q", data)
	}
	if _, err := c.ReadFile(ctx, d.Handle, "../outside"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := c.Exec(ctx, d.Handle, protocol.ExecRequest{WorkingDir: "../", Command: "pwd"}); err == nil {
		t.Fatal("working directory escaped workspace")
	}
	_, err = c.Exec(ctx, d.Handle, protocol.ExecRequest{Command: "ln -sf /etc/passwd relay-link"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadFile(ctx, d.Handle, "relay-link"); err == nil {
		t.Fatal("symlink escaped workspace")
	}
	if _, err := c.StartTask(ctx, d.Handle, protocol.TaskRequest{}); err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("unsupported task: %v", err)
	}
	if _, err := c.Desktop(ctx, d.Handle); err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("unsupported desktop: %v", err)
	}
	_, _ = c.Exec(ctx, d.Handle, protocol.ExecRequest{Command: "rm -f cancel.pid"})
	done := make(chan error, 1)
	go func() {
		_, err := c.Exec(ctx, d.Handle, protocol.ExecRequest{Command: "sleep 60 & echo $! > cancel.pid; wait"})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	started := false
	for time.Now().Before(deadline) {
		if _, err := c.ReadFile(ctx, d.Handle, "cancel.pid"); err == nil {
			started = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !started {
		t.Fatal("cancellation test command did not start")
	}
	no := false
	if _, err := c.UpdateDevice(ctx, d.Handle, controller.DeviceUpdate{Authorized: &no}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revoked command completed successfully")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revocation did not cancel SSH RPC")
	}
	yes := true
	if _, err := c.UpdateDevice(ctx, d.Handle, controller.DeviceUpdate{Authorized: &yes}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.TestConnection(ctx, d.ID, d.ActiveConnection); err != nil {
		t.Fatal(err)
	}
	result, err = c.Exec(ctx, d.Handle, protocol.ExecRequest{Command: "ps -p \"$(cat cancel.pid)\" -o stat= | grep -qv Z"})
	if err != nil || result.ExitCode == 0 {
		t.Fatalf("revoked command's child is still running: %+v %v", result, err)
	}
	restarted, err := controller.New(controller.Config{StatePath: filepath.Join(os.Getenv("AGENTRELAY_HOME"), "controller.json")})
	if err != nil {
		t.Fatal(err)
	}
	restarted.Close()
	// Cleanup only the files created by this test, within its isolated workspace.
	_, _ = c.Exec(ctx, d.Handle, protocol.ExecRequest{Command: "rm -f relay-test.txt relay-link cancel.pid"})
}

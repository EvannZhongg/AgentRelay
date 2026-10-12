//go:build linux || darwin

package worker

import (
	"AgentRelay/internal/protocol"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTimeoutTerminatesDescendants(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{Workspace: root})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	result, err := s.run(context.Background(), protocol.ExecRequest{Command: "sleep 30 & echo $! > child.pid; wait", TimeoutMS: 300})
	if err != nil || !result.TimedOut {
		t.Fatalf("timeout: %+v %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
		// Minimal Linux containers may retain unreaped zombies; they are dead.
		if data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil && strings.Contains(string(data), ") Z ") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("child process survived execution timeout")
}

package worker

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"AgentRelay/internal/protocol"
)

func TestWorkerExecSyncAndPathBoundary(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{Workspace: root, Name: "test-worker"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/exec", "application/json", strings.NewReader(`{"command":"Write-Output AgentRelay-ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exec status = %d", resp.StatusCode)
	}
	var result protocol.ExecResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "AgentRelay-ok") {
		t.Fatalf("unexpected exec result: %+v", result)
	}

	payload := protocol.SyncRequest{Root: "project", Files: []protocol.FileEntry{{Path: "hello.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("hello"))}}}
	data, _ := json.Marshal(payload)
	resp, err = http.Post(server.URL+"/v1/files/sync", "application/json", strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sync status = %d", resp.StatusCode)
	}
	content, err := os.ReadFile(filepath.Join(root, "project", "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello" {
		t.Fatalf("content = %q", content)
	}

	data, _ = json.Marshal(protocol.SyncRequest{Files: []protocol.FileEntry{{Path: "..\\escape.txt", ContentBase64: "aA=="}}})
	resp, err = http.Post(server.URL+"/v1/files/sync", "application/json", strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal status = %d", resp.StatusCode)
	}
}

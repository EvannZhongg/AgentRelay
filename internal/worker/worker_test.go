package worker

import (
	"AgentRelay/internal/testshell"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	execBody, _ := json.Marshal(protocol.ExecRequest{Command: testshell.Native("Write-Output AgentRelay-ok", "printf AgentRelay-ok")})
	resp, err := http.Post(server.URL+"/v1/exec", "application/json", bytes.NewReader(execBody))
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
	if err := os.MkdirAll(filepath.Join(root, "project"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "project", "hello.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}

	postJSON := func(path string, value any, out any) *http.Response {
		t.Helper()
		data, _ := json.Marshal(value)
		response, postErr := http.Post(server.URL+path, "application/json", bytes.NewReader(data))
		if postErr != nil {
			t.Fatal(postErr)
		}
		if out != nil && response.StatusCode < 300 {
			if err := json.NewDecoder(response.Body).Decode(out); err != nil {
				t.Fatal(err)
			}
		}
		response.Body.Close()
		return response
	}
	var started protocol.SyncStartResponse
	resp = postJSON("/v1/files/sync/start", protocol.SyncStartRequest{Root: "project", Mode: protocol.SyncModeUpdate}, &started)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("sync start status = %d", resp.StatusCode)
	}
	content := []byte("hello")
	sum := sha256.Sum256(content)
	entry := protocol.SyncManifestEntry{Path: "hello.txt", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), Mode: 0644}
	resp = postJSON("/v1/files/sync/"+started.ID+"/manifest", protocol.SyncManifestRequest{Entries: []protocol.SyncManifestEntry{entry}, Final: true}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status = %d", resp.StatusCode)
	}
	var plan protocol.SyncPlanResponse
	resp = postJSON("/v1/files/sync/"+started.ID+"/plan", struct{}{}, &plan)
	if resp.StatusCode != http.StatusOK || len(plan.Upload) != 1 {
		t.Fatalf("plan = %+v, status = %d", plan, resp.StatusCode)
	}
	query := url.Values{"path": {"hello.txt"}, "offset": {"0"}, "sha256": {hex.EncodeToString(sum[:])}}
	resp, err = http.Post(server.URL+"/v1/files/sync/"+started.ID+"/file?"+query.Encode(), "application/octet-stream", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d", resp.StatusCode)
	}
	resp, err = http.Post(server.URL+"/v1/files/sync/"+started.ID+"/file?"+query.Encode(), "application/octet-stream", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("idempotent retry status = %d", resp.StatusCode)
	}
	beforeCommit, err := os.ReadFile(filepath.Join(root, "project", "hello.txt"))
	if err != nil || string(beforeCommit) != "old" {
		t.Fatalf("target changed before commit: %q, %v", beforeCommit, err)
	}
	resp = postJSON("/v1/files/sync/"+started.ID+"/commit", struct{}{}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("commit status = %d", resp.StatusCode)
	}
	written, err := os.ReadFile(filepath.Join(root, "project", "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "hello" {
		t.Fatalf("content = %q", written)
	}

	var invalid protocol.SyncStartResponse
	postJSON("/v1/files/sync/start", protocol.SyncStartRequest{Root: "project", Mode: protocol.SyncModeUpdate}, &invalid)
	bad := protocol.SyncManifestEntry{Path: "..\\escape.txt", Size: 1, SHA256: strings.Repeat("0", 64)}
	resp = postJSON("/v1/files/sync/"+invalid.ID+"/manifest", protocol.SyncManifestRequest{Entries: []protocol.SyncManifestEntry{bad}, Final: true}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal status = %d", resp.StatusCode)
	}
}

func TestWorkerSessionLifecycle(t *testing.T) {
	s, err := New(Config{Workspace: t.TempDir(), Name: "session-worker"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	client := &http.Client{}
	post := func(path string, value any) protocol.Session {
		data, _ := json.Marshal(value)
		resp, err := client.Post(server.URL+path, "application/json", strings.NewReader(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		var out protocol.Session
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	session := post("/v1/sessions", protocol.SessionRequest{})
	defer s.sessions.close(session.ID)
	if session.ID == "" || session.Status != "running" {
		t.Fatalf("session = %+v", session)
	}
	input := protocol.SessionInput{Input: testshell.Native("Write-Output AgentRelay-session-ok\r\n", "printf '%s\\n' AgentRelay-session-ok\n")}
	data, _ := json.Marshal(input)
	resp, err := client.Post(server.URL+"/v1/sessions/"+session.ID+"/input", "application/json", strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("input status = %d", resp.StatusCode)
	}
	found := false
	for i := 0; i < 200; i++ {
		resp, err = client.Get(server.URL + "/v1/sessions/" + session.ID)
		if err != nil {
			t.Fatal(err)
		}
		var current protocol.Session
		_ = json.NewDecoder(resp.Body).Decode(&current)
		resp.Body.Close()
		if strings.Contains(current.Stdout, "AgentRelay-session-ok") {
			found = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !found {
		t.Fatal("session output was not returned")
	}
	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/v1/sessions/"+session.ID, nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("close status = %d", resp.StatusCode)
	}
}

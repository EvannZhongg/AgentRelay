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
	input := protocol.SessionInput{Input: "Write-Output AgentRelay-session-ok\r\n"}
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

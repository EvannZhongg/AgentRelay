package controller

import (
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"
	"AgentRelay/internal/workspace"
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMissingDeviceHandlesAreRejected(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "controller.json")
	initial := registry{Schema: 4, Devices: map[string]record{
		"private-id": {Device: protocol.Device{ID: "private-id", Name: "laptop", Workspace: t.TempDir()}},
	}}
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{StatePath: filename}); err == nil {
		t.Fatal("missing handle accepted")
	}
	var saved registry
	if err := state.Load(filename, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Devices["private-id"].Device.Handle != "" {
		t.Fatal("invalid state was migrated")
	}
}

func TestExpiredPairingCannotCreateDevice(t *testing.T) {
	s, err := New(Config{StatePath: filepath.Join(t.TempDir(), "controller.json")})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(protocol.PairRequest{Device: protocol.Device{Name: "expired", Workspace: "workspace"}})
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, httptest.NewRequest("POST", "/v1/pair", bytes.NewReader(data)))
	if out.Code != 202 {
		t.Fatalf("pair request: status=%d", out.Code)
	}
	var request protocol.PairingRequest
	if err := json.Unmarshal(out.Body.Bytes(), &request); err != nil {
		t.Fatal(err)
	}
	s.pairings[request.ID].request.ExpiresAt = time.Now().Add(-time.Second)
	status := httptest.NewRecorder()
	s.Handler().ServeHTTP(status, httptest.NewRequest("GET", "/v1/pair/"+request.ID, nil))
	if status.Code != 404 || len(s.store.Devices) != 0 {
		t.Fatalf("expired pairing: status=%d devices=%d", status.Code, len(s.store.Devices))
	}
}

func TestPairingNeedsControllerApproval(t *testing.T) {
	s, err := New(Config{StatePath: filepath.Join(t.TempDir(), "controller.json")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	makeRequest := func(name string) protocol.PairingRequest {
		data, _ := json.Marshal(protocol.PairRequest{Device: protocol.Device{Name: name, Workspace: t.TempDir()}})
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, httptest.NewRequest("POST", "/v1/pair", bytes.NewReader(data)))
		if out.Code != 202 {
			t.Fatalf("request status: %d", out.Code)
		}
		var request protocol.PairingRequest
		if err := json.Unmarshal(out.Body.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		return request
	}
	first := makeRequest("first")
	if first.Verification == "" {
		t.Fatal("verification number missing")
	}
	if len(s.store.Devices) != 0 {
		t.Fatal("pending request created a device")
	}
	second := makeRequest("second")
	if first.Verification == second.Verification {
		t.Fatal("verification number was reused")
	}
	approve := func(id, action string) protocol.PairingStatus {
		out := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/pair-requests/"+id+"/"+action, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		s.Handler().ServeHTTP(out, req)
		if out.Code != 200 {
			t.Fatalf("%s status: %d %s", action, out.Code, out.Body.String())
		}
		var status protocol.PairingStatus
		if err := json.Unmarshal(out.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	if status := approve(first.ID, "reject"); status.Status != "rejected" || status.Credentials != nil {
		t.Fatalf("rejected request: %+v", status)
	}
	if status := approve(second.ID, "approve"); status.Status != "approved" || status.Credentials == nil {
		t.Fatalf("approved request: %+v", status)
	}
	if len(s.store.Devices) != 1 {
		t.Fatalf("devices after approval: %d", len(s.store.Devices))
	}
}
func TestCorruptStateIsReported(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "controller.json")
	if err := os.WriteFile(filename, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{StatePath: filename}); err == nil {
		t.Fatal("corrupt state silently overwritten")
	}
	data, err := os.ReadFile(filename)
	if err != nil || string(data) != "{" {
		t.Fatalf("corrupt state modified: %q, %v", data, err)
	}
}
func TestMappingValidationAndLongestMatch(t *testing.T) {
	local := t.TempDir()
	child := filepath.Join(local, "sub")
	mappings := map[string]string{local: "projects/parent", child: "projects/child"}
	if mapped, ok := workspace.Mapped(mappings, filepath.Join(child, "tests")); !ok || mapped != "projects/child/tests" {
		t.Fatalf("mapped=%q found=%v", mapped, ok)
	}
	if _, ok := workspace.Mapped(mappings, local+"-other"); ok {
		t.Fatal("matched unrelated path prefix")
	}
	for _, remote := range []string{"../outside", "sub/../../outside", "C:\\outside", "/outside", "\\\\server\\share"} {
		if workspace.Relative(remote) {
			t.Fatalf("unsafe remote mapping accepted: %q", remote)
		}
	}
}

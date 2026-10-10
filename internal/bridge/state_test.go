package bridge

import (
	"AgentRelay/internal/protocol"
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExpiredPairingCannotCreateDevice(t *testing.T) {
	s, err := New(Config{StatePath: filepath.Join(t.TempDir(), "bridge.json")})
	if err != nil {
		t.Fatal(err)
	}
	code := s.NewPairing().Code
	s.pairings[code] = time.Now().Add(-time.Second)
	data, _ := json.Marshal(protocol.PairRequest{Code: code, Device: protocol.Device{Name: "expired", Workspace: "workspace"}})
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, httptest.NewRequest("POST", "/v1/pair", bytes.NewReader(data)))
	if out.Code != 403 || len(s.store.Devices) != 0 {
		t.Fatalf("expired pairing: status=%d devices=%d", out.Code, len(s.store.Devices))
	}
}
func TestCorruptStateIsReported(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "bridge.json")
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
	if mapped, ok := mappedDir(mappings, filepath.Join(child, "tests")); !ok || mapped != "projects/child/tests" {
		t.Fatalf("mapped=%q found=%v", mapped, ok)
	}
	if _, ok := mappedDir(mappings, local+"-other"); ok {
		t.Fatal("matched unrelated path prefix")
	}
	for _, remote := range []string{"../outside", "sub/../../outside", "C:\\outside", "/outside", "\\\\server\\share"} {
		if validRemote(remote) {
			t.Fatalf("unsafe remote mapping accepted: %q", remote)
		}
	}
}

package controller

import (
	"encoding/json"
	"testing"

	"DevFreight/internal/protocol"
)

func TestResolveWorkingDirRejectsNonObjectBodies(t *testing.T) {
	policy := protocol.WorkspacePolicy{Root: "/srv/repo", Mode: protocol.WorkspaceRemote}
	for _, body := range []string{"", "null", "[]", `"value"`, "3"} {
		if _, err := resolveWorkingDir(policy, []byte(body)); err == nil {
			t.Fatalf("body %q was accepted", body)
		}
	}
}

func TestResolveWorkingDirRewritesMappedDirectory(t *testing.T) {
	policy := protocol.WorkspacePolicy{Root: "/srv/repo", Mode: protocol.WorkspaceRemote}
	out, err := resolveWorkingDir(policy, []byte(`{"command":"pwd","working_dir":"/srv/repo/src","working_dir_kind":"remote","timeout_ms":1000}`))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["working_dir"] != "src" {
		t.Fatalf("working_dir = %v", fields["working_dir"])
	}
	if _, ok := fields["working_dir_kind"]; ok {
		t.Fatal("working_dir_kind was forwarded to the target")
	}
	if fields["command"] != "pwd" || fields["timeout_ms"] != float64(1000) {
		t.Fatalf("fields were not preserved: %v", fields)
	}
	if _, err := resolveWorkingDir(policy, []byte(`{"working_dir":"/etc","working_dir_kind":"remote"}`)); err == nil {
		t.Fatal("directory outside the workspace was accepted")
	}
}

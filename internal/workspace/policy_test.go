package workspace

import (
	"DevFreight/internal/protocol"
	"path/filepath"
	"testing"
)

func TestRemotePathsUseTargetRulesOnEveryController(t *testing.T) {
	for _, tc := range []struct{ root, dir, want string }{
		{"/srv/repo", "/srv/repo/src", "src"},
		{"/srv/repo", "src", "src"},
		{"/srv/repo", "", ""},
		{"/", "/srv/repo", "srv/repo"},
		{`C:\repo`, `c:\REPO\src`, "src"},
		{`C:\`, `C:\repo`, "repo"},
		{`\\server\share\repo`, `\\server\share\repo\src`, "src"},
	} {
		p := protocol.WorkspacePolicy{Root: tc.root, Mode: protocol.WorkspaceRemote}
		if err := Validate(p); err != nil {
			t.Fatal(err)
		}
		got, err := Resolve(p, tc.dir, "auto")
		if err != nil || got != tc.want {
			t.Fatalf("%q / %q: got %q, %v", tc.root, tc.dir, got, err)
		}
	}
	for _, dir := range []string{"/srv/repo-other/src", "../outside", "sub/../../outside", `C:\other`, "sub\x00bad"} {
		if _, err := Resolve(protocol.WorkspacePolicy{Root: "/srv/repo", Mode: protocol.WorkspaceRemote}, dir, "auto"); err == nil {
			t.Fatalf("accepted %q", dir)
		}
	}
}

func TestLocalAuthorityIsExplicitAndSyncIsOptional(t *testing.T) {
	local := t.TempDir()
	p := protocol.WorkspacePolicy{Root: "/remote", Mode: protocol.WorkspaceLocalSync, Mappings: map[string]string{local: "project", filepath.Join(local, "sub"): "other"}}
	got, err := Resolve(p, filepath.Join(local, "sub", "tests"), "auto")
	if err != nil || got != "other/tests" {
		t.Fatalf("mapped: %q %v", got, err)
	}
	if got, err := Resolve(p, "existing-repo", "auto"); err != nil || got != "existing-repo" {
		t.Fatalf("unsynced execution: %q %v", got, err)
	}
	if _, err := Resolve(p, t.TempDir(), "local"); err == nil {
		t.Fatal("unmapped local path accepted")
	}
	if got, err := Resolve(p, "/remote/existing-repo", "remote"); err != nil || got != "existing-repo" {
		t.Fatalf("explicit remote: %q %v", got, err)
	}
	p.Mode = protocol.WorkspaceRemote
	p.Mappings = nil
	if _, err := Resolve(p, local, "local"); err == nil {
		t.Fatal("remote source used local mapping")
	}
	if _, err := Resolve(p, "", "invalid"); err == nil {
		t.Fatal("invalid directory kind accepted")
	}
}

func TestPolicyRejectsSourceAmbiguity(t *testing.T) {
	for _, p := range []protocol.WorkspacePolicy{
		{Root: "/repo", Mode: ""},
		{Root: "relative", Mode: protocol.WorkspaceRemote},
		{Root: "/repo", Mode: protocol.WorkspaceRemote, Mappings: map[string]string{t.TempDir(): "source"}},
		{Root: "/repo", Mode: protocol.WorkspaceLocalSync, Mappings: map[string]string{t.TempDir(): "../source"}},
	} {
		if err := Validate(p); err == nil {
			t.Fatalf("accepted policy %+v", p)
		}
	}
}

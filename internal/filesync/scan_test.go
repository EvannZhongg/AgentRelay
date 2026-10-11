package filesync

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestScanAppliesNestedGitIgnoreAndAgentRelayIgnore(t *testing.T) {
	t.Setenv("AGENTRELAY_HOME", t.TempDir())
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "# comment\n*.tmp\n!keep.tmp\nbuild/\n")
	write(".agentrelayignore", "*.log\ncustom.bin\n")
	write("a.txt", "a")
	write("# comment", "included")
	write("a.tmp", "ignored")
	write("keep.tmp", "included")
	write("build/output.txt", "ignored")
	write("nested/.gitignore", "/secret.txt\n")
	write("nested/secret.txt", "ignored")
	write("nested/keep.txt", "included")
	write("trace.log", "ignored")
	write("custom.bin", "ignored")
	write(".git/config", "ignored")

	result, err := Scan(Options{SourcePath: root, UseGitIgnore: true})
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		paths = append(paths, entry.Path)
	}
	want := []string{"# comment", ".agentrelayignore", ".gitignore", "a.txt", "keep.tmp", "nested/.gitignore", "nested/keep.txt"}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	if result.Ignored < 5 {
		t.Fatalf("ignored = %d", result.Ignored)
	}
}

func TestScanRejectsSymbolicLinks(t *testing.T) {
	t.Setenv("AGENTRELAY_HOME", t.TempDir())
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if _, err := Scan(Options{SourcePath: root}); err == nil {
		t.Fatal("symbolic link was accepted")
	}
}

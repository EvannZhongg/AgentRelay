package filesync

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"AgentRelay/internal/state"
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

func TestScanSkipsSymbolicLinks(t *testing.T) {
	t.Setenv("AGENTRELAY_HOME", t.TempDir())
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	result, err := Scan(Options{SourcePath: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 0 {
		t.Fatalf("entries = %+v", result.Entries)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "skipped symbolic link link.txt") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

func TestScanRecoversFromInvalidCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTRELAY_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "sync-cache.json"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Scan(Options{SourcePath: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "ignored invalid sync hash cache") {
		t.Fatalf("scan result = %+v", result)
	}
	var cache scanCache
	if err := state.Load(filepath.Join(home, "sync-cache.json"), &cache); err != nil {
		t.Fatalf("cache was not rebuilt: %v", err)
	}
}

func TestScanContinuesWhenCacheCannotBeSaved(t *testing.T) {
	homeFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(homeFile, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTRELAY_HOME", homeFile)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Scan(Options{SourcePath: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || len(result.Warnings) < 1 || !strings.Contains(result.Warnings[len(result.Warnings)-1], "could not save sync hash cache") {
		t.Fatalf("scan result = %+v", result)
	}
}

package pathutil

import "testing"

func TestResolveUnderRootRejectsEscapesAndAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"..\\outside.txt", "C:\\outside.txt", "/outside.txt"} {
		if _, err := ResolveUnderRoot(root, path); err == nil {
			t.Fatalf("expected %q to be rejected", path)
		}
	}
	inside, err := ResolveUnderRoot(root, "project\\file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if inside == "" {
		t.Fatal("expected resolved path")
	}
}

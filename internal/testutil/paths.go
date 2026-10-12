// Package testutil supplies helpers shared by the controller-side test suites.
package testutil

import (
	"path/filepath"
	"strings"
	"testing"
)

// TargetPathSpellings returns the ways a target may name the same directory.
// The filesystem can report a path differently from the caller: Windows
// expands an 8.3 temporary directory such as RUNNER~1 to its long name, and
// macOS reports the /private prefix of a /var path. Native commands print what
// the OS resolves, so assertions accept every spelling.
func TargetPathSpellings(t *testing.T, path string) []string {
	t.Helper()
	spellings := []string{path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
		spellings = append(spellings, resolved)
	}
	return spellings
}

// OutputContainsPath reports whether command output names the directory in any
// spelling the target may use.
func OutputContainsPath(t *testing.T, output, path string) bool {
	t.Helper()
	for _, spelling := range TargetPathSpellings(t, path) {
		if strings.Contains(output, spelling) {
			return true
		}
	}
	return false
}

// OutputIsPath reports whether command output is exactly the directory in any
// spelling the target may use.
func OutputIsPath(t *testing.T, output, path string) bool {
	t.Helper()
	trimmed := strings.TrimSpace(output)
	for _, spelling := range TargetPathSpellings(t, path) {
		if trimmed == spelling {
			return true
		}
	}
	return false
}

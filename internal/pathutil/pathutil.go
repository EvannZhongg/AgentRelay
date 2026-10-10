package pathutil

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResolveUnderRoot prevents a remote path from escaping the configured worker workspace.
func ResolveUnderRoot(root, requested string) (string, error) {
	if strings.TrimSpace(requested) == "" {
		return filepath.Clean(root), nil
	}
	// Remote paths use slash semantics so a Windows client can address a Windows worker
	// consistently even when the request was assembled on another host.
	requested = strings.ReplaceAll(requested, "\\", string(filepath.Separator))
	if filepath.IsAbs(requested) || strings.HasPrefix(requested, "/") || strings.HasPrefix(requested, string(filepath.Separator)) || filepath.VolumeName(requested) != "" || (len(requested) >= 2 && requested[1] == ':') {
		return "", fmt.Errorf("path %q must be workspace-relative", requested)
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cleanPath := filepath.Clean(filepath.Join(cleanRoot, requested))
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside workspace", requested)
	}
	return cleanPath, nil
}

// Package workspace resolves paths according to the selected connection's
// source authority. It never requires a sync to execute in a remote workspace.
package workspace

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"AgentRelay/internal/protocol"
)

func Relative(value string) bool {
	value = strings.ReplaceAll(value, "\\", "/")
	if strings.HasPrefix(value, "/") || strings.ContainsAny(value, ":\x00") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func Validate(p protocol.WorkspacePolicy) error {
	if p.Mode != protocol.WorkspaceLocalSync && p.Mode != protocol.WorkspaceRemote {
		return errors.New("workspace mode must be local_sync or remote")
	}
	root := strings.ReplaceAll(p.Root, "\\", "/")
	if strings.ContainsRune(root, '\x00') || !(strings.HasPrefix(root, "/") || (len(root) >= 3 && root[1:3] == ":/")) {
		return errors.New("workspace root must be an absolute target path")
	}
	if p.Mode == protocol.WorkspaceRemote && len(p.Mappings) != 0 {
		return errors.New("remote workspace cannot have controller source mappings")
	}
	for local, remote := range p.Mappings {
		if !filepath.IsAbs(local) || !Relative(remote) {
			return errors.New("mapping needs absolute controller path and workspace-relative target path")
		}
	}
	return nil
}

func Mapped(mappings map[string]string, dir string) (string, bool) {
	if !filepath.IsAbs(dir) {
		return "", false
	}
	best, result := "", ""
	for local, remote := range mappings {
		rel, err := filepath.Rel(local, dir)
		if err == nil && Relative(rel) && len(local) > len(best) {
			best, result = local, path.Join(strings.ReplaceAll(remote, "\\", "/"), filepath.ToSlash(rel))
		}
	}
	return result, best != ""
}

// Resolve returns a target-relative directory. "auto" maps controller paths
// only in local_sync mode; remote mode interprets every path on the target.
func Resolve(p protocol.WorkspacePolicy, dir, kind string) (string, error) {
	if kind == "" {
		kind = "auto"
	}
	if kind != "auto" && kind != "local" && kind != "remote" {
		return "", errors.New("working_dir_kind must be auto, local or remote")
	}
	if kind == "local" || (kind == "auto" && p.Mode == protocol.WorkspaceLocalSync && filepath.IsAbs(dir)) {
		if p.Mode != protocol.WorkspaceLocalSync {
			return "", errors.New("local directory requires a local_sync workspace")
		}
		if mapped, ok := Mapped(p.Mappings, dir); ok {
			return mapped, nil
		}
		return "", errors.New("controller directory has no sync mapping; synchronize it or use an explicit remote directory")
	}
	dir = strings.ReplaceAll(dir, "\\", "/")
	root := path.Clean(strings.ReplaceAll(p.Root, "\\", "/"))
	if strings.ContainsRune(dir, '\x00') {
		return "", errors.New("invalid directory")
	}
	if strings.HasPrefix(dir, "/") || (len(dir) >= 2 && dir[1] == ':') {
		clean := path.Clean(dir)
		compareRoot, compareDir := path.Clean(root), clean
		if strings.Contains(root, ":") || strings.HasPrefix(root, "//") {
			compareRoot, compareDir = strings.ToLower(compareRoot), strings.ToLower(compareDir)
		}
		if compareRoot == compareDir {
			return "", nil
		}
		prefix := strings.TrimRight(compareRoot, "/") + "/"
		if !strings.HasPrefix(compareDir, prefix) {
			return "", fmt.Errorf("directory %q is outside the target workspace", dir)
		}
		dir = clean[len(prefix):]
	}
	if !Relative(dir) {
		return "", errors.New("directory must remain inside the target workspace")
	}
	if dir == "" {
		return "", nil
	}
	return path.Clean(dir), nil
}

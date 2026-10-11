package filesync

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"

	gitignore "github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

type Options struct {
	SourcePath   string
	Root         string
	Mode         string
	UseGitIgnore bool
	Exclude      []string
	DryRun       bool
}

type ScanResult struct {
	Root           string
	Entries        []protocol.SyncManifestEntry
	IgnorePatterns []protocol.SyncIgnorePattern
	Ignored        int
	Warnings       []string
}

type cacheEntry struct {
	Size      int64  `json:"size"`
	ModTimeNS int64  `json:"mod_time_ns"`
	SHA256    string `json:"sha256"`
}

type scanCache struct {
	Roots map[string]map[string]cacheEntry `json:"roots"`
}

var cacheMu sync.Mutex

func Scan(options Options) (ScanResult, error) {
	root, err := filepath.Abs(options.SourcePath)
	if err != nil {
		return ScanResult{}, err
	}
	linkInfo, err := os.Lstat(root)
	if err != nil {
		return ScanResult{}, err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return ScanResult{}, errors.New("source_path must not be a symbolic link")
	}
	info, err := os.Stat(root)
	if err != nil {
		return ScanResult{}, err
	}
	if !info.IsDir() {
		return ScanResult{}, errors.New("source_path must be a directory")
	}
	patterns, rules, err := loadPatterns(root, options)
	if err != nil {
		return ScanResult{}, err
	}
	matcher := gitignore.NewMatcher(patterns)
	cacheMu.Lock()
	defer cacheMu.Unlock()
	result := ScanResult{Root: root, IgnorePatterns: rules}
	cache := scanCache{Roots: map[string]map[string]cacheEntry{}}
	if err := state.Load(state.Path("sync-cache"), &cache); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("ignored invalid sync hash cache: %v", err))
		cache = scanCache{Roots: map[string]map[string]cacheEntry{}}
	}
	if cache.Roots == nil {
		cache.Roots = map[string]map[string]cacheEntry{}
	}
	old := cache.Roots[root]
	next := map[string]cacheEntry{}
	err = filepath.WalkDir(root, func(localPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if localPath == root {
			return nil
		}
		rel, err := filepath.Rel(root, localPath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		parts := strings.Split(rel, "/")
		if hardIgnored(parts) || matcher.Match(parts, entry.IsDir()) {
			result.Ignored++
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipped symbolic link %s", rel))
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if !fileInfo.Mode().IsRegular() {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipped non-regular file %s", rel))
			return nil
		}
		cached, ok := old[rel]
		var sum string
		if ok && cached.Size == fileInfo.Size() && cached.ModTimeNS == fileInfo.ModTime().UnixNano() {
			sum = cached.SHA256
		} else {
			sum, err = hashFile(localPath)
			if err != nil {
				return err
			}
		}
		next[rel] = cacheEntry{Size: fileInfo.Size(), ModTimeNS: fileInfo.ModTime().UnixNano(), SHA256: sum}
		result.Entries = append(result.Entries, protocol.SyncManifestEntry{Path: rel, Size: fileInfo.Size(), SHA256: sum, Mode: uint32(fileInfo.Mode().Perm())})
		return nil
	})
	if err != nil {
		return ScanResult{}, err
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Path < result.Entries[j].Path })
	cache.Roots[root] = next
	if err := state.Save(state.Path("sync-cache"), cache); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not save sync hash cache: %v", err))
	}
	return result, nil
}

func loadPatterns(root string, options Options) ([]gitignore.Pattern, []protocol.SyncIgnorePattern, error) {
	patterns := make([]gitignore.Pattern, 0)
	rules := make([]protocol.SyncIgnorePattern, 0)
	if options.UseGitIgnore {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path != root {
					rel, _ := filepath.Rel(root, path)
					if hardIgnored(strings.Split(filepath.ToSlash(rel), "/")) {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if entry.Name() != ".gitignore" || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			dir, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			domain := []string{}
			if dir != "." {
				domain = strings.Split(filepath.ToSlash(dir), "/")
			}
			parsed, parsedRules, err := parsePatternFile(path, domain)
			if err != nil {
				return err
			}
			patterns = append(patterns, parsed...)
			rules = append(rules, parsedRules...)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	path := filepath.Join(root, ".agentrelayignore")
	{
		info, statErr := os.Lstat(path)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, nil, statErr
		}
		if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
			parsed, parsedRules, err := parsePatternFile(path, nil)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, nil, err
			}
			patterns = append(patterns, parsed...)
			rules = append(rules, parsedRules...)
		}
	}
	for _, pattern := range options.Exclude {
		patterns = append(patterns, gitignore.ParsePattern(pattern, nil))
		rules = append(rules, protocol.SyncIgnorePattern{Pattern: pattern})
	}
	return patterns, rules, nil
}

func parsePatternFile(path string, domain []string) ([]gitignore.Pattern, []protocol.SyncIgnorePattern, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	patterns := make([]gitignore.Pattern, 0, len(lines))
	rules := make([]protocol.SyncIgnorePattern, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, gitignore.ParsePattern(line, domain))
		rules = append(rules, protocol.SyncIgnorePattern{Pattern: line, Domain: append([]string(nil), domain...)})
	}
	return patterns, rules, nil
}

func hardIgnored(parts []string) bool {
	for _, part := range parts {
		if part == ".git" || part == ".agentrelay" {
			return true
		}
	}
	return false
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

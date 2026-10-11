package worker

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"AgentRelay/internal/pathutil"
	"AgentRelay/internal/protocol"

	gitignore "github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

const (
	syncChunkSize = 4 << 20
	syncTTL       = 30 * time.Minute
)

type managedEntry struct {
	Size      int64  `json:"size"`
	ModTimeNS int64  `json:"mod_time_ns"`
	SHA256    string `json:"sha256"`
	Mode      uint32 `json:"mode,omitempty"`
}

type managedManifest struct {
	Root    string                  `json:"root"`
	Entries map[string]managedEntry `json:"entries"`
}

type syncState struct {
	mu       sync.Mutex
	id       string
	root     string
	rootPath string
	mode     string
	stage    string
	expires  atomic.Int64
	manifest map[string]protocol.SyncManifestEntry
	ignore   []protocol.SyncIgnorePattern
	matcher  gitignore.Matcher
	final    bool
	planned  bool
	upload   map[string]bool
	deletes  []string
	plan     protocol.SyncPlanResponse
}

type syncStore struct {
	mu        sync.Mutex
	workspace string
	values    map[string]*syncState
}

func newSyncStore(workspace string) *syncStore {
	_ = os.RemoveAll(filepath.Join(workspace, ".agentrelay", "staging"))
	return &syncStore{workspace: workspace, values: map[string]*syncState{}}
}

func (s *Server) syncStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req protocol.SyncStartRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Mode != protocol.SyncModeUpdate && req.Mode != protocol.SyncModeMirror {
		writeError(w, http.StatusBadRequest, errors.New("sync mode must be update or mirror"))
		return
	}
	if reservedSyncRoot(req.Root) {
		writeError(w, http.StatusBadRequest, errors.New("remote root uses the reserved .agentrelay directory"))
		return
	}
	root, err := resolveWithoutSymlinks(s.cfg.Workspace, req.Root)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id, err := randomSyncID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	expires := time.Now().Add(syncTTL)
	stage := filepath.Join(s.cfg.Workspace, ".agentrelay", "staging", id)
	if err := os.MkdirAll(stage, 0700); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	session := &syncState{id: id, root: req.Root, rootPath: root, mode: req.Mode, stage: stage, manifest: map[string]protocol.SyncManifestEntry{}, upload: map[string]bool{}}
	session.expires.Store(expires.UnixNano())
	s.syncs.mu.Lock()
	expired := s.syncs.cleanupExpiredLocked()
	for _, active := range s.syncs.values {
		if active.root == req.Root {
			s.syncs.mu.Unlock()
			removeSyncStages(expired)
			_ = os.RemoveAll(stage)
			writeError(w, http.StatusConflict, errors.New("another sync is already active for this root"))
			return
		}
	}
	s.syncs.values[id] = session
	s.syncs.mu.Unlock()
	removeSyncStages(expired)
	writeJSON(w, http.StatusCreated, protocol.SyncStartResponse{ID: id, Root: req.Root, ChunkSizeBytes: syncChunkSize, ExpiresAt: expires})
}

func (s *Server) syncSession(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/files/sync/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, errors.New("sync id is required"))
		return
	}
	session := s.syncs.acquire(parts[0])
	if session == nil {
		writeError(w, http.StatusNotFound, errors.New("sync session not found or expired"))
		return
	}
	defer func() {
		session.expires.Store(time.Now().Add(syncTTL).UnixNano())
		session.mu.Unlock()
	}()
	if len(parts) == 1 && r.Method == http.MethodDelete {
		s.syncs.remove(session)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	switch parts[1] {
	case "manifest":
		s.receiveManifest(w, r, session)
	case "plan":
		s.planSync(w, session)
	case "file":
		s.receiveSyncChunk(w, r, session)
	case "commit":
		s.commitSync(w, session)
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown sync operation"))
	}
}

func (s *Server) receiveManifest(w http.ResponseWriter, r *http.Request, session *syncState) {
	if session.final || session.planned {
		writeError(w, http.StatusConflict, errors.New("manifest is already complete"))
		return
	}
	var req protocol.SyncManifestRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.validateSyncRoot(session); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	casePaths := map[string]string{}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		for path := range session.manifest {
			casePaths[strings.ToLower(path)] = path
		}
	}
	for _, entry := range req.Entries {
		entry.Path = strings.ReplaceAll(entry.Path, "\\", "/")
		if entry.Path == "" || entry.Path == "." || path.Clean(entry.Path) != entry.Path || entry.Size < 0 || len(entry.SHA256) != 64 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid manifest entry %q", entry.Path))
			return
		}
		if _, err := hex.DecodeString(entry.SHA256); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid sha256 for %s", entry.Path))
			return
		}
		if _, err := resolveWithoutSymlinks(session.rootPath, entry.Path); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if _, exists := session.manifest[entry.Path]; exists {
			writeError(w, http.StatusBadRequest, fmt.Errorf("duplicate manifest path %q", entry.Path))
			return
		}
		for existing := range session.manifest {
			if strings.HasPrefix(existing, entry.Path+"/") || strings.HasPrefix(entry.Path, existing+"/") {
				writeError(w, http.StatusBadRequest, fmt.Errorf("manifest paths %q and %q conflict as file and directory", existing, entry.Path))
				return
			}
		}
		if len(casePaths) > 0 {
			key := strings.ToLower(entry.Path)
			if other, exists := casePaths[key]; exists && other != entry.Path {
				writeError(w, http.StatusBadRequest, fmt.Errorf("case-conflicting paths %q and %q", other, entry.Path))
				return
			}
			casePaths[key] = entry.Path
		}
		session.manifest[entry.Path] = entry
	}
	for _, rule := range req.Ignore {
		if len(rule.Pattern) > 8192 {
			writeError(w, http.StatusBadRequest, errors.New("ignore pattern exceeds 8192 bytes"))
			return
		}
		for _, part := range rule.Domain {
			if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "/\\") {
				writeError(w, http.StatusBadRequest, errors.New("invalid ignore pattern domain"))
				return
			}
		}
		session.ignore = append(session.ignore, rule)
	}
	session.final = req.Final
	if session.final {
		patterns := make([]gitignore.Pattern, 0, len(session.ignore))
		for _, rule := range session.ignore {
			patterns = append(patterns, gitignore.ParsePattern(rule.Pattern, rule.Domain))
		}
		session.matcher = gitignore.NewMatcher(patterns)
	}
	writeJSON(w, http.StatusOK, protocol.SyncManifestResponse{Received: len(session.manifest)})
}

func (s *Server) planSync(w http.ResponseWriter, session *syncState) {
	if !session.final {
		writeError(w, http.StatusConflict, errors.New("manifest is not complete"))
		return
	}
	if session.planned {
		writeJSON(w, http.StatusOK, session.plan)
		return
	}
	if err := s.validateSyncRoot(session); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	managed, err := s.syncs.loadManaged(session.root)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	paths := make([]string, 0, len(session.manifest))
	for path := range session.manifest {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	plan := protocol.SyncPlanResponse{}
	for _, relative := range paths {
		entry := session.manifest[relative]
		destination, err := resolveWithoutSymlinks(session.rootPath, relative)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		info, statErr := os.Lstat(destination)
		unchanged := false
		if statErr == nil && info.Mode().IsRegular() && info.Size() == entry.Size {
			known, ok := managed.Entries[relative]
			remoteHash := ""
			if ok && known.Size == info.Size() && known.ModTimeNS == info.ModTime().UnixNano() {
				remoteHash = known.SHA256
			} else {
				remoteHash, err = hashPath(destination)
				if err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
			}
			modeMatches := runtime.GOOS == "windows" || entry.Mode == 0 || uint32(info.Mode().Perm()) == entry.Mode
			unchanged = remoteHash == entry.SHA256 && modeMatches
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, statErr)
			return
		}
		if unchanged {
			plan.Unchanged++
			plan.UnchangedBytes += entry.Size
		} else {
			plan.Upload = append(plan.Upload, relative)
			session.upload[relative] = true
		}
	}
	if session.mode == protocol.SyncModeMirror {
		for relative := range managed.Entries {
			if _, exists := session.manifest[relative]; !exists && !isIgnored(relative, session.matcher) {
				plan.Delete = append(plan.Delete, relative)
			}
		}
		sort.Strings(plan.Delete)
	}
	session.deletes = append([]string(nil), plan.Delete...)
	session.plan = plan
	session.planned = true
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) receiveSyncChunk(w http.ResponseWriter, r *http.Request, session *syncState) {
	if !session.planned {
		writeError(w, http.StatusConflict, errors.New("sync has not been planned"))
		return
	}
	relative := strings.ReplaceAll(r.URL.Query().Get("path"), "\\", "/")
	entry, exists := session.manifest[relative]
	if !exists || !session.upload[relative] {
		writeError(w, http.StatusBadRequest, errors.New("path is not in the upload plan"))
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid chunk offset"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, syncChunkSize+1))
	if err != nil || len(data) > syncChunkSize {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("sync chunk exceeds 4 MiB"))
		return
	}
	chunkSum := sha256.Sum256(data)
	if hex.EncodeToString(chunkSum[:]) != r.URL.Query().Get("sha256") {
		writeError(w, http.StatusBadRequest, errors.New("sync chunk checksum mismatch"))
		return
	}
	stagePath, err := pathutil.ResolveUnderRoot(filepath.Join(session.stage, "files"), relative)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(stagePath), 0700); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	current := int64(0)
	if info, err := os.Stat(stagePath); err == nil {
		current = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current == offset+int64(len(data)) && current > offset {
		existing := make([]byte, len(data))
		f, readErr := os.Open(stagePath)
		if readErr == nil {
			_, readErr = f.ReadAt(existing, offset)
			_ = f.Close()
		}
		existingSum := sha256.Sum256(existing)
		if readErr == nil && existingSum == chunkSum {
			writeJSON(w, http.StatusOK, map[string]any{"received": current, "complete": current == entry.Size})
			return
		}
	}
	if current != offset || current+int64(len(data)) > entry.Size {
		writeError(w, http.StatusConflict, fmt.Errorf("unexpected chunk offset for %s: got %d, want %d", relative, offset, current))
		return
	}
	f, err := os.OpenFile(stagePath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	_, err = f.Seek(offset, io.SeekStart)
	if err == nil {
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": current + int64(len(data)), "complete": current+int64(len(data)) == entry.Size})
}

type appliedFile struct {
	destination string
	backup      string
	hadOriginal bool
}

func (s *Server) commitSync(w http.ResponseWriter, session *syncState) {
	if !session.planned {
		writeError(w, http.StatusConflict, errors.New("sync has not been planned"))
		return
	}
	if err := s.validateSyncRoot(session); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	for relative := range session.upload {
		entry := session.manifest[relative]
		stagePath, _ := pathutil.ResolveUnderRoot(filepath.Join(session.stage, "files"), relative)
		info, err := os.Stat(stagePath)
		if err != nil || info.Size() != entry.Size {
			writeError(w, http.StatusConflict, fmt.Errorf("upload for %s is incomplete", relative))
			return
		}
		sum, err := hashPath(stagePath)
		if err != nil || sum != entry.SHA256 {
			writeError(w, http.StatusConflict, fmt.Errorf("upload checksum mismatch for %s", relative))
			return
		}
	}
	if err := os.MkdirAll(session.rootPath, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	backupRoot := filepath.Join(session.stage, "backup")
	applied := make([]appliedFile, 0, len(session.upload)+len(session.deletes))
	rollback := func() {
		for i := len(applied) - 1; i >= 0; i-- {
			item := applied[i]
			_ = os.Remove(item.destination)
			if item.hadOriginal {
				_ = os.MkdirAll(filepath.Dir(item.destination), 0755)
				_ = os.Rename(item.backup, item.destination)
			}
		}
	}
	uploads := make([]string, 0, len(session.upload))
	for relative := range session.upload {
		uploads = append(uploads, relative)
	}
	sort.Strings(uploads)
	var uploadedBytes int64
	for _, relative := range uploads {
		entry := session.manifest[relative]
		destination, err := resolveWithoutSymlinks(session.rootPath, relative)
		if err != nil {
			rollback()
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			rollback()
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		stagePath, _ := pathutil.ResolveUnderRoot(filepath.Join(session.stage, "files"), relative)
		backup, _ := pathutil.ResolveUnderRoot(backupRoot, relative)
		item := appliedFile{destination: destination, backup: backup}
		if _, err := os.Lstat(destination); err == nil {
			if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil || os.Rename(destination, backup) != nil {
				rollback()
				writeError(w, http.StatusInternalServerError, fmt.Errorf("back up %s", relative))
				return
			}
			item.hadOriginal = true
		} else if !errors.Is(err, os.ErrNotExist) {
			rollback()
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		applied = append(applied, item)
		if err := os.Rename(stagePath, destination); err != nil {
			rollback()
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if entry.Mode != 0 {
			if err := os.Chmod(destination, os.FileMode(entry.Mode)); err != nil {
				rollback()
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		uploadedBytes += entry.Size
	}
	deleted := 0
	for _, relative := range session.deletes {
		destination, err := resolveWithoutSymlinks(session.rootPath, relative)
		if err != nil {
			rollback()
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			rollback()
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		backup, _ := pathutil.ResolveUnderRoot(filepath.Join(backupRoot, "deleted"), relative)
		if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil || os.Rename(destination, backup) != nil {
			rollback()
			writeError(w, http.StatusInternalServerError, fmt.Errorf("stage deletion of %s", relative))
			return
		}
		applied = append(applied, appliedFile{destination: destination, backup: backup, hadOriginal: true})
		deleted++
	}
	previous, err := s.syncs.loadManaged(session.root)
	if err != nil {
		rollback()
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	managed := managedManifest{Root: session.root, Entries: map[string]managedEntry{}}
	for relative, entry := range session.manifest {
		destination, _ := pathutil.ResolveUnderRoot(session.rootPath, relative)
		info, err := os.Stat(destination)
		if err != nil {
			rollback()
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		managed.Entries[relative] = managedEntry{Size: entry.Size, ModTimeNS: info.ModTime().UnixNano(), SHA256: entry.SHA256, Mode: entry.Mode}
	}
	for relative, entry := range previous.Entries {
		_, present := session.manifest[relative]
		if !present && (session.mode == protocol.SyncModeUpdate || isIgnored(relative, session.matcher)) {
			managed.Entries[relative] = entry
		}
	}
	if err := s.syncs.saveManaged(managed); err != nil {
		rollback()
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	result := protocol.SyncCommitResponse{Uploaded: len(uploads), Deleted: deleted, UploadedBytes: uploadedBytes}
	s.syncs.remove(session)
	writeJSON(w, http.StatusOK, result)
}

func (s *syncStore) acquire(id string) *syncState {
	s.mu.Lock()
	expired := s.cleanupExpiredLocked()
	session := s.values[id]
	s.mu.Unlock()
	removeSyncStages(expired)
	if session == nil {
		return nil
	}
	session.mu.Lock()
	s.mu.Lock()
	current := s.values[id]
	if current != session {
		s.mu.Unlock()
		session.mu.Unlock()
		return nil
	}
	session.expires.Store(time.Now().Add(syncTTL).UnixNano())
	s.mu.Unlock()
	return session
}

func (s *syncStore) cleanupExpiredLocked() []*syncState {
	now := time.Now()
	var expired []*syncState
	for _, session := range s.values {
		if now.UnixNano() <= session.expires.Load() || !session.mu.TryLock() {
			continue
		}
		delete(s.values, session.id)
		expired = append(expired, session)
		session.mu.Unlock()
	}
	return expired
}

func (s *syncStore) remove(session *syncState) {
	s.mu.Lock()
	if s.values[session.id] == session {
		delete(s.values, session.id)
	}
	s.mu.Unlock()
	_ = os.RemoveAll(session.stage)
}

func removeSyncStages(sessions []*syncState) {
	for _, session := range sessions {
		_ = os.RemoveAll(session.stage)
	}
}

func (s *syncStore) manifestPath(root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(s.workspace, ".agentrelay", "manifests", hex.EncodeToString(sum[:16])+".json")
}

func (s *syncStore) loadManaged(root string) (managedManifest, error) {
	result := managedManifest{Root: root, Entries: map[string]managedEntry{}}
	data, err := os.ReadFile(s.manifestPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return managedManifest{}, err
	}
	if result.Root != root || result.Entries == nil {
		return managedManifest{}, errors.New("invalid managed sync manifest")
	}
	return result, nil
}

func (s *syncStore) saveManaged(manifest managedManifest) error {
	path := s.manifestPath(manifest.Root)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	backup := path + ".previous"
	_ = os.Remove(backup)
	hadPrevious := false
	if _, statErr := os.Stat(path); statErr == nil {
		if err := os.Rename(path, backup); err != nil {
			return err
		}
		hadPrevious = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := os.Rename(name, path); err != nil {
		if hadPrevious {
			_ = os.Rename(backup, path)
		}
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func resolveWithoutSymlinks(root, relative string) (string, error) {
	target, err := pathutil.ResolveUnderRoot(root, relative)
	if err != nil {
		return "", err
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(cleanRoot, target)
	if err != nil {
		return "", err
	}
	current := cleanRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path %q traverses a symbolic link", relative)
		}
	}
	return target, nil
}

func (s *Server) validateSyncRoot(session *syncState) error {
	root, err := resolveWithoutSymlinks(s.cfg.Workspace, session.root)
	if err != nil {
		return err
	}
	if filepath.Clean(root) != filepath.Clean(session.rootPath) {
		return errors.New("sync root changed during the session")
	}
	return nil
}

func reservedSyncRoot(root string) bool {
	clean := strings.Trim(strings.ReplaceAll(root, "\\", "/"), "/")
	return clean == ".agentrelay" || strings.HasPrefix(clean, ".agentrelay/")
}

func isIgnored(path string, matcher gitignore.Matcher) bool {
	return matcher != nil && matcher.Match(strings.Split(path, "/"), false)
}

func randomSyncID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func hashPath(path string) (string, error) {
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

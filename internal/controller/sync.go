package controller

import (
	"DevFreight/internal/filesync"
	"DevFreight/internal/protocol"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const manifestBatchSize = 512

func (c *Client) Sync(ctx context.Context, id string, options filesync.Options) (result protocol.SyncResponse, err error) {
	startedAt := time.Now()
	if options.Mode == "" {
		options.Mode = protocol.SyncModeUpdate
	}
	scan, err := filesync.Scan(options)
	if err != nil {
		return result, err
	}
	var started protocol.SyncStartResponse
	err = c.Do(ctx, httpMethodPost, devicePath(id)+"/files/sync/start", protocol.SyncStartRequest{SourcePath: scan.Root, Root: options.Root, Mode: options.Mode}, &started)
	if err != nil {
		return result, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = c.Do(context.Background(), httpMethodDelete, devicePath(id)+"/files/sync/"+url.PathEscape(started.ID), nil, nil)
		}
	}()
	entryOffset, ignoreOffset := 0, 0
	for entryOffset < len(scan.Entries) || ignoreOffset < len(scan.IgnorePatterns) || (entryOffset == 0 && ignoreOffset == 0) {
		entryEnd := min(entryOffset+manifestBatchSize, len(scan.Entries))
		ignoreEnd := min(ignoreOffset+manifestBatchSize, len(scan.IgnorePatterns))
		final := entryEnd == len(scan.Entries) && ignoreEnd == len(scan.IgnorePatterns)
		var acknowledged protocol.SyncManifestResponse
		request := protocol.SyncManifestRequest{Entries: scan.Entries[entryOffset:entryEnd], Ignore: scan.IgnorePatterns[ignoreOffset:ignoreEnd], Final: final}
		if err = c.Do(ctx, httpMethodPost, syncOperationPath(id, started.ID, "manifest"), request, &acknowledged); err != nil {
			return result, err
		}
		if final {
			break
		}
		entryOffset, ignoreOffset = entryEnd, ignoreEnd
	}
	var plan protocol.SyncPlanResponse
	if err = c.Do(ctx, httpMethodPost, syncOperationPath(id, started.ID, "plan"), struct{}{}, &plan); err != nil {
		return result, err
	}
	result = protocol.SyncResponse{Root: started.Root, Scanned: len(scan.Entries), Ignored: scan.Ignored, Unchanged: plan.Unchanged, UnchangedBytes: plan.UnchangedBytes, DryRun: options.DryRun, Warnings: scan.Warnings}
	if options.DryRun {
		result.Uploaded = len(plan.Upload)
		result.Deleted = len(plan.Delete)
		result.DurationMS = time.Since(startedAt).Milliseconds()
		return result, nil
	}
	entries := make(map[string]protocol.SyncManifestEntry, len(scan.Entries))
	for _, entry := range scan.Entries {
		entries[entry.Path] = entry
	}
	chunkSize := started.ChunkSizeBytes
	if chunkSize <= 0 || chunkSize > 8<<20 {
		return result, errors.New("worker returned an invalid sync chunk size")
	}
	for _, relative := range plan.Upload {
		entry, ok := entries[relative]
		if !ok {
			return result, fmt.Errorf("worker requested unknown manifest path %q", relative)
		}
		if err = c.uploadFile(ctx, id, started.ID, scan.Root, entry, int(chunkSize)); err != nil {
			return result, err
		}
	}
	var commit protocol.SyncCommitResponse
	if err = c.Do(ctx, httpMethodPost, syncOperationPath(id, started.ID, "commit"), struct{}{}, &commit); err != nil {
		return result, err
	}
	committed = true
	result.Uploaded = commit.Uploaded
	result.Deleted = commit.Deleted
	result.UploadedBytes = commit.UploadedBytes
	result.DurationMS = time.Since(startedAt).Milliseconds()
	return result, nil
}

const (
	httpMethodPost   = "POST"
	httpMethodDelete = "DELETE"
)

func syncOperationPath(deviceID, syncID, operation string) string {
	return devicePath(deviceID) + "/files/sync/" + url.PathEscape(syncID) + "/" + operation
}

func (c *Client) uploadFile(ctx context.Context, deviceID, syncID, root string, entry protocol.SyncManifestEntry, chunkSize int) error {
	path := filepath.Join(root, filepath.FromSlash(entry.Path))
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("source file %s is no longer a regular file", entry.Path)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	relativeResolved, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || !filepath.IsLocal(relativeResolved) {
		return fmt.Errorf("source file %s resolves outside source_path", entry.Path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buffer := make([]byte, chunkSize)
	hash := sha256.New()
	offset := int64(0)
	for {
		n, readErr := f.Read(buffer)
		if n > 0 || (entry.Size == 0 && offset == 0) {
			chunk := buffer[:n]
			chunkHash := sha256.Sum256(chunk)
			query := url.Values{}
			query.Set("path", entry.Path)
			query.Set("offset", fmt.Sprint(offset))
			query.Set("sha256", hex.EncodeToString(chunkHash[:]))
			endpoint := syncOperationPath(deviceID, syncID, "file") + "?" + query.Encode()
			if err := c.uploadChunk(ctx, endpoint, chunk); err != nil {
				return err
			}
			_, _ = hash.Write(chunk)
			offset += int64(n)
			if entry.Size == 0 {
				break
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if offset != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
		return fmt.Errorf("source file %s changed while it was being synchronized", entry.Path)
	}
	return nil
}

func (c *Client) uploadChunk(ctx context.Context, endpoint string, chunk []byte) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		last = c.do(ctx, httpMethodPost, endpoint, "application/octet-stream", chunk, nil)
		if last == nil {
			return nil
		}
		var response *responseError
		if errors.As(last, &response) && response.status < 500 {
			return last
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return last
}

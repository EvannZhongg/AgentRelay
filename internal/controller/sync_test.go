package controller_test

import (
	"DevFreight/internal/filesync"
	"DevFreight/internal/protocol"
	"DevFreight/internal/testutil"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIncrementalChunkedSyncIgnoreAndMirror(t *testing.T) {
	remoteWorkspace := t.TempDir()
	relay := testutil.New(t, remoteWorkspace)
	source := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "ignored.txt\n")
	write("keep.txt", "first")
	write("ignored.txt", "never uploaded")
	bigPath := filepath.Join(source, "large.bin")
	if err := os.WriteFile(bigPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(bigPath, 34<<20); err != nil {
		t.Fatal(err)
	}
	options := filesync.Options{SourcePath: source, Root: "project", Mode: protocol.SyncModeUpdate, UseGitIgnore: true}

	first, err := relay.Client.Sync(context.Background(), relay.Device.ID, options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Uploaded != 3 || first.UploadedBytes <= 32<<20 {
		t.Fatalf("first sync = %+v", first)
	}
	if info, err := os.Stat(filepath.Join(remoteWorkspace, "project", "large.bin")); err != nil || info.Size() != 34<<20 {
		t.Fatalf("large file = %+v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(remoteWorkspace, "project", "ignored.txt")); !os.IsNotExist(err) {
		t.Fatalf("ignored file exists: %v", err)
	}

	second, err := relay.Client.Sync(context.Background(), relay.Device.ID, options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Uploaded != 0 || second.Unchanged != 3 {
		t.Fatalf("second sync = %+v", second)
	}

	write("keep.txt", "second")
	if err := os.Remove(bigPath); err != nil {
		t.Fatal(err)
	}
	unmanaged := filepath.Join(remoteWorkspace, "project", "build-output.txt")
	if err := os.WriteFile(unmanaged, []byte("keep remotely"), 0600); err != nil {
		t.Fatal(err)
	}
	updated, err := relay.Client.Sync(context.Background(), relay.Device.ID, options)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Uploaded != 1 || updated.Deleted != 0 {
		t.Fatalf("update sync = %+v", updated)
	}
	if _, err := os.Stat(filepath.Join(remoteWorkspace, "project", "large.bin")); err != nil {
		t.Fatal("update mode removed an old managed file")
	}

	write(".gitignore", "ignored.txt\nlarge.bin\n")
	options.Mode = protocol.SyncModeMirror
	preserved, err := relay.Client.Sync(context.Background(), relay.Device.ID, options)
	if err != nil {
		t.Fatal(err)
	}
	if preserved.Deleted != 0 {
		t.Fatalf("ignored managed file was deleted: %+v", preserved)
	}
	write(".gitignore", "ignored.txt\n")
	mirrored, err := relay.Client.Sync(context.Background(), relay.Device.ID, options)
	if err != nil {
		t.Fatal(err)
	}
	if mirrored.Deleted != 1 {
		t.Fatalf("mirror sync = %+v", mirrored)
	}
	if _, err := os.Stat(filepath.Join(remoteWorkspace, "project", "large.bin")); !os.IsNotExist(err) {
		t.Fatalf("managed file still exists: %v", err)
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Fatal("mirror removed an unmanaged file")
	}
}

func TestSyncDryRunDoesNotWriteOrPersistMapping(t *testing.T) {
	relay := testutil.New(t, "")
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "new.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := relay.Client.Sync(context.Background(), relay.Device.ID, filesync.Options{SourcePath: source, Root: "dry", Mode: protocol.SyncModeUpdate, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Uploaded != 1 {
		t.Fatalf("dry run = %+v", result)
	}
	devices, err := relay.Client.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := devices[0].Mappings[source]; exists {
		t.Fatal("dry run persisted a workspace mapping")
	}
}

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"os"
	"path/filepath"

	"AgentRelay/internal/bridge"
	"AgentRelay/internal/pathutil"
	"AgentRelay/internal/protocol"
)

func pullCommand(args []string) error {
	f := flag.NewFlagSet("pull", flag.ContinueOnError)
	base := f.String("bridge", bridge.DefaultURL, "bridge URL")
	id := f.String("device", "", "device ID")
	path := f.String("path", "", "remote workspace-relative file path")
	out := f.String("out", "", "local destination directory; omit to return base64 JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("--device is required")
	}
	paths := f.Args()
	if *path != "" {
		paths = append(paths, *path)
	}
	if len(paths) == 0 {
		return errors.New("pull requires --path or positional remote file paths")
	}
	files := make([]protocol.ReadResponse, 0, len(paths))
	c := bridge.NewClient(*base)
	for _, path := range paths {
		file, err := c.ReadFile(context.Background(), *id, path)
		if err != nil {
			return err
		}
		files = append(files, file)
	}
	if *out == "" {
		return printJSON(files)
	}
	root, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	type savedFile struct {
		Path      string `json:"path"`
		LocalPath string `json:"local_path"`
		Bytes     int64  `json:"bytes"`
	}
	saved := make([]savedFile, 0, len(files))
	for _, file := range files {
		local, err := pathutil.ResolveUnderRoot(root, file.Path)
		if err != nil {
			return err
		}
		data, err := base64.StdEncoding.DecodeString(file.ContentBase64)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(local), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(local, data, 0644); err != nil {
			return err
		}
		saved = append(saved, savedFile{file.Path, local, int64(len(data))})
	}
	return printJSON(saved)
}

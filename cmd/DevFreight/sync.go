package main

import (
	"context"
	"errors"
	"flag"
	"strings"

	"DevFreight/internal/controller"
	"DevFreight/internal/filesync"
	"DevFreight/internal/protocol"
)

type syncPatterns []string

func (p *syncPatterns) String() string { return strings.Join(*p, ",") }
func (p *syncPatterns) Set(value string) error {
	*p = append(*p, value)
	return nil
}

func syncCommand(args []string) error {
	f := flag.NewFlagSet("sync", flag.ContinueOnError)
	base := f.String("controller", controller.DefaultURL, "controller URL")
	id := f.String("device", "", "device ID")
	source := f.String("source", ".", "local source directory")
	remote := f.String("remote", "", "remote root (automatically mapped when omitted)")
	mode := f.String("mode", protocol.SyncModeUpdate, "sync mode: update or mirror")
	noGitIgnore := f.Bool("no-gitignore", false, "do not apply nested .gitignore files")
	dryRun := f.Bool("dry-run", false, "plan without changing the device")
	var exclude syncPatterns
	f.Var(&exclude, "exclude", "gitignore-compatible pattern (repeatable)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("--device is required")
	}
	if *mode != protocol.SyncModeUpdate && *mode != protocol.SyncModeMirror {
		return errors.New("--mode must be update or mirror")
	}
	result, err := controller.NewClient(*base).Sync(context.Background(), *id, filesync.Options{SourcePath: *source, Root: *remote, Mode: *mode, UseGitIgnore: !*noGitIgnore, Exclude: exclude, DryRun: *dryRun})
	if err != nil {
		return err
	}
	return printJSON(result)
}

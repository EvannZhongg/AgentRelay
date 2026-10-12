package main

import (
	"context"
	"errors"
	"flag"

	"AgentRelay/internal/controller"
	"AgentRelay/internal/protocol"
)

func connectionCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: AgentRelay connection add-ssh|show|test|replace-ssh|replace-worker")
	}
	f := flag.NewFlagSet("connection", flag.ContinueOnError)
	base := f.String("controller", controller.DefaultURL, "Controller URL")
	device := f.String("device", "", "device handle or alias; omit for a new SSH device")
	pairing := f.String("pairing", "", "pending Worker pairing ID for replacement")
	name := f.String("name", "", "new device alias")
	root := f.String("workspace", "", "existing absolute target workspace (POSIX, drive or UNC path)")
	platform := f.String("platform", "auto", "SSH target executor: auto, windows or posix")
	mode := f.String("workspace-mode", protocol.WorkspaceRemote, "workspace mode: remote or local_sync")
	host := f.String("host", "", "SSH hostname or IP")
	user := f.String("user", "", "SSH user")
	port := f.Int("port", 22, "SSH port")
	key := f.String("identity-file", "", "OpenSSH identity file; otherwise use ssh-agent/default identities")
	known := f.String("known-hosts-file", "", "OpenSSH known_hosts file")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	c, ctx := controller.NewClient(*base), context.Background()
	spec := protocol.ConnectionSpec{Kind: "ssh", SSH: &protocol.SSHConfig{Platform: *platform, Host: *host, User: *user, Port: *port, IdentityFile: *key, KnownHostsFile: *known}, Workspace: protocol.WorkspacePolicy{Root: *root, Mode: *mode}}
	if args[0] == "add-ssh" {
		if *device != "" {
			return errors.New("add-ssh creates a new device; use replace-ssh to change an existing device")
		}
		out, err := c.CreateDevice(ctx, protocol.DeviceCreate{Name: *name, Connection: spec})
		if err != nil {
			return err
		}
		return printJSON(out)
	}
	if *device == "" {
		return errors.New("--device is required")
	}
	switch args[0] {
	case "show":
		ds, err := c.Devices(ctx)
		if err != nil {
			return err
		}
		id, err := controller.ResolveDeviceSelector(ds, *device)
		if err != nil {
			return err
		}
		for _, d := range ds {
			if d.ID == id {
				return printJSON(d.Connection)
			}
		}
	case "test":
		out, err := c.TestConnection(ctx, *device)
		if err != nil {
			return err
		}
		return printJSON(out)
	case "replace-ssh", "replace-worker":
		in := protocol.ConnectionReplacement{Connection: &spec}
		if args[0] == "replace-worker" {
			if *pairing == "" {
				return errors.New("--pairing is required")
			}
			in = protocol.ConnectionReplacement{PairingID: *pairing}
		}
		out, err := c.ReplaceConnection(ctx, *device, in)
		if err != nil {
			return err
		}
		return printJSON(out)
	}
	return errors.New("unknown connection command")
}

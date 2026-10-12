package main

import (
	"context"
	"errors"
	"flag"
	"net/url"

	"AgentRelay/internal/controller"
	"AgentRelay/internal/protocol"
)

func connectionCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: AgentRelay connection add-ssh|list|test|select|remove|attach-worker")
	}
	f := flag.NewFlagSet("connection", flag.ContinueOnError)
	base := f.String("controller", controller.DefaultURL, "Controller URL")
	device := f.String("device", "", "device handle or alias; omit for a new SSH device")
	id := f.String("id", "", "connection ID or pairing ID for attach-worker")
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
	if args[0] == "add-ssh" {
		ssh := protocol.SSHConfig{Platform: *platform, Host: *host, User: *user, Port: *port, IdentityFile: *key, KnownHostsFile: *known}
		spec := protocol.ConnectionSpec{Kind: "ssh", SSH: &ssh, Workspace: protocol.WorkspacePolicy{Root: *root, Mode: *mode}}
		if *device == "" {
			out, err := c.CreateDevice(ctx, protocol.DeviceCreate{Name: *name, Connection: spec})
			if err != nil {
				return err
			}
			return printJSON(out)
		}
		out, err := c.AddConnection(ctx, *device, spec)
		if err != nil {
			return err
		}
		return printJSON(out)
	}
	if *device == "" {
		return errors.New("--device is required")
	}
	ds, err := c.Devices(ctx)
	if err != nil {
		return err
	}
	privateID, err := controller.ResolveDeviceSelector(ds, *device)
	if err != nil {
		return err
	}
	if args[0] == "list" {
		for _, d := range ds {
			if d.ID == privateID {
				return printJSON(d.Connections)
			}
		}
	}
	if *id == "" {
		return errors.New("--id is required")
	}
	switch args[0] {
	case "test":
		out, err := c.TestConnection(ctx, privateID, *id)
		if err != nil {
			return err
		}
		return printJSON(out)
	case "select":
		out, err := c.UpdateDevice(ctx, privateID, controller.DeviceUpdate{ActiveConnection: id})
		if err != nil {
			return err
		}
		return printJSON(out)
	case "remove":
		return c.Do(ctx, "DELETE", "/v1/devices/"+url.PathEscape(privateID)+"/connections/"+url.PathEscape(*id), nil, nil)
	case "attach-worker":
		out, err := c.AttachPairing(ctx, *id, privateID)
		if err != nil {
			return err
		}
		out.Credentials = nil
		return printJSON(out)
	}
	return errors.New("unknown connection command")
}

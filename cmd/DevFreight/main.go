package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"DevFreight/internal/controller"
	"DevFreight/internal/protocol"
)

func publicDevices(devices []protocol.Device) []map[string]any {
	return controller.AgentDevices(devices)
}

func main() {
	if len(os.Args) < 2 {
		if err := setupCommand(nil); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	var err error
	switch os.Args[1] {
	case "setup":
		err = setupCommand(os.Args[2:])
	case "controller":
		err = controllerCommand(os.Args[2:])
	case "gui":
		err = guiCommand(os.Args[2:])
	case "device":
		err = deviceCommand(os.Args[2:])
	case "connection":
		err = connectionCommand(os.Args[2:])
	case "desktop":
		err = desktopCommand(os.Args[2:])
	case "exec":
		err = execCommand(os.Args[2:])
	case "task":
		err = taskCommand(os.Args[2:])
	case "sync":
		err = syncCommand(os.Args[2:])
	case "pull":
		err = pullCommand(os.Args[2:])
	case "session":
		err = sessionCommand(os.Args[2:])
	case "mcp":
		err = mcpCommand(os.Args[2:])
	default:
		usage()
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func client(args []string) (*controller.Client, context.Context, error) {
	f := flag.NewFlagSet("client", flag.ContinueOnError)
	base := f.String("controller", controller.DefaultURL, "controller URL")
	if err := f.Parse(args); err != nil {
		return nil, nil, err
	}
	return controller.NewClient(*base), context.Background(), nil
}
func deviceCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: DevFreight device list|rename|authorize|revoke|remove")
	}
	switch args[0] {
	case "list":
		c, ctx, err := client(args[1:])
		if err != nil {
			return err
		}
		out, err := c.Devices(ctx)
		if err != nil {
			return err
		}
		return printJSON(publicDevices(out))
	case "rename", "authorize", "revoke", "remove":
		f := flag.NewFlagSet("device", flag.ContinueOnError)
		base := f.String("controller", controller.DefaultURL, "controller URL")
		id := f.String("device", "", "device ID")
		name := f.String("name", "", "device alias")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return errors.New("--device is required")
		}
		c := controller.NewClient(*base)
		if args[0] == "remove" {
			return c.Do(context.Background(), "DELETE", "/v1/devices/"+url.PathEscape(*id), nil, nil)
		}
		update := controller.DeviceUpdate{}
		if args[0] == "rename" {
			update.Name = name
		} else {
			authorized := args[0] == "authorize"
			update.Authorized = &authorized
		}
		d, err := c.UpdateDevice(context.Background(), *id, update)
		if err != nil {
			return err
		}
		return printJSON(d)
	default:
		return fmt.Errorf("unknown device command %q", args[0])
	}
}
func execCommand(args []string) error {
	f := flag.NewFlagSet("exec", flag.ContinueOnError)
	base := f.String("controller", controller.DefaultURL, "controller URL")
	id := f.String("device", "", "device ID")
	dir := f.String("dir", "", "directory in the selected workspace")
	dirKind := f.String("dir-kind", "auto", "directory interpretation: auto, local or remote")
	timeout := f.Int64("timeout-ms", 0, "timeout")
	var env environmentFlags
	f.Var(&env, "env", "environment override KEY=VALUE (repeatable)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *id == "" || f.NArg() == 0 {
		return errors.New("usage: DevFreight exec --device <id> [--dir <path>] <command>")
	}
	result, err := controller.NewClient(*base).Exec(context.Background(), *id, protocol.ExecRequest{Command: strings.Join(f.Args(), " "), WorkingDir: *dir, WorkingDirKind: *dirKind, TimeoutMS: *timeout, Env: env})
	if err != nil {
		return err
	}
	return printJSON(result)
}
func taskCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: DevFreight task start|list|get|retrieve|cancel")
	}
	f := flag.NewFlagSet("task", flag.ContinueOnError)
	base := f.String("controller", controller.DefaultURL, "controller URL")
	id := f.String("device", "", "device ID")
	taskID := f.String("id", "", "task ID")
	dir := f.String("dir", "", "directory for start")
	dirKind := f.String("dir-kind", "auto", "directory interpretation: auto, local or remote")
	timeout := f.Int64("timeout-ms", 0, "execution timeout for start")
	var env environmentFlags
	f.Var(&env, "env", "environment override KEY=VALUE (repeatable)")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	c := controller.NewClient(*base)
	if *id == "" {
		return errors.New("--device is required")
	}
	switch args[0] {
	case "start":
		if f.NArg() == 0 {
			return errors.New("task start requires a command")
		}
		task, err := c.StartTask(context.Background(), *id, protocol.TaskRequest{ExecRequest: protocol.ExecRequest{Command: strings.Join(f.Args(), " "), WorkingDir: *dir, WorkingDirKind: *dirKind, Env: env, TimeoutMS: *timeout}})
		if err != nil {
			return err
		}
		return printJSON(task)
	case "list":
		tasks, err := c.ListTasks(context.Background(), *id)
		if err != nil {
			return err
		}
		return printJSON(tasks)
	case "get", "retrieve", "cancel":
		if *taskID == "" {
			return fmt.Errorf("task %s requires --id", args[0])
		}
		if args[0] == "cancel" {
			if err := c.CancelTask(context.Background(), *id, *taskID); err != nil {
				return err
			}
		}
		var task protocol.Task
		var err error
		if args[0] == "retrieve" {
			task, err = c.RetrieveTask(context.Background(), *id, *taskID)
		} else {
			task, err = c.GetTask(context.Background(), *id, *taskID)
		}
		if err != nil {
			return err
		}
		return printJSON(task)
	default:
		return fmt.Errorf("unknown task command %q", args[0])
	}
}

type mcpServerConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type mcpConfig struct {
	Servers map[string]mcpServerConfig `json:"mcpServers"`
}

func mcpCommand(args []string) error {
	if len(args) == 0 || args[0] != "config" {
		return errors.New("usage: DevFreight mcp config [--controller <url>] [--command <path>] [--name <server-name>]")
	}
	f := flag.NewFlagSet("mcp config", flag.ContinueOnError)
	base := f.String("controller", controller.DefaultURL, "Controller URL")
	command := f.String("command", defaultMCPCommand(), "MCP server executable")
	name := f.String("name", "devfreight", "MCP server name")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return errors.New("--name cannot be empty")
	}
	config := mcpConfig{Servers: map[string]mcpServerConfig{*name: {Command: *command, Args: []string{"--controller", *base}}}}
	return printJSON(config)
}

func defaultMCPCommand() string {
	if configured := strings.TrimSpace(os.Getenv("DEVFREIGHT_MCP_COMMAND")); configured != "" {
		return configured
	}
	executable, err := os.Executable()
	if err != nil {
		return "DevFreight-mcp"
	}
	extension := filepath.Ext(executable)
	name := strings.TrimSuffix(filepath.Base(executable), extension)
	if strings.EqualFold(name, "DevFreight") {
		candidate := filepath.Join(filepath.Dir(executable), "DevFreight-mcp"+extension)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "DevFreight-mcp"
}
func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}
func usage() {
	fmt.Println("Connection management: DevFreight connection add-ssh|show|test|replace-ssh|replace-worker")
	fmt.Println("DevFreight\n\nUsage:\n  DevFreight setup\n  DevFreight controller serve\n  DevFreight gui\n  DevFreight device list|rename|authorize|revoke|remove\n  DevFreight exec --device <alias|handle> [--dir <local-project-or-remote-path>] <command>\n  DevFreight task start|list|get|retrieve|cancel --device <alias|handle> [--id <task-id>]\n  DevFreight sync --device <alias|handle> --source <dir> [--mode update|mirror] [--dry-run]\n  DevFreight pull --device <alias|handle> [--out <local-dir>] <remote-path>...\n  DevFreight session create|list|get|send|close --device <alias|handle> [--id <session-id>]\n  DevFreight desktop info|capture|input --device <alias|handle> [--json <object>]\n  DevFreight mcp config")
}

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"AgentRelay/internal/bridge"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/worker"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "worker":
		err = workerCommand(os.Args[2:])
	case "bridge":
		err = bridgeCommand(os.Args[2:])
	case "device":
		err = deviceCommand(os.Args[2:])
	case "exec":
		err = execCommand(os.Args[2:])
	case "task":
		err = taskCommand(os.Args[2:])
	case "sync":
		err = syncCommand(os.Args[2:])
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

func workerCommand(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("usage: AgentRelay worker serve --workspace <dir> [--listen :8765]")
	}
	f := flag.NewFlagSet("worker serve", flag.ContinueOnError)
	workspace := f.String("workspace", "", "worker workspace root")
	listen := f.String("listen", ":8765", "listen address")
	name := f.String("name", "", "device name")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *workspace == "" {
		return errors.New("--workspace is required")
	}
	s, err := worker.New(worker.Config{ListenAddr: *listen, Workspace: *workspace, Name: *name})
	if err != nil {
		return err
	}
	fmt.Printf("worker listening on %s (workspace %s)\n", *listen, *workspace)
	return s.ListenAndServe()
}
func bridgeCommand(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("usage: AgentRelay bridge serve [--listen :8764]")
	}
	f := flag.NewFlagSet("bridge serve", flag.ContinueOnError)
	listen := f.String("listen", ":8764", "listen address")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	s := bridge.New(bridge.Config{ListenAddr: *listen})
	fmt.Printf("bridge listening on %s\n", *listen)
	return s.ListenAndServe()
}
func client(args []string) (*bridge.Client, context.Context, error) {
	f := flag.NewFlagSet("client", flag.ContinueOnError)
	base := f.String("bridge", "http://127.0.0.1:8764", "bridge URL")
	if err := f.Parse(args); err != nil {
		return nil, nil, err
	}
	return bridge.NewClient(*base), context.Background(), nil
}
func deviceCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: AgentRelay device add|list")
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
		return printJSON(out)
	case "add":
		f := flag.NewFlagSet("device add", flag.ContinueOnError)
		base := f.String("bridge", "http://127.0.0.1:8764", "bridge URL")
		workerURL := f.String("worker", "", "worker URL")
		name := f.String("name", "", "device name")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *workerURL == "" {
			return errors.New("--worker is required")
		}
		d, err := bridge.NewClient(*base).AddDevice(context.Background(), *workerURL, *name)
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
	base := f.String("bridge", "http://127.0.0.1:8764", "bridge URL")
	id := f.String("device", "", "device ID")
	dir := f.String("dir", "", "workspace-relative directory")
	timeout := f.Int64("timeout-ms", 0, "timeout")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *id == "" || f.NArg() == 0 {
		return errors.New("usage: AgentRelay exec --device <id> [--dir <path>] <command>")
	}
	result, err := bridge.NewClient(*base).Exec(context.Background(), *id, protocol.ExecRequest{Command: strings.Join(f.Args(), " "), WorkingDir: *dir, TimeoutMS: *timeout})
	if err != nil {
		return err
	}
	return printJSON(result)
}
func taskCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: AgentRelay task start|get")
	}
	f := flag.NewFlagSet("task", flag.ContinueOnError)
	base := f.String("bridge", "http://127.0.0.1:8764", "bridge URL")
	id := f.String("device", "", "device ID")
	taskID := f.String("id", "", "task ID")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	c := bridge.NewClient(*base)
	if *id == "" {
		return errors.New("--device is required")
	}
	switch args[0] {
	case "start":
		if f.NArg() == 0 {
			return errors.New("task start requires a command")
		}
		task, err := c.StartTask(context.Background(), *id, protocol.TaskRequest{ExecRequest: protocol.ExecRequest{Command: strings.Join(f.Args(), " ")}})
		if err != nil {
			return err
		}
		return printJSON(task)
	case "get":
		if *taskID == "" {
			return errors.New("task get requires --id")
		}
		task, err := c.GetTask(context.Background(), *id, *taskID)
		if err != nil {
			return err
		}
		return printJSON(task)
	default:
		return fmt.Errorf("unknown task command %q", args[0])
	}
}
func syncCommand(args []string) error {
	f := flag.NewFlagSet("sync", flag.ContinueOnError)
	base := f.String("bridge", "http://127.0.0.1:8764", "bridge URL")
	id := f.String("device", "", "device ID")
	source := f.String("source", ".", "local source directory")
	remote := f.String("remote", ".", "remote workspace-relative root")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("--device is required")
	}
	files := make([]protocol.FileEntry, 0)
	err := filepath.WalkDir(*source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(*source, path)
		if err != nil {
			return err
		}
		files = append(files, protocol.FileEntry{Path: filepath.ToSlash(rel), ContentBase64: base64.StdEncoding.EncodeToString(data)})
		return nil
	})
	if err != nil {
		return err
	}
	result, err := bridge.NewClient(*base).Sync(context.Background(), *id, protocol.SyncRequest{Root: *remote, Files: files})
	if err != nil {
		return err
	}
	return printJSON(result)
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
		return errors.New("usage: AgentRelay mcp config [--bridge <url>] [--command <path>] [--name <server-name>]")
	}
	f := flag.NewFlagSet("mcp config", flag.ContinueOnError)
	base := f.String("bridge", "http://127.0.0.1:8764", "Bridge URL")
	command := f.String("command", defaultMCPCommand(), "MCP server executable")
	name := f.String("name", "agent-relay", "MCP server name")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return errors.New("--name cannot be empty")
	}
	config := mcpConfig{Servers: map[string]mcpServerConfig{*name: {Command: *command, Args: []string{"--bridge", *base}}}}
	return printJSON(config)
}

func defaultMCPCommand() string {
	if configured := strings.TrimSpace(os.Getenv("AGENTRELAY_MCP_COMMAND")); configured != "" {
		return configured
	}
	executable, err := os.Executable()
	if err != nil {
		return "AgentRelay-mcp"
	}
	extension := filepath.Ext(executable)
	name := strings.TrimSuffix(filepath.Base(executable), extension)
	if strings.EqualFold(name, "AgentRelay") {
		candidate := filepath.Join(filepath.Dir(executable), "AgentRelay-mcp"+extension)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "AgentRelay-mcp"
}
func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}
func usage() {
	fmt.Println("AgentRelay\n\nUsage:\n  AgentRelay worker serve --workspace <dir>\n  AgentRelay bridge serve\n  AgentRelay device add --worker http://host:8765\n  AgentRelay device list\n  AgentRelay exec --device <id> <command>\n  AgentRelay task start|get --device <id>\n  AgentRelay sync --device <id> --source <dir>\n  AgentRelay mcp config [--bridge <url>]")
}

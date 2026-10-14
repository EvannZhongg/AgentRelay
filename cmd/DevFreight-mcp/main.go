package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"DevFreight/internal/controller"
	"DevFreight/internal/filesync"
	"DevFreight/internal/protocol"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

const (
	currentProtocolVersion = "2025-06-18"
	serverName             = "DevFreight"
	serverVersion          = "0.3.0"
)

func main() {
	base := flag.String("controller", controller.DefaultURL, "Controller URL")
	flag.Parse()
	client := controller.NewClient(*base)
	if err := serve(client, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serve(client *controller.Client, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var req rpcRequest
		if !json.Valid(scanner.Bytes()) {
			if err := encoder.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "Parse error"}}); err != nil {
				return err
			}
			continue
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" || !validID(req.ID) {
			if err := encoder.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32600, Message: "Invalid Request"}}); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		result, err := handle(context.Background(), client, req)
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		if err != nil {
			var rpcErr *rpcError
			if !errors.As(err, &rpcErr) {
				rpcErr = &rpcError{Code: -32603, Message: err.Error()}
			}
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
		if err := encoder.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(id)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return false
	}
	switch value.(type) {
	case string, json.Number, nil:
		return true
	default:
		return false
	}
}

func handle(ctx context.Context, client *controller.Client, req rpcRequest) (any, error) {
	switch req.Method {
	case "initialize":
		return initializeResult(req.Params), nil
	case "notifications/initialized", "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefinitions()}, nil
	case "tools/call":
		result, err := callTool(ctx, client, req.Params)
		if err != nil {
			return toolError(err), nil
		}
		return result, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func initializeResult(raw json.RawMessage) map[string]any {
	protocolVersion := currentProtocolVersion
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(raw, &params) == nil {
		switch params.ProtocolVersion {
		case "2024-11-05", "2025-03-26", "2025-06-18":
			protocolVersion = params.ProtocolVersion
		}
	}
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]string{"name": serverName, "version": serverVersion},
	}
}

func toolDefinitions() []map[string]any {
	stringProperty := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	return append([]map[string]any{
		{"name": "device_list", "description": "List logical devices using persistent handles and aliases, authorization, workspace_mode (local_sync or remote), workspace mappings, capabilities and availability. Each device appears once. Check advertised capabilities before requesting an operation. Private IDs and connection configuration are omitted; offline entries remain visible.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
		{"name": "device_exec", "description": "Execute a command on a device selected by a persistent handle or alias and return stdout, stderr and exit status.", "inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"device_id": stringProperty("Persistent device handle or alias. Do not use the private device ID."), "command": stringProperty("Native shell command."), "working_dir_kind": map[string]any{"type": "string", "enum": []string{"auto", "local", "remote"}, "description": "auto follows workspace_mode; local requires a sync mapping; remote always uses the target filesystem."}, "working_dir": stringProperty("In local_sync mode: a mapped controller directory or target-relative directory. In remote mode: a target-relative or absolute directory inside the authorized root. Sync is optional."),
				"env": map[string]any{"type": "object", "additionalProperties": stringProperty("Environment variable value.")}, "timeout_ms": map[string]any{"type": "integer", "minimum": 1, "description": "Execution timeout in milliseconds."},
			}, "required": []string{"device_id", "command"},
		}},
		{"name": "device_sync", "description": "Incrementally synchronize a local directory to a device using ignore rules, checksums and bounded chunks.", "inputSchema": map[string]any{
			"type": "object", "additionalProperties": false, "properties": map[string]any{
				"device_id": stringProperty("Persistent device handle or alias."), "root": stringProperty("Remote root; automatically mapped from source_path when omitted."), "source_path": stringProperty("Local directory to synchronize and map to the remote root."),
				"mode":          map[string]any{"type": "string", "enum": []string{"update", "mirror"}, "description": "update keeps remote extras; mirror deletes files managed by an earlier sync but now absent."},
				"exclude":       map[string]any{"type": "array", "items": stringProperty("Gitignore-compatible exclusion pattern; prefix with ! to re-include."), "description": "Additional gitignore-compatible patterns."},
				"use_gitignore": map[string]any{"type": "boolean", "description": "Apply nested .gitignore files; defaults to true."},
				"dry_run":       map[string]any{"type": "boolean", "description": "Plan the synchronization without changing the device."},
			}, "required": []string{"device_id", "source_path"},
		}},
		{"name": "device_pull", "description": "Read remote files or test results from a device workspace.", "inputSchema": map[string]any{
			"type": "object", "additionalProperties": false, "properties": map[string]any{"device_id": stringProperty("Persistent device handle or alias."), "path": stringProperty("One remote workspace-relative file path."), "paths": map[string]any{"type": "array", "items": stringProperty("Remote file path.")}}, "required": []string{"device_id"},
		}},
		{"name": "device_task", "description": "Start, list, query or cancel a task. get retains the record; retrieve returns a finished task and deletes its record, and fails while the task is running.", "inputSchema": map[string]any{
			"type": "object", "additionalProperties": false, "properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"start", "list", "get", "cancel", "retrieve"}}, "device_id": stringProperty("Persistent device handle or alias."), "task_id": stringProperty("Task ID for get, cancel, or retrieve."), "command": stringProperty("Command for start."), "working_dir_kind": map[string]any{"type": "string", "enum": []string{"auto", "local", "remote"}, "description": "auto follows workspace_mode; local requires a sync mapping; remote always uses the target filesystem."}, "working_dir": stringProperty("Directory interpreted according to workspace_mode and working_dir_kind."), "env": map[string]any{"type": "object", "additionalProperties": stringProperty("Environment variable value.")}, "timeout_ms": map[string]any{"type": "integer", "minimum": 1},
			}, "required": []string{"device_id", "action"},
		}},
		{"name": "device_session", "description": "Create, list, poll, send input to, or close a persistent shell session. Output is cumulative; send returns immediately, so poll get for new output. This is a pipe-based shell, not a PTY.", "inputSchema": map[string]any{
			"type": "object", "additionalProperties": false, "properties": map[string]any{
				"action":    map[string]any{"type": "string", "enum": []string{"create", "list", "get", "send", "close"}},
				"device_id": stringProperty("Persistent device handle or alias."), "session_id": stringProperty("Session ID for get, send, or close."),
				"command": stringProperty("Optional initial shell command for create."), "working_dir_kind": map[string]any{"type": "string", "enum": []string{"auto", "local", "remote"}, "description": "auto follows workspace_mode; local requires a sync mapping; remote always uses the target filesystem."}, "working_dir": stringProperty("Directory interpreted according to workspace_mode and working_dir_kind for create."),
				"env":   map[string]any{"type": "object", "additionalProperties": stringProperty("Environment variable value.")},
				"input": stringProperty("Exact stdin text for send. Include a newline to execute a shell command; multiline PowerShell blocks need a final blank line."),
			}, "required": []string{"device_id", "action"},
		}},
	}, desktopToolDefinitions()...)
}

func callTool(ctx context.Context, client *controller.Client, raw json.RawMessage) (any, error) {
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&call); err != nil {
		return nil, err
	}
	if call.Arguments == nil {
		call.Arguments = map[string]any{}
	}
	if value, exists := call.Arguments["env"]; exists {
		values, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("env must be an object of string values")
		}
		for key, item := range values {
			if _, ok := item.(string); !ok {
				return nil, fmt.Errorf("env.%s must be a string", key)
			}
		}
	}
	switch call.Name {
	case "device_desktop", "device_capture", "device_input":
		return callDesktop(ctx, client, call.Name, call.Arguments)
	case "device_list":
		devices, err := client.Devices(ctx)
		return toolJSON(agentDeviceList(devices)), err
	case "device_exec":
		id, err := requiredString(call.Arguments, "device_id")
		if err != nil {
			return nil, err
		}
		command, err := requiredString(call.Arguments, "command")
		if err != nil {
			return nil, err
		}
		req := protocol.ExecRequest{Command: command, WorkingDir: optionalString(call.Arguments, "working_dir"), WorkingDirKind: optionalString(call.Arguments, "working_dir_kind"), Env: stringMap(call.Arguments["env"])}
		req.TimeoutMS, err = optionalInt64(call.Arguments, "timeout_ms")
		if err != nil {
			return nil, err
		}
		result, err := client.Exec(ctx, id, req)
		return toolJSON(result), err
	case "device_sync":
		id, err := requiredString(call.Arguments, "device_id")
		if err != nil {
			return nil, err
		}
		source, err := requiredString(call.Arguments, "source_path")
		if err != nil {
			return nil, err
		}
		mode := optionalString(call.Arguments, "mode")
		if mode == "" {
			mode = protocol.SyncModeUpdate
		}
		useGitIgnore, err := optionalBool(call.Arguments, "use_gitignore", true)
		if err != nil {
			return nil, err
		}
		dryRun, err := optionalBool(call.Arguments, "dry_run", false)
		if err != nil {
			return nil, err
		}
		result, err := client.Sync(ctx, id, filesync.Options{SourcePath: source, Root: optionalString(call.Arguments, "root"), Mode: mode, UseGitIgnore: useGitIgnore, Exclude: stringSlice(call.Arguments["exclude"]), DryRun: dryRun})
		return toolJSON(result), err
	case "device_pull":
		id, err := requiredString(call.Arguments, "device_id")
		if err != nil {
			return nil, err
		}
		paths := stringSlice(call.Arguments["paths"])
		if path := optionalString(call.Arguments, "path"); path != "" {
			paths = append(paths, path)
		}
		if len(paths) == 0 {
			return nil, errors.New("device_pull requires path or paths")
		}
		files := make([]protocol.ReadResponse, 0, len(paths))
		for _, path := range paths {
			file, readErr := client.ReadFile(ctx, id, path)
			if readErr != nil {
				return nil, readErr
			}
			files = append(files, file)
		}
		return toolJSON(files), nil
	case "device_task":
		return callTask(ctx, client, call.Arguments)
	case "device_session":
		return callSession(ctx, client, call.Arguments)
	default:
		return nil, fmt.Errorf("unknown tool: %s", call.Name)
	}
}

func callTask(ctx context.Context, client *controller.Client, args map[string]any) (any, error) {
	id, err := requiredString(args, "device_id")
	if err != nil {
		return nil, err
	}
	action := optionalString(args, "action")
	if action == "" {
		return nil, errors.New("device_task requires action")
	}
	switch action {
	case "start":
		command, commandErr := requiredString(args, "command")
		if commandErr != nil {
			return nil, commandErr
		}
		timeout, timeoutErr := optionalInt64(args, "timeout_ms")
		if timeoutErr != nil {
			return nil, timeoutErr
		}
		task, taskErr := client.StartTask(ctx, id, protocol.TaskRequest{ExecRequest: protocol.ExecRequest{Command: command, WorkingDir: optionalString(args, "working_dir"), WorkingDirKind: optionalString(args, "working_dir_kind"), Env: stringMap(args["env"]), TimeoutMS: timeout}})
		return toolJSON(task), taskErr
	case "list":
		tasks, listErr := client.ListTasks(ctx, id)
		return toolJSON(tasks), listErr
	case "get", "retrieve":
		taskID, taskErr := requiredString(args, "task_id")
		if taskErr != nil {
			return nil, taskErr
		}
		var task protocol.Task
		var getErr error
		if action == "retrieve" {
			task, getErr = client.RetrieveTask(ctx, id, taskID)
		} else {
			task, getErr = client.GetTask(ctx, id, taskID)
		}
		return toolJSON(task), getErr
	case "cancel":
		taskID, taskErr := requiredString(args, "task_id")
		if taskErr != nil {
			return nil, taskErr
		}
		if cancelErr := client.CancelTask(ctx, id, taskID); cancelErr != nil {
			return nil, cancelErr
		}
		task, getErr := client.GetTask(ctx, id, taskID)
		return toolJSON(task), getErr
	default:
		return nil, fmt.Errorf("unsupported device_task action: %s", action)
	}
}

func callSession(ctx context.Context, client *controller.Client, args map[string]any) (any, error) {
	id, err := requiredString(args, "device_id")
	if err != nil {
		return nil, err
	}
	action, err := requiredString(args, "action")
	if err != nil {
		return nil, err
	}
	switch action {
	case "create":
		result, err := client.CreateSession(ctx, id, protocol.SessionRequest{Command: optionalString(args, "command"), WorkingDir: optionalString(args, "working_dir"), WorkingDirKind: optionalString(args, "working_dir_kind"), Env: stringMap(args["env"])})
		return toolJSON(result), err
	case "list":
		result, err := client.ListSessions(ctx, id)
		return toolJSON(result), err
	case "get", "send", "close":
		sessionID, err := requiredString(args, "session_id")
		if err != nil {
			return nil, err
		}
		var result protocol.Session
		switch action {
		case "get":
			result, err = client.GetSession(ctx, id, sessionID)
		case "close":
			result, err = client.CloseSession(ctx, id, sessionID)
		case "send":
			input, ok := args["input"].(string)
			if !ok {
				return nil, errors.New("input must be a string")
			}
			result, err = client.WriteSession(ctx, id, sessionID, input)
		}
		return toolJSON(result), err
	default:
		return nil, fmt.Errorf("unsupported device_session action: %s", action)
	}
}

func requiredString(args map[string]any, key string) (string, error) {
	value := optionalString(args, key)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}
func optionalBool(args map[string]any, key string, defaultValue bool) (bool, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return defaultValue, nil
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return result, nil
}
func optionalString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}
func optionalInt64(args map[string]any, key string) (int64, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return 0, nil
	}
	switch number := value.(type) {
	case float64:
		if number < 1 || number != float64(int64(number)) {
			return 0, fmt.Errorf("%s must be a positive integer", key)
		}
		return int64(number), nil
	case json.Number:
		parsed, err := number.Int64()
		if err != nil || parsed < 1 || parsed > (1<<63-1)/1000000 {
			return 0, fmt.Errorf("%s must be a positive integer", key)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
}
func stringMap(value any) map[string]string {
	result := map[string]string{}
	values, ok := value.(map[string]any)
	if !ok {
		return result
	}
	for key, item := range values {
		if text, ok := item.(string); ok {
			result[key] = text
		}
	}
	return result
}
func stringSlice(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, text)
		}
	}
	return result
}
func toolJSON(value any) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": mustJSON(value)}}}
}

func agentDeviceList(devices []protocol.Device) []map[string]any {
	return controller.AgentDevices(devices)
}

func toolError(err error) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}
}
func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return strings.TrimSpace(string(data))
}

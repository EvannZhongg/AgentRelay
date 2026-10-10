package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"AgentRelay/internal/bridge"
	"AgentRelay/internal/protocol"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type rpcResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

const (
	currentProtocolVersion = "2025-06-18"
	serverName             = "AgentRelay"
	serverVersion          = "0.1.0"
)

func main() {
	base := flag.String("bridge", "http://127.0.0.1:8764", "bridge URL")
	flag.Parse()
	client := bridge.NewClient(*base)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			_ = encoder.Encode(rpcResponse{JSONRPC: "2.0", Error: map[string]any{"code": -32700, "message": err.Error()}})
			continue
		}
		if req.ID == nil && strings.HasPrefix(req.Method, "notifications/") {
			continue
		}
		result, err := handle(context.Background(), client, req)
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		if err != nil {
			resp.Error = map[string]any{"code": -32000, "message": err.Error()}
		} else {
			resp.Result = result
		}
		_ = encoder.Encode(resp)
	}
}

func handle(ctx context.Context, c *bridge.Client, req rpcRequest) (any, error) {
	switch req.Method {
	case "initialize":
		return initializeResult(req.Params), nil
	case "notifications/initialized":
		return map[string]any{}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": []any{
			map[string]any{"name": "list_devices", "description": "List registered AgentRelay devices", "inputSchema": map[string]any{"type": "object"}},
			map[string]any{"name": "exec", "description": "Execute a command in a device workspace", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"device_id": map[string]string{"type": "string"}, "command": map[string]string{"type": "string"}, "working_dir": map[string]string{"type": "string"}}, "required": []string{"device_id", "command"}}},
			map[string]any{"name": "sync_files", "description": "Synchronize base64 encoded files to a device", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"device_id": map[string]string{"type": "string"}, "root": map[string]string{"type": "string"}, "files": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]string{"type": "string"}, "content_base64": map[string]string{"type": "string"}}, "required": []string{"path", "content_base64"}}}}, "required": []string{"device_id", "files"}}},
		}}, nil
	case "tools/call":
		result, err := callTool(ctx, c, req.Params)
		if err != nil {
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}, nil
		}
		return result, nil
	default:
		return nil, fmt.Errorf("method not found: %s", req.Method)
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
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]string{"name": serverName, "version": serverVersion},
	}
}

func callTool(ctx context.Context, c *bridge.Client, raw json.RawMessage) (any, error) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	switch p.Name {
	case "list_devices":
		d, err := c.Devices(ctx)
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": mustJSON(d)}}}, err
	case "exec":
		id, _ := p.Arguments["device_id"].(string)
		command, _ := p.Arguments["command"].(string)
		dir, _ := p.Arguments["working_dir"].(string)
		if strings.TrimSpace(id) == "" || strings.TrimSpace(command) == "" {
			return nil, fmt.Errorf("exec requires device_id and command")
		}
		result, err := c.Exec(ctx, id, protocol.ExecRequest{Command: command, WorkingDir: dir})
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": mustJSON(result)}}}, err
	case "sync_files":
		id, _ := p.Arguments["device_id"].(string)
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("sync_files requires device_id")
		}
		root, _ := p.Arguments["root"].(string)
		rawFiles, _ := json.Marshal(p.Arguments["files"])
		var files []protocol.FileEntry
		if err := json.Unmarshal(rawFiles, &files); err != nil {
			return nil, err
		}
		for i := range files {
			if _, err := base64.StdEncoding.DecodeString(files[i].ContentBase64); err != nil {
				return nil, fmt.Errorf("files[%d] content_base64: %w", i, err)
			}
		}
		result, err := c.Sync(ctx, id, protocol.SyncRequest{Root: root, Files: files})
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": mustJSON(result)}}}, err
	default:
		return nil, fmt.Errorf("unknown tool: %s", p.Name)
	}
}
func mustJSON(v any) string { data, _ := json.Marshal(v); return strings.TrimSpace(string(data)) }

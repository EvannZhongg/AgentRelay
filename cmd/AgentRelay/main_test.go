package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPConfig(t *testing.T) {
	config := mcpConfig{Servers: map[string]mcpServerConfig{"agent-relay": {Command: "AgentRelay-mcp", Args: []string{"--bridge", "http://127.0.0.1:8764"}}}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"mcpServers"`) || !strings.Contains(string(data), `"AgentRelay-mcp"`) {
		t.Fatalf("unexpected config: %s", data)
	}
}

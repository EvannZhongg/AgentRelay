package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPConfig(t *testing.T) {
	config := mcpConfig{Servers: map[string]mcpServerConfig{"devfreight": {Command: "DevFreight-mcp", Args: []string{"--controller", "https://localhost:8764"}}}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"mcpServers"`) || !strings.Contains(string(data), `"DevFreight-mcp"`) {
		t.Fatalf("unexpected config: %s", data)
	}
}

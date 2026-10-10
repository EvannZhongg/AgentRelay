package main

import (
	"context"
	"encoding/json"
	"testing"

	"AgentRelay/internal/bridge"
)

func TestMCPInitializeNegotiatesSupportedVersion(t *testing.T) {
	params, _ := json.Marshal(map[string]string{"protocolVersion": "2025-03-26"})
	result, err := handle(context.Background(), bridge.NewClient("http://127.0.0.1:1"), rpcRequest{Method: "initialize", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	got := result.(map[string]any)["protocolVersion"]
	if got != "2025-03-26" {
		t.Fatalf("protocol version = %v", got)
	}
}

func TestMCPPing(t *testing.T) {
	result, err := handle(context.Background(), bridge.NewClient("http://127.0.0.1:1"), rpcRequest{Method: "ping"})
	if err != nil || result == nil {
		t.Fatalf("ping result = %#v, error = %v", result, err)
	}
}

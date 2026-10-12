package main

import (
	"encoding/json"
	"strings"
	"testing"

	"AgentRelay/internal/protocol"
)

func TestAgentDeviceListingOmitsConnectionConfiguration(t *testing.T) {
	d := protocol.Device{ID: "private-device", Handle: "dev-example", Name: "computer", ActiveConnection: "private-connection", Capabilities: protocol.Capabilities{Exec: true}, Connections: []protocol.Connection{{ID: "private-connection", Kind: "ssh", SSH: &protocol.SSHConfig{Host: "private-host", User: "private-user", IdentityFile: "private-key-file"}}}}
	data, err := json.Marshal(agentDeviceList([]protocol.Device{d}))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-device", "private-connection", "private-host", "private-user", "private-key-file", "active_connection", "connections"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("agent learned connection configuration: %s", data)
		}
	}
	if !strings.Contains(string(data), "dev-example") || !strings.Contains(string(data), "capabilities") {
		t.Fatal("agent identity or capabilities missing")
	}
}

package controller

import (
	"AgentRelay/internal/protocol"
	"testing"
)

func TestResolveDeviceSelector(t *testing.T) {
	devices := []protocol.Device{{ID: "z", Handle: "dev-z", Name: "Build"}, {ID: "a", Handle: "dev-a", Name: "Laptop"}}
	if got, err := ResolveDeviceSelector(devices, "dev-z"); err != nil || got != "z" {
		t.Fatalf("handle = %q, %v", got, err)
	}
	if got, err := ResolveDeviceSelector(devices, "laptop"); err != nil || got != "a" {
		t.Fatalf("alias = %q, %v", got, err)
	}
	if _, err := ResolveDeviceSelector([]protocol.Device{{ID: "a", Name: "same"}, {ID: "b", Name: "same"}}, "same"); err == nil {
		t.Fatal("ambiguous alias was accepted")
	}
	if got, err := ResolveDeviceSelector([]protocol.Device{{ID: "a", Handle: "dev-b", Name: "dev-c"}, {ID: "b", Handle: "dev-c", Name: "other"}}, "dev-c"); err != nil || got != "b" {
		t.Fatalf("handle must take precedence over alias: %q, %v", got, err)
	}
}

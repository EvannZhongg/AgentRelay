package main

import (
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"
	"strings"
	"testing"
)

func TestWorkerRejectsUnpinnedManualSetup(t *testing.T) {
	err := workerCommand([]string{"setup", "--controller", "https://localhost:8764", "--workspace", t.TempDir(), "--name", "worker"})
	if err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("unpinned setup: %v", err)
	}
}

func TestWorkerRejectsOldCredentialsWithoutMigration(t *testing.T) {
	t.Setenv("AGENTRELAY_HOME", t.TempDir())
	if err := state.Save(state.Path("worker"), protocol.Credentials{Schema: 2, DeviceID: "old"}); err != nil {
		t.Fatal(err)
	}
	err := workerCommand([]string{"serve"})
	if err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("old credentials: %v", err)
	}
}

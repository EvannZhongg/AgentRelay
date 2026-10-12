package backend

import (
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/transport"
	_ "embed"
)

// Execution belongs to the capability backend. SSH itself only carries RPC.
//
//go:embed ssh_executor.py
var sshExecutor string

func SSHCaller(config protocol.SSHConfig, workspace string) Call {
	return (transport.SSH{Config: config, Workspace: workspace, Program: sshExecutor}).Call
}

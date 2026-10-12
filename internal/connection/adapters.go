// Package connection binds transport configuration to optional capabilities.
// Controller routing depends on Adapter, never on SSH or a WebSocket peer.
package connection

import (
	"errors"

	"AgentRelay/internal/backend"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/transport"
)

type Adapter interface {
	Validate(protocol.ConnectionSpec) error
	Capabilities() protocol.Capabilities
	Probe() transport.Request
	Bind(protocol.Connection, backend.Call) (backend.Call, error)
}

// Registry is configured once when constructing the Controller. Additional
// adapters can provide capabilities without changing MCP or operation routing.
type Registry map[string]Adapter

func Defaults() Registry { return Registry{"ssh": SSH{}, "worker": Worker{}} }

type SSH struct{}

func (SSH) Probe() transport.Request { return transport.Request{Method: "GET", Path: "/probe"} }
func (SSH) Validate(c protocol.ConnectionSpec) error {
	if len(c.Options) != 0 {
		return errors.New("SSH uses ssh configuration, not adapter options")
	}
	if c.SSH == nil {
		return errors.New("SSH configuration is required")
	}
	return transport.ValidateSSH(*c.SSH, c.Workspace.Root)
}
func (SSH) Capabilities() protocol.Capabilities {
	return protocol.Capabilities{Exec: true, Files: true, Shells: []string{"sh"}, MaxPayload: 32 << 20}
}
func (SSH) Bind(c protocol.Connection, _ backend.Call) (backend.Call, error) {
	if c.SSH == nil {
		return nil, errors.New("SSH configuration missing")
	}
	return backend.SSHCaller(*c.SSH, c.Workspace.Root), nil
}

type Worker struct{}

func (Worker) Probe() transport.Request { return transport.Request{Method: "GET", Path: "/v1/device"} }
func (Worker) Validate(c protocol.ConnectionSpec) error {
	if len(c.Options) != 0 {
		return errors.New("Worker does not accept adapter options")
	}
	if c.SSH != nil {
		return errors.New("Worker connection cannot contain SSH configuration")
	}
	return nil
}
func (Worker) Capabilities() protocol.Capabilities { return protocol.Capabilities{} }
func (Worker) Bind(_ protocol.Connection, live backend.Call) (backend.Call, error) {
	if live == nil {
		return nil, errors.New("Worker connection is offline")
	}
	return live, nil
}

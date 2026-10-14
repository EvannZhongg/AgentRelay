// Package connection binds transport configuration to optional capabilities.
// Controller routing depends on Adapter, never on SSH or a WebSocket peer.
package connection

import (
	"context"
	"encoding/json"
	"errors"

	"DevFreight/internal/backend"
	"DevFreight/internal/protocol"
	"DevFreight/internal/transport"
)

type Adapter interface {
	Validate(protocol.ConnectionSpec) error
	Capabilities() protocol.Capabilities
	Probe() transport.Request
	Bind(protocol.Connection, backend.Call) (backend.Call, error)
	CheckIdle(context.Context, protocol.Connection, backend.Call) error
}

// Registry is configured once when constructing the Controller. Additional
// adapters can provide capabilities without changing MCP or operation routing.
type Registry map[string]Adapter

func Defaults() Registry { return Registry{"ssh": SSH{}, "worker": Worker{}} }

type SSH struct{}

// SSH executes synchronous calls only; the Controller guards their lifetime.
func (SSH) CheckIdle(context.Context, protocol.Connection, backend.Call) error { return nil }

func (SSH) Probe() transport.Request { return transport.Request{Method: "GET", Path: "/probe"} }
func (SSH) Validate(c protocol.ConnectionSpec) error {
	if len(c.Options) != 0 {
		return errors.New("SSH uses ssh configuration, not adapter options")
	}
	if c.SSH == nil {
		return errors.New("SSH configuration is required")
	}
	return backend.ValidateSSH(*c.SSH, c.Workspace.Root)
}
func (SSH) Capabilities() protocol.Capabilities {
	// The probe reports the actual target shell; it is unknown before testing.
	return protocol.Capabilities{Exec: true, Files: true, MaxPayload: 32 << 20}
}
func (SSH) Bind(c protocol.Connection, _ backend.Call) (backend.Call, error) {
	if c.SSH == nil {
		return nil, errors.New("SSH configuration missing")
	}
	return backend.SSHCaller(*c.SSH, c.Workspace.Root), nil
}

type Worker struct{}

func (Worker) CheckIdle(ctx context.Context, c protocol.Connection, live backend.Call) error {
	if !c.Online || live == nil {
		return errors.New("reconnect Worker before changing its connection; activity cannot be verified while offline")
	}
	resp, err := live(ctx, transport.Request{Method: "GET", Path: "/v1/activity"})
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return errors.New("could not verify Worker activity")
	}
	var activity protocol.Activity
	if err := json.Unmarshal(resp.Body, &activity); err != nil {
		return err
	}
	if activity.Busy() {
		return errors.New("finish active requests, tasks, sessions and syncs before changing connection")
	}
	return nil
}

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

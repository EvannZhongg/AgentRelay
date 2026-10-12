# Development

This document is for contributors. User setup and day-to-day commands belong in the [README](../README.md).

## Repository Layout

~~~text
cmd/AgentRelay       controller CLI
cmd/AgentRelay-mcp   stdio MCP adapter
internal/bridge      HTTPS controller, pairing and routing
internal/backend     optional device capability interfaces and RPC adapters
internal/filesync    controller-side scanning, ignore evaluation and hashing
internal/worker      Worker lifecycle, shell, files, tasks and sessions
internal/desktop     native desktop backends and validation
internal/protocol    shared request/response contracts
internal/transport   authenticated WebSocket RPC and heartbeats
internal/discovery   LAN controller discovery
internal/state       persisted controller and Worker state
docs/                operator and contributor documentation
reference/            upstream reference projects and experiments
~~~

## Local Build And Tests

Use Go 1.25 or the version declared by the module. From the repository root:

~~~powershell
go test ./...
go test -race ./...
go vet ./...
go build -o AgentRelay.exe ./cmd/AgentRelay
go build -ldflags "-H=windowsgui" -o AgentRelay-mcp.exe ./cmd/AgentRelay-mcp
~~~

Windows PowerShell is the tested execution environment. The POSIX shell fallback is experimental. The MCP executable uses stdio; the Windows GUI linker flag prevents an extra console window when an agent launches it.

## Implementation Boundaries

- Keep the shared wire contract in `internal/protocol`; do not duplicate request structs in the CLI, MCP adapter or platform backends.
- Keep transport concerns in `internal/transport`. Worker operations run through the authenticated connection and must not add a Worker listening port.
- Keep native OS calls behind `internal/desktop.Backend`. Platform code handles OS details; the service owns validation, coordinate conversion and serialization.
- Keep controller state changes atomic and preserve the existing authorization checks for every device operation.
- Treat the controller workspace as the source of truth. Remote paths must remain below the authorized Worker workspace.

## Adding A Remote Operation

1. Define or update the request and response types in `internal/protocol`.
2. Add a capability interface/implementation and the Worker or SSH executor operation. Advertise only supported capabilities and route through the selected connection's capability set.
3. Add the Bridge client method and local-only controller endpoint when needed.
4. Expose the operation in the CLI and/or MCP adapter with matching validation and error behavior.
5. Add unit and lifecycle coverage for authorization, disconnects, path boundaries and persistence as applicable.
6. Update `docs/architecture.md` and the relevant user-facing README section if the operation changes behavior.

## Desktop Changes

Read [Desktop access](desktop.md) before changing the desktop contract. Test coordinate units, preview scaling, permission errors, cancellation and reconnect behavior. Keep screenshot limits and artifact handling in the Bridge rather than in a platform backend.

## Change Checklist

- Run `gofmt` on changed Go files.
- Run `go test ./...`, `go test -race ./...` and `go vet ./...`.
- Check that CLI help, MCP tool schemas and documentation agree.
- Verify that new endpoints preserve loopback and device authorization rules.
- Avoid committing generated executables, local state, captures or `goal.md`.

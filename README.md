# AgentRelay

AgentRelay is a small bridge between a coding agent running on a control computer and native execution environments on other computers. The first implementation uses one JSON/HTTP RPC protocol over IP for both LAN and future USB networking. It contains no agent loop or model runtime.

## First-phase architecture

```text
Coding Agent -- MCP stdio or CLI --> Bridge (:8764)
                                      |
                                      | JSON/HTTP v1
                                      v
                                  Worker (:8765)
                                      |
                         workspace-scoped files and shell
```

The shared contract is in `internal/protocol`. The Bridge keeps the registered device catalog in memory and proxies calls. The Worker exposes the same API regardless of the transport and selects a native command runner at runtime. On Windows it runs PowerShell (`powershell.exe`); the shared service also has a POSIX shell fallback for development and future workers. A worker workspace is the boundary for all remote file paths and working directories.

The current registration flow is intentionally explicit: the operator starts a Worker, then adds its HTTP URL to the Bridge. Discovery, pairing, authentication, persistence, incremental sync and multi-device scheduling are later layers over this contract.

## Build

```powershell
go test ./...
go vet ./...
go build -o AgentRelay.exe ./cmd/AgentRelay
go build -o AgentRelay-mcp.exe ./cmd/AgentRelay-mcp
```

## Windows-to-Windows smoke test

On the target Windows computer:

```powershell
AgentRelay worker serve --workspace C:\AgentRelay\workspace --listen :8765
```

On the control Windows computer:

```powershell
AgentRelay bridge serve --listen :8764
AgentRelay device add --worker http://TARGET_IP:8765
AgentRelay device list
AgentRelay exec --device <device-id> "dotnet test"
AgentRelay sync --device <device-id> --source . --remote project
AgentRelay exec --device <device-id> --dir project "dotnet test"
```

The Worker API also supports asynchronous jobs:

```powershell
AgentRelay task start --device <device-id> "npm run build"
AgentRelay task get --device <device-id> --id task-1
```

## MCP

AgentRelay exposes a standard MCP stdio server. It negotiates the MCP protocol version during `initialize`, advertises tools through `tools/list`, and executes them through `tools/call`.

Generate a ready-to-paste configuration manually:

```powershell
AgentRelay mcp config --bridge http://127.0.0.1:8764
```

The output has this shape:

```json
{
  "mcpServers": {
    "agent-relay": {
      "command": "C:\\path\\to\\AgentRelay-mcp.exe",
      "args": [
        "--bridge",
        "http://127.0.0.1:8764"
      ]
    }
  }
}
```

Paste that object into the MCP JSON configuration used by the coding agent. The executable path can be overridden explicitly:

```powershell
AgentRelay mcp config --command C:\Tools\AgentRelay-mcp.exe --name agent-relay
```

The same adapter can also be started directly:

```text
AgentRelay-mcp --bridge http://127.0.0.1:8764
```

It exposes `list_devices`, `exec`, and `sync_files`. `sync_files` accepts file entries with `path` and base64 `content_base64`, allowing an agent integration to reuse the same Bridge API without a second transport protocol. The adapter uses newline-delimited JSON-RPC messages on stdin/stdout, as required by MCP stdio transport.

## API surface (v1)

Worker endpoints:

- `GET /v1/health`, `GET /v1/device`
- `POST /v1/exec`
- `POST /v1/files/sync`, `GET /v1/files/read?path=...`
- `POST /v1/tasks`, `GET /v1/tasks`, `GET/DELETE /v1/tasks/{id}`

Bridge endpoints mirror Worker execution endpoints under `/v1/devices/{device-id}/...`, with `GET/POST /v1/devices` for the in-memory catalog.

## Current limitations

The first phase deliberately leaves authentication, pairing approval, TLS, service installation, persistent device storage, LAN discovery, incremental hashing, interactive PTY streams and multi-device orchestration out of scope. Commands are authorized by whoever can reach the Worker URL, so expose the Worker only on a trusted development network until the authorization layer is added.

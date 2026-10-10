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

The shared contract is in `internal/protocol`. The Bridge keeps the registered device catalog in memory and proxies calls. The Worker exposes the same API regardless of the transport. Platform shell launch and process cleanup are isolated in `internal/worker/shell_windows.go` and `shell_other.go`; Windows PowerShell is the currently tested target, and the POSIX fallback is experimental. Windows cancellation and session close terminate the shell's process tree. A worker workspace is the boundary for all remote file paths and working directories.

The current registration flow is intentionally explicit: the operator starts a Worker, then adds its HTTP URL to the Bridge. Discovery, pairing, authentication, persistence, incremental sync and multi-device scheduling are later layers over this contract.

## Build

```powershell
go test ./...
go vet ./...
go build -o AgentRelay.exe ./cmd/AgentRelay
go build -ldflags "-H=windowsgui" -o AgentRelay-mcp.exe ./cmd/AgentRelay-mcp
```

`AgentRelay.exe` is a console program because it is driven from a terminal. `AgentRelay-mcp.exe` is built with the Windows GUI subsystem so an MCP client can start it as a child process without flashing a console window; it still speaks newline-delimited JSON-RPC over stdin and stdout.

## Launchers

`start-worker.bat`, `start-bridge.bat` and `mcp-config.bat` wrap the CLI for double-click use. They expect `AgentRelay.exe` in the same directory as the launcher, which is where the build above places it.

Double-clicking `AgentRelay.exe` directly is not useful: it prints usage and exits immediately, so the window vanishes. `AgentRelay-mcp.exe` is never started by hand, because an MCP client starts it over stdio.

- `start-worker.bat` accepts an optional workspace path; dragging a folder onto the file uses that folder as the worker workspace.
- `start-bridge.bat` accepts an optional listen address and defaults to `:8764`.
- `mcp-config.bat` prints the MCP configuration JSON and accepts an optional bridge URL.

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

The CLI and MCP adapter support asynchronous jobs with the same lifecycle:

```powershell
AgentRelay task start --device <device-id> --dir project --env NODE_ENV=test --timeout-ms 60000 "npm run build"
AgentRelay task list --device <device-id>
AgentRelay task get --device <device-id> --id task-1
AgentRelay task retrieve --device <device-id> --id task-1
AgentRelay task cancel --device <device-id> --id task-2
```

`get` queries a task without removing it. `retrieve` atomically returns and deletes a finished task, including failed or cancelled tasks. It returns HTTP 409 while the process is still running or being cancelled, and HTTP 404 once the record is retrieved or expired. Poll `get` until `finished_at` is present before retrieving. Failed tasks include an `error` explaining launch errors, nonzero exit codes, or timeouts; execution details remain in `result`.

Persistent shell sessions and file retrieval are also available from the CLI:

```powershell
AgentRelay session create --device <device-id> --dir project --env APP_ENV=test
AgentRelay session list --device <device-id>
AgentRelay session send --device <device-id> --id session-1 --input "Write-Output 'hello'`r`n"
AgentRelay session send --device <device-id> --id session-1 --input-file commands.txt
AgentRelay session get --device <device-id> --id session-1
AgentRelay session close --device <device-id> --id session-1
AgentRelay pull --device <device-id> --out .\results project\report.txt project\build.log
```

`--env KEY=VALUE` is repeatable for `exec`, `task start`, and `session create`. Session input is sent exactly as supplied; `--input-file -` reads stdin. `pull` preserves workspace-relative paths under `--out`; without `--out` it prints base64 JSON. It also accepts `--path` for a single file.

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

It exposes these Agent tools:

- `device_list`: probe and list all registered workers with their capabilities, including offline entries with `online: false` and the last successful `last_seen` timestamp, consistently with the HTTP/CLI catalog.
- `device_exec`: execute a command with `working_dir`, `env`, and `timeout_ms`.
- `device_sync`: upload a local `source_path` or explicit base64 file entries.
- `device_pull`: read one or more remote workspace files and test results.
- `device_task`: start, list, query, retrieve, or cancel asynchronous tasks.
- `device_session`: create, list, poll, send input to, or close a persistent shell session. Session output is cumulative and is returned by `get`/`send`.

The adapter uses newline-delimited JSON-RPC messages on stdin/stdout, as required by MCP stdio transport. Sessions use a pipe-based native shell (`powershell` on Windows) and are intended for long-running commands and incremental stdin input; they do not provide terminal resize, ANSI/PTY semantics, or interactive password prompts.

Example `tools/call` arguments for sessions:

```json
{"name":"device_session","arguments":{"device_id":"<device-id>","action":"create","working_dir":"project"}}
{"name":"device_session","arguments":{"device_id":"<device-id>","action":"send","session_id":"session-1","input":"Write-Output 'hello'\r\n"}}
{"name":"device_session","arguments":{"device_id":"<device-id>","action":"get","session_id":"session-1"}}
{"name":"device_session","arguments":{"device_id":"<device-id>","action":"close","session_id":"session-1"}}
```

`send` returns after writing stdin; poll `get` for subsequent output. Input is preserved exactly, including whitespace and newlines. Multiline PowerShell blocks need a final blank line. For synchronous execution, tasks, and sessions, each output stream retains up to 4 MiB; `output_truncated` reports discarded excess. Task `get` retains the record; `retrieve` returns and deletes it after execution ends. Use `device_pull` for generated files (returned as base64).

Task and session records survive MCP client disconnection but are held in Worker memory and do not survive Worker restart. Each store holds at most 128 records, including active processes. Finished records expire one hour after completion; cleanup is lazy, on store access or creation (and task completion). Creating a record at capacity evicts the oldest finished records first. Active or not-yet-reaped processes are never evicted; when all slots are active, creation returns HTTP 409. Close sessions or cancel tasks and wait for process cleanup to free capacity. Session close retains its final output within this retention window.

## API surface (v1)

Worker endpoints:

- `GET /v1/health`, `GET /v1/device`
- `POST /v1/exec`
- `POST /v1/files/sync`, `GET /v1/files/read?path=...`
- `POST /v1/tasks`, `GET /v1/tasks`, `GET/DELETE /v1/tasks/{id}`
- `POST /v1/tasks/{id}/retrieve` (return and remove a finished task)
- `POST/GET /v1/sessions`, `GET/DELETE /v1/sessions/{id}`, `POST /v1/sessions/{id}/input`

Bridge endpoints mirror Worker execution endpoints under `/v1/devices/{device-id}/...`, with `GET/POST /v1/devices` for the in-memory catalog.

## Current limitations

The first phase deliberately leaves authentication, pairing approval, TLS, service installation, persistent device storage, LAN discovery, incremental hashing, interactive PTY streams and multi-device orchestration out of scope. Commands are authorized by whoever can reach the Worker URL, so expose the Worker only on a trusted development network until the authorization layer is added.

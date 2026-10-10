English | [简体中文](README.zh-CN.md)

# AgentRelay

AgentRelay connects a coding agent on a control computer to native execution environments on other computers. It contains no model runtime or agent loop.

```text
Coding Agent -- MCP stdio / CLI --> Bridge (:8764, HTTPS)
                                      ^
                                      | Worker establishes one persistent WSS connection
                                      | bidirectional, concurrent RPC
                                    Worker
                                      |
                         workspace files / native shell / desktop
```

Workers open no inbound ports. LAN, Ethernet and future USB IP networking use the same connection protocol. The Bridge persists paired devices; connection state and Worker process stores remain in memory.

## Build

```powershell
go test ./...
go test -race ./...
go vet ./...
go build -o AgentRelay.exe ./cmd/AgentRelay
go build -ldflags "-H=windowsgui" -o AgentRelay-mcp.exe ./cmd/AgentRelay-mcp
```

Windows PowerShell is the tested execution target. The POSIX shell fallback is experimental. The MCP executable uses stdio and is launched by the coding agent; the GUI subsystem prevents a flashing Windows console.

## First Setup

On both computers run `AgentRelay setup`, or double-click `AgentRelay.exe`. Choose **controller** on the coding agent's computer. The Bridge starts and waits for connection requests.

Choose **worker** on the target computer. Select the discovered controller, then choose a workspace folder and device alias. The Worker sends a connection request and shows a verification number. The controller shows the same number with the device name; approve or reject it there. After approval AgentRelay generates the device identity, saves credentials and starts the outbound connection automatically.

The default Bridge uses TCP 8764 and LAN discovery uses UDP 8763. Only the controller needs inbound access. The Worker must select a discovered controller so it can pin the advertised certificate fingerprint before the first HTTPS request. A manually entered URL without a discovered fingerprint is rejected; this prevents an unverified first request from establishing trust. The verification number is random for each request and is only a visual reference; users never copy certificate strings, pairing codes or tokens.

Pending requests expire after five minutes. A device is created and credentials are issued only after the controller approves the request.

Subsequent starts need no pairing or workspace arguments:

```powershell
# Controller
AgentRelay bridge serve
# Target computer
AgentRelay worker serve
```

`start-bridge.bat` and `start-worker.bat` wrap these commands. The Worker launcher opens setup on its first run; dragging a folder onto it runs a new pairing with that workspace. `AgentRelay worker setup` explicitly pairs again. Changing the authorized workspace requires a new pairing; ordinary restarts retain the existing device ID.

## Agent Access

On the controller, print the MCP configuration:

```powershell
AgentRelay mcp config
```

Add the returned `mcpServers` entry to the coding agent's configuration. `mcp-config.bat` also prints it. The adapter reads the controller's saved certificate identity automatically. Both the CLI and MCP adapter must run on the controller as the same user that runs the Bridge. The controller API is restricted to loopback callers; remote computers can only pair and establish authenticated Worker connections.

The tools are `device_list`, `device_exec`, `device_sync`, `device_pull`, `device_task`, `device_session`, `device_desktop`, `device_capture` and `device_input`. `device_list` includes saved aliases, authorization, capabilities, workspace mappings, offline devices and their last successful registration time.

## Desktop Access

Desktop access uses an independent native backend interface: Win32/GDI/SendInput on Windows, and system CoreGraphics plus `screencapture` on macOS. It has no RobotGo dependency and builds without CGO. Run the Worker in an interactive user session; on macOS grant Screen Recording and Accessibility permissions. Windows has been tested on a connected device; macOS arm64/amd64 builds have been checked, with runtime testing still required.

`device_desktop` reports permissions and monitors. `device_capture` returns a standard MCP PNG image, monitor metadata and a controller-local full-resolution Artifact path with dimensions and SHA256. Previews default to at most 1600×1200; original PNGs are saved under the Bridge state directory's `artifacts/captures` and require manual cleanup. Limits: 64M pixels and 12 MiB encoded PNG per capture.

`device_input` supports move, click, drag, scroll, key combinations and Unicode text. Global coordinates use physical pixels on Windows and points on macOS. Supply `display_id` for display-relative coordinates, and the capture's `width/height` as `reference_width/reference_height` to use preview pixels. Display indices may change after reconnecting monitors. Inputs affect the interactive desktop beyond the file workspace, are serialized and never replayed on reconnection.

```powershell
AgentRelay desktop info --device <id>
AgentRelay desktop capture --device <id> --display 0 --max-width 960 --max-height 540
AgentRelay desktop input --device <id> --json '{"action":"click","x":300,"y":204}'
AgentRelay desktop input --device <id> --json '{"action":"text","text":"Hello"}'
```

Update Bridge, Worker and the MCP executable, then reconnect the MCP client to refresh its tool list. See [Desktop architecture and contract](docs/desktop-capability.md) for details.

## Workflows

```powershell
AgentRelay device list
AgentRelay sync --device <id> --source C:\src\project
AgentRelay exec --device <id> --dir C:\src\project "dotnet test"
AgentRelay task start --device <id> --dir C:\src\project --env NODE_ENV=test --timeout-ms 60000 "npm run build"
AgentRelay task list --device <id>
AgentRelay task get --device <id> --id task-1
AgentRelay task retrieve --device <id> --id task-1
AgentRelay task cancel --device <id> --id task-2
```

Sync automatically assigns and persists a remote directory for the local project. Later syncs reuse it. Execute, task start and session create accept an absolute mapped local project directory or one of its subdirectories, as well as explicit remote workspace-relative paths. `sync --remote <path>` overrides the automatic mapping; the response includes the chosen `root`. For MCP, `device_sync` with `source_path` follows the same mapping flow; explicit `files` use the supplied remote `root`.

`get` polls a task without removing it. `retrieve` returns and deletes a completed, failed or cancelled task. A still-running task returns 409; a missing, expired or already retrieved task returns 404. Wait for `finished_at` before retrieving. Failed tasks include an `error` and available execution details in `result`.

```powershell
AgentRelay session create --device <id> --dir C:\src\project --env APP_ENV=test
AgentRelay session list --device <id>
AgentRelay session send --device <id> --id session-1 --input "Write-Output 'hello'`r`n"
AgentRelay session send --device <id> --id session-1 --input-file commands.txt
AgentRelay session get --device <id> --id session-1
AgentRelay session close --device <id> --id session-1
AgentRelay pull --device <id> --out .\results <remote-root>/report.txt
```

`--env KEY=VALUE` is repeatable for execution, task start and session create. Session input is sent unchanged; `--input-file -` reads stdin. `send` returns after writing; poll `get` for subsequent output. Sessions use native shell pipes, without PTY, terminal resizing or interactive password prompts. `pull` preserves remote relative paths under `--out`; without it, the result is base64 JSON.

## Device Management

```powershell
AgentRelay device rename --device <id> --name build-machine
AgentRelay device revoke --device <id>
AgentRelay device authorize --device <id>
AgentRelay device remove --device <id>
```

Revocation is saved before the active connection is closed and blocks subsequent connections and operations. After authorization is restored, restart the Worker with `worker serve`. Removal deletes the saved device and invalidates its credentials; pair again to use it.

## Persistence And Reconnection

State is stored in the current user's configuration directory under `AgentRelay` (`%APPDATA%\AgentRelay` on Windows), with atomic JSON replacement. `AGENTRELAY_HOME` can select a separate state directory for an entire CLI/MCP/Bridge installation. The Bridge stores its TLS identity, stable device IDs, credential hashes, aliases, authorization, workspace roots and local-to-remote project mappings. The Worker stores the Bridge URL, certificate fingerprint, device ID, credential and workspace. Credentials are not included in device listings.

After a Bridge restart, saved devices initially appear offline and Workers reconnect using their saved identities, with exponential retry delays up to 30 seconds. Reconnecting refreshes capabilities without replacing aliases or project mappings. The saved Bridge address must remain reachable; if its address changes, use Worker setup to pair at the new address.

Async tasks and shell sessions survive MCP disconnection, network reconnection and Bridge restarts while the same Worker process remains alive. Worker restarts keep the device identity and workspace but start with empty task/session stores; running processes are not reconstructed. Task metadata is currently memory-only. Synchronous execution is cancelled when its RPC connection is lost. Failed or interrupted RPC calls are never automatically replayed, because execution may have already started.

Each task/session store retains at most 128 records. Finished records expire after one hour and can be evicted to make room; active processes are never evicted. Full stores return 409. Each output stream retains up to 4 MiB and reports `output_truncated`. Windows cancellation and session close terminate the process tree; orderly Worker shutdown cancels tasks and closes sessions.

## API And Scope

The shared contract lives in `internal/protocol`; `internal/transport` implements multiplexed request/response/cancel messages and heartbeats over WebSocket. Worker operation handlers are invoked inside that transport and are never served on a Worker listening port.

Bridge HTTPS endpoints:

- `GET /v1/health`
- `POST /v1/pair`, `GET /v1/pair/{request-id}`, `GET /v1/connect` (authenticated WebSocket upgrade)
- Local only: `GET /v1/pair-requests`, `POST /v1/pair-requests/{request-id}/approve|reject`, `GET /v1/devices`, `PATCH/DELETE /v1/devices/{id}`
- Local only: `/v1/devices/{id}/exec`, `/files/sync`, `/files/read`, `/tasks`, `/tasks/{task-id}`, `/tasks/{task-id}/retrieve`, `/sessions`, `/sessions/{session-id}`, `/sessions/{session-id}/input`
- Local only: `GET /v1/devices/{id}/desktop`, `POST /v1/devices/{id}/desktop/capture`, `POST /v1/devices/{id}/desktop/input`

`PATCH` accepts `name`, `authorized` and `workspace_mappings` (absolute controller paths to Worker-relative directories). Worker URL registration, Worker listening flags and the previous device-ID derivation have been removed. There is no old-data migration or compatibility layer. Service installation, incremental sync, PTY and multi-device scheduling remain outside the current implementation.

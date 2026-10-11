English | [简体中文](README.zh-CN.md)

# AgentRelay

AgentRelay gives a coding agent a secure, controlled connection to native execution environments on other computers. The agent stays on the controller; a lightweight Worker runs commands, syncs files, manages tasks and sessions, and can optionally control an interactive desktop.

```text
Coding agent -- MCP stdio / CLI --> Bridge (controller, HTTPS)
                                      ^
                                      | outbound, authenticated WSS
                                    Worker (target computer)
                                      |
                           workspace / shell / desktop
```

The Worker opens no inbound port. LAN, Ethernet and future IP-over-USB connections use the same protocol. AgentRelay does not include a model runtime or an agent loop.

## What You Need

- A Windows controller running the coding agent. Windows PowerShell is the tested target.
- A target computer with the AgentRelay Worker and a workspace directory.
- Network reachability from the Worker to the controller. The controller accepts inbound TCP `8764`; discovery uses UDP `8763`.
- macOS desktop access additionally needs Screen Recording and Accessibility permissions. POSIX shell support and macOS runtime support are experimental.

## Quick Start

Build the two executables from the repository, or use the matching binaries in a release:

```powershell
go build -o AgentRelay.exe ./cmd/AgentRelay
go build -ldflags "-H=windowsgui" -o AgentRelay-mcp.exe ./cmd/AgentRelay-mcp
```

1. On the controller, run `AgentRelay gui` to start the Bridge, enable LAN discovery, and open the device management UI. `AgentRelay setup` with `controller` remains available for terminal-only operation.
2. On the target computer, run `AgentRelay setup` and choose `worker`. Select the discovered controller, choose a workspace folder and set a device alias.
3. Compare the verification number shown on both computers, then approve the request in the controller terminal. The Worker saves its credentials and reconnects automatically.
4. On later starts, use `AgentRelay bridge serve` on the controller and `AgentRelay worker serve` on the target. `start-bridge.bat` and `start-worker.bat` are Windows shortcuts.

Pairing requests expire after five minutes. A device and credentials are created only after approval. Pairing must use a discovered controller so the Worker can pin its certificate fingerprint; an unverified manual URL is rejected.

## Connect Your Agent

Run this on the controller, as the same user that runs the Bridge:

```powershell
AgentRelay mcp config
```

Copy the returned `mcpServers` entry into the coding agent's MCP configuration. `mcp-config.bat` prints the same JSON. Restart or reconnect the MCP client after upgrading AgentRelay so its tool list is refreshed.

Available MCP tools are `device_list`, `device_exec`, `device_sync`, `device_pull`, `device_task`, `device_session`, `device_desktop`, `device_capture` and `device_input`.

## Common Operations

Find paired devices, sync a project, run a command and collect a result:

```powershell
AgentRelay device list
AgentRelay sync --device build-machine --source C:\src\project
AgentRelay sync --device build-machine --source C:\src\project --mode mirror --exclude .cache/
AgentRelay exec --device build-machine --dir C:\src\project "dotnet test"
AgentRelay task start --device build-machine --dir C:\src\project --timeout-ms 60000 "npm run build"
AgentRelay task get --device build-machine --id <task-id>
AgentRelay task retrieve --device build-machine --id <task-id>
AgentRelay pull --device build-machine --out .\results <remote-root>\report.txt
```

`device_list` exposes a persistent short handle generated at pairing time (for example `dev-a1b2c3d4`), aliases, status and capabilities, without exposing private device IDs. The required `device_id` argument accepts either a handle or an alias. Disconnects, reconnects, and adding or removing devices do not change the handle.

Use sessions for long-lived shell processes:

```powershell
AgentRelay session create --device <id> --dir C:\src\project
AgentRelay session send --device <id> --id <session-id> --input "Write-Output 'hello'`r`n"
AgentRelay session get --device <id> --id <session-id>
AgentRelay session close --device <id> --id <session-id>
```

Remote paths are relative to the Worker's authorized workspace. `sync` compares SHA-256 manifests and uploads changed files in bounded chunks, so directories and individual files are not limited by the 32 MiB RPC payload cap. It applies nested `.gitignore` files and a root `.agentrelayignore` by default; repeat `--exclude` for additional gitignore-compatible patterns or use `--no-gitignore`. `--mode update` keeps remote extras, while `--mode mirror` removes only files managed by an earlier sync and now absent locally. `--dry-run` reports the plan without changing the Worker. A successful sync persists the local-project-to-remote-root mapping, so later commands can use the same local project directory. Tasks and sessions are in memory and are not reconstructed after a Worker process restart.

Manage authorization without pairing again:

```powershell
AgentRelay device rename --device dev-a1b2c3d4 --name build-machine
AgentRelay device revoke --device build-machine
AgentRelay device authorize --device build-machine
AgentRelay device remove --device build-machine
```

## Desktop Access

The optional desktop tools inspect displays, capture PNG screenshots and send mouse/keyboard input. Run the Worker in the logged-in interactive session. Coordinates are physical pixels on Windows and points on macOS; use capture metadata when mapping preview pixels to native coordinates.

```powershell
AgentRelay desktop info --device <id>
AgentRelay desktop capture --device <id> --display 0 --max-width 960 --max-height 540
AgentRelay desktop input --device <id> --json '{"action":"click","x":300,"y":204}'
```

See [Desktop access](docs/desktop.md) for permissions, coordinate mapping, limits and the MCP/API contract.

## Documentation

| Document | Audience | Contents |
| --- | --- | --- |
| [Architecture](docs/architecture.md) | Operators and integrators | Components, trust model, persistence, lifecycle and API scope |
| [Development](docs/development.md) | Contributors | Repository layout, build/test commands, implementation boundaries and change checklist |
| [Desktop access](docs/desktop.md) | Desktop feature users and maintainers | Native backends, screenshot artifacts, input schema and platform validation |

## Security Notes

The controller's management API is loopback-only. Workers make the outbound connection and are authenticated with credentials issued during approved pairing. File operations stay under the authorized workspace; desktop input intentionally affects the full interactive desktop and should be treated as privileged automation.

For implementation details, compatibility limits and endpoint listings, start with [Architecture](docs/architecture.md). For a code change, follow [Development](docs/development.md).

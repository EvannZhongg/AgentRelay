English | [简体中文](README.zh-CN.md)

# AgentRelay

AgentRelay gives a coding agent a secure, controlled connection to native execution environments on other computers. The agent stays on the controller and sees devices with explicit capabilities. Each device can select SSH or a native Worker as its execution connection.

```text
Coding agent -- MCP stdio / CLI --> Controller (HTTPS)
                                      | selected device connection
                        +-------------+-------------+
                        |                           |
                  SSH + native shell         Native Worker (WSS)
                  shell / file reads         shell / files / tasks
                                             sessions / desktop
```

The Worker opens no inbound port. LAN, Ethernet and future IP-over-USB connections use the same protocol. AgentRelay does not include a model runtime or an agent loop.

## What You Need

- A Windows, macOS or Linux controller running the coding agent and AgentRelay Controller.
- A Windows, macOS or Linux target with a workspace directory and either SSH + native system shell/tools or the native AgentRelay Worker. SSH needs no Python or additional language runtime; see [requirements](docs/ssh.md).
- Network reachability from the Worker to the controller. The controller accepts inbound TCP `8764`; discovery uses UDP `8763`.
- Worker targets run one native executable and need no Python, Node.js, LLM or Agent Runtime. macOS desktop access needs Screen Recording and Accessibility permissions; Linux desktop is currently unsupported.

## Quick Start

Build the Controller/MCP programs on the controller and the standalone Worker for its target, or use release binaries:

```powershell
go build -o AgentRelay.exe ./cmd/AgentRelay
go build -ldflags "-H=windowsgui" -o AgentRelay-mcp.exe ./cmd/AgentRelay-mcp
go build -o AgentRelay-worker.exe ./cmd/AgentRelay-worker
```

1. On the controller, run `AgentRelay gui` (or `AgentRelay setup`) to open device management. For terminal operation use `AgentRelay controller serve`.
2. On the target, run `AgentRelay-worker setup`. Choose a discovered Controller, a workspace folder and an alias.
3. Compare verification numbers and approve in the Controller GUI/terminal. Worker saves its credentials.
4. Later, run `AgentRelay-worker serve`. Windows shortcuts are `start-controller.bat` and `start-worker.bat`.

Pairing requests expire after five minutes. Devices/credentials are created only after approval. For another subnet, specify `--controller`, a verified `--fingerprint`, `--workspace` and `--name` when running Worker setup. Unpinned URLs are rejected. Use `scripts/build.ps1` or `sh scripts/build.sh` for Windows/macOS/Linux amd64 and arm64 binaries; macOS/Linux names have no `.exe`, and the Windows-only MCP linker flag is omitted there.

## Connect Your Agent

For deployment without a Worker, use **添加 SSH 连接** in the GUI, then **测试**. SSH provides execution and file reads, defaulting to the remote repository as source. A device may have both SSH and Worker connections with independent roots/policies and appears once to the agent. See [connections](docs/connections.md). Controller state and Worker credentials require schema 3; use fresh state and configure/pair again. Old data, commands and flags have no compatibility aliases.

Run this on the controller, as the same user that runs the Controller:

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

Each selected connection exposes `workspace_mode`: Worker defaults to `local_sync` (local source mappings); SSH defaults to `remote` (target repository). Both modes permit execution without synchronization. In remote mode, run `AgentRelay exec --device linux-dev --dir /home/dev/repo "pwd"` directly. `--dir-kind local|remote|auto` (MCP `working_dir_kind`) resolves path ambiguity. Remote mode disables sync to protect its source repository. Policies and mappings belong to each connection and can be edited in the GUI.

Use sessions for long-lived shell processes:

```powershell
AgentRelay session create --device <id> --dir C:\src\project
AgentRelay session send --device <id> --id <session-id> --input "Write-Output 'hello'`r`n"
AgentRelay session get --device <id> --id <session-id>
AgentRelay session close --device <id> --id <session-id>
```

Remote paths are checked against the selected connection's authorized root; absolute target paths are normalized to relative paths before execution. `sync` compares SHA-256 manifests and uploads changed files in bounded chunks, so directories and individual files are not limited by the 32 MiB RPC payload cap. It applies nested `.gitignore` files and a root `.agentrelayignore` by default; repeat `--exclude` for additional gitignore-compatible patterns or use `--no-gitignore`. `--mode update` keeps remote extras, while `--mode mirror` removes only files managed by an earlier sync and now absent locally. `--dry-run` reports the plan without changing the Worker. A successful sync persists the local-project-to-remote-root mapping, so later commands can use the same local project directory. Tasks and sessions are in memory and are not reconstructed after a Worker process restart.

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

English | [简体中文](README.zh-CN.md)

# DevFreight

## What is DevFreight?

DevFreight is open-source infrastructure for cross-device development and testing with existing coding agents such as Codex, Claude Code and Cursor. Through standard MCP or CLI interfaces, one agent can use real Windows, macOS and Linux computers to sync code, run native commands, build, test, debug and collect results, without changing the agent's internal architecture.

**One agent, multiple device execution environments.** Development decisions stay with the agent on the controller; target devices provide their own operating systems, compilers and tools. Source code stays on the controller by default and is incrementally synchronized to remote workspaces. Each device exposes its available capabilities through an authorized SSH or native Worker connection; SSH can also operate directly on an existing remote repository.

An agent can develop a project on Windows and validate it on a Mac, run tests across several operating systems concurrently, or keep a service running on one device while testing its client on another. Logs, reports and build artifacts return to the controller so the agent can fix code and repeat the development cycle.

```text
Coding agent -- MCP stdio / CLI --> Controller (HTTPS)
                                      | single device connection
                        +-------------+-------------+
                        |                           |
                  SSH + native shell         Native Worker (WSS)
                  shell / file reads         shell / files / tasks
                                             sessions / desktop
```

The target runs a standalone native Worker without a model, Python environment or full agent installation, and does not need an SSH server. The Worker opens no inbound port. LAN and Ethernet share the same protocol, which future IP-over-USB connections can reuse. DevFreight manages device connections, workspaces and execution state; task planning and model calls remain with the external agent.

## What You Need

- A Windows, macOS or Linux controller running the coding agent and DevFreight Controller.
- A Windows, macOS or Linux target with a workspace directory and either SSH + native system shell/tools or the native DevFreight Worker. SSH needs no Python or additional language runtime; see [requirements](docs/ssh.md).
- Network reachability from the Worker to the controller. The controller accepts inbound TCP `8764`; discovery uses UDP `8763`.
- Worker targets run one native executable and need no Python, Node.js, LLM or Agent Runtime. macOS desktop access needs Screen Recording and Accessibility permissions; Linux desktop is currently unsupported.

## Quick Start

Build the Controller/MCP programs on the controller and the standalone Worker for its target, or use release binaries:

```powershell
go build -o DevFreight.exe ./cmd/DevFreight
go build -ldflags "-H=windowsgui" -o DevFreight-mcp.exe ./cmd/DevFreight-mcp
go build -o DevFreight-worker.exe ./cmd/DevFreight-worker
```

1. On the controller, run `DevFreight gui` (or `DevFreight setup`) to open device management. For terminal operation use `DevFreight controller serve`.
2. On the target, run `DevFreight-worker setup`. Choose a discovered Controller, a workspace folder and an alias.
3. Compare verification numbers and approve in the Controller GUI/terminal. Worker saves its credentials.
4. Later, run `DevFreight-worker serve`. Windows shortcuts are `start-controller.bat` and `start-worker.bat`.

Pairing requests expire after five minutes. Devices/credentials are created only after approval. For another subnet, specify `--controller`, a verified `--fingerprint`, `--workspace` and `--name` when running Worker setup. Unpinned URLs are rejected. Use `scripts/build.ps1` or `sh scripts/build.sh` for Windows/macOS/Linux amd64 and arm64 binaries; macOS/Linux names have no `.exe`, and the Windows-only MCP linker flag is omitted there.

Configuration defaults to the `DevFreight` directory under the user's configuration directory; set `DEVFREIGHT_HOME` to override it. When upgrading from an earlier project name, update the MCP entry, environment variables and `.devfreightignore` filename, and configure/pair devices using fresh state. Upgrade both the Controller and Worker together because discovery and connection identifiers use the new name.

## Connect Your Agent

For deployment without a Worker, use **新增设备 / 更换为 SSH** in the GUI, then **测试**. SSH provides execution and file reads, defaulting to the remote repository as source. A device has exactly one connection. Explicit replacement preserves its alias and handle, replaces configuration and discards old credentials/mappings; execution, tasks, sessions and syncs must be idle. Worker must be online to verify activity. Agents see operational capabilities, including `desktop: false` for SSH, with no connection configuration or management tools. See [connections](docs/connections.md). Controller state requires schema 4; Worker credentials retain schema 3. Older Controller state is rejected without compatibility layers or migration; configure/pair using fresh state.


Run this on the controller, as the same user that runs the Controller:

```powershell
DevFreight mcp config
```

Copy the returned `mcpServers` entry into the coding agent's MCP configuration. `mcp-config.bat` prints the same JSON. Restart or reconnect the MCP client after upgrading DevFreight so its tool list is refreshed.

Available MCP tools are `device_list`, `device_exec`, `device_sync`, `device_pull`, `device_task`, `device_session`, `device_desktop`, `device_capture` and `device_input`.

## Common Operations

Find paired devices, sync a project, run a command and collect a result:

```powershell
DevFreight device list
DevFreight sync --device build-machine --source C:\src\project
DevFreight sync --device build-machine --source C:\src\project --mode mirror --exclude .cache/
DevFreight exec --device build-machine --dir C:\src\project "dotnet test"
DevFreight task start --device build-machine --dir C:\src\project --timeout-ms 60000 "npm run build"
DevFreight task get --device build-machine --id <task-id>
DevFreight task retrieve --device build-machine --id <task-id>
DevFreight pull --device build-machine --out .\results <remote-root>\report.txt
```

`device_list` exposes a persistent short handle generated at pairing time (for example `dev-a1b2c3d4`), aliases, status and capabilities, without exposing private device IDs. The required `device_id` argument accepts either a handle or an alias. Disconnects, reconnects, and adding or removing devices do not change the handle.

Each device exposes `workspace_mode`: Worker defaults to `local_sync` (local source mappings); SSH defaults to `remote` (target repository). Both modes permit execution without synchronization. In remote mode, run `DevFreight exec --device linux-dev --dir /home/dev/repo "pwd"` directly. `--dir-kind local|remote|auto` (MCP `working_dir_kind`) resolves path ambiguity. Remote mode disables sync to protect its source repository. Policies and mappings belong to each connection and can be edited in the GUI.

Use sessions for long-lived shell processes:

```powershell
DevFreight session create --device <id> --dir C:\src\project
DevFreight session send --device <id> --id <session-id> --input "Write-Output 'hello'`r`n"
DevFreight session get --device <id> --id <session-id>
DevFreight session close --device <id> --id <session-id>
```

Remote paths are checked against the device connection's authorized root; absolute target paths are normalized to relative paths before execution. `sync` compares SHA-256 manifests and uploads changed files in bounded chunks, so directories and individual files are not limited by the 32 MiB RPC payload cap. It applies nested `.gitignore` files and a root `.devfreightignore` by default; repeat `--exclude` for additional gitignore-compatible patterns or use `--no-gitignore`. `--mode update` keeps remote extras, while `--mode mirror` removes only files managed by an earlier sync and now absent locally. `--dry-run` reports the plan without changing the Worker. A successful sync persists the local-project-to-remote-root mapping, so later commands can use the same local project directory. Tasks and sessions are in memory and are not reconstructed after a Worker process restart.

Manage authorization without pairing again:

```powershell
DevFreight device rename --device dev-a1b2c3d4 --name build-machine
DevFreight device revoke --device build-machine
DevFreight device authorize --device build-machine
DevFreight device remove --device build-machine
```

## Desktop Access

The optional desktop tools inspect displays, capture PNG screenshots and send mouse/keyboard input. Run the Worker in the logged-in interactive session. Coordinates are physical pixels on Windows and points on macOS; use capture metadata when mapping preview pixels to native coordinates.

```powershell
DevFreight desktop info --device <id>
DevFreight desktop capture --device <id> --display 0 --max-width 960 --max-height 540
DevFreight desktop input --device <id> --json '{"action":"click","x":300,"y":204}'
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

## License

DevFreight is licensed under [Apache-2.0](LICENSE).

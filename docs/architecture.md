# Architecture

DevFreight Controller runs beside an existing coding agent on Windows, macOS or Linux. The agent itself is outside this project. Targets either use SSH or run the standalone native DevFreight-worker executable. Worker targets need no Python, Node.js, LLM or Agent Runtime.

```text
Agent -> MCP / CLI -> Controller local HTTPS API
                         |
                   logical device registry
                         |
                 single connection + workspace policy
                         |
                    capability interfaces
                         |
                  connection adapter registry
                    /                \
                  SSH           Worker WSS RPC       future adapters
                   |                  |
            ephemeral executor    native operations
```

## Boundaries

| Layer | Ownership |
| --- | --- |
| Device identity | Stable ID, handle, alias, authorization, desktop permission and one required connection binding |
| Connection | Transport configuration, credential hash, configuration revision, root, workspace mode, source mappings, capabilities, platform metadata and status |
| Workspace policy | Determines source authority and resolves controller/target paths before execution |
| Capability backend | Optional Exec, Files, Sync, Tasks, Sessions and Desktop interfaces |
| Transport | RPC frames, heartbeat, cancellation and byte transfer |
| Worker | Native shell, file, task, session and optional desktop execution; outbound connection and pairing |

`internal/controller` separates service lifecycle, enrollment/WSS registration, device management, connection management, operation routing and workspace/sync orchestration. Operation routing never inspects a connection kind or accesses a WebSocket peer. `internal/connection.Adapter` validates configuration, binds a caller and defines capability probing and idle checks. Register another adapter through `controller.Config.Adapters`; MCP tools and device selectors remain unchanged. `internal/backend` implements the capability contract; `internal/transport` carries requests.

Controller device listings project the bound connection's workspace, capabilities, platform and availability. These values are not persisted as device identity. Each device has exactly one connection. Registry validation rejects missing bindings, extra connections and orphaned connections. An explicit replacement preserves identity and replaces configuration; hostname matching never merges identities.

## Deployment

Controller installations contain `DevFreight` (CLI, Controller, browser GUI) and `DevFreight-mcp` (stdio MCP adapter), plus the user's agent. Worker installations contain only `DevFreight-worker` and pairing state. Worker does not depend on the Controller, SSH executor or Controller source-scanning packages. Libraries are linked into the executable. `scripts/build.ps1` and `scripts/build.sh` build CGO-free Windows/macOS/Linux amd64 and arm64 binaries.

Windows uses built-in PowerShell; macOS/Linux use `sh`. Worker cancellation terminates process trees on Windows and process groups on POSIX. SSH uses ephemeral native Shell scripts rather than Python: Windows owns children in a Job Object, while POSIX walks and terminates the active process tree. Its native system requirements and detached-process limits are documented in [native SSH executors](ssh.md). Desktop support is optional: Windows/macOS have native implementations; Linux advertises desktop as unsupported. macOS capture/input require Screen Recording/Accessibility permissions and an interactive login session.

## Workspace Authority

Every connection has a `workspace` object with `root`, `mode` and optional `mappings`:

| Mode | Source authority | Command paths | Sync |
| --- | --- | --- | --- |
| `local_sync` | Controller source is authoritative for synchronized projects | Target-relative paths work immediately; mapped absolute Controller paths work after a successful sync | Requires backend sync capability |
| `remote` | Repository on the target is authoritative | Target-relative paths or absolute target paths within the authorized root | Disabled to avoid overwriting remote source |

Worker defaults to `local_sync`; SSH defaults to `remote`. Worker can also use `remote`. Execution never requires a prior sync: an empty working directory means the authorized root; a target-relative directory can reference an existing repository in either mode.

`working_dir_kind` (`--dir-kind` in CLI) is `auto`, `local` or `remote`. `auto` follows the selected mode. `local` requires a mapping in `local_sync`; `remote` always interprets the path on the target. This resolves ambiguity when both hosts have similar absolute paths. Target path handling is independent of the Controller OS, including Windows drive paths and POSIX roots. File reads always address the target filesystem. Native backends perform their own path checks as well.

`sync` scans local source using gitignore-compatible rules and SHA-256 manifests, uploading changes in 4 MiB chunks. `update` retains target extras; `mirror` deletes only previously managed paths absent from source. Successful remote commit persists a mapping on that connection. Failed or dry-run syncs do not create mappings. Replacing or editing the connection is blocked while operations, tasks, sessions or syncs are active. Mappings do not carry over during replacement. The connection can only be deleted together with its device.

Sync sessions expire after 30 minutes. Files are staged and checksum-verified before commit; abort/shutdown/expiry removes staging. Commit rolls back reported errors, but a process crash mid-commit can leave a mixed tree. `.devfreight` is reserved for staging/manifests. Synchronization rejects symbolic links.

## Enrollment And Trust

Worker opens outbound WSS and no listening port. Discovery advertises the HTTPS address and certificate fingerprint (UDP 8763; default HTTPS/WSS TCP 8764). Alternatively supply `--controller` and a fingerprint verified on the Controller. Unpinned URLs are rejected.

Pairing shows a verification number on both hosts and expires after five minutes. Approval creates a new device. The explicit replacement action consumes a pending pairing request to replace an authorized device's sole connection while preserving its ID/handle/alias. The old connection and credentials are discarded only after the replacement is saved. Changing a Worker root requires pairing again; changing its workspace mode is Controller policy configuration.

Management/operation APIs are loopback-only with origin checks. WSS transport code does not own device identity. Revocation persists before the connection and in-flight operations are cancelled. Configuration revisions reject stale requests and probe results. A device reservation blocks new operations throughout the idle check and durable commit. Adapters define how to verify idleness: SSH has synchronous calls tracked by the Controller; Worker reports active requests (including queued RPC handlers), unfinished tasks/sessions and syncs using a private activity endpoint. Offline Worker replacement is rejected until it reconnects. This endpoint is not an Agent tool or a device operation. There is no automatic transport fallback or command replay.

Shell commands run with the target account's normal privileges; a workspace is a file/directory policy, not an OS sandbox. DevFreight does not install compilers or project dependencies on the target.

## State And Resources

State lives in the user's DevFreight configuration directory or `DEVFREIGHT_HOME`. `controller.json` contains schema **4**, TLS identity and separate device/connection maps. Worker credentials require schema **3**. Old state is rejected without migration, backfill or compatibility routes; configure/pair using fresh state. Commands are `DevFreight controller serve`, `DevFreight-worker setup|serve` and `--controller`, with no old Bridge/combined-Worker aliases.

Restart marks connections offline. Workers reconnect with bounded exponential backoff; SSH needs a connectivity test. Tasks/sessions survive network/Controller reconnect while the same Worker remains alive, but Worker restart does not restore processes. IDs belong to their originating connection. Each store retains at most 128 records, finished records expire after an hour, and output is bounded to 4 MiB with truncation metadata. Sessions use shell pipes, not a PTY.

Interrupted commands are never replayed. Sync chunk uploads can retry identical offsets/checksums; commit and desktop input cannot. If remote commit succeeds but saving the Controller mapping fails, the API reports that partial outcome explicitly.

See [connections](connections.md) for API examples and [development](development.md) for verification. Native Windows and Linux/WSL tests can run locally; macOS runtime/permission checks need a Mac. CI runs native test jobs on all three operating systems.

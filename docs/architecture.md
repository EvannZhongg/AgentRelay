# Architecture

AgentRelay is an infrastructure layer for coding agents. It does not run a model, plan work, or expose an agent loop. The controller hosts the Bridge and the agent-facing CLI/MCP adapter; target computers expose capabilities through SSH or a native Worker. Devices own stable identity; connections own transport and credentials. See [Devices, connections and capabilities](connections.md) for the schema, capability matrix and management workflow.

## Components

| Component | Responsibility |
| --- | --- |
| Bridge | Pairing, device records, authorization, workspace mappings, request routing and local HTTPS API |
| Worker | Outbound authenticated connection, native shell execution, file access, tasks, sessions and optional desktop operations |
| CLI / GUI | Human-facing controller commands and the embedded cross-platform browser UI for pairing, device management and remote operations |
| MCP adapter | Stdio JSON-RPC server exposing the remote operations as MCP tools |
| Discovery | LAN advertisement of the controller URL and certificate fingerprint used during pairing |

The request path is `MCP/CLI -> Bridge HTTPS -> selected connection -> capability backend`. Native Worker connections use authenticated WSS; SSH connections use strict OpenSSH channels and an ephemeral Python executor. The Worker does not listen for inbound HTTP requests. `internal/protocol` defines shared messages, `internal/backend` composes optional capability interfaces and `internal/transport` carries RPC.

## Trust And Pairing

1. The controller starts the Bridge and advertises its HTTPS URL, port and certificate fingerprint on the local network.
2. The Worker selects a discovered controller, pins the advertised fingerprint, and sends a pairing request with its proposed name and workspace.
3. The controller shows a short verification number. The operator compares it with the Worker output and approves or rejects the request.
4. Approval creates a device or attaches to an explicitly selected existing device with the same workspace. Credentials belong to a new Worker connection. The Worker stores the Bridge URL, fingerprint, device ID, connection ID, credential and workspace, then opens the outbound WSS connection.

Pairing requests expire after five minutes. A manually entered URL without a trusted discovery fingerprint is rejected. Revoking a device is persisted before its active connection is closed; removal deletes the device record and invalidates its credentials.

The controller management API is loopback-only. Remote computers can pair and establish their authenticated Worker connection, but cannot call controller device-management endpoints directly. `AgentRelay gui` serves the embedded UI from the same Bridge origin, so it uses the same registry and API as the CLI and does not maintain a second device database. Each approved pairing receives a persistent short handle; agent-facing selectors use that handle or the managed alias, never a connection-order number.

## Workspaces And Operations

The controller workspace is the source of truth. `sync` scans the local directory with gitignore-compatible rules, sends a SHA-256 manifest, uploads only changed files in 4 MiB chunks, and applies verified staging files with rollback on reported commit errors. `update` mode preserves remote extras; `mirror` deletes only paths recorded in an earlier managed manifest and excludes ignored paths. A successful commit persists a mapping from the absolute controller path to the Worker-relative root. `exec`, task creation and session creation may then use the mapped controller path or an explicit Worker-relative path. File reads and writes are resolved below the authorized Worker workspace.

Sync sessions expire after 30 minutes. Upload chunks are checksum-verified and idempotent by path and offset. Incomplete or aborted sessions only leave staging data, which is removed on abort, Worker shutdown, or the next sync after expiry; target files are not changed before commit. No target file can contain a partial upload, although a Worker process crash during commit may leave a mixed tree. Worker-managed manifests and staging data live below the reserved `.agentrelay` workspace directory. Source and destination symbolic links are rejected.

Synchronous execution is cancelled when its RPC connection is lost. Tasks and shell sessions can outlive an MCP request and survive Bridge/network reconnection while the same Worker process remains alive. A Worker restart retains identity and workspace credentials but does not reconstruct running processes or in-memory task/session records.

Each task and session store retains at most 128 records. Finished records expire after one hour; active processes are never evicted. Output streams retain up to 4 MiB and report `output_truncated`. Sessions use native shell pipes, not a PTY, and do not support terminal resizing or interactive password prompts.

## Persistence

State is stored under the current user's `AgentRelay` configuration directory (`%APPDATA%\AgentRelay` on Windows). Set `AGENTRELAY_HOME` to isolate a complete CLI/MCP/Bridge installation. The Bridge persists its TLS identity, device IDs, credential hashes, aliases, authorization, desktop control permission, workspace roots and project mappings. Controller-side file hashes are cached there by source path. The Worker persists pairing credentials and its workspace; managed sync manifests live in the workspace's reserved `.agentrelay` directory. Credentials and private device IDs are not returned in agent-facing device listings.

After a Bridge restart, saved devices initially appear offline. Workers reconnect with exponential backoff (up to 30 seconds) and refresh capabilities without replacing aliases or mappings. If the saved Bridge address changes, run `AgentRelay worker setup` to pair again.

## Controller API Scope

The Bridge exposes health and pairing endpoints to the network as needed for setup. Device management and operation endpoints are local-only:

- Pairing and connection: `GET /v1/health`, `POST /v1/pair`, `GET /v1/pair/{request-id}`, `GET /v1/connect`.
- Pairing approval and devices: `/v1/pair-requests`, `/v1/devices`.
- Management UI: `GET /ui` (served by the same Bridge registry and origin).
- Operations: `/v1/devices/{id}/exec`, `/files/sync/start`, `/files/sync/{session}/{manifest,plan,file,commit}`, `/files/read`, `/tasks`, `/sessions` and their sub-resources.
- Desktop: `GET /v1/devices/{id}/desktop`, `POST /v1/devices/{id}/desktop/capture`, `POST /v1/devices/{id}/desktop/input`.

Bridge state schema 2 stores device and connection maps separately. Previous state and Worker credentials are rejected; no migration or compatibility layers are provided. Service installation, PTY support and multi-device scheduling are outside the current implementation.

## Failure And Recovery Rules

- Failed or interrupted RPC calls are never automatically replayed because the operation may already have started. Sync chunk uploads are the exception: the client may safely retry an identical chunk at the same offset; commit is not replayed automatically.
- A disconnected Worker is shown as offline until it reconnects; saved aliases and mappings remain available.
- A still-running task cannot be retrieved. Poll it with `get` until `finished_at` is set, then call `retrieve`.
- Desktop input is serialized in the Worker and is never replayed after reconnecting.

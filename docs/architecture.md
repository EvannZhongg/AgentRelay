# Architecture

AgentRelay is an infrastructure layer for coding agents. It does not run a model, plan work, or expose an agent loop. The controller hosts the Bridge and the agent-facing CLI/MCP adapter; target computers run a lightweight Worker.

## Components

| Component | Responsibility |
| --- | --- |
| Bridge | Pairing, device records, authorization, workspace mappings, request routing and local HTTPS API |
| Worker | Outbound authenticated connection, native shell execution, file access, tasks, sessions and optional desktop operations |
| CLI | Human-facing controller commands for pairing, device management and remote operations |
| MCP adapter | Stdio JSON-RPC server exposing the remote operations as MCP tools |
| Discovery | LAN advertisement of the controller URL and certificate fingerprint used during pairing |

The normal request path is `MCP/CLI -> Bridge HTTPS -> authenticated WSS -> Worker handler`. The Worker does not listen for inbound HTTP requests. `internal/protocol` defines shared messages; `internal/transport` provides multiplexed request/response, cancellation and heartbeats.

## Trust And Pairing

1. The controller starts the Bridge and advertises its HTTPS URL, port and certificate fingerprint on the local network.
2. The Worker selects a discovered controller, pins the advertised fingerprint, and sends a pairing request with its proposed name and workspace.
3. The controller shows a short verification number. The operator compares it with the Worker output and approves or rejects the request.
4. Approval creates a stable device ID and credentials. The Worker stores the Bridge URL, fingerprint, device ID, credential and workspace, then opens the outbound WSS connection.

Pairing requests expire after five minutes. A manually entered URL without a trusted discovery fingerprint is rejected. Revoking a device is persisted before its active connection is closed; removal deletes the device record and invalidates its credentials.

The controller management API is loopback-only. Remote computers can pair and establish their authenticated Worker connection, but cannot call controller device-management endpoints directly.

## Workspaces And Operations

The controller workspace is the source of truth. `sync` uploads a directory and persists a mapping from an absolute controller path to a Worker-relative root. `exec`, task creation and session creation may then use the mapped controller path or an explicit Worker-relative path. File reads and writes are resolved below the authorized Worker workspace.

Synchronous execution is cancelled when its RPC connection is lost. Tasks and shell sessions can outlive an MCP request and survive Bridge/network reconnection while the same Worker process remains alive. A Worker restart retains identity and workspace credentials but does not reconstruct running processes or in-memory task/session records.

Each task and session store retains at most 128 records. Finished records expire after one hour; active processes are never evicted. Output streams retain up to 4 MiB and report `output_truncated`. Sessions use native shell pipes, not a PTY, and do not support terminal resizing or interactive password prompts.

## Persistence

State is stored under the current user's `AgentRelay` configuration directory (`%APPDATA%\AgentRelay` on Windows). Set `AGENTRELAY_HOME` to isolate a complete CLI/MCP/Bridge installation. The Bridge persists its TLS identity, device IDs, credential hashes, aliases, authorization, workspace roots and project mappings. The Worker persists its pairing credentials and workspace. Credentials are not returned in device listings.

After a Bridge restart, saved devices initially appear offline. Workers reconnect with exponential backoff (up to 30 seconds) and refresh capabilities without replacing aliases or mappings. If the saved Bridge address changes, run `AgentRelay worker setup` to pair again.

## Controller API Scope

The Bridge exposes health and pairing endpoints to the network as needed for setup. Device management and operation endpoints are local-only:

- Pairing and connection: `GET /v1/health`, `POST /v1/pair`, `GET /v1/pair/{request-id}`, `GET /v1/connect`.
- Pairing approval and devices: `/v1/pair-requests`, `/v1/devices`.
- Operations: `/v1/devices/{id}/exec`, `/files/sync`, `/files/read`, `/tasks`, `/sessions` and their sub-resources.
- Desktop: `GET /v1/devices/{id}/desktop`, `POST /v1/devices/{id}/desktop/capture`, `POST /v1/devices/{id}/desktop/input`.

There is no compatibility layer for removed Worker URL registration, Worker listening flags or the previous device-ID derivation. Service installation, incremental sync, PTY support and multi-device scheduling are outside the current implementation.

## Failure And Recovery Rules

- Failed or interrupted RPC calls are never automatically replayed because the operation may already have started.
- A disconnected Worker is shown as offline until it reconnects; saved aliases and mappings remain available.
- A still-running task cannot be retrieved. Poll it with `get` until `finished_at` is set, then call `retrieve`.
- Desktop input is serialized in the Worker and is never replayed after reconnecting.

# Devices, Connections And Capabilities

A Device owns a stable handle, alias, authorization, workspace and project mappings. A Connection owns its transport configuration, credentials, advertised capabilities and live status. A device may have several connections and exactly one selected execution connection. MCP lists the device once and omits connection IDs, SSH settings and credentials.

| Capability | Pure SSH v1 | Native Worker |
| --- | --- | --- |
| Execute native commands | Yes, `/bin/sh` | Yes, native shell |
| Read workspace files | Yes, up to 16 MiB per file | Yes |
| Incremental sync | No | Yes |
| Background tasks / persistent sessions | No | Yes |
| Desktop info / capture / input | No | Optional, native OS and interactive session dependent |

`internal/backend` defines separate Exec, Files, Sync, Tasks, Sessions and Desktop interfaces, composed as optional capabilities. The RPC adapters invoke these capabilities through either transport. The ephemeral Python executor belongs to the capability backend; the SSH transport only opens channels and carries RPC. Worker desktop operations continue to use `internal/desktop.Backend` and its validation service. SSH and WSS carry execution requests; neither runs agent reasoning.

## Pure SSH Setup

The controller requires OpenSSH `ssh`. The remote target requires a POSIX shell, Python 3 and an existing absolute workspace directory. There is no AgentRelay binary, listener, persistent helper or service to deploy on the SSH target. Each request starts a Python standard-library RPC executor in memory through an SSH channel.

Use key authentication through an identity file, default OpenSSH identities or ssh-agent. SSH runs in batch mode with strict host-key checking; provision a verified host key in known_hosts before testing. SSH config-file overrides, passwords and interactive prompts are not supported. Connection configuration contains local file paths, never private-key contents or passwords.

In `AgentRelay gui`, open **添加 SSH 连接**, select **创建新设备**, fill the alias, workspace, host, user, port and optional key/known_hosts paths. Add the connection, then click **测试**. For an existing device, select it in the same form or click **为此设备添加 SSH**. Use **设为执行连接** to select a connection. Edits invalidate the last connectivity test; run **测试** again.

CLI equivalents:

```powershell
AgentRelay connection add-ssh --name linux-build --workspace /home/builder/work --host 192.168.1.20 --user builder --identity-file C:\keys\builder
AgentRelay connection list --device linux-build
AgentRelay connection test --device linux-build --id <connection-id>
AgentRelay exec --device linux-build "uname -a"
AgentRelay connection select --device linux-build --id <connection-id>
AgentRelay connection remove --device linux-build --id <connection-id>
```

Add SSH to an existing device with `connection add-ssh --device <handle-or-alias> --host ... --user ...`. It uses that device's workspace. SSH v1 requires a POSIX workspace, so attaching SSH to a Windows Worker workspace is unsupported.

## Add Worker To The Same Device

Pair a native Worker normally, using exactly the same workspace path. At approval, choose **附加到 [device]** instead of **创建新设备**. The controller issues new credentials for the Worker connection, retains the existing handle and alias, and leaves the selected execution connection unchanged. There is no automatic hostname matching or device merge.

Terminal approval to attach a pending pairing:

```powershell
AgentRelay connection attach-worker --device linux-build --id <pairing-request-id>
```

Connections are selected explicitly. Unsupported operations return HTTP 501 with the missing capability; an unavailable selected connection returns HTTP 503. Requests are never silently replayed through another connection. Finish or abort active syncs before switching, editing or deleting their connection. Task/session IDs belong to the connection that created them; select that connection to manage them again.

SSH output retains 1 MiB per stream and reports truncation. Timeout is at most 30 minutes. Cancellation closes the SSH channel; the executor monitors channel loss and kills the command's process group. Commands run with the remote user's full shell privileges, while file reads and working directories are confined to the authorized workspace and checked after resolving symbolic links.

## State And Management API

Bridge state schema 2 stores independent `devices` and `connections` maps. Worker credentials require `connection_id`. Previous state formats are rejected; there is no migration, handle backfill or compatibility adapter. Use a fresh `AGENTRELAY_HOME` and pair/configure devices again.

Connection status resets to offline on Bridge restart. Workers reconnect automatically. Test SSH connections again after restart or a transport failure. Device revocation closes every Worker connection and rejects SSH execution and tests; device removal deletes all associated connection records. Removing one connection preserves the device and its other connections. Removing the selected connection clears its execution selection.

All management endpoints are loopback-only and use the same origin protections as the existing UI:

- `POST /v1/devices`: DeviceCreate with name, workspace and SSH configuration.
- `POST /v1/devices/{device}/connections`: add SSHConfig to an existing device.
- `PATCH /v1/devices/{device}/connections/{connection}`: replace SSHConfig.
- `DELETE /v1/devices/{device}/connections/{connection}`: remove a connection.
- `POST /v1/devices/{device}/connections/{connection}/test`: validate connectivity and probe capabilities.
- `PATCH /v1/devices/{device}` with `active_connection`: choose the execution connection.
- `POST /v1/pair-requests/{pairing}/approve` with optional `device_id`: attach Worker to an existing device.

`GET /v1/devices` returns connection configuration for controller management. The MCP/CLI device listing returns only the device's selected capabilities and status.

For a real SSH regression test, set `AGENTRELAY_TEST_SSH` to DeviceCreate JSON pointing to an isolated disposable workspace and run `go test ./internal/bridge -run TestSSHIntegration -v`. This test executes commands and creates/removes its own test files under that workspace.

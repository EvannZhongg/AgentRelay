# Devices, Connections And Workspaces

A device owns identity and authorization. Each connection independently owns a transport, root, workspace mode, source mappings and capabilities. MCP presents one stable handle/alias per device with the selected policy/capabilities, omitting SSH configuration, credentials and private IDs.

| Capability | SSH | Native Worker |
| --- | --- | --- |
| Execute | Windows PowerShell / macOS and Linux sh | Windows PowerShell / macOS and Linux sh |
| Read files | Up to 16 MiB per file | Yes |
| Incremental sync | Unavailable | Available in local_sync mode |
| Tasks / persistent sessions | Unavailable | Yes |
| Desktop | Unavailable | Optional Windows/macOS; Linux unsupported |

## SSH With A Remote Repository

Controller uses OpenSSH, key/agent authentication, batch mode and strict known-host checking. Windows targets use their system PowerShell; macOS/Linux targets use native sh and system utilities. Neither requires Python or an AgentRelay binary. See [native SSH executors](ssh.md) for requirements, platform selection and lifecycle limits.

```text
AgentRelay connection add-ssh --name linux-dev --workspace /home/dev/repo --host 192.168.1.20 --user dev
AgentRelay connection list --device linux-dev
AgentRelay connection test --device linux-dev --id <connection-id>
AgentRelay exec --device linux-dev --dir /home/dev/repo/src "pwd"
```

SSH defaults to remote mode: the repository stays on the target and no local project/sync step is required. Supply an independent `--workspace` when adding SSH to an existing device. Roots need not match other connections. SSH edits reset connectivity; test again before execution.

## Worker Pairing

```text
AgentRelay-worker setup --workspace <target-root>
AgentRelay-worker serve
```

Discovery pins the Controller certificate. For another subnet, verify its fingerprint (printed by controller serve or shown in the GUI), then use:

```text
AgentRelay-worker setup --controller https://<controller-host>:8764 --fingerprint <sha256> --workspace <target-root> --name build-machine
```

Approve the matching verification number in the Controller GUI/terminal. Choose an existing device at approval to attach without changing identity. Worker roots may differ from SSH/other Worker roots. Every Worker connection gets an independent credential.

Worker defaults to local_sync. Use `--workspace-mode remote` at setup for an existing remote repository; Controller GUI can also change mode. Worker root changes require pairing again. An empty or target-relative directory permits execution immediately in either mode.

```text
AgentRelay exec --device build-machine --dir existing-repo --dir-kind remote "<native command>"
AgentRelay sync --device build-machine --source <absolute-controller-source>
AgentRelay exec --device build-machine --dir <absolute-controller-source> "<native command>"
```

The latter commands use local_sync. `working_dir_kind=auto` maps absolute Controller paths only in that mode; remote mode treats absolute paths as target paths under the root. Explicit remote disambiguates absolute target paths in local_sync. File reads/pulls always refer to target paths. Remote-mode connections do not advertise sync, preventing accidental repository replacement.

## Selection And Failure

```text
AgentRelay connection select --device <alias-or-handle> --id <connection-id>
AgentRelay connection attach-worker --device <alias-or-handle> --id <pairing-id>
AgentRelay connection remove --device <alias-or-handle> --id <connection-id>
```

Select transports explicitly. Unsupported capability returns 501; offline connection returns 503. No silent fallback, replay or identity merge. Finish/abort syncs before changing the selected connection. Task/session IDs belong to the originating connection. Revocation cancels requests on all adapters and closes all Worker connections. Removing a selected connection clears selection, preserving identity.

## Controller API

Management endpoints are local-only. `POST /v1/devices` accepts:

```json
{
  "name": "linux-dev",
  "connection": {
    "kind": "ssh",
    "ssh": {"host": "192.168.1.20", "port": 22, "user": "dev"},
    "workspace": {"root": "/home/dev/repo", "mode": "remote"}
  }
}
```

- `POST /v1/devices/{device}/connections`: accepts the connection object above.
- `PATCH /v1/devices/{device}/connections/{connection}`: replaces configuration/workspace policy. Kind is immutable; Worker root is pairing-authorized.
- `DELETE /v1/devices/{device}/connections/{connection}`: removes a connection.
- `POST /v1/devices/{device}/connections/{connection}/test`: invokes the adapter probe.
- `PATCH /v1/devices/{device}`: updates identity, authorization, desktop permission or active_connection. Workspace policy belongs to the connection API.
- `POST /v1/pair-requests/{pairing}/approve` with optional device_id: attaches Worker pairing.
- `GET /v1/controller`: certificate fingerprint for verified manual pairing.

local_sync policies may include `mappings`, from absolute Controller source paths to target-relative roots. Remote mode rejects mappings. Configuration revisions invalidate stale operations/probes. Controller state and Worker credentials require schema 3; there is no old-format compatibility or migration.

For a real SSH test, set `AGENTRELAY_TEST_SSH` to the DeviceCreate JSON above with an isolated disposable root and run `go test ./internal/controller -run TestSSHIntegration -v`. The test creates/removes its own files there.

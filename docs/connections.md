# Devices, Connections And Workspaces

A device owns identity and authorization. Every device must bind exactly one connection. That connection owns its transport, root, workspace mode, source mappings and capabilities. MCP presents one stable handle/alias per device with operational capabilities (including a boolean `desktop`), omitting transport kinds, SSH hosts, Worker addresses, credentials, private IDs and management tools. Unified operations do not imply equal capabilities.

| Capability | SSH | Native Worker |
| --- | --- | --- |
| Execute | Windows PowerShell / macOS and Linux sh | Windows PowerShell / macOS and Linux sh |
| Read files | Up to 16 MiB per file | Yes |
| Incremental sync | Unavailable | Available in local_sync mode |
| Tasks / persistent sessions | Unavailable | Yes |
| Desktop | Unavailable | Optional Windows/macOS; Linux unsupported |

## SSH With A Remote Repository

Controller uses OpenSSH, key/agent authentication, batch mode and strict known-host checking. Windows targets use their system PowerShell; macOS/Linux targets use native sh and system utilities. Neither requires Python or an DevFreight binary. See [native SSH executors](ssh.md) for requirements, platform selection and lifecycle limits.

```text
DevFreight connection add-ssh --name linux-dev --workspace /home/dev/repo --host 192.168.1.20 --user dev
DevFreight connection show --device linux-dev
DevFreight connection test --device linux-dev
DevFreight exec --device linux-dev --dir /home/dev/repo/src "pwd"
```

SSH defaults to remote mode: the repository stays on the target and no local project/sync step is required. Use explicit replacement to change an existing device to SSH; supply the replacement workspace and configuration. SSH edits reset connectivity; test again before execution.

## Worker Pairing

```text
DevFreight-worker setup --workspace <target-root>
DevFreight-worker serve
```

Discovery pins the Controller certificate. For another subnet, verify its fingerprint (printed by controller serve or shown in the GUI), then use:

```text
DevFreight-worker setup --controller https://<controller-host>:8764 --fingerprint <sha256> --workspace <target-root> --name build-machine
```

Approve the matching verification number to create a new device. To change an existing device to Worker, choose that device in the GUI and execute **更换连接方式**, or use `connection replace-worker` below. This consumes the pending pairing request, preserves the device ID/handle/alias, deletes the old connection and invalidates its credentials. The replacement root can differ from the previous root.

Worker defaults to local_sync. Use `--workspace-mode remote` at setup for an existing remote repository; Controller GUI can also change mode. Worker root changes require pairing again. An empty or target-relative directory permits execution immediately in either mode.

```text
DevFreight exec --device build-machine --dir existing-repo --dir-kind remote "<native command>"
DevFreight sync --device build-machine --source <absolute-controller-source>
DevFreight exec --device build-machine --dir <absolute-controller-source> "<native command>"
```

The latter commands use local_sync. `working_dir_kind=auto` maps absolute Controller paths only in that mode; remote mode treats absolute paths as target paths under the root. Explicit remote disambiguates absolute target paths in local_sync. File reads/pulls always refer to target paths. Remote-mode connections do not advertise sync, preventing accidental repository replacement.

## Explicit Replacement And Failure

```text
DevFreight connection replace-ssh --device <alias-or-handle> --workspace /home/dev/repo --host 192.168.1.20 --user dev
DevFreight connection replace-worker --device <alias-or-handle> --pairing <pending-pairing-id>
```

Replacement is permitted only when execution requests, background tasks, shell sessions and syncs have ended. Configuration edits use the same guard. While checking activity and committing state, the Controller rejects new device operations and concurrent management changes with 409. Worker activity is checked on the device, including activity surviving a Controller restart. An offline Worker must reconnect before its idle state can be verified. Cancelled task status alone does not permit replacement: the process must have finished.

Replacement preserves identity and authorization, replaces configuration/capabilities/workspace policy, and discards old mappings and credentials. The state commit is atomic; save failure retains the previous connection. SSH needs a connectivity test after replacement. Worker needs to connect using the newly issued credentials. Finished task/session records belong to the old connection and are no longer accessible through this device after replacement; retrieve needed results first.

Unsupported capabilities return 501; offline devices return 503. SSH never advertises native Desktop. There is no automatic selection, fallback, replay or hostname-based identity merge. A connection cannot be added alongside the existing connection or deleted independently; remove the device to remove its binding.

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

- `POST /v1/devices/{device}/connection/replace`: explicit replacement, accepting either `{"connection": <spec>}` or `{"pairing_id": "<pending-id>"}`. Worker replacements require pairing. Returns the device's management view without credentials.
- `PATCH /v1/devices/{device}/connection`: edits the sole connection configuration/workspace policy while idle. Kind changes require replacement; Worker root changes require new pairing and replacement.
- `POST /v1/devices/{device}/connection/test`: invokes the sole connection's adapter probe.
- `PATCH /v1/devices/{device}`: updates alias, authorization or desktop permission; it cannot select a connection.
- `DELETE /v1/devices/{device}`: removes the device and its connection.
- `POST /v1/pair-requests/{pairing}/approve`: creates a new Worker device. Existing-device replacement uses the explicit replacement endpoint.
- `GET /v1/controller`: certificate fingerprint for verified manual pairing.

Management device views contain one `connection` object and its `connection_id`. No connection arrays, add/select/remove routes or compatibility aliases exist. Worker receives replacement credentials through its pairing status polling; they never enter MCP output.

local_sync policies may include `mappings`, from absolute Controller source paths to target-relative roots. Remote mode rejects mappings. Configuration revisions reject stale operations/probes. Controller state requires schema **4**, with a strict device/connection bijection; Worker credentials retain schema **3** because their wire format is unchanged. Older Controller state is rejected without migration or compatibility layers; initialize fresh state and configure/pair again.

For a real SSH test, set `DEVFREIGHT_TEST_SSH` to the DeviceCreate JSON above with an isolated disposable root and run `go test ./internal/controller -run TestSSHIntegration -v`. The test creates/removes its own files there.

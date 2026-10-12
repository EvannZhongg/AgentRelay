# Native SSH Executors

SSH works independently of the Controller OS. The Controller needs OpenSSH `ssh`; targets need an SSH server, their native shell and the system tools below. No Python, Node.js, Agent Runtime, Worker binary or persistent AgentRelay service is installed on SSH targets.

| Target | Executor | Requirements |
| --- | --- | --- |
| Windows | Windows PowerShell 5.1 | PowerShell's bundled .NET Framework in FullLanguage mode; Windows Job Object API |
| macOS | Native `sh` script | System `base64`, `ps`, `awk`, `mktemp`, `mkfifo`, `head`, `wc`, `env`, `sleep`, `uname`, `tr`, `cat`, `rm`, `dirname`, `basename` |
| Linux | Native `sh` script | Same system utilities; `ps -eo pid=,ppid=` and fractional `sleep` support |

This is **no additional language-runtime installation**, rather than a claim that SSH alone supplies execution or that every stripped-down OS image has all required utilities. The POSIX probe checks required utilities and process-tree support. Windows uses the system PowerShell installation; constrained language/security policies can prevent its native executor from running.

## Configuration

`ssh.platform` is `auto`, `windows` or `posix`. `auto` selects Windows for drive/UNC roots and POSIX for absolute POSIX roots. It never uses the Controller's OS. Specify the executor explicitly if the host exposes a different environment. A mismatched platform/root is rejected before OpenSSH launches. There is no failure-based Shell fallback or command replay.

```text
AgentRelay connection add-ssh --name windows-dev --platform windows --workspace C:\src\repo --host 192.168.1.30 --user builder
AgentRelay connection add-ssh --name mac-dev --platform posix --workspace /Users/builder/repo --host 192.168.1.31 --user builder
AgentRelay connection add-ssh --name linux-dev --workspace /home/builder/repo --host 192.168.1.32 --user builder
AgentRelay connection show --device windows-dev
AgentRelay connection test --device windows-dev
AgentRelay exec --device windows-dev --dir C:\src\repo "Get-ChildItem"
```

GUI SSH configuration includes the same platform selector. SSH defaults to the `remote` workspace policy, so commands can use the existing target repository without local source or synchronization. Probe reports the actual OS, architecture and native shell. Before probing, shell identity is unknown. Capabilities remain Exec and Files; tasks, sessions, sync and desktop require a backend that advertises them.

Windows OpenSSH can configure its default command shell; see [Microsoft's configuration documentation](https://learn.microsoft.com/en-us/windows-server/administration/openssh/openssh-server-configuration). The Windows bootstrap is a short ASCII `powershell.exe -EncodedCommand` invocation tested through both `cmd.exe` and PowerShell. The larger executor and request travel over stdin, and user commands are sent to the child through a separate stdin gate. They are never interpolated into the SSH command line.

## Execution And Transport

`internal/transport.SSH.Run` carries opaque bytes over a strict OpenSSH channel. It does not interpret workspace policies, commands or request schemas. `internal/backend/ssh.go` validates the operation, chooses a native executor, serializes input, and translates bounded `AgentRelaySSH1` result frames into the existing operation contract. The Python executor is removed; no old wire-format compatibility path exists.

POSIX scripts use safely quoted request values and a parsed function so the channel-loss watcher cannot consume script input. Commands run from a temporary script file. Output collectors retain at most 1 MiB per stream while draining excess bytes, keeping staging storage bounded. Temporary execution files are removed on normal completion, timeout or trapped channel loss. SIGKILL or a host crash can leave a temporary directory. File reads retain at most 16 MiB.

Windows collectors drain byte streams concurrently. The child waits for its command on stdin until the executor has assigned a Job Object. Closing the job stops its processes and children, including descendants holding output pipes; this uses the [Windows job lifetime mechanism](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects). No visible command window is created. Timeout is reported with exit code 124 and `timed_out=true`. Channel loss terminates the job and the Controller returns cancellation rather than replaying the command.

POSIX cancellation freezes the active command process and recursively terminates its descendants using the native process table. Deliberately detached/reparented processes are outside this synchronous lifecycle; use Worker tasks for managed background work. Collectors cannot hang indefinitely on a pipe held open by descendants. Timeout is at most 30 minutes. `duration_ms` includes SSH and executor startup because timing is measured by the Controller.

File and working-directory paths remain below the authorized connection root. POSIX verifies physical directories and rejects final file symlinks; Windows rejects intermediate reparse points as well. Neither shell is an OS sandbox: a command still has the account's usual privileges.

## Verification

`go test ./internal/backend` runs the native executor for its host OS, checking Unicode/quoting, environment values, nonzero native exit codes, binary reads, truncation, long input, timeout and channel-loss child cleanup. On Windows it additionally launches the bootstrap through both default shell types. Native macOS tests run in the macOS CI job. Symlink tests run where OS privileges permit.

For a real SSH end-to-end test, set `AGENTRELAY_TEST_SSH` to DeviceCreate JSON with an isolated target workspace and verified key/known_hosts files, then run `go test ./internal/controller -run TestSSHIntegration -v`. The test chooses commands from the **target** platform, independently of the test runner OS, and checks execution, files, authorization and cancellation.

[English](README.md) | 简体中文

# AgentRelay

AgentRelay 是一个小型桥接层，用于连接运行在**主控计算机**上的编码 Agent 与**其他计算机上的原生执行环境**。首个实现使用同一套基于 IP 的 JSON/HTTP RPC 协议，同时服务于局域网，以及将来的 USB 网络。它不包含 Agent 循环，也不包含模型运行时。

## 第一阶段架构

```text
Coding Agent -- MCP stdio or CLI --> Bridge (:8764)
                                      |
                                      | JSON/HTTP v1
                                      v
                                  Worker (:8765)
                                      |
                         workspace-scoped files and shell
```

共享契约位于 `internal/protocol`。Bridge 在内存中保存已注册设备的目录并代理调用。Worker 无论使用何种传输方式，都暴露同一套 API。平台相关的 shell 启动与进程清理被隔离在 `internal/worker/shell_windows.go` 与 `shell_other.go` 中；当前经过测试的目标是 Windows PowerShell，POSIX 回退路径属于实验性质。在 Windows 上，取消操作与会话关闭会终止 shell 的整个进程树。worker workspace 是所有远程文件路径与工作目录的边界。

当前的注册流程是刻意显式的：由操作者先启动 Worker，再把它的 HTTP 地址添加到 Bridge。设备发现、配对、鉴权、持久化、增量同步与多设备调度，都是建立在该契约之上的后续层次。

## 构建

```powershell
go test ./...
go vet ./...
go build -o AgentRelay.exe ./cmd/AgentRelay
go build -ldflags "-H=windowsgui" -o AgentRelay-mcp.exe ./cmd/AgentRelay-mcp
```

`AgentRelay.exe` 是控制台程序，因为它由终端驱动。`AgentRelay-mcp.exe` 以 Windows GUI 子系统构建，这样 MCP 客户端把它作为子进程启动时不会闪出控制台窗口；它依然通过 stdin/stdout 使用以换行分隔的 JSON-RPC 通信。

## 启动器

`start-worker.bat`、`start-bridge.bat` 和 `mcp-config.bat` 封装了 CLI，便于双击使用。它们要求 `AgentRelay.exe` 与启动器位于同一目录，也就是上面的构建命令所放置的位置。

直接双击 `AgentRelay.exe` 没有用处：它会打印用法并立即退出，因此窗口会一闪而过。`AgentRelay-mcp.exe` 从不需要手动启动，因为它由 MCP 客户端通过 stdio 启动。

- `start-worker.bat` 接受一个可选的 workspace 路径；把文件夹拖到该文件上，就会用这个文件夹作为 worker workspace。
- `start-bridge.bat` 接受一个可选的监听地址，默认为 `:8764`。
- `mcp-config.bat` 打印 MCP 配置 JSON，并接受一个可选的 bridge URL。

## Windows 到 Windows 冒烟测试

在目标 Windows 计算机上：

```powershell
AgentRelay worker serve --workspace C:\AgentRelay\workspace --listen :8765
```

在主控 Windows 计算机上：

```powershell
AgentRelay bridge serve --listen :8764
AgentRelay device add --worker http://TARGET_IP:8765
AgentRelay device list
AgentRelay exec --device <device-id> "dotnet test"
AgentRelay sync --device <device-id> --source . --remote project
AgentRelay exec --device <device-id> --dir project "dotnet test"
```

CLI 与 MCP 适配器支持具有相同生命周期的异步任务：

```powershell
AgentRelay task start --device <device-id> --dir project --env NODE_ENV=test --timeout-ms 60000 "npm run build"
AgentRelay task list --device <device-id>
AgentRelay task get --device <device-id> --id task-1
AgentRelay task retrieve --device <device-id> --id task-1
AgentRelay task cancel --device <device-id> --id task-2
```

`get` 查询任务但不会移除它。`retrieve` 原子地返回并删除一个已完成的任务，包括失败或已取消的任务。当进程仍在运行或正在取消时，它返回 HTTP 409；记录已被取回或过期后返回 HTTP 404。请轮询 `get` 直到出现 `finished_at`，再执行 `retrieve`。失败的任务会包含一个 `error`，说明启动错误、非零退出码或超时；执行细节仍保留在 `result` 中。

CLI 还提供持久 shell 会话与文件取回：

```powershell
AgentRelay session create --device <device-id> --dir project --env APP_ENV=test
AgentRelay session list --device <device-id>
AgentRelay session send --device <device-id> --id session-1 --input "Write-Output 'hello'`r`n"
AgentRelay session send --device <device-id> --id session-1 --input-file commands.txt
AgentRelay session get --device <device-id> --id session-1
AgentRelay session close --device <device-id> --id session-1
AgentRelay pull --device <device-id> --out .\results project\report.txt project\build.log
```

`--env KEY=VALUE` 在 `exec`、`task start` 和 `session create` 上可重复使用。会话输入按原样发送；`--input-file -` 从 stdin 读取。`pull` 会把 workspace 相对路径保留在 `--out` 之下；不带 `--out` 时打印 base64 JSON。它还接受 `--path` 用于单个文件。

## MCP

AgentRelay 暴露一个标准的 MCP stdio 服务器。它在 `initialize` 期间协商 MCP 协议版本，通过 `tools/list` 公布工具，并通过 `tools/call` 执行它们。

手动生成一份可直接粘贴的配置：

```powershell
AgentRelay mcp config --bridge http://127.0.0.1:8764
```

输出具有如下结构：

```json
{
  "mcpServers": {
    "agent-relay": {
      "command": "C:\\path\\to\\AgentRelay-mcp.exe",
      "args": [
        "--bridge",
        "http://127.0.0.1:8764"
      ]
    }
  }
}
```

把该对象粘贴到编码 Agent 所使用的 MCP JSON 配置中。可执行文件路径可以显式覆盖：

```powershell
AgentRelay mcp config --command C:\Tools\AgentRelay-mcp.exe --name agent-relay
```

同一适配器也可以直接启动：

```text
AgentRelay-mcp --bridge http://127.0.0.1:8764
```

它暴露以下 Agent 工具：

- `device_list`：探测并列出所有已注册 worker 及其能力，包括 `online: false` 的离线条目与最后一次成功的 `last_seen` 时间戳，与 HTTP/CLI 目录保持一致。
- `device_exec`：带 `working_dir`、`env` 和 `timeout_ms` 执行命令。
- `device_sync`：上传本地 `source_path` 或显式的 base64 文件条目。
- `device_pull`：读取一个或多个远程 workspace 文件与测试结果。
- `device_task`：启动、列出、查询、取回或取消异步任务。
- `device_session`：创建、列出、轮询、向其中发送输入或关闭一个持久 shell 会话。会话输出是累积的，由 `get`/`send` 返回。

适配器按 MCP stdio 传输的要求，在 stdin/stdout 上使用以换行分隔的 JSON-RPC 消息。会话使用基于管道的原生 shell（Windows 上为 `powershell`），面向长时间运行的命令与增量 stdin 输入；它们不提供终端缩放、ANSI/PTY 语义或交互式密码提示。

会话的 `tools/call` 参数示例：

```json
{"name":"device_session","arguments":{"device_id":"<device-id>","action":"create","working_dir":"project"}}
{"name":"device_session","arguments":{"device_id":"<device-id>","action":"send","session_id":"session-1","input":"Write-Output 'hello'\r\n"}}
{"name":"device_session","arguments":{"device_id":"<device-id>","action":"get","session_id":"session-1"}}
{"name":"device_session","arguments":{"device_id":"<device-id>","action":"close","session_id":"session-1"}}
```

`send` 在写入 stdin 后返回；后续输出请轮询 `get`。输入按原样保留，包括空白与换行。多行 PowerShell 块需要末尾空行。对于同步执行、任务与会话，每条输出流最多保留 4 MiB；`output_truncated` 报告被丢弃的多余部分。任务的 `get` 保留记录；`retrieve` 在执行结束后返回并删除它。请使用 `device_pull` 获取生成的文件（以 base64 返回）。

任务与会话记录在 MCP 客户端断开后仍然存活，但它们保存在 Worker 内存中，不会在 Worker 重启后存活。每个存储最多持有 128 条记录，包括活跃进程。已完成的记录在完成后一小时过期；清理是惰性的，发生在访问或创建存储时（以及任务完成时）。在容量已满时创建记录，会优先淘汰最旧的已完成记录。活跃或尚未回收的进程永远不会被淘汰；当所有槽位都被活跃进程占用时，创建返回 HTTP 409。请关闭会话或取消任务并等待进程清理，以释放容量。会话关闭会在该保留窗口内保留其最终输出。

## API 接口面（v1）

Worker 端点：

- `GET /v1/health`, `GET /v1/device`
- `POST /v1/exec`
- `POST /v1/files/sync`, `GET /v1/files/read?path=...`
- `POST /v1/tasks`, `GET /v1/tasks`, `GET/DELETE /v1/tasks/{id}`
- `POST /v1/tasks/{id}/retrieve`（返回并移除一个已完成任务）
- `POST/GET /v1/sessions`, `GET/DELETE /v1/sessions/{id}`, `POST /v1/sessions/{id}/input`

Bridge 端点在 `/v1/devices/{device-id}/...` 下镜像 Worker 的执行端点，并用 `GET/POST /v1/devices` 管理内存中的设备目录。

## 当前限制

第一阶段刻意把鉴权、配对审批、TLS、服务安装、设备持久化存储、局域网发现、增量哈希、交互式 PTY 流与多设备编排排除在范围之外。命令由任何能访问 Worker URL 的人授权，因此在鉴权层加入之前，请只把 Worker 暴露在可信的开发网络中。

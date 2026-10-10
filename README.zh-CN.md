[English](README.md) | 简体中文

# AgentRelay

AgentRelay 连接主控电脑上的 Coding Agent 与其他电脑的原生执行环境，不包含模型运行时或 Agent 循环。

```text
Coding Agent -- MCP stdio / CLI --> Bridge (:8764, HTTPS)
                                      ^
                                      | Worker 主动建立一条持久 WSS 连接
                                      | 双向、并发 RPC
                                    Worker
                                      |
                         Workspace 文件 / 原生 Shell / 桌面
```

Worker 不开放任何入站端口。局域网、网线和未来的 USB IP 网络使用同一套连接协议。Bridge 持久化已配对设备；在线连接和 Worker 的进程状态保存在内存中。

## 构建

```powershell
go test ./...
go test -race ./...
go vet ./...
go build -o AgentRelay.exe ./cmd/AgentRelay
go build -ldflags "-H=windowsgui" -o AgentRelay-mcp.exe ./cmd/AgentRelay-mcp
```

当前经过测试的执行平台是 Windows PowerShell，POSIX 回退仍属实验性质。MCP 程序由 Coding Agent 通过 stdio 启动；Windows GUI 子系统可避免弹出控制台窗口。

## 首次配置

两台电脑都运行 `AgentRelay setup`，也可以直接双击 `AgentRelay.exe`。在运行 Coding Agent 的电脑选择 **controller**，Bridge 启动后等待连接请求。

在受控电脑选择 **worker**：选择自动发现的主控端，选择 Workspace 目录和设备别名。Worker 发起连接请求并显示验证数字；主控端会显示设备名称和相同数字，用户在主控端批准或拒绝。批准后 AgentRelay 自动生成设备身份、保存凭据并建立出站连接。

默认 Bridge 使用 TCP 8764，局域网发现使用 UDP 8763，只需主控电脑允许入站访问。Worker 必须选择自动发现的主控端，以便在首次 HTTPS 请求前固定发现结果中的证书指纹。没有已知发现指纹时手动输入的 URL 会被拒绝，避免通过未校验证书的首次请求建立信任。验证数字会为每次请求随机生成，只用于目视核对；用户不需要复制证书字符串、配对码或令牌。

待审批请求五分钟后过期。只有主控批准后才会创建设备并发放凭据。

之后启动不需要重复配对，也不需要重新填写 Workspace：

```powershell
# 主控电脑
AgentRelay bridge serve
# 受控电脑
AgentRelay worker serve
```

`start-bridge.bat` 和 `start-worker.bat` 封装上述命令。Worker 启动器首次运行会进入配置向导；拖入文件夹会以该目录进行新的配对。`AgentRelay worker setup` 可以主动重新配对。更换已授权的 Workspace 需要新配对，普通重启保留设备 ID。

## 接入 Agent

在主控电脑生成 MCP 配置：

```powershell
AgentRelay mcp config
```

把返回的 `mcpServers` 条目加入 Coding Agent 的配置。`mcp-config.bat` 也可生成它。适配器自动读取保存的 Bridge 证书身份。CLI 与 MCP 必须在主控电脑以启动 Bridge 的同一用户运行。主控 API 仅接受本机回环连接；其他电脑只能配对和建立经过认证的 Worker 连接。

工具包括 `device_list`、`device_exec`、`device_sync`、`device_pull`、`device_task`、`device_session`、`device_desktop`、`device_capture` 和 `device_input`。`device_list` 返回持久化别名、授权、能力、Workspace 映射、离线设备及最后一次成功注册的时间。

## 桌面能力

桌面能力通过独立的原生 Backend 接口接入：Windows 使用 Win32/GDI/SendInput，macOS 使用系统 CoreGraphics 和 `screencapture`。不依赖 RobotGo，构建无需 CGO。Worker 需要运行在用户的交互会话中；macOS 需要屏幕录制和辅助功能权限。Windows 已完成在线设备验证；macOS arm64/amd64 已通过交叉编译，仍需实机验证。

`device_desktop` 返回权限和显示器信息。`device_capture` 返回标准 MCP PNG 图片、显示器元数据，以及主控端完整原图 Artifact 的绝对路径、尺寸与 SHA256。默认预览不超过 1600×1200；原图保存于 Bridge 状态目录下的 `artifacts/captures`，目前需手动清理。每张截图上限为 64M 像素和编码后 12 MiB PNG。

`device_input` 支持移动、点击、拖动、滚动、组合键及 Unicode 文本。Windows 全局坐标使用物理像素，macOS 使用 point；提供 `display_id` 时为屏幕相对坐标，再提供截图返回的 `width/height` 作为 `reference_width/reference_height` 即可使用预览像素。显示器重接后索引可能变化。输入作用于整个交互桌面，超出文件 Workspace；操作串行执行，断线后不自动重放。

```powershell
AgentRelay desktop info --device <id>
AgentRelay desktop capture --device <id> --display 0 --max-width 960 --max-height 540
AgentRelay desktop input --device <id> --json '{"action":"click","x":300,"y":204}'
AgentRelay desktop input --device <id> --json '{"action":"text","text":"你好"}'
```

更新 Bridge、Worker 和 MCP 程序后，重新连接 MCP 客户端以刷新工具列表。详见[Desktop 架构与接口说明](docs/desktop-capability.md)。

## 开发流程

```powershell
AgentRelay device list
AgentRelay sync --device <id> --source C:\src\project
AgentRelay exec --device <id> --dir C:\src\project "dotnet test"
AgentRelay task start --device <id> --dir C:\src\project --env NODE_ENV=test --timeout-ms 60000 "npm run build"
AgentRelay task list --device <id>
AgentRelay task get --device <id> --id task-1
AgentRelay task retrieve --device <id> --id task-1
AgentRelay task cancel --device <id> --id task-2
```

同步会自动为本地项目分配远端目录，并持久化映射，之后同步复用该目录。执行、启动任务和创建会话可直接指定已映射本地项目的绝对路径或其子目录，也可指定显式的远端 Workspace 相对路径。`sync --remote <path>` 可以覆盖自动映射；响应中的 `root` 返回最终远端目录。MCP 的 `device_sync` 使用 `source_path` 时遵循同样的自动映射流程；显式 `files` 使用指定的远端 `root`。

`get` 只查询任务，`retrieve` 返回并删除已经完成、失败或取消的任务。仍在运行时返回 409，记录不存在、已过期或已取回时返回 404。等待 `finished_at` 出现后再取回；失败任务包含 `error`，已有执行结果保留在 `result` 中。

```powershell
AgentRelay session create --device <id> --dir C:\src\project --env APP_ENV=test
AgentRelay session list --device <id>
AgentRelay session send --device <id> --id session-1 --input "Write-Output 'hello'`r`n"
AgentRelay session send --device <id> --id session-1 --input-file commands.txt
AgentRelay session get --device <id> --id session-1
AgentRelay session close --device <id> --id session-1
AgentRelay pull --device <id> --out .\results <remote-root>/report.txt
```

执行、启动任务和创建会话都支持重复的 `--env KEY=VALUE`。会话输入按原样发送；`--input-file -` 读取 stdin。`send` 在写入后返回，后续输出通过 `get` 查询。会话使用原生 Shell 管道，不提供 PTY、终端缩放或交互式密码提示。`pull` 在 `--out` 下保留远端相对目录，不带该参数时返回 base64 JSON。

## 设备管理

```powershell
AgentRelay device rename --device <id> --name build-machine
AgentRelay device revoke --device <id>
AgentRelay device authorize --device <id>
AgentRelay device remove --device <id>
```

撤销授权先落盘，再关闭活动连接，并阻止后续连接和操作。恢复授权后，用 `worker serve` 重启 Worker 即可继续使用。删除设备会移除保存的记录并使凭据失效，之后需要重新配对。

## 持久化与重连

状态保存在当前用户的配置目录下的 `AgentRelay` 文件夹中，Windows 默认是 `%APPDATA%\AgentRelay`，使用原子替换保存 JSON。可用 `AGENTRELAY_HOME` 为整套 CLI、MCP、Bridge 指定独立状态目录。Bridge 保存 TLS 身份、稳定设备 ID、凭据哈希、别名、授权、Workspace 根目录和本地项目到远端目录的映射。Worker 保存 Bridge 地址、证书指纹、设备 ID、凭据和 Workspace。设备列表不会泄露凭据。

Bridge 重启后，持久化设备先显示离线；Worker 使用原有身份自动重连，失败时指数退避，最长等待 30 秒。重连更新设备能力，但不会覆盖别名或项目映射。保存的 Bridge 地址必须仍可达；地址变化时，可通过 Worker 配置向导在新地址重新配对。

只要 Worker 进程未重启，异步任务和 Shell 会话就会在 MCP 断开、网络重连和 Bridge 重启后继续存在。Worker 重启保留设备身份和 Workspace，但任务与会话存储从空开始，不重建运行进程。任务元数据目前仅保存在内存中。同步执行在 RPC 连接断开时取消；失败或中断的 RPC 不会自动重放，以免重复执行已启动的操作。

任务和会话存储各最多保留 128 条记录。完成记录一小时后过期，也可为新记录提前淘汰；活跃进程不会被淘汰，容量满时返回 409。每条输出流最多保留 4 MiB，超出部分由 `output_truncated` 标记。Windows 取消任务或关闭会话会终止进程树；正常停止 Worker 会取消任务并关闭会话。

## API 与范围

共享契约位于 `internal/protocol`，`internal/transport` 在 WebSocket 上实现并发请求、响应、取消和心跳。Worker 操作处理器通过该连接内部调用，不对外监听 HTTP 端口。

Bridge HTTPS 端点：

- `GET /v1/health`
- `POST /v1/pair`、`GET /v1/pair/{request-id}`、`GET /v1/connect`（经过凭据认证的 WebSocket 升级）
- 仅本机：`GET /v1/pair-requests`、`POST /v1/pair-requests/{request-id}/approve|reject`、`GET /v1/devices`、`PATCH/DELETE /v1/devices/{id}`
- 仅本机：`/v1/devices/{id}/exec`、`/files/sync`、`/files/read`、`/tasks`、`/tasks/{task-id}`、`/tasks/{task-id}/retrieve`、`/sessions`、`/sessions/{session-id}`、`/sessions/{session-id}/input`
- 仅本机：`GET /v1/devices/{id}/desktop`、`POST /v1/devices/{id}/desktop/capture`、`POST /v1/devices/{id}/desktop/input`

`PATCH` 接受 `name`、`authorized` 和 `workspace_mappings`，映射键为主控端绝对路径，值为 Worker 相对目录。旧 Worker URL 注册、Worker 监听参数和旧设备 ID 推导方式已移除，不提供旧数据迁移或兼容层。服务安装、增量同步、PTY 和多设备调度尚未实现。

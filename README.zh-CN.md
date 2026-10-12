[English](README.md) | 简体中文

# AgentRelay

AgentRelay 让 Coding Agent 安全、受控地连接其他电脑上的原生执行环境。Agent 只运行在主控电脑上，通过统一设备能力接口执行操作；每台设备可选择 SSH 或原生 Worker 连接。

```text
Coding Agent -- MCP stdio / CLI --> Controller（主控端，HTTPS）
                                      | 设备的执行连接
                        +-------------+-------------+
                        |                           |
                  SSH + Python 3             原生 Worker（WSS）
                  Shell / 文件读取           Shell / 文件 / 任务
                                             会话 / 桌面
```

Worker 不开放入站端口。局域网、网线以及未来的 USB IP 网络使用同一套协议。AgentRelay 不包含模型运行时或 Agent 循环。

## 使用条件

- 一台运行 Coding Agent 和 AgentRelay Controller 的 Windows、macOS 或 Linux 主控电脑。
- 一台提供 Workspace 目录的目标电脑，可使用 SSH + Python 3（POSIX 系统）或原生 AgentRelay Worker。
- Worker 能访问主控电脑；主控默认接收 TCP `8764`，局域网发现使用 UDP `8763`。
- Worker 被控端仅运行一个原生可执行文件，不需要 Python、Node.js、LLM 或 Agent Runtime。macOS 桌面能力需要“屏幕录制”和“辅助功能”权限；Linux 暂不提供桌面操作。

## 快速开始

主控端使用 Controller 和 MCP 程序，被控端仅使用独立 Worker。可以从仓库构建或使用发行版：

```powershell
go build -o AgentRelay.exe ./cmd/AgentRelay
go build -ldflags "-H=windowsgui" -o AgentRelay-mcp.exe ./cmd/AgentRelay-mcp
go build -o AgentRelay-worker.exe ./cmd/AgentRelay-worker
```

1. 主控电脑运行 `AgentRelay gui` 或 `AgentRelay setup` 打开设备管理；终端模式运行 `AgentRelay controller serve`。
2. 被控电脑运行 `AgentRelay-worker setup`，选择发现到的主控端、工作区和设备别名。
3. 核对两端显示的验证数字，在主控 GUI 或终端批准请求。Worker 保存凭据并自动重连。
4. 后续被控端运行 `AgentRelay-worker serve`。Windows 也可使用 `start-controller.bat` 和 `start-worker.bat`。

配对请求五分钟后过期，批准后才创建设备和凭据。跨网段时，可在 Worker setup 中指定 `--controller`、经主控核对的 `--fingerprint`、`--workspace` 和 `--name`；不接受未固定证书的 URL。`scripts/build.ps1` 或 `sh scripts/build.sh` 可构建三种系统的 amd64/arm64 版本。macOS/Linux 程序名不带 `.exe`，MCP 构建不使用 Windows 专用链接参数。

## 接入 Agent

无需部署 Worker 时，在 GUI 中选择“添加 SSH 连接”，填写配置后点击“测试”。SSH 首版支持命令执行和工作区文件读取；原生 Worker 提供增量同步、任务、持久会话和可选桌面能力。桌面操作取决于系统、交互会话及权限，不作为跨系统稳定能力保证。

同一设备可保留 SSH 与 Worker 连接，通过“设为执行连接”明确选择。每条连接独立配置工作区根目录、模式和映射，不要求根目录一致。批准 Worker 配对时可“附加到”已有设备，Agent 始终看到同一个稳定句柄。详见[设备、连接和能力](docs/connections.md)。主控状态和 Worker 凭据使用 schema 3，旧数据直接拒绝；请使用新的 `AGENTRELAY_HOME` 重新配置和配对，不保留旧命令或参数的兼容别名。

在主控电脑以运行 Controller 的同一用户执行：

```powershell
AgentRelay mcp config
```

把返回的 `mcpServers` 条目加入 Coding Agent 的 MCP 配置。`mcp-config.bat` 也会输出同样的 JSON。升级 AgentRelay 后重新连接 MCP 客户端，以刷新工具列表。

可用 MCP 工具包括 `device_list`、`device_exec`、`device_sync`、`device_pull`、`device_task`、`device_session`、`device_desktop`、`device_capture` 和 `device_input`。

## 常用操作

查看设备、同步项目、运行命令并获取结果：

```powershell
AgentRelay device list
AgentRelay sync --device build-machine --source C:\src\project
AgentRelay sync --device build-machine --source C:\src\project --mode mirror --exclude .cache/
AgentRelay exec --device build-machine --dir C:\src\project "dotnet test"
AgentRelay task start --device build-machine --dir C:\src\project --timeout-ms 60000 "npm run build"
AgentRelay task get --device build-machine --id <task-id>
AgentRelay task retrieve --device build-machine --id <task-id>
AgentRelay pull --device build-machine --out .\results <remote-root>\report.txt
```

`device_list` 返回配对时生成且持久化的短句柄（如 `dev-a1b2c3d4`）、别名、在线状态和能力，不返回设备专有 ID。所有 Agent/CLI 操作的 `device_id` 参数仍然必填，但可以填写短句柄或别名；断线、重连、增删设备都不会改变短句柄。

选中连接会暴露 `workspace_mode`：Worker 默认 `local_sync`，主控本地源码是同步源；SSH 默认 `remote`，远端仓库是源。两种模式均可不经同步直接执行。远端模式可直接运行 `AgentRelay exec --device linux-dev --dir /home/dev/repo "pwd"`，并禁用同步以保护远端源码。`--dir-kind local|remote|auto`（MCP 的 `working_dir_kind`）明确路径属于主控还是被控端。GUI 中的模式和映射均按连接配置。

需要长期运行的 Shell 进程时使用会话：

```powershell
AgentRelay session create --device <id> --dir C:\src\project
AgentRelay session send --device <id> --id <session-id> --input "Write-Output 'hello'`r`n"
AgentRelay session get --device <id> --id <session-id>
AgentRelay session close --device <id> --id <session-id>
```

远端路径按选中连接的授权根目录检查，根目录内的远端绝对路径会转换为相对路径。`local_sync` 模式下，`sync` 通过 SHA-256 manifest 比较差异并分块上传变化文件，目录和单文件不受 32 MiB RPC 载荷上限约束。默认应用嵌套的 `.gitignore` 和根目录 `.agentrelayignore`；可重复使用 `--exclude` 添加规则，或用 `--no-gitignore` 关闭 `.gitignore`。`--mode update` 保留远端额外文件，`--mode mirror` 只删除此前由同步管理、但本地已不存在的文件；`--dry-run` 只返回计划。同步成功后，在该连接上持久化本地项目到远端目录的映射，后续命令可以使用同一个本地目录。任务和会话保存在内存中，Worker 进程重启后不会恢复。

无需重新配对即可管理授权：

```powershell
AgentRelay device rename --device dev-a1b2c3d4 --name build-machine
AgentRelay device revoke --device build-machine
AgentRelay device authorize --device build-machine
AgentRelay device remove --device build-machine
```

## 桌面能力

可选的桌面工具可以查询显示器、截取 PNG 并发送鼠标/键盘输入。Worker 必须运行在已登录的交互会话中。Windows 坐标使用物理像素，macOS 使用 point；使用截图元数据把预览像素映射到原生坐标。

```powershell
AgentRelay desktop info --device <id>
AgentRelay desktop capture --device <id> --display 0 --max-width 960 --max-height 540
AgentRelay desktop input --device <id> --json '{"action":"click","x":300,"y":204}'
```

权限、坐标换算、限制以及 MCP/API 契约见[桌面能力文档](docs/desktop.md)。

## 文档导航

| 文档 | 面向读者 | 内容 |
| --- | --- | --- |
| [Architecture](docs/architecture.md) | 运维和集成开发者 | 组件、信任模型、持久化、生命周期和 API 范围 |
| [Development](docs/development.md) | 贡献者 | 目录结构、构建/测试、实现边界和改动检查清单 |
| [Desktop access](docs/desktop.md) | 桌面能力使用者和维护者 | 原生后端、截图 Artifact、输入契约和平台验证 |

## 安全提示

主控管理 API 仅接受本机回环请求。Worker 主动建立出站连接，并使用批准配对时签发的凭据认证。文件操作限制在授权 Workspace 内；桌面输入会影响整个交互桌面，应按高权限自动化处理。

实现细节、兼容性限制和端点列表请先阅读 [Architecture](docs/architecture.md)；修改代码请遵循 [Development](docs/development.md)。

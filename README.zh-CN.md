[English](README.md) | 简体中文

# DevFreight

## 什么是 DevFreight？

DevFreight 是面向现有 Coding Agent 的开源跨设备开发与联调基础设施。它让 Codex、Claude Code、Cursor 等 Agent 通过标准 MCP 或 CLI 接口，把真实的 Windows、macOS、Linux 电脑作为可调用的执行环境，完成代码同步、原生终端执行、编译构建、测试调试及结果回收，无需修改 Agent 内部架构。

**一个 Agent，多个设备执行环境。** 开发和任务决策仍由主控电脑上的 Agent 完成，目标设备提供各自的操作系统、编译器和工具链。源码默认保留在主控电脑上，通过增量同步更新远端工作区。每台设备通过经过授权的 SSH 或原生 Worker 连接，按实际能力提供操作接口；SSH 也可直接使用已有的远端仓库。

Agent 可以在 Windows 上开发项目、在 Mac 上构建验证，也可以同时在多个操作系统上运行测试，或在一台设备上启动服务、在另一台设备上测试客户端。执行日志、测试报告和构建产物统一回收到主控，供 Agent 分析问题、修改代码并持续验证。

```text
Coding Agent -- MCP stdio / CLI --> Controller（主控端，HTTPS）
                                      | 设备的执行连接
                        +-------------+-------------+
                        |                           |
                  SSH + 原生 Shell           原生 Worker（WSS）
                  Shell / 文件读取           Shell / 文件 / 任务
                                             会话 / 桌面
```

目标设备只需运行独立的原生 Worker，无需安装大模型、Python 环境、完整 Agent 或 SSH Server。Worker 不开放入站端口，局域网和网线连接使用同一套协议，未来的 USB IP 网络也可复用。DevFreight 负责设备连接、工作区和执行状态管理，任务规划与模型调用仍由外部 Agent 完成。

## 使用条件

- 一台运行 Coding Agent 和 DevFreight Controller 的 Windows、macOS 或 Linux 主控电脑。
- 一台 Windows、macOS 或 Linux 被控电脑，提供工作区目录，可使用 SSH + 系统原生 Shell/工具，或原生 DevFreight Worker。SSH 无需 Python 等额外语言运行时；详见[原生 SSH 执行器](docs/ssh.md)。
- Worker 能访问主控电脑；主控默认接收 TCP `8764`，局域网发现使用 UDP `8763`。
- Worker 被控端仅运行一个原生可执行文件，不需要 Python、Node.js、LLM 或 Agent Runtime。macOS 桌面能力需要“屏幕录制”和“辅助功能”权限；Linux 暂不提供桌面操作。

## 快速开始

主控端使用 Controller 和 MCP 程序，被控端仅使用独立 Worker。可以从仓库构建或使用发行版：

```powershell
go build -o DevFreight.exe ./cmd/DevFreight
go build -ldflags "-H=windowsgui" -o DevFreight-mcp.exe ./cmd/DevFreight-mcp
go build -o DevFreight-worker.exe ./cmd/DevFreight-worker
```

1. 主控电脑运行 `DevFreight gui` 或 `DevFreight setup` 打开设备管理；终端模式运行 `DevFreight controller serve`。
2. 被控电脑运行 `DevFreight-worker setup`，选择发现到的主控端、工作区和设备别名。
3. 核对两端显示的验证数字，在主控 GUI 或终端批准请求。Worker 保存凭据并自动重连。
4. 后续被控端运行 `DevFreight-worker serve`。Windows 也可使用 `start-controller.bat` 和 `start-worker.bat`。

配对请求五分钟后过期，批准后才创建设备和凭据。跨网段时，可在 Worker setup 中指定 `--controller`、经主控核对的 `--fingerprint`、`--workspace` 和 `--name`；不接受未固定证书的 URL。`scripts/build.ps1` 或 `sh scripts/build.sh` 可构建三种系统的 amd64/arm64 版本。macOS/Linux 程序名不带 `.exe`，MCP 构建不使用 Windows 专用链接参数。

配置默认位于用户配置目录下的 `DevFreight` 子目录，可通过 `DEVFREIGHT_HOME` 覆盖。从此前项目名称升级时，请更新 MCP 配置、环境变量和 `.devfreightignore` 文件名，并使用新的状态重新配置、配对设备。设备发现和连接标识也使用新名称，因此 Controller 和 Worker 需一起升级。

## 接入 Agent

无需部署 Worker 时，在 GUI 中选择“新增设备 / 更换为 SSH”，填写配置后点击“测试”。SSH 首版支持命令执行和工作区文件读取；原生 Worker 提供增量同步、任务、持久会话和可选桌面能力。桌面操作取决于系统、交互会话及权限，不作为跨系统稳定能力保证。

每台设备只绑定一种连接方式。显式“更换连接方式”保留别名、Handle 和设备 ID，替换连接配置并废弃旧凭据与映射；只有执行请求、后台任务、会话和同步均已结束时才能更换。Worker 离线时需重连以确认活动状态。Agent 只看到设备和操作能力，SSH 的 `desktop` 始终为 `false`，不暴露连接配置或管理工具。详见[设备、连接和能力](docs/connections.md)。主控状态改用 schema 4，Worker 凭据仍为 schema 3（格式未变）；旧主控数据直接拒绝，请使用新的 `DEVFREIGHT_HOME` 重新配置和配对，无迁移或兼容层。


在主控电脑以运行 Controller 的同一用户执行：

```powershell
DevFreight mcp config
```

把返回的 `mcpServers` 条目加入 Coding Agent 的 MCP 配置。`mcp-config.bat` 也会输出同样的 JSON。升级 DevFreight 后重新连接 MCP 客户端，以刷新工具列表。

可用 MCP 工具包括 `device_list`、`device_exec`、`device_sync`、`device_pull`、`device_task`、`device_session`、`device_desktop`、`device_capture` 和 `device_input`。

## 常用操作

查看设备、同步项目、运行命令并获取结果：

```powershell
DevFreight device list
DevFreight sync --device build-machine --source C:\src\project
DevFreight sync --device build-machine --source C:\src\project --mode mirror --exclude .cache/
DevFreight exec --device build-machine --dir C:\src\project "dotnet test"
DevFreight task start --device build-machine --dir C:\src\project --timeout-ms 60000 "npm run build"
DevFreight task get --device build-machine --id <task-id>
DevFreight task retrieve --device build-machine --id <task-id>
DevFreight pull --device build-machine --out .\results <remote-root>\report.txt
```

`device_list` 返回配对时生成且持久化的短句柄（如 `dev-a1b2c3d4`）、别名、在线状态和能力，不返回设备专有 ID。所有 Agent/CLI 操作的 `device_id` 参数仍然必填，但可以填写短句柄或别名；断线、重连、增删设备都不会改变短句柄。

设备会暴露 `workspace_mode`：Worker 默认 `local_sync`，主控本地源码是同步源；SSH 默认 `remote`，远端仓库是源。两种模式均可不经同步直接执行。远端模式可直接运行 `DevFreight exec --device linux-dev --dir /home/dev/repo "pwd"`，并禁用同步以保护远端源码。`--dir-kind local|remote|auto`（MCP 的 `working_dir_kind`）明确路径属于主控还是被控端。GUI 中的模式和映射均按连接配置。

需要长期运行的 Shell 进程时使用会话：

```powershell
DevFreight session create --device <id> --dir C:\src\project
DevFreight session send --device <id> --id <session-id> --input "Write-Output 'hello'`r`n"
DevFreight session get --device <id> --id <session-id>
DevFreight session close --device <id> --id <session-id>
```

远端路径按设备连接的授权根目录检查，根目录内的远端绝对路径会转换为相对路径。`local_sync` 模式下，`sync` 通过 SHA-256 manifest 比较差异并分块上传变化文件，目录和单文件不受 32 MiB RPC 载荷上限约束。默认应用嵌套的 `.gitignore` 和根目录 `.devfreightignore`；可重复使用 `--exclude` 添加规则，或用 `--no-gitignore` 关闭 `.gitignore`。`--mode update` 保留远端额外文件，`--mode mirror` 只删除此前由同步管理、但本地已不存在的文件；`--dry-run` 只返回计划。同步成功后，在该连接上持久化本地项目到远端目录的映射，后续命令可以使用同一个本地目录。任务和会话保存在内存中，Worker 进程重启后不会恢复。

无需重新配对即可管理授权：

```powershell
DevFreight device rename --device dev-a1b2c3d4 --name build-machine
DevFreight device revoke --device build-machine
DevFreight device authorize --device build-machine
DevFreight device remove --device build-machine
```

## 桌面能力

可选的桌面工具可以查询显示器、截取 PNG 并发送鼠标/键盘输入。Worker 必须运行在已登录的交互会话中。Windows 坐标使用物理像素，macOS 使用 point；使用截图元数据把预览像素映射到原生坐标。

```powershell
DevFreight desktop info --device <id>
DevFreight desktop capture --device <id> --display 0 --max-width 960 --max-height 540
DevFreight desktop input --device <id> --json '{"action":"click","x":300,"y":204}'
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

## 许可证

DevFreight 使用 [Apache-2.0](LICENSE) 许可证。

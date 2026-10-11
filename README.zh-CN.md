[English](README.md) | 简体中文

# AgentRelay

AgentRelay 让 Coding Agent 安全、受控地连接其他电脑上的原生执行环境。Agent 只运行在主控电脑上；轻量 Worker 负责执行命令、同步文件、管理任务和会话，也可以按需控制交互桌面。

```text
Coding Agent -- MCP stdio / CLI --> Bridge（主控端，HTTPS）
                                      ^
                                      | 出站、认证的 WSS 连接
                                    Worker（目标电脑）
                                      |
                           Workspace / Shell / 桌面
```

Worker 不开放入站端口。局域网、网线以及未来的 USB IP 网络使用同一套协议。AgentRelay 不包含模型运行时或 Agent 循环。

## 使用条件

- 一台运行 Coding Agent 的 Windows 主控电脑，当前以 Windows PowerShell 为主要测试目标。
- 一台安装 AgentRelay Worker 并提供 Workspace 目录的目标电脑。
- Worker 能访问主控电脑；主控默认接收 TCP `8764`，局域网发现使用 UDP `8763`。
- macOS 桌面能力还需要“屏幕录制”和“辅助功能”权限。POSIX Shell 和 macOS 实机运行仍属实验性质。

## 快速开始

从仓库构建两个程序，或使用发行版中的对应程序：

```powershell
go build -o AgentRelay.exe ./cmd/AgentRelay
go build -ldflags "-H=windowsgui" -o AgentRelay-mcp.exe ./cmd/AgentRelay-mcp
```

1. 在主控电脑运行 `AgentRelay setup`，选择 `controller`。保持程序运行，它会启动 Bridge 并等待配对请求。
2. 在目标电脑运行 `AgentRelay setup`，选择 `worker`。选择发现到的主控端，指定 Workspace 目录和设备别名。
3. 核对两台电脑显示的验证数字，然后在主控端终端批准请求。Worker 会保存凭据并自动重连。
4. 后续启动时，主控运行 `AgentRelay bridge serve`，目标电脑运行 `AgentRelay worker serve`。Windows 下也可使用 `start-bridge.bat` 和 `start-worker.bat`。

配对请求五分钟后过期，只有批准后才会创建设备和凭据。配对必须选择自动发现的主控端，以便 Worker 固定证书指纹；未验证的手动 URL 会被拒绝。

## 接入 Agent

在主控电脑以运行 Bridge 的同一用户执行：

```powershell
AgentRelay mcp config
```

把返回的 `mcpServers` 条目加入 Coding Agent 的 MCP 配置。`mcp-config.bat` 也会输出同样的 JSON。升级 AgentRelay 后重新连接 MCP 客户端，以刷新工具列表。

可用 MCP 工具包括 `device_list`、`device_exec`、`device_sync`、`device_pull`、`device_task`、`device_session`、`device_desktop`、`device_capture` 和 `device_input`。

## 常用操作

查看设备、同步项目、运行命令并获取结果：

```powershell
AgentRelay device list
AgentRelay sync --device <id> --source C:\src\project
AgentRelay sync --device <id> --source C:\src\project --mode mirror --exclude .cache/
AgentRelay exec --device <id> --dir C:\src\project "dotnet test"
AgentRelay task start --device <id> --dir C:\src\project --timeout-ms 60000 "npm run build"
AgentRelay task get --device <id> --id <task-id>
AgentRelay task retrieve --device <id> --id <task-id>
AgentRelay pull --device <id> --out .\results <remote-root>\report.txt
```

需要长期运行的 Shell 进程时使用会话：

```powershell
AgentRelay session create --device <id> --dir C:\src\project
AgentRelay session send --device <id> --id <session-id> --input "Write-Output 'hello'`r`n"
AgentRelay session get --device <id> --id <session-id>
AgentRelay session close --device <id> --id <session-id>
```

远端路径都相对于 Worker 授权的 Workspace。`sync` 通过 SHA-256 manifest 比较差异，并以固定大小的分块上传变化文件，因此目录和单文件不再受 32 MiB RPC 载荷上限约束。默认应用嵌套的 `.gitignore` 和根目录 `.agentrelayignore`；可重复使用 `--exclude` 添加 gitignore 兼容规则，或用 `--no-gitignore` 关闭 `.gitignore`。`--mode update` 保留远端额外文件，`--mode mirror` 只删除此前由同步管理、但本地已不存在的文件；`--dry-run` 只返回计划。同步成功后会持久化本地项目到远端目录的映射，后续命令可以继续使用同一个本地项目目录。任务和会话只保存在内存中，Worker 进程重启后不会恢复。

无需重新配对即可管理授权：

```powershell
AgentRelay device rename --device <id> --name build-machine
AgentRelay device revoke --device <id>
AgentRelay device authorize --device <id>
AgentRelay device remove --device <id>
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

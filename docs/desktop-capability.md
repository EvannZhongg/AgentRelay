# Desktop Capability 架构与使用

AgentRelay 的原有职责是把主控端 Agent 的请求转发到已配对 Worker：`MCP/CLI → Bridge 本机 HTTPS → WSS RPC → Worker 内部 HTTP Handler → 原生操作`。协议集中在 `internal/protocol`，传输负责请求、响应、取消和心跳；Worker 不监听入站端口。Desktop 沿用这条调用链和原有授权机制。

## 模块职责

| 模块 | 职责 |
| --- | --- |
| `internal/protocol/desktop.go` | 能力、显示器、截图、Artifact、输入契约 |
| `internal/desktop/desktop.go` | `Backend` 接口、输入校验、权限检查、坐标换算、进程内串行化 |
| `internal/desktop/native_windows.go` | Win32 显示器枚举、GDI 截图、SendInput 鼠标和键盘 |
| `internal/desktop/native_darwin.go` | CoreGraphics/CoreFoundation 输入与屏幕元数据、系统 screencapture 截图 |
| `internal/desktop/unsupported.go` | 其他平台明确返回不支持 |
| `internal/worker/desktop.go` | 内部 Handler，注册时声明 Desktop 能力 |
| `internal/bridge/desktop.go` | 完整 PNG 校验、主控原图落盘、缩放预览 |
| `cmd/AgentRelay-mcp/desktop.go` | MCP 工具定义、标准图片 content 与结构化元数据 |
| `cmd/AgentRelay/desktop.go` | CLI 查询、截图与输入 |

平台只实现 `Info() (DesktopInfo, error)`、`Capture(context.Context, Display) (image.Image, error)`、`Input(context.Context, DesktopInput) error`。接口不依赖 MCP、Bridge 或 WebSocket，可以注入测试 Backend。平台代码仅负责操作系统细节，Service 统一处理坐标和调用顺序；Bridge 负责主控文件保存。

参考实现用于理解原生 API 和能力划分，运行时和构建均不依赖 RobotGo，也没有复制其平台封装。Windows 使用 Go 的系统调用；macOS 使用 `purego` 绑定系统 Framework，构建无需 CGO。预览缩放使用 `golang.org/x/image/draw`。保持仓库原有 Go 1.25。

## MCP 与 API

新增三个工具：

- `device_desktop(device_id)`：返回 backend、坐标单位、截屏/输入权限、所有显示器及其全局边界、像素尺寸和缩放比例。
- `device_capture(device_id, display_id=0, max_width=1600, max_height=1200)`：截取一台显示器。返回标准 MCP `{type:"image", mimeType:"image/png", data:"..."}`、JSON 文本元数据和 `structuredContent`。元数据不重复图片 base64。
- `device_input(device_id, action, ...)`：支持 `move`、`click`、`drag`、`scroll`、`key`、`text`。

Bridge 本机端点为 `GET /v1/devices/{id}/desktop`、`POST /v1/devices/{id}/desktop/capture` 和 `POST /v1/devices/{id}/desktop/input`，Worker 内部对应 `/v1/desktop` 路由。既有设备配对、证书验证、凭据验证、撤销授权与本机 API 限制继续生效。桌面输入作用于整个交互桌面，其范围超出文件 Workspace。旧 Worker 未声明能力时返回明确的升级提示。

更新 Bridge、Worker 和 MCP 可执行文件后，重新连接 MCP 客户端以刷新工具列表。Worker 应运行在登录用户的交互会话中。

## 截图与 Artifact

Worker 编码完整 PNG，通过现有 WSS 返回。Bridge 保留原始字节，在其状态文件同目录的 `artifacts/captures/capture-*.png` 保存独立文件，并返回：

```json
{
  "mime_type": "image/png",
  "width": 960,
  "height": 540,
  "coordinate_unit": "physical_pixels",
  "display": {
    "id": 0,
    "primary": true,
    "bounds": {"x": 0, "y": 0, "width": 1920, "height": 1080},
    "pixel_width": 1920, "pixel_height": 1080,
    "scale_x": 1, "scale_y": 1
  },
  "artifact": {
    "path": "C:\\Users\\user\\AppData\\Roaming\\AgentRelay\\artifacts\\captures\\capture-123.png",
    "mime_type": "image/png",
    "width": 1920, "height": 1080,
    "bytes": 133917,
    "sha256": "..."
  }
}
```

另有 `captured_at` 和 `displays`。Artifact 路径是**主控端**绝对路径，可直接由主控 Agent 读取高清原图。预览按比例缩小、从不放大；不影响 Artifact 原图。路径是本地文件引用，当前没有额外 Artifact 服务或下载端点。默认状态目录在 Windows 为 `%APPDATA%\AgentRelay`；`AGENTRELAY_HOME` 可指定目录。

原图目前没有自动过期或配额清理，需要按需手动清理 `artifacts/captures`。截图上限为 64M 像素、编码后 PNG 12 MiB；现有 RPC 的 JSON/base64 双层封装需要预留内存与帧大小空间。RPC 写入期限为 60 秒，读取心跳期限为 120 秒，MCP/CLI 截图调用期限为 90 秒。超限返回错误，不静默降低原图质量。

## 输入与坐标

Windows 全局坐标使用物理像素；每次原生调用设置并还原线程 DPI awareness，保证多屏截图与输入使用同一单位。macOS 全局坐标使用 point，Retina 原图像素可能为 point 的两倍或其他比例，返回的 `pixel_width/pixel_height` 与 `scale_x/scale_y` 描述此关系。

未提供 `display_id` 时，输入坐标是全局坐标，支持副屏位于主屏左侧/上方时的负值。提供 `display_id` 时，坐标相对该屏幕左上角。再提供 `reference_width/reference_height` 时，坐标按这张预览的尺寸换算到该屏幕的原生坐标：

```json
{"device_id":"<id>","action":"click","display_id":0,"reference_width":960,"reference_height":540,"x":150,"y":102}
```

在 1920×1080 Windows 屏幕上，上述点击落在全局 `(300,204)`。请使用截图返回的实际 `width/height`，不要假定预览总是达到最大尺寸。显示器索引从 0 开始，0 是主显示器；热插拔或主屏变化后索引可能改变，应重新查询/截图。

| action | 参数与行为 |
| --- | --- |
| `move` | 必需 `x,y` |
| `click` | 必需 `x,y`；`button=left/right/middle`；`clicks=1/2` |
| `drag` | 必需 `x,y,to_x,to_y`；可选 button；结束或取消时释放按钮 |
| `scroll` | `delta_x/delta_y`，正值向右/下，单位为滚轮档；每轴 -100..100；可选 `x,y` 定位 |
| `key` | `key` 为 a-z、0-9、enter、tab、space、backspace、delete、escape、方向键、home/end/pageup/pagedown、f1-f12；可选 `modifiers` |
| `text` | `text` 按 Unicode 输入，保留空白，不修改剪贴板；最多 16384 UTF-8 字节 |

修饰键支持 `ctrl/alt/shift/cmd/win`，最多四个且不可重复。Windows 的 cmd 对应 Windows 键，macOS 的 win 对应 Command。文本不是 Shell 命令；换行/Tab 最终表现受目标控件影响，应用导航建议用 `key=enter/tab`。操作在 Worker 进程内串行执行；取消不能撤销已完成的 OS 输入。连接中断时不自动重放，避免重复点击或输入。

CLI 示例（PowerShell）：

```powershell
AgentRelay desktop info --device <id>
AgentRelay desktop capture --device <id> --display 0 --max-width 960 --max-height 540
AgentRelay desktop input --device <id> --json '{"action":"click","x":300,"y":204}'
AgentRelay desktop input --device <id> --json '{"action":"key","key":"a","modifiers":["ctrl"]}'
AgentRelay desktop input --device <id> --json '{"action":"text","text":"你好"}'
```

CLI 截图输出元数据和原图路径；MCP 截图同时返回图片 content。

## 平台前提与验证

Windows 需要已解锁的交互桌面；系统安全桌面、UAC 提权窗口以及更高权限应用可能阻止输入。macOS 需要给 Worker 对应进程/启动宿主授予“屏幕录制”和“辅助功能”权限；`device_desktop` 返回当前授权状态。系统截屏使用 `/usr/sbin/screencapture`，原生事件使用 CoreGraphics，不依赖剪贴板或 AppleScript。

自动化覆盖预览坐标映射、负坐标屏幕、Retina 原图、非法输入、Unicode/空白、取消/释放、真实 HTTPS/WSS 调用链、标准 MCP 图片、Artifact 字节校验及设备撤销授权。Windows 在线设备已验证 1920×1080 原图与 960×540 MCP 预览、预览点击、Ctrl+A 事件、中文/emoji/空白/换行/Tab、Enter、Backspace；拖动产生正确的按下/释放事件并选中文本，向下滚动产生 -120 的原生滚轮事件。macOS arm64/amd64 已交叉编译；尚无 macOS 实机权限、Retina 和原生事件验证。后续在 macOS 上应优先验证这三项以及多屏负坐标场景。

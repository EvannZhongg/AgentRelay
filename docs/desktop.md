# Desktop Access

Desktop access follows the existing request path: `MCP/CLI -> Controller HTTPS -> authenticated WSS -> Worker desktop handler -> native OS API`. It uses the existing pairing, certificate, credential and authorization checks. The Worker must run in a logged-in interactive session.

## Modules

| Module | Responsibility |
| --- | --- |
| `internal/protocol/desktop.go` | Capability, display, capture, artifact and input contracts |
| `internal/desktop` | Backend interface, validation, permission checks, coordinate conversion and serialization |
| `internal/desktop/native_windows.go` | Win32 display enumeration, GDI capture and SendInput |
| `internal/desktop/native_darwin.go` | CoreGraphics metadata/input and system `screencapture` |
| `internal/desktop/unsupported.go` | Explicit unsupported-platform response |
| `internal/controller/desktop.go` | PNG validation, original artifact storage and preview scaling |
| `cmd/AgentRelay-mcp/desktop.go` | MCP tool schemas and image content |
| `cmd/AgentRelay/desktop.go` | CLI commands |

The platform backend implements `Info`, `Capture` and `Input` without depending on MCP, Controller or WebSocket packages. The implementation does not depend on RobotGo and builds without CGO.

## Tools And Endpoints

- `device_desktop(device_id)` returns permissions, coordinate unit, displays, global bounds, pixel dimensions and scale.
- `device_capture(device_id, display_id, max_width, max_height)` returns a standard MCP PNG image plus display metadata and a controller-local original artifact.
- `device_input(device_id, action, ...)` supports `move`, `click`, `drag`, `scroll`, `key` and `text`.

The controller endpoints are `GET /v1/devices/{id}/desktop`, `POST /v1/devices/{id}/desktop/capture` and `POST /v1/devices/{id}/desktop/input`. An older Worker that does not advertise desktop capability receives an explicit upgrade error.

## Captures And Artifacts

The Worker encodes the full PNG and returns it through the existing RPC. The Controller preserves the original under its state directory at `artifacts/captures/capture-*.png` and returns metadata similar to:

~~~json
{
  "mime_type": "image/png",
  "width": 960,
  "height": 540,
  "coordinate_unit": "physical_pixels",
  "display": {"id": 0, "pixel_width": 1920, "pixel_height": 1080},
  "artifact": {
    "path": "C:\\Users\\user\\AppData\\Roaming\\AgentRelay\\artifacts\\captures\\capture-123.png",
    "width": 1920,
    "height": 1080,
    "bytes": 133917,
    "sha256": "..."
  }
}
~~~

The artifact path is on the controller and can be read by the controller-side agent. Previews are scaled down proportionally and never enlarged. The current implementation does not expire or quota artifacts; clean up `artifacts/captures` manually. A capture is limited to 64M pixels and a 12 MiB encoded PNG. Use the returned preview `width` and `height`; do not assume the requested maximum was reached.

## Coordinates And Input

Windows global coordinates use physical pixels. macOS global coordinates use points; Retina pixel dimensions and `scale_x`/`scale_y` describe the relationship. Without `display_id`, coordinates are global and may be negative when a display is left or above the primary display. With `display_id`, coordinates are relative to that display. With `reference_width` and `reference_height`, preview pixels are mapped to native display coordinates:

~~~json
{"device_id":"<id>","action":"click","display_id":0,"reference_width":960,"reference_height":540,"x":150,"y":102}
~~~

On a 1920x1080 Windows display this maps to native `(300,204)`. Display indexes start at zero, and hot-plugging or changing the primary display can change them; query desktop info again after such changes.

| Action | Parameters |
| --- | --- |
| `move` | `x`, `y` |
| `click` | `x`, `y\); optional `button` (`left`, `right`, `middle`) and `clicks` (`1`, `2`) |
| `drag` | `x`, `y`, `to_x`, `to_y\); optional `button` |
| `scroll` | `delta_x`, `delta_y` in wheel steps from -100 to 100; optional `x`, `y` |
| `key` | named key plus optional `modifiers` (`ctrl`, `alt`, `shift`, `cmd`, `win`) |
| `text` | exact Unicode text, up to 16384 UTF-8 bytes |

Input is serialized in the Worker. Cancellation cannot undo an OS event that already happened, and disconnected input is never replayed. Text is not a shell command; use `key` for navigation such as Enter or Tab.

## Platform Requirements

Windows requires an unlocked interactive desktop. Secure desktops, elevated prompts and higher-integrity applications may reject input. macOS requires Screen Recording and Accessibility permissions for the Worker process or its launch host. `device_desktop` reports the current permission state.

After updating Controller, Worker and the MCP executable, reconnect the MCP client to refresh tool definitions. Validate Windows capture/input on a connected device; macOS arm64/amd64 builds are supported, but runtime permission, Retina and multi-display validation is still required.

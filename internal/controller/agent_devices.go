package controller

import "AgentRelay/internal/protocol"

// AgentDeviceView is an allowlist, so future private connection fields cannot
// accidentally enter MCP, CLI discovery or the device information operation.
func AgentDeviceView(d protocol.Device) map[string]any {
	caps := map[string]any{
		"exec": d.Capabilities.Exec, "files": d.Capabilities.Files,
		"desktop": d.Capabilities.Desktop != nil && d.Capabilities.Desktop.Supported,
		"sync":    d.Capabilities.Sync, "tasks": d.Capabilities.Tasks, "sessions": d.Capabilities.Sessions,
	}
	if len(d.Capabilities.Shells) != 0 {
		caps["shells"] = d.Capabilities.Shells
	}
	if d.Capabilities.MaxPayload != 0 {
		caps["max_payload_bytes"] = d.Capabilities.MaxPayload
	}
	return map[string]any{
		"device": d.Handle, "handle": d.Handle, "alias": d.Name,
		"online": d.Online, "authorized": d.Authorized, "desktop_access": d.DesktopAccess,
		"os": d.OS, "architecture": d.Architecture,
		"workspace": d.Workspace, "workspace_mode": d.WorkspaceMode, "workspace_mappings": d.Mappings,
		"capabilities": caps,
	}
}

func AgentDevices(devices []protocol.Device) []map[string]any {
	out := make([]map[string]any, 0, len(devices))
	for _, d := range SortDevices(devices) {
		out = append(out, AgentDeviceView(d))
	}
	return out
}

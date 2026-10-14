package main

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"DevFreight/internal/controller"
	"DevFreight/internal/protocol"
)

func desktopToolDefinitions() []map[string]any {
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	integer := func(description string) map[string]any {
		return map[string]any{"type": "integer", "description": description}
	}
	device := str("Persistent device handle or alias. Do not use the private device ID.")
	return []map[string]any{
		{"name": "device_desktop", "description": "Inspect native desktop access, permissions, connected monitors and global bounds. Windows coordinates use physical pixels; macOS uses points. Display indices can change when monitors reconnect.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"device_id": device}, "required": []string{"device_id"}}, "annotations": map[string]any{"readOnlyHint": true}},
		{"name": "device_capture", "description": "Capture a device display. Returns a standard MCP PNG image and monitor metadata. The full-resolution original is saved as a controller-local artifact with path, dimensions and SHA256. Use returned width/height as reference_width/reference_height with device_input display_id to click preview pixels.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"device_id": device, "display_id": map[string]any{"type": "integer", "minimum": 0, "default": 0}, "max_width": map[string]any{"type": "integer", "minimum": 1, "maximum": 8192, "default": 1600}, "max_height": map[string]any{"type": "integer", "minimum": 1, "maximum": 8192, "default": 1200}}, "required": []string{"device_id"}}, "annotations": map[string]any{"readOnlyHint": true}},
		{"name": "device_input", "description": "Perform native mouse or keyboard input in the active interactive desktop. Actions: move/click require x,y; drag also requires to_x,to_y; scroll uses delta_x/delta_y (positive right/down, wheel notches) and optional x,y; key taps a named key with modifiers; text types exact Unicode without changing the clipboard. Coordinates are global unless display_id is supplied, in which case they are relative to that display. Optional reference dimensions map screenshot-preview pixels to native coordinates. Actions are serialized and are never replayed on reconnect.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"device_id": device, "action": map[string]any{"type": "string", "enum": []string{"move", "click", "drag", "scroll", "key", "text"}},
			"x": integer("Horizontal coordinate."), "y": integer("Vertical coordinate."), "to_x": integer("Drag end horizontal coordinate."), "to_y": integer("Drag end vertical coordinate."),
			"display_id": map[string]any{"type": "integer", "minimum": 0}, "reference_width": map[string]any{"type": "integer", "minimum": 1}, "reference_height": map[string]any{"type": "integer", "minimum": 1},
			"button": map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}, "default": "left"}, "clicks": map[string]any{"type": "integer", "minimum": 1, "maximum": 2},
			"delta_x": map[string]any{"type": "integer", "minimum": -100, "maximum": 100}, "delta_y": map[string]any{"type": "integer", "minimum": -100, "maximum": 100},
			"key": str("Named key: a-z, 0-9, enter, tab, space, backspace, delete, escape, arrows, home, end, pageup, pagedown, f1-f12."), "modifiers": map[string]any{"type": "array", "maxItems": 4, "uniqueItems": true, "items": map[string]any{"type": "string", "enum": []string{"ctrl", "alt", "shift", "cmd", "win"}}}, "text": str("Exact text; whitespace and newlines are preserved. Maximum 16384 UTF-8 bytes.")}, "required": []string{"device_id", "action"}}, "annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false}},
	}
}

func callDesktop(ctx context.Context, c *controller.Client, name string, args map[string]any) (any, error) {
	id, err := requiredString(args, "device_id")
	if err != nil {
		return nil, err
	}
	fields := make(map[string]any, len(args))
	for k, v := range args {
		if k != "device_id" {
			fields[k] = v
		}
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	decode := func(dst any) error {
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		return d.Decode(dst)
	}
	timeout := 30 * time.Second
	if name == "device_capture" {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch name {
	case "device_desktop":
		if err := decode(&struct{}{}); err != nil {
			return nil, err
		}
		info, err := c.Desktop(ctx, id)
		return toolJSON(info), err
	case "device_capture":
		var req protocol.CaptureRequest
		if err := decode(&req); err != nil {
			return nil, err
		}
		out, err := c.Capture(ctx, id, req)
		if err != nil {
			return nil, err
		}
		img := out.ImageBase64
		out.ImageBase64 = ""
		return map[string]any{"content": []any{map[string]any{"type": "image", "data": img, "mimeType": out.MimeType}, map[string]any{"type": "text", "text": mustJSON(out)}}, "structuredContent": out}, nil
	default:
		var req protocol.DesktopInput
		if err := decode(&req); err != nil {
			return nil, err
		}
		out, err := c.Input(ctx, id, req)
		return toolJSON(out), err
	}
}

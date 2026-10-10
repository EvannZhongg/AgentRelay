package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"AgentRelay/internal/bridge"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/testutil"
)

type testDesktop struct{ last protocol.DesktopInput }

func (f *testDesktop) Info() (protocol.DesktopInfo, error) {
	return protocol.DesktopInfo{DesktopCapability: protocol.DesktopCapability{Supported: true, Backend: "fake"}, CaptureAllowed: true, InputAllowed: true, CoordinateUnit: "physical_pixels", Displays: []protocol.Display{{ID: 0, Primary: true, Bounds: protocol.Bounds{Width: 800, Height: 600}, PixelWidth: 800, PixelHeight: 600, ScaleX: 1, ScaleY: 1}}}, nil
}
func (f *testDesktop) Capture(context.Context, protocol.Display) (image.Image, error) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	img.Set(200, 150, color.RGBA{R: 255, A: 255})
	return img, nil
}
func (f *testDesktop) Input(ctx context.Context, r protocol.DesktopInput) error {
	f.last = r
	return nil
}

func TestMCPDesktopImageArtifactAndInput(t *testing.T) {
	f := &testDesktop{}
	relay := testutil.NewWithDesktop(t, "", f)
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		args["device_id"] = relay.Device.ID
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
		out, err := callTool(context.Background(), relay.Client, params)
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}
	call("device_desktop", map[string]any{})
	result := call("device_capture", map[string]any{"max_width": 400, "max_height": 300})
	content := result["content"].([]any)
	block := content[0].(map[string]any)
	if block["type"] != "image" || block["mimeType"] != "image/png" {
		t.Fatalf("invalid MCP image: %+v", block)
	}
	data, err := base64.StdEncoding.DecodeString(block["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Bounds().Dx() != 400 || preview.Bounds().Dy() != 300 {
		t.Fatal("incorrect preview size")
	}
	metadata := content[1].(map[string]any)["text"].(string)
	if strings.Contains(metadata, "image_base64") {
		t.Fatal("base64 duplicated in metadata")
	}
	var capture protocol.CaptureResponse
	if err := json.Unmarshal([]byte(metadata), &capture); err != nil {
		t.Fatal(err)
	}
	if capture.Artifact == nil || !filepath.IsAbs(capture.Artifact.Path) {
		t.Fatal("missing local artifact reference")
	}
	data, err = os.ReadFile(capture.Artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if original.Bounds().Dx() != 800 || capture.Artifact.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("original artifact was altered")
	}
	call("device_input", map[string]any{"action": "click", "display_id": 0, "x": 100, "y": 75, "reference_width": 400, "reference_height": 300})
	if *f.last.X != 200 || *f.last.Y != 150 {
		t.Fatalf("preview click mapped to %+v", f.last)
	}
	text := "  \u4e2d\u6587\n "
	call("device_input", map[string]any{"action": "text", "text": text})
	if f.last.Text != text {
		t.Fatalf("input trimmed: %q", f.last.Text)
	}
	no := false
	if _, err = relay.Client.UpdateDevice(context.Background(), relay.Device.ID, bridge.DeviceUpdate{Authorized: &no}); err != nil {
		t.Fatal(err)
	}
	if _, err = relay.Client.Capture(context.Background(), relay.Device.ID, protocol.CaptureRequest{}); err == nil {
		t.Fatal("revoked device allowed capture")
	}
}

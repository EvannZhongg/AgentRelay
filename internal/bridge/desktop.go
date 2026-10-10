package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"AgentRelay/internal/protocol"
	"golang.org/x/image/draw"
)

func (c *Client) Desktop(ctx context.Context, id string) (out protocol.DesktopInfo, err error) {
	err = c.Do(ctx, "GET", devicePath(id)+"/desktop", nil, &out)
	return
}
func (c *Client) Capture(ctx context.Context, id string, req protocol.CaptureRequest) (out protocol.CaptureResponse, err error) {
	err = c.Do(ctx, "POST", devicePath(id)+"/desktop/capture", req, &out)
	return
}
func (c *Client) Input(ctx context.Context, id string, req protocol.DesktopInput) (out protocol.DesktopInputResult, err error) {
	err = c.Do(ctx, "POST", devicePath(id)+"/desktop/input", req, &out)
	return
}

func (s *Server) captureArtifact(request, response []byte) ([]byte, error) {
	var req protocol.CaptureRequest
	var out protocol.CaptureResponse
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	if req.MaxWidth < 0 || req.MaxHeight < 0 || req.MaxWidth > 8192 || req.MaxHeight > 8192 {
		return nil, errors.New("invalid preview dimensions")
	}
	if err := json.Unmarshal(response, &out); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(out.ImageBase64)
	if err != nil {
		return nil, err
	}
	if len(data) > 12<<20 {
		return nil, errors.New("capture exceeds artifact byte limit")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("invalid capture PNG: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 64<<20 {
		return nil, errors.New("capture exceeds artifact pixel limit")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(s.cfg.StatePath), "artifacts", "captures"))
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(root, "capture-*.png")
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		_ = f.Close()
		if !complete {
			_ = os.Remove(f.Name())
		}
	}()
	if err = f.Chmod(0600); err != nil {
		return nil, err
	}
	if _, err = f.Write(data); err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	out.Artifact = &protocol.CaptureArtifact{Path: f.Name(), MimeType: "image/png", Width: config.Width, Height: config.Height, Bytes: len(data), SHA256: hex.EncodeToString(sum[:])}
	w, h := req.MaxWidth, req.MaxHeight
	if w == 0 {
		w = 1600
	}
	if h == 0 {
		h = 1200
	}
	scale := math.Min(1, math.Min(float64(w)/float64(config.Width), float64(h)/float64(config.Height)))
	out.Width, out.Height = max(1, int(float64(config.Width)*scale)), max(1, int(float64(config.Height)*scale))
	if scale < 1 {
		preview := image.NewRGBA(image.Rect(0, 0, out.Width, out.Height))
		draw.ApproxBiLinear.Scale(preview, preview.Bounds(), img, img.Bounds(), draw.Src, nil)
		var buf bytes.Buffer
		if err = png.Encode(&buf, preview); err != nil {
			return nil, err
		}
		out.ImageBase64 = base64.StdEncoding.EncodeToString(buf.Bytes())
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	complete = true
	return encoded, nil
}

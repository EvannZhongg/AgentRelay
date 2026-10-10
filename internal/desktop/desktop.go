// Package desktop owns native desktop access independently of the transport.
package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"time"
	"unicode/utf8"

	"AgentRelay/internal/protocol"
)

var ErrUnavailable = errors.New("desktop access unavailable; an interactive Windows or macOS session is required")

func withRelease(action, release func() error) error {
	defer func() {
		if p := recover(); p != nil {
			_ = release()
			panic(p)
		}
	}()
	err := action()
	return errors.Join(err, release())
}

type Backend interface {
	Info() (protocol.DesktopInfo, error)
	Capture(context.Context, protocol.Display) (image.Image, error)
	Input(context.Context, protocol.DesktopInput) error
}

type Service struct{ backend Backend }

// All desktop calls share a lock because input affects process-global OS state.
var gate = make(chan struct{}, 1)

func New() *Service                           { return NewWithBackend(newBackend()) }
func NewWithBackend(b Backend) *Service       { return &Service{backend: b} }
func Capability() *protocol.DesktopCapability { return platformCapability() }

func (s *Service) lock(ctx context.Context) error {
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Info(ctx context.Context) (protocol.DesktopInfo, error) {
	if err := s.lock(ctx); err != nil {
		return protocol.DesktopInfo{}, err
	}
	defer func() { <-gate }()
	return s.backend.Info()
}

func display(info protocol.DesktopInfo, id int) (protocol.Display, error) {
	for _, d := range info.Displays {
		if d.ID == id {
			return d, nil
		}
	}
	return protocol.Display{}, fmt.Errorf("display_id %d is not connected", id)
}

func (s *Service) Capture(ctx context.Context, req protocol.CaptureRequest) (protocol.CaptureResponse, error) {
	var out protocol.CaptureResponse
	if req.MaxWidth < 0 || req.MaxHeight < 0 || req.MaxWidth > 8192 || req.MaxHeight > 8192 {
		return out, errors.New("preview dimensions must be between 0 and 8192")
	}
	if err := s.lock(ctx); err != nil {
		return out, err
	}
	defer func() { <-gate }()
	info, err := s.backend.Info()
	if err != nil {
		return out, err
	}
	if !info.CaptureAllowed {
		return out, errors.New("screen capture permission denied; grant Screen Recording to the worker on macOS")
	}
	d, err := display(info, req.DisplayID)
	if err != nil {
		return out, err
	}
	if d.PixelWidth <= 0 || d.PixelHeight <= 0 || int64(d.PixelWidth)*int64(d.PixelHeight) > 64<<20 {
		return out, errors.New("display exceeds capture pixel limit")
	}
	img, err := s.backend.Capture(ctx, d)
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if img == nil || img.Bounds().Empty() {
		return out, errors.New("native capture returned an empty image")
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		return out, err
	}
	// RPC wraps base64 JSON in another base64 envelope; leave headroom for both.
	if data.Len() > 12<<20 {
		return out, errors.New("full-resolution PNG exceeds the 12 MiB capture limit")
	}
	d.PixelWidth, d.PixelHeight = img.Bounds().Dx(), img.Bounds().Dy()
	d.ScaleX, d.ScaleY = float64(d.PixelWidth)/float64(d.Bounds.Width), float64(d.PixelHeight)/float64(d.Bounds.Height)
	for i := range info.Displays {
		if info.Displays[i].ID == d.ID {
			info.Displays[i] = d
		}
	}
	return protocol.CaptureResponse{Display: d, Displays: info.Displays, CoordinateUnit: info.CoordinateUnit, CapturedAt: time.Now(), MimeType: "image/png", Width: d.PixelWidth, Height: d.PixelHeight, ImageBase64: base64.StdEncoding.EncodeToString(data.Bytes())}, nil
}

func (s *Service) Input(ctx context.Context, req protocol.DesktopInput) (protocol.DesktopInputResult, error) {
	var out protocol.DesktopInputResult
	if err := validate(req); err != nil {
		return out, err
	}
	if err := s.lock(ctx); err != nil {
		return out, err
	}
	defer func() { <-gate }()
	info, err := s.backend.Info()
	if err != nil {
		return out, err
	}
	if !info.InputAllowed {
		return out, errors.New("input permission denied; grant Accessibility to the worker on macOS")
	}
	if req.X != nil {
		x, y, err := coordinates(info, req, *req.X, *req.Y)
		if err != nil {
			return out, err
		}
		req.X, req.Y = &x, &y
		if req.Action == "drag" {
			tx, ty, err := coordinates(info, req, *req.ToX, *req.ToY)
			if err != nil {
				return out, err
			}
			req.ToX, req.ToY = &tx, &ty
		}
	}
	if req.Button == "" {
		req.Button = "left"
	}
	if req.Clicks == 0 {
		req.Clicks = 1
	}
	if err := s.backend.Input(ctx, req); err != nil {
		return out, err
	}
	return protocol.DesktopInputResult{Action: req.Action, CompletedAt: time.Now()}, nil
}

func validate(r protocol.DesktopInput) error {
	if (r.X == nil) != (r.Y == nil) {
		return errors.New("x and y must be supplied together")
	}
	if (r.Action == "key" || r.Action == "text") && r.X != nil {
		return errors.New("keyboard actions do not accept mouse coordinates")
	}
	switch r.Action {
	case "move", "click", "drag":
		if r.X == nil || r.Y == nil {
			return errors.New("x and y are required")
		}
	case "scroll":
		if r.DeltaX == 0 && r.DeltaY == 0 {
			return errors.New("scroll requires a nonzero delta")
		}
	case "key":
		if r.Key == "" {
			return errors.New("key is required")
		}
	case "text":
		if !utf8.ValidString(r.Text) || len(r.Text) > 16384 || r.Text == "" {
			return errors.New("text must be nonempty UTF-8, at most 16384 bytes")
		}
	default:
		return errors.New("action must be move, click, drag, scroll, key or text")
	}
	if r.Action == "drag" && (r.ToX == nil || r.ToY == nil) {
		return errors.New("drag requires to_x and to_y")
	}
	if r.Button != "" && r.Button != "left" && r.Button != "right" && r.Button != "middle" {
		return errors.New("button must be left, right or middle")
	}
	if r.Clicks < 0 || r.Clicks > 2 {
		return errors.New("clicks must be 1 or 2")
	}
	if r.DeltaX < -100 || r.DeltaX > 100 || r.DeltaY < -100 || r.DeltaY > 100 {
		return errors.New("scroll deltas must be between -100 and 100")
	}
	if r.ReferenceWidth < 0 || r.ReferenceHeight < 0 || (r.ReferenceWidth == 0) != (r.ReferenceHeight == 0) {
		return errors.New("reference_width and reference_height must both be positive")
	}
	if r.ReferenceWidth > 0 && r.DisplayID == nil {
		return errors.New("reference dimensions require display_id")
	}
	if len(r.Modifiers) > 4 {
		return errors.New("at most four modifiers are supported")
	}
	seen := map[string]bool{}
	for _, m := range r.Modifiers {
		if (m != "ctrl" && m != "alt" && m != "shift" && m != "cmd" && m != "win") || seen[m] {
			return errors.New("modifiers must be unique ctrl, alt, shift, cmd or win")
		}
		seen[m] = true
	}
	return nil
}

func coordinates(info protocol.DesktopInfo, r protocol.DesktopInput, x, y int) (int, int, error) {
	if r.DisplayID != nil {
		d, err := display(info, *r.DisplayID)
		if err != nil {
			return 0, 0, err
		}
		w, h := d.Bounds.Width, d.Bounds.Height
		if r.ReferenceWidth > 0 {
			w, h = r.ReferenceWidth, r.ReferenceHeight
		}
		if x < 0 || y < 0 || x >= w || y >= h {
			return 0, 0, errors.New("coordinates are outside the selected display")
		}
		x = d.Bounds.X + int(math.Floor(float64(x)*float64(d.Bounds.Width)/float64(w)))
		y = d.Bounds.Y + int(math.Floor(float64(y)*float64(d.Bounds.Height)/float64(h)))
	}
	for _, d := range info.Displays {
		b := d.Bounds
		if x >= b.X && y >= b.Y && x-b.X < b.Width && y-b.Y < b.Height {
			return x, y, nil
		}
	}
	return 0, 0, errors.New("coordinates are outside the connected displays")
}

package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"testing"

	"DevFreight/internal/protocol"
)

type fakeBackend struct {
	calls        int
	last         protocol.DesktopInput
	inputAllowed bool
}

func (f *fakeBackend) Info() (protocol.DesktopInfo, error) {
	return protocol.DesktopInfo{CaptureAllowed: true, InputAllowed: f.inputAllowed, CoordinateUnit: "points", Displays: []protocol.Display{{ID: 0, Bounds: protocol.Bounds{X: -1200, Y: 100, Width: 1200, Height: 800}, PixelWidth: 2400, PixelHeight: 1600, ScaleX: 2, ScaleY: 2}}}, nil
}
func (f *fakeBackend) Capture(context.Context, protocol.Display) (image.Image, error) {
	return image.NewRGBA(image.Rect(0, 0, 2400, 1600)), nil
}
func (f *fakeBackend) Input(ctx context.Context, r protocol.DesktopInput) error {
	f.calls++
	f.last = r
	return nil
}
func ptr(n int) *int { return &n }

func TestPreviewCoordinatesAndUnicode(t *testing.T) {
	f := &fakeBackend{inputAllowed: true}
	s := NewWithBackend(f)
	r := protocol.DesktopInput{Action: "click", DisplayID: ptr(0), X: ptr(300), Y: ptr(200), ReferenceWidth: 600, ReferenceHeight: 400}
	if _, err := s.Input(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if *f.last.X != -600 || *f.last.Y != 500 {
		t.Fatalf("mapped input: %+v", f.last)
	}
	text := "  \u4e2d\u6587\U0001f600\n "
	if _, err := s.Input(context.Background(), protocol.DesktopInput{Action: "text", Text: text}); err != nil {
		t.Fatal(err)
	}
	if f.last.Text != text {
		t.Fatalf("text changed: %q", f.last.Text)
	}
}

func TestRejectedInputHasNoSideEffects(t *testing.T) {
	f := &fakeBackend{inputAllowed: true}
	s := NewWithBackend(f)
	for _, r := range []protocol.DesktopInput{
		{Action: "click", X: ptr(0)}, {Action: "click", X: ptr(9000), Y: ptr(9000)},
		{Action: "drag", X: ptr(-10), Y: ptr(100)}, {Action: "click", X: ptr(0), Y: ptr(0), DisplayID: ptr(99)},
		{Action: "click", X: ptr(600), Y: ptr(200), DisplayID: ptr(0), ReferenceWidth: 600, ReferenceHeight: 400},
		{Action: "key", Key: "a", Modifiers: []string{"ctrl", "ctrl"}}, {Action: "text", Text: string([]byte{0xff})},
		{Action: "scroll", DeltaY: 101}, {Action: "scroll", DeltaY: 1, X: ptr(1)},
		{Action: "key", Key: "a", X: ptr(1)}, {Action: "text", Text: "test", X: ptr(1), Y: ptr(1)},
		{Action: "move", X: ptr(-100), Y: ptr(100), ReferenceWidth: 100, ReferenceHeight: 100},
	} {
		if _, err := s.Input(context.Background(), r); err == nil {
			t.Fatalf("accepted invalid input: %+v", r)
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid input reached native backend")
	}
	f.inputAllowed = false
	if _, err := s.Input(context.Background(), protocol.DesktopInput{Action: "text", Text: "test"}); err == nil {
		t.Fatal("permission denial ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Input(ctx, protocol.DesktopInput{Action: "text", Text: "test"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestFullResolutionCapture(t *testing.T) {
	s := NewWithBackend(&fakeBackend{})
	out, err := s.Capture(context.Background(), protocol.CaptureRequest{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(out.ImageBase64)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 2400 || out.Display.ScaleX != 2 || out.Width != 2400 {
		t.Fatalf("lost Retina resolution: %+v", out)
	}
	if _, err := s.Capture(context.Background(), protocol.CaptureRequest{DisplayID: 4}); err == nil {
		t.Fatal("invalid monitor accepted")
	}
}

func TestReleaseOnFailure(t *testing.T) {
	want := errors.New("cancelled drag")
	released := false
	err := withRelease(func() error { return want }, func() error { released = true; return nil })
	if !released || !errors.Is(err, want) {
		t.Fatal("held input was not released")
	}
}

//go:build darwin

package desktop

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"AgentRelay/internal/protocol"
	"github.com/ebitengine/purego"
)

type cgPoint struct{ X, Y float64 }
type cgSize struct{ Width, Height float64 }
type cgRect struct {
	Origin cgPoint
	Size   cgSize
}

type macBackend struct {
	err             error
	mainDisplay     func() uint32
	activeDisplays  func(uint32, *uint32, *uint32) int32
	bounds          func(uint32) cgRect
	pixelWidth      func(uint32) uint64
	pixelHeight     func(uint32) uint64
	copyMode        func(uint32) uintptr
	modePixelWidth  func(uintptr) uint64
	modePixelHeight func(uintptr) uint64
	releaseMode     func(uintptr)
	captureAllowed  func() bool
	inputAllowed    func() bool
	createMouse     func(uintptr, uint32, cgPoint, uint32) uintptr
	createKey       func(uintptr, uint16, bool) uintptr
	createScroll    func(uintptr, uint32, uint32, int32, int32, int32) uintptr
	setUnicode      func(uintptr, uint64, *uint16)
	setFlags        func(uintptr, uint64)
	setInteger      func(uintptr, uint32, int64)
	post            func(uint32, uintptr)
	release         func(uintptr)
	createEvent     func(uintptr) uintptr
	location        func(uintptr) cgPoint
}

func platformCapability() *protocol.DesktopCapability {
	return &protocol.DesktopCapability{Supported: true, Backend: "coregraphics", Actions: []string{"move", "click", "drag", "scroll", "key", "text"}}
}

func newBackend() Backend {
	b := &macBackend{}
	frameworks := []struct {
		path     string
		bindings map[string]any
	}{
		{"/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", map[string]any{
			"CGDisplayCopyDisplayMode": &b.copyMode, "CGDisplayModeGetPixelWidth": &b.modePixelWidth, "CGDisplayModeGetPixelHeight": &b.modePixelHeight, "CGDisplayModeRelease": &b.releaseMode,
			"CGMainDisplayID": &b.mainDisplay, "CGGetActiveDisplayList": &b.activeDisplays, "CGDisplayBounds": &b.bounds, "CGDisplayPixelsWide": &b.pixelWidth, "CGDisplayPixelsHigh": &b.pixelHeight, "CGPreflightScreenCaptureAccess": &b.captureAllowed,
			"CGEventCreateMouseEvent": &b.createMouse, "CGEventCreateKeyboardEvent": &b.createKey, "CGEventCreateScrollWheelEvent2": &b.createScroll, "CGEventKeyboardSetUnicodeString": &b.setUnicode, "CGEventSetFlags": &b.setFlags, "CGEventSetIntegerValueField": &b.setInteger, "CGEventPost": &b.post, "CGEventCreate": &b.createEvent, "CGEventGetLocation": &b.location}},
		{"/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", map[string]any{"CFRelease": &b.release}},
		{"/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", map[string]any{"AXIsProcessTrusted": &b.inputAllowed}},
	}
	for _, f := range frameworks {
		h, err := purego.Dlopen(f.path, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			b.err = err
			return b
		}
		// System frameworks stay loaded for the lifetime of their function pointers.
		for name, fn := range f.bindings {
			addr, err := purego.Dlsym(h, name)
			if err != nil {
				b.err = fmt.Errorf("%s: %w", name, err)
				return b
			}
			purego.RegisterFunc(fn, addr)
		}
	}
	return b
}

func (b *macBackend) Info() (protocol.DesktopInfo, error) {
	info := protocol.DesktopInfo{DesktopCapability: *platformCapability(), CoordinateUnit: "points"}
	if b.err != nil {
		return info, b.err
	}
	info.CaptureAllowed, info.InputAllowed = b.captureAllowed(), b.inputAllowed()
	var count uint32
	if b.activeDisplays(0, nil, &count) != 0 || count == 0 || count > 128 {
		return info, ErrUnavailable
	}
	ids := make([]uint32, count)
	if b.activeDisplays(count, &ids[0], &count) != 0 {
		return info, errors.New("CGGetActiveDisplayList failed")
	}
	if count == 0 || int(count) > len(ids) {
		return info, errors.New("display topology changed; retry desktop info")
	}
	ids = ids[:count]
	for i, id := range ids {
		if id == b.mainDisplay() {
			ids[0], ids[i] = ids[i], ids[0]
			break
		}
	}
	for i, id := range ids {
		r := b.bounds(id)
		w, h := int(r.Size.Width), int(r.Size.Height)
		if w <= 0 || h <= 0 {
			return info, errors.New("invalid display bounds")
		}
		pw, ph := int(b.pixelWidth(id)), int(b.pixelHeight(id))
		if mode := b.copyMode(id); mode != 0 {
			pw, ph = int(b.modePixelWidth(mode)), int(b.modePixelHeight(mode))
			b.releaseMode(mode)
		}
		info.Displays = append(info.Displays, protocol.Display{ID: i, Primary: id == b.mainDisplay(), Bounds: protocol.Bounds{X: int(r.Origin.X), Y: int(r.Origin.Y), Width: w, Height: h}, PixelWidth: pw, PixelHeight: ph, ScaleX: float64(pw) / float64(w), ScaleY: float64(ph) / float64(h)})
	}
	return info, nil
}

func (b *macBackend) Capture(ctx context.Context, d protocol.Display) (image.Image, error) {
	f, err := os.CreateTemp("", "agentrelay-capture-*.png")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Close(); err != nil {
		return nil, err
	}
	// Apple's screenshot utility uses the supported system capture path on new
	// macOS releases and preserves Retina pixels. -R selects global point bounds.
	r := d.Bounds
	region := fmt.Sprintf("%d,%d,%d,%d", r.X, r.Y, r.Width, r.Height)
	output, err := exec.CommandContext(ctx, "/usr/sbin/screencapture", "-x", "-t", "png", "-R", region, name).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("screencapture: %w: %s (check Screen Recording permission)", err, strings.TrimSpace(string(output)))
	}
	f, err = os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	config, err := png.DecodeConfig(f)
	if err != nil {
		return nil, err
	}
	if int64(config.Width)*int64(config.Height) > 64<<20 {
		return nil, errors.New("capture exceeds pixel limit")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return nil, err
	}
	return png.Decode(f)
}

func (b *macBackend) postEvent(ev uintptr) error {
	if ev == 0 {
		return errors.New("CoreGraphics could not create input event")
	}
	defer b.release(ev)
	b.post(0, ev)
	return nil
}

func macKey(key string) (uint16, error) {
	k := strings.ToLower(key)
	named := map[string]uint16{"enter": 36, "tab": 48, "space": 49, "backspace": 51, "delete": 117, "escape": 53, "esc": 53, "left": 123, "right": 124, "down": 125, "up": 126, "home": 115, "end": 119, "pageup": 116, "pagedown": 121, "ctrl": 59, "shift": 56, "alt": 58, "cmd": 55, "win": 55,
		"a": 0, "b": 11, "c": 8, "d": 2, "e": 14, "f": 3, "g": 5, "h": 4, "i": 34, "j": 38, "k": 40, "l": 37, "m": 46, "n": 45, "o": 31, "p": 35, "q": 12, "r": 15, "s": 1, "t": 17, "u": 32, "v": 9, "w": 13, "x": 7, "y": 16, "z": 6, "0": 29, "1": 18, "2": 19, "3": 20, "4": 21, "5": 23, "6": 22, "7": 26, "8": 28, "9": 25}
	if v, ok := named[k]; ok {
		return v, nil
	}
	fs := []uint16{122, 120, 99, 118, 96, 97, 98, 100, 101, 109, 103, 111}
	if strings.HasPrefix(k, "f") {
		i, err := strconv.Atoi(k[1:])
		if err == nil && i >= 1 && i <= len(fs) {
			return fs[i-1], nil
		}
	}
	return 0, fmt.Errorf("unsupported key %q; use text for literal characters", key)
}

func (b *macBackend) Input(ctx context.Context, r protocol.DesktopInput) error {
	if b.err != nil {
		return b.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p := cgPoint{}
	if r.X != nil {
		p = cgPoint{float64(*r.X), float64(*r.Y)}
	} else {
		ev := b.createEvent(0)
		if ev == 0 {
			return errors.New("cannot query cursor")
		}
		p = b.location(ev)
		b.release(ev)
	}
	button, down, up, drag := uint32(0), uint32(1), uint32(2), uint32(6)
	if r.Button == "right" {
		button, down, up, drag = 1, 3, 4, 7
	}
	if r.Button == "middle" {
		button, down, up, drag = 2, 25, 26, 27
	}
	mouse := func(kind uint32, pos cgPoint, count int) error {
		ev := b.createMouse(0, kind, pos, button)
		if ev != 0 {
			b.setInteger(ev, 1, int64(count))
		}
		return b.postEvent(ev)
	}
	if r.X != nil {
		if err := mouse(5, p, 1); err != nil {
			return err
		}
	}
	switch r.Action {
	case "move":
		return nil
	case "click":
		for i := 1; i <= r.Clicks; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := mouse(down, p, i); err != nil {
				return err
			}
			if err := mouse(up, p, i); err != nil {
				return err
			}
			if i < r.Clicks {
				time.Sleep(50 * time.Millisecond)
			}
		}
		return nil
	case "drag":
		if err := mouse(down, p, 1); err != nil {
			return err
		}
		current := p
		return withRelease(func() error {
			for i := 1; i <= 20; i++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				current = cgPoint{p.X + (float64(*r.ToX)-p.X)*float64(i)/20, p.Y + (float64(*r.ToY)-p.Y)*float64(i)/20}
				if err := mouse(drag, current, 1); err != nil {
					return err
				}
				time.Sleep(10 * time.Millisecond)
			}
			return nil
		}, func() error { return mouse(up, current, 1) })
	case "scroll":
		return b.postEvent(b.createScroll(0, 1, 2, int32(-r.DeltaY), int32(-r.DeltaX), 0))
	case "key":
		key, err := macKey(r.Key)
		if err != nil {
			return err
		}
		flags := uint64(0)
		for _, m := range r.Modifiers {
			flags |= map[string]uint64{"shift": 1 << 17, "ctrl": 1 << 18, "alt": 1 << 19, "cmd": 1 << 20, "win": 1 << 20}[m]
		}
		for _, down := range []bool{true, false} {
			ev := b.createKey(0, key, down)
			if ev != 0 {
				b.setFlags(ev, flags)
			}
			if err := b.postEvent(ev); err != nil {
				return err
			}
		}
		return nil
	case "text":
		for _, c := range r.Text {
			if err := ctx.Err(); err != nil {
				return err
			}
			units := utf16.Encode([]rune{c})
			for _, down := range []bool{true, false} {
				ev := b.createKey(0, 0, down)
				if ev != 0 {
					b.setUnicode(ev, uint64(len(units)), &units[0])
				}
				if err := b.postEvent(ev); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return errors.New("unsupported action")
}

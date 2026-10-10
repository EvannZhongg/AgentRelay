//go:build windows

package desktop

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"AgentRelay/internal/protocol"
)

var user32 = syscall.NewLazyDLL("user32.dll")
var gdi32 = syscall.NewLazyDLL("gdi32.dll")

type windowsBackend struct{}

func newBackend() Backend { return windowsBackend{} }
func platformCapability() *protocol.DesktopCapability {
	return &protocol.DesktopCapability{Supported: true, Backend: "win32", Actions: []string{"move", "click", "drag", "scroll", "key", "text"}}
}

// Keep capture and input in the same physical-pixel coordinate system even on
// mixed-DPI monitors, without changing the DPI policy of the entire process.
func dpiThread() func() {
	runtime.LockOSThread()
	p := user32.NewProc("SetThreadDpiAwarenessContext")
	var previous uintptr
	if p.Find() == nil {
		previous, _, _ = p.Call(^uintptr(3))
	}
	return func() {
		if previous != 0 {
			_, _, _ = p.Call(previous)
		}
		runtime.UnlockOSThread()
	}
}

func winCall(dll *syscall.LazyDLL, name string, args ...uintptr) (uintptr, error) {
	r, _, e := dll.NewProc(name).Call(args...)
	if r == 0 {
		if e == syscall.Errno(0) {
			return 0, fmt.Errorf("%s failed", name)
		}
		return 0, fmt.Errorf("%s: %w", name, e)
	}
	return r, nil
}

type winRect struct{ Left, Top, Right, Bottom int32 }
type monitorInfo struct {
	Size          uint32
	Monitor, Work winRect
	Flags         uint32
}

type monitorEnumeration struct {
	info *protocol.DesktopInfo
	err  error
}

var monitorMu sync.Mutex
var activeEnumeration *monitorEnumeration

// Go's Windows callbacks cannot be freed; allocate this trampoline only once.
var monitorCallback = syscall.NewCallback(func(monitor, dc, rect, data uintptr) uintptr {
	enum := activeEnumeration
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if _, err := winCall(user32, "GetMonitorInfoW", monitor, uintptr(unsafe.Pointer(&mi))); err != nil {
		enum.err = err
		return 0
	}
	b := mi.Monitor
	w, h := int(b.Right-b.Left), int(b.Bottom-b.Top)
	enum.info.Displays = append(enum.info.Displays, protocol.Display{ID: len(enum.info.Displays), Primary: mi.Flags&1 != 0, Bounds: protocol.Bounds{X: int(b.Left), Y: int(b.Top), Width: w, Height: h}, PixelWidth: w, PixelHeight: h, ScaleX: 1, ScaleY: 1})
	return 1
})

func (windowsBackend) Info() (protocol.DesktopInfo, error) {
	monitorMu.Lock()
	defer monitorMu.Unlock()
	defer dpiThread()()
	info := protocol.DesktopInfo{DesktopCapability: *platformCapability(), CoordinateUnit: "physical_pixels"}
	d, _, _ := user32.NewProc("OpenInputDesktop").Call(0, 0, 0x101)
	if d != 0 {
		info.CaptureAllowed, info.InputAllowed = true, true
		_, _, _ = user32.NewProc("CloseDesktop").Call(d)
	}
	enum := monitorEnumeration{info: &info}
	activeEnumeration = &enum
	defer func() { activeEnumeration = nil }()
	if _, err := winCall(user32, "EnumDisplayMonitors", 0, 0, monitorCallback, 0); err != nil {
		return info, err
	}
	if enum.err != nil {
		return info, enum.err
	}
	if len(info.Displays) == 0 {
		return info, ErrUnavailable
	}
	// Primary display is always index 0; secondary indices follow enumeration.
	for i := range info.Displays {
		if info.Displays[i].Primary {
			info.Displays[0], info.Displays[i] = info.Displays[i], info.Displays[0]
			break
		}
	}
	for i := range info.Displays {
		info.Displays[i].ID = i
	}
	return info, nil
}

func (windowsBackend) Capture(ctx context.Context, d protocol.Display) (image.Image, error) {
	defer dpiThread()()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w, h := d.Bounds.Width, d.Bounds.Height
	screen, err := winCall(user32, "GetDC", 0)
	if err != nil {
		return nil, err
	}
	defer user32.NewProc("ReleaseDC").Call(0, screen)
	dc, err := winCall(gdi32, "CreateCompatibleDC", screen)
	if err != nil {
		return nil, err
	}
	defer gdi32.NewProc("DeleteDC").Call(dc)
	bmp, err := winCall(gdi32, "CreateCompatibleBitmap", screen, uintptr(w), uintptr(h))
	if err != nil {
		return nil, err
	}
	defer gdi32.NewProc("DeleteObject").Call(bmp)
	old, err := winCall(gdi32, "SelectObject", dc, bmp)
	if err != nil {
		return nil, err
	}
	_, err = winCall(gdi32, "BitBlt", dc, 0, 0, uintptr(w), uintptr(h), screen, uintptr(d.Bounds.X), uintptr(d.Bounds.Y), 0x40CC0020)
	_, _, _ = gdi32.NewProc("SelectObject").Call(dc, old)
	if err != nil {
		return nil, err
	}
	var header [40]byte
	binary.LittleEndian.PutUint32(header[0:], 40)
	binary.LittleEndian.PutUint32(header[4:], uint32(w))
	binary.LittleEndian.PutUint32(header[8:], uint32(-int32(h)))
	binary.LittleEndian.PutUint16(header[12:], 1)
	binary.LittleEndian.PutUint16(header[14:], 32)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rows, err := winCall(gdi32, "GetDIBits", dc, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(unsafe.Pointer(&header[0])), 0)
	if err != nil {
		return nil, err
	}
	if int(rows) != h {
		return nil, errors.New("GetDIBits returned an incomplete screenshot")
	}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+2], img.Pix[i+3] = img.Pix[i+2], img.Pix[i], 255
	}
	return img, nil
}

func sendInput(kind uint32, fill func([]byte)) error {
	size, offset := 28, 4
	if unsafe.Sizeof(uintptr(0)) == 8 {
		size, offset = 40, 8
	}
	buf := make([]byte, size)
	binary.LittleEndian.PutUint32(buf, kind)
	fill(buf[offset:])
	r, _, err := user32.NewProc("SendInput").Call(1, uintptr(unsafe.Pointer(&buf[0])), uintptr(size))
	if r != 1 {
		return fmt.Errorf("SendInput failed (desktop may be locked or target elevated): %v", err)
	}
	return nil
}

func winMouse(flags uint32, data int32) error {
	return sendInput(0, func(b []byte) {
		binary.LittleEndian.PutUint32(b[8:], uint32(data))
		binary.LittleEndian.PutUint32(b[12:], flags)
	})
}

func winKey(vk, scan uint16, flags uint32) error {
	return sendInput(1, func(b []byte) {
		binary.LittleEndian.PutUint16(b, vk)
		binary.LittleEndian.PutUint16(b[2:], scan)
		binary.LittleEndian.PutUint32(b[4:], flags)
	})
}

func windowsKey(key string) (uint16, uint32, error) {
	k := strings.ToLower(key)
	named := map[string]uint16{"enter": 13, "tab": 9, "space": 32, "backspace": 8, "delete": 46, "escape": 27, "esc": 27, "left": 37, "up": 38, "right": 39, "down": 40, "home": 36, "end": 35, "pageup": 33, "pagedown": 34, "insert": 45, "ctrl": 17, "alt": 18, "shift": 16, "win": 91, "cmd": 91}
	v, ok := named[k]
	if !ok && len(k) == 1 && ((k[0] >= 'a' && k[0] <= 'z') || (k[0] >= '0' && k[0] <= '9')) {
		v = uint16(strings.ToUpper(k)[0])
		ok = true
	}
	if !ok {
		for i := 1; i <= 12; i++ {
			if k == fmt.Sprintf("f%d", i) {
				v = uint16(111 + i)
				ok = true
				break
			}
		}
	}
	if !ok {
		return 0, 0, fmt.Errorf("unsupported key %q; use text for literal characters", key)
	}
	flags := uint32(0)
	if (v >= 33 && v <= 46) || v == 91 {
		flags = 1
	}
	return v, flags, nil
}

func (windowsBackend) Input(ctx context.Context, r protocol.DesktopInput) error {
	defer dpiThread()()
	move := func(x, y int) error { _, err := winCall(user32, "SetCursorPos", uintptr(x), uintptr(y)); return err }
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.X != nil {
		if err := move(*r.X, *r.Y); err != nil {
			return err
		}
	}
	down, up := uint32(2), uint32(4)
	if r.Button == "right" {
		down, up = 8, 16
	}
	if r.Button == "middle" {
		down, up = 32, 64
	}
	switch r.Action {
	case "move":
		return nil
	case "click":
		for i := 0; i < r.Clicks; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := winMouse(down, 0); err != nil {
				return err
			}
			if err := winMouse(up, 0); err != nil {
				return err
			}
			if i+1 < r.Clicks {
				time.Sleep(50 * time.Millisecond)
			}
		}
		return nil
	case "drag":
		if err := winMouse(down, 0); err != nil {
			return err
		}
		return withRelease(func() error {
			for i := 1; i <= 20; i++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := move(*r.X+(*r.ToX-*r.X)*i/20, *r.Y+(*r.ToY-*r.Y)*i/20); err != nil {
					return err
				}
				time.Sleep(10 * time.Millisecond)
			}
			return nil
		}, func() error { return winMouse(up, 0) })
	case "scroll":
		if r.DeltaY != 0 {
			if err := winMouse(0x800, int32(-r.DeltaY*120)); err != nil {
				return err
			}
		}
		if r.DeltaX != 0 {
			return winMouse(0x1000, int32(r.DeltaX*120))
		}
		return nil
	case "text":
		for _, u := range utf16.Encode([]rune(r.Text)) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := winKey(0, u, 4); err != nil {
				return err
			}
			if err := winKey(0, u, 6); err != nil {
				return err
			}
		}
		return nil
	case "key":
		v, flags, err := windowsKey(r.Key)
		if err != nil {
			return err
		}
		type heldKey struct {
			vk    uint16
			flags uint32
		}
		var held []heldKey
		return withRelease(func() error {
			for _, m := range r.Modifiers {
				vk, f, e := windowsKey(m)
				if e != nil {
					return e
				}
				if e = winKey(vk, 0, f); e != nil {
					return e
				}
				held = append(held, heldKey{vk, f})
			}
			if err := winKey(v, 0, flags); err != nil {
				return err
			}
			return winKey(v, 0, flags|2)
		}, func() error {
			var err error
			for i := len(held) - 1; i >= 0; i-- {
				err = errors.Join(err, winKey(held[i].vk, 0, held[i].flags|2))
			}
			return err
		})
	}
	return errors.New("unsupported action")
}

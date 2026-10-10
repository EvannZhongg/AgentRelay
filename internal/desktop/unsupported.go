//go:build !windows && !darwin

package desktop

import (
	"AgentRelay/internal/protocol"
	"context"
	"image"
)

type unsupported struct{}

func newBackend() Backend { return unsupported{} }
func platformCapability() *protocol.DesktopCapability {
	return &protocol.DesktopCapability{Backend: "unsupported"}
}
func (unsupported) Info() (protocol.DesktopInfo, error) {
	return protocol.DesktopInfo{}, ErrUnavailable
}
func (unsupported) Capture(context.Context, protocol.Display) (image.Image, error) {
	return nil, ErrUnavailable
}
func (unsupported) Input(context.Context, protocol.DesktopInput) error { return ErrUnavailable }

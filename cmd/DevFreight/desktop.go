package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"
	"time"

	"DevFreight/internal/controller"
	"DevFreight/internal/protocol"
)

func desktopCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: DevFreight desktop info|capture|input --device <id> [--json <object>]")
	}
	f := flag.NewFlagSet("desktop", flag.ContinueOnError)
	base := f.String("controller", controller.DefaultURL, "controller URL")
	id := f.String("device", "", "paired device ID")
	display := f.Int("display", 0, "display index for capture")
	width := f.Int("max-width", 1600, "preview maximum width")
	height := f.Int("max-height", 1200, "preview maximum height")
	raw := f.String("json", "", "input request as JSON")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("--device is required")
	}
	c := controller.NewClient(*base)
	timeout := 30 * time.Second
	if args[0] == "capture" {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	switch args[0] {
	case "info":
		out, err := c.Desktop(ctx, *id)
		if err != nil {
			return err
		}
		return printJSON(out)
	case "capture":
		out, err := c.Capture(ctx, *id, protocol.CaptureRequest{DisplayID: *display, MaxWidth: *width, MaxHeight: *height})
		if err != nil {
			return err
		}
		out.ImageBase64 = ""
		return printJSON(out)
	case "input":
		var req protocol.DesktopInput
		d := json.NewDecoder(strings.NewReader(*raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&req); err != nil {
			return err
		}
		if err := d.Decode(new(any)); err != io.EOF {
			return errors.New("expected one input JSON object")
		}
		out, err := c.Input(ctx, *id, req)
		if err != nil {
			return err
		}
		return printJSON(out)
	default:
		return errors.New("desktop action must be info, capture or input")
	}
}

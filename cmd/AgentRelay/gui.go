package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"AgentRelay/internal/controller"
	"AgentRelay/internal/discovery"
)

func guiCommand(args []string) error {
	f := flag.NewFlagSet("gui", flag.ContinueOnError)
	listen := f.String("listen", ":8764", "controller listen address")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: AgentRelay gui [--listen :8764]")
	}
	s, err := controller.New(controller.Config{ListenAddr: *listen})
	if err != nil {
		return err
	}
	defer s.Close()
	_, portText, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("listen port must be between 1 and 65535")
	}
	url := fmt.Sprintf("https://localhost:%d/ui", port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host, _ := os.Hostname()
	go func() {
		if err := discovery.Serve(ctx, host, s.Fingerprint(), port); err != nil && ctx.Err() == nil {
			// GUI mode has no terminal; discovery failure is non-fatal because
			// the management UI can still be opened locally.
		}
	}()
	errCh := make(chan error, 1)
	go func() { errCh <- s.ListenAndServe() }()
	time.Sleep(250 * time.Millisecond)
	if err := openBrowser(url); err != nil {
		return fmt.Errorf("open management UI: %w", err)
	}
	return <-errCh
}

func openBrowser(url string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		command, args = "open", []string{url}
	default:
		command, args = "xdg-open", []string{url}
	}
	return exec.Command(command, args...).Start()
}

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"AgentRelay/internal/bridge"
	"AgentRelay/internal/discovery"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"
	"AgentRelay/internal/worker"
)

func prompt(input *bufio.Reader, label, fallback string) (string, error) {
	fmt.Printf("%s", label)
	if fallback != "" {
		fmt.Printf(" [%s]", fallback)
	}
	fmt.Print(": ")
	line, err := input.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("interactive setup requires terminal input: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = fallback
	}
	return line, nil
}
func setupCommand(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: AgentRelay setup")
	}
	input := bufio.NewReader(os.Stdin)
	role, err := prompt(input, "This computer: 1 controller, 2 worker", "1")
	if err != nil {
		return err
	}
	switch role {
	case "1", "controller":
		return bridgeCommand([]string{"serve"})
	case "2", "worker":
		var credentials protocol.Credentials
		if err := state.Load(state.Path("worker"), &credentials); err != nil {
			return err
		}
		if credentials.DeviceID != "" {
			return runWorker(credentials)
		}
		return setupWorker(input, "", "")
	default:
		return errors.New("choose 1 or 2")
	}
}
func workerCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: AgentRelay worker setup|serve")
	}
	f := flag.NewFlagSet("worker", flag.ContinueOnError)
	base := f.String("bridge", "", "controller URL (discovered when omitted)")
	workspace := f.String("workspace", "", "workspace for first setup")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "setup" {
		return setupWorker(bufio.NewReader(os.Stdin), *base, *workspace)
	}
	if args[0] != "serve" {
		return errors.New("usage: AgentRelay worker setup|serve")
	}
	if *base != "" || *workspace != "" {
		return errors.New("configure bridge and workspace with worker setup; serve uses saved pairing")
	}
	var credentials protocol.Credentials
	if err := state.Load(state.Path("worker"), &credentials); err != nil {
		return err
	}
	if credentials.DeviceID == "" {
		return setupWorker(bufio.NewReader(os.Stdin), "", "")
	}
	return runWorker(credentials)
}
func setupWorker(input *bufio.Reader, base, workspace string) error {
	ctx := context.Background()
	if base == "" {
		fmt.Println("Searching for controllers...")
		found, err := discovery.Discover(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Discovery:", err)
		}
		if len(found) > 0 {
			for i, item := range found {
				fmt.Printf("%d. %s (%s)\n", i+1, item.Name, item.URL)
			}
			selection, err := prompt(input, "Controller number, or URL", "1")
			if err != nil {
				return err
			}
			index, err := strconv.Atoi(selection)
			if err == nil {
				if index < 1 || index > len(found) {
					return errors.New("invalid controller number")
				}
				base = found[index-1].URL
			} else {
				base = selection
			}
		}
		if base == "" {
			var err error
			base, err = prompt(input, "Controller URL", "")
			if err != nil {
				return err
			}
		}
	}
	code, err := prompt(input, "Pairing code shown on the controller", "")
	if err != nil {
		return err
	}
	if workspace == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		workspace, err = prompt(input, "Workspace folder", filepath.Join(home, "AgentRelay", "workspace"))
		if err != nil {
			return err
		}
	}
	host, _ := os.Hostname()
	name, err := prompt(input, "Device alias", host)
	if err != nil {
		return err
	}
	s, err := worker.New(worker.Config{Workspace: workspace, Name: name})
	if err != nil {
		return err
	}
	credentials, err := s.Pair(ctx, base, code, state.Path("worker"))
	if err != nil {
		return err
	}
	fmt.Printf("Paired as %s. Configuration saved.\n", credentials.DeviceID)
	return runWorker(credentials)
}
func runWorker(credentials protocol.Credentials) error {
	s, err := worker.New(worker.Config{Workspace: credentials.Workspace, DeviceID: credentials.DeviceID})
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	fmt.Printf("Connecting to %s (workspace %s). Ctrl+C to stop.\n", credentials.BridgeURL, credentials.Workspace)
	err = s.Run(ctx, credentials, func(err error) { fmt.Fprintln(os.Stderr, err) })
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
func bridgeCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: AgentRelay bridge serve|pair")
	}
	f := flag.NewFlagSet("bridge", flag.ContinueOnError)
	listen := f.String("listen", ":8764", "controller listen address")
	base := f.String("bridge", bridge.DefaultURL, "local controller URL")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "pair" {
		pairing, err := bridge.NewClient(*base).Pairing(context.Background())
		if err != nil {
			return err
		}
		fmt.Printf("Pairing code (one use, valid for five minutes):\n%s\n", pairing.Code)
		return nil
	}
	if args[0] != "serve" {
		return errors.New("usage: AgentRelay bridge serve|pair")
	}
	s, err := bridge.New(bridge.Config{ListenAddr: *listen})
	if err != nil {
		return err
	}
	defer s.Close()
	_, portText, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return err
	}
	if port < 1 || port > 65535 {
		return errors.New("listen port must be between 1 and 65535")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host, _ := os.Hostname()
	go func() {
		if err := discovery.Serve(ctx, host, s.Fingerprint(), port); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "Discovery unavailable:", err)
		}
	}()
	fmt.Printf("Controller listening on %s. Ctrl+C to stop.\n", *listen)
	fmt.Printf("Pairing code (one use, valid for five minutes):\n%s\n", s.NewPairing().Code)
	fmt.Println("For another device or an expired code: AgentRelay bridge pair")
	return s.ListenAndServe()
}

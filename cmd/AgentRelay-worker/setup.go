package main

import (
	"AgentRelay/internal/discovery"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"
	"AgentRelay/internal/worker"
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
func workerCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: AgentRelay-worker setup|serve")
	}
	f := flag.NewFlagSet("worker", flag.ContinueOnError)
	workspace := f.String("workspace", "", "workspace for first setup")
	base := f.String("controller", "", "controller HTTPS URL; requires --fingerprint")
	fingerprint := f.String("fingerprint", "", "trusted controller certificate SHA-256 fingerprint")
	name := f.String("name", "", "device alias for manual pairing")
	mode := f.String("workspace-mode", protocol.WorkspaceLocalSync, "workspace mode: local_sync or remote")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "setup" {
		if *base != "" || *fingerprint != "" {
			if *base == "" || *fingerprint == "" || *workspace == "" || *name == "" {
				return errors.New("manual setup requires --controller, --fingerprint, --workspace and --name")
			}
			return pairWorker(*base, *fingerprint, *workspace, *name, *mode)
		}
		return setupWorker(bufio.NewReader(os.Stdin), *workspace, *mode)
	}
	if args[0] != "serve" {
		return errors.New("usage: AgentRelay-worker setup|serve")
	}
	if *workspace != "" || *base != "" || *fingerprint != "" || *name != "" {
		return errors.New("configure controller and workspace with worker setup; serve uses saved pairing")
	}
	var credentials protocol.Credentials
	if err := state.Load(state.Path("worker"), &credentials); err != nil {
		return err
	}
	if credentials.DeviceID == "" {
		return setupWorker(bufio.NewReader(os.Stdin), "", *mode)
	}
	if credentials.Schema != 3 {
		return errors.New("unsupported Worker credentials schema; pair using fresh state")
	}
	return runWorker(credentials)
}
func setupWorker(input *bufio.Reader, workspace, mode string) error {
	ctx := context.Background()
	fmt.Println("Searching for controllers...")
	found, err := discovery.Discover(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Discovery:", err)
	}
	if len(found) == 0 {
		return errors.New("no controller with a trusted certificate was discovered")
	}
	for i, item := range found {
		fmt.Printf("%d. %s (%s)\n", i+1, item.Name, item.URL)
	}
	selection, err := prompt(input, "Controller number", "1")
	if err != nil {
		return err
	}
	index, err := strconv.Atoi(selection)
	if err != nil || index < 1 || index > len(found) {
		return errors.New("select a discovered controller")
	}
	base := found[index-1].URL
	fingerprint := found[index-1].Fingerprint
	if fingerprint == "" {
		return errors.New("discovered controller did not provide a certificate fingerprint")
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
	return pairWorker(base, fingerprint, workspace, name, mode)
}
func pairWorker(base, fingerprint, workspace, name, mode string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	s, err := worker.New(worker.Config{Workspace: workspace, WorkspaceMode: mode, Name: name})
	if err != nil {
		return err
	}
	defer s.Close()
	credentials, err := s.Pair(ctx, base, fingerprint, state.Path("worker"), func(request protocol.PairingRequest) {
		fmt.Printf("Pairing request sent. Approve device %s on the controller; verification number: %s\n", request.Device.Name, request.Verification)
		fmt.Println("Waiting for controller approval...")
	})
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
	fmt.Printf("Connecting to %s (workspace %s). Ctrl+C to stop.\n", credentials.ControllerURL, credentials.Workspace)
	err = s.Run(ctx, credentials, func(err error) { fmt.Fprintln(os.Stderr, err) })
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

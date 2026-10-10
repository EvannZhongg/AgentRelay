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
	"time"

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
		return setupWorker(input, "")
	default:
		return errors.New("choose 1 or 2")
	}
}
func workerCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: AgentRelay worker setup|serve")
	}
	f := flag.NewFlagSet("worker", flag.ContinueOnError)
	workspace := f.String("workspace", "", "workspace for first setup")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "setup" {
		return setupWorker(bufio.NewReader(os.Stdin), *workspace)
	}
	if args[0] != "serve" {
		return errors.New("usage: AgentRelay worker setup|serve")
	}
	if *workspace != "" {
		return errors.New("configure bridge and workspace with worker setup; serve uses saved pairing")
	}
	var credentials protocol.Credentials
	if err := state.Load(state.Path("worker"), &credentials); err != nil {
		return err
	}
	if credentials.DeviceID == "" {
		return setupWorker(bufio.NewReader(os.Stdin), "")
	}
	return runWorker(credentials)
}
func setupWorker(input *bufio.Reader, workspace string) error {
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
	s, err := worker.New(worker.Config{Workspace: workspace, Name: name})
	if err != nil {
		return err
	}
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
	fmt.Printf("Connecting to %s (workspace %s). Ctrl+C to stop.\n", credentials.BridgeURL, credentials.Workspace)
	err = s.Run(ctx, credentials, func(err error) { fmt.Fprintln(os.Stderr, err) })
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
func bridgeCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: AgentRelay bridge serve")
	}
	f := flag.NewFlagSet("bridge", flag.ContinueOnError)
	listen := f.String("listen", ":8764", "controller listen address")
	base := f.String("bridge", bridge.DefaultURL, "local controller URL")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] != "serve" {
		return errors.New("usage: AgentRelay bridge serve")
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
	if *base == bridge.DefaultURL {
		*base = "https://localhost:" + strconv.Itoa(port)
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
	fmt.Println("Waiting for Worker pairing requests; each request will show a verification number for approval.")
	go func() {
		if err := pairingApprovalLoop(ctx, *base); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "Pairing approval stopped:", err)
		}
	}()
	err = s.ListenAndServe()
	cancel()
	return err
}

func pairingApprovalLoop(ctx context.Context, base string) error {
	client := bridge.NewClient(base)
	input := bufio.NewReader(os.Stdin)
	seen := map[string]time.Time{}
	for {
		requests, err := client.PairRequests(ctx)
		if err == nil {
			active := make(map[string]struct{}, len(requests))
			for _, request := range requests {
				active[request.ID] = struct{}{}
				if _, ok := seen[request.ID]; ok {
					continue
				}
				fmt.Printf("\n%s requests a connection\nVerification number: %s\nApprove with A or reject with R: ", request.Device.Name, request.Verification)
				line, readErr := readApprovalLine(ctx, input)
				if readErr != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return fmt.Errorf("interactive approval unavailable: %w; use the local pairing approval API for non-interactive operation", readErr)
				}
				approve := strings.EqualFold(strings.TrimSpace(line), "a")
				if _, decideErr := client.DecidePairing(ctx, request.ID, approve); decideErr != nil {
					fmt.Fprintln(os.Stderr, "Pairing request handling failed:", decideErr)
					continue
				}
				seen[request.ID] = time.Now()
			}
			for id := range seen {
				if _, ok := active[id]; !ok {
					delete(seen, id)
				}
			}
			for len(seen) > 1024 {
				var oldestID string
				var oldest time.Time
				for id, at := range seen {
					if oldestID == "" || at.Before(oldest) {
						oldestID, oldest = id, at
					}
				}
				delete(seen, oldestID)
			}
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func readApprovalLine(ctx context.Context, input *bufio.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		line, err := input.ReadString('\n')
		resultCh <- result{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case out := <-resultCh:
		return out.line, out.err
	}
}

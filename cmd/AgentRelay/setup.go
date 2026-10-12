package main

import (
	"AgentRelay/internal/controller"
	"AgentRelay/internal/discovery"
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

func setupCommand(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: AgentRelay setup")
	}
	return guiCommand(nil)
}
func controllerCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: AgentRelay controller serve")
	}
	f := flag.NewFlagSet("controller", flag.ContinueOnError)
	listen := f.String("listen", ":8764", "controller listen address")
	base := f.String("controller", controller.DefaultURL, "local controller URL")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] != "serve" {
		return errors.New("usage: AgentRelay controller serve")
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
	if err != nil {
		return err
	}
	if port < 1 || port > 65535 {
		return errors.New("listen port must be between 1 and 65535")
	}
	if *base == controller.DefaultURL {
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
	fmt.Printf("Certificate fingerprint: %s\n", s.Fingerprint())
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
	client := controller.NewClient(base)
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

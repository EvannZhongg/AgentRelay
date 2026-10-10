package discovery

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryRoundTripAndCancellation(t *testing.T) {
	// Do not compete with a running user's discovery listener.
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{Port: Port})
	if err != nil {
		t.Skipf("discovery port in use: %v", err)
	}
	_ = probe.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	fingerprint := strings.Repeat("a", 64)
	go func() { done <- Serve(ctx, "test-controller", fingerprint, 8764) }()
	deadline := time.Now().Add(6 * time.Second)
	for {
		checkCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		found, err := Discover(checkCtx)
		stop()
		if err == nil {
			for _, item := range found {
				if item.Name == "test-controller" && item.Fingerprint == fingerprint && item.Port == 8764 {
					cancel()
					select {
					case <-done:
						return
					case <-time.After(time.Second):
						t.Fatal("discovery listener did not stop")
					}
				}
			}
		}
		if time.Now().After(deadline) {
			break
		}
	}
	t.Fatal("controller was not discovered")
}

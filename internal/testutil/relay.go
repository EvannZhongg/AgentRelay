package testutil

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"AgentRelay/internal/bridge"
	"AgentRelay/internal/desktop"
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"
	"AgentRelay/internal/transport"
	"AgentRelay/internal/worker"
)

type Relay struct {
	Bridge      *bridge.Server
	HTTP        *httptest.Server
	Client      *bridge.Client
	Worker      *worker.Server
	Peer        *transport.Peer
	Device      protocol.Device
	Credentials protocol.Credentials
	StatePath   string
}

func New(t *testing.T, root string) *Relay {
	return NewWithDesktop(t, root, nil)
}

func NewWithDesktop(t *testing.T, root string, backend desktop.Backend) *Relay {
	t.Helper()
	if root == "" {
		root = t.TempDir()
	}
	t.Setenv("AGENTRELAY_HOME", t.TempDir())
	statePath := state.Path("bridge")
	b, err := bridge.New(bridge.Config{StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewUnstartedServer(b.Handler())
	httpServer.TLS, err = b.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	httpServer.StartTLS()
	t.Cleanup(httpServer.Close)
	t.Cleanup(b.Close)
	w, err := worker.New(worker.Config{Workspace: root, Name: "test-worker", Desktop: backend})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	pairCtx, cancelPair := context.WithCancel(context.Background())
	defer cancelPair()
	requestCh := make(chan protocol.PairingRequest, 1)
	credsCh := make(chan struct {
		creds protocol.Credentials
		err   error
	}, 1)
	go func() {
		creds, err := w.Pair(pairCtx, httpServer.URL, b.Fingerprint(), state.Path("worker"), func(request protocol.PairingRequest) { requestCh <- request })
		credsCh <- struct {
			creds protocol.Credentials
			err   error
		}{creds, err}
	}()
	request := <-requestCh
	if _, err := bridge.NewClient(httpServer.URL).DecidePairing(context.Background(), request.ID, true); err != nil {
		t.Fatal(err)
	}
	result := <-credsCh
	if result.err != nil {
		t.Fatal(result.err)
	}
	creds := result.creds
	peer, err := w.Connect(context.Background(), creds)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(peer.Close)
	client := bridge.NewClient(httpServer.URL)
	devices, err := client.Devices(context.Background())
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices: %+v, %v", devices, err)
	}
	return &Relay{Bridge: b, HTTP: httpServer, Client: client, Worker: w, Peer: peer, Device: devices[0], Credentials: creds, StatePath: statePath}
}
func WaitOffline(t *testing.T, c *bridge.Client, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		devices, err := c.Devices(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range devices {
			if d.ID == id && !d.Online {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("device did not go offline")
}

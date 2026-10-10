package transport

import (
	"context"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func peerPair(t *testing.T, left, right Handler) (*Peer, *Peer) {
	t.Helper()
	accepted := make(chan *Peer, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- NewPeer(conn, right)
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	a, b := NewPeer(conn, left), <-accepted
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	return a, b
}
func TestConcurrentBidirectionalRPC(t *testing.T) {
	echo := func(ctx context.Context, req Request) Response { return Response{200, []byte(req.Path)} }
	a, b := peerPair(t, echo, echo)
	var group sync.WaitGroup
	failures := make(chan error, 80)
	for i := 0; i < 40; i++ {
		for _, peer := range []*Peer{a, b} {
			group.Add(1)
			go func(i int, p *Peer) {
				defer group.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				path := fmt.Sprintf("/request/%d", i)
				resp, err := p.Call(ctx, Request{Method: "GET", Path: path})
				if err != nil || string(resp.Body) != path {
					failures <- fmt.Errorf("response=%+v error=%v", resp, err)
				}
			}(i, peer)
		}
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}
func TestRPCCancellationAndDisconnect(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprint(disconnect), func(t *testing.T) {
			started, canceled := make(chan struct{}), make(chan struct{})
			handler := func(ctx context.Context, req Request) Response {
				close(started)
				<-ctx.Done()
				close(canceled)
				return Response{Status: 408}
			}
			a, _ := peerPair(t, handler, handler)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := a.Call(ctx, Request{Method: "POST", Path: "/wait"}); done <- err }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not start")
			}
			if disconnect {
				a.Close()
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("call unexpectedly succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("call remained blocked")
			}
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("remote work was not canceled")
			}
		})
	}
}

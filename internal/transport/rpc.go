package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const MaxMessage = 64 << 20

// Full-resolution captures can take longer than command responses on Wi-Fi.
// Keep heartbeats compatible with an in-flight bulk frame.
const writeTimeout = 60 * time.Second
const readTimeout = 120 * time.Second

type Request struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   []byte `json:"body,omitempty"`
}
type Response struct {
	Status int    `json:"status"`
	Body   []byte `json:"body,omitempty"`
}
type frame struct {
	Type     string    `json:"type"`
	ID       string    `json:"id"`
	Request  *Request  `json:"request,omitempty"`
	Response *Response `json:"response,omitempty"`
}
type Handler func(context.Context, Request) Response

type Peer struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan Response
	active  map[string]context.CancelFunc
	next    atomic.Uint64
	done    chan struct{}
	once    sync.Once
	handler Handler
	slots   chan struct{}
}

func NewPeer(conn *websocket.Conn, handler Handler) *Peer {
	p := &Peer{conn: conn, handler: handler, pending: map[string]chan Response{}, active: map[string]context.CancelFunc{}, done: make(chan struct{}), slots: make(chan struct{}, 128)}
	conn.SetReadLimit(MaxMessage)
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(readTimeout)) })
	go p.read()
	go p.heartbeat()
	return p
}
func (p *Peer) Done() <-chan struct{} { return p.done }
func (p *Peer) Close() {
	p.once.Do(func() {
		close(p.done)
		_ = p.conn.Close()
		p.mu.Lock()
		for _, cancel := range p.active {
			cancel()
		}
		p.mu.Unlock()
	})
}
func (p *Peer) send(f frame) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	err := p.conn.WriteJSON(f)
	if err != nil {
		p.Close()
	}
	return err
}
func (p *Peer) Call(ctx context.Context, req Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	id := strconv.FormatUint(p.next.Add(1), 10)
	ch := make(chan Response, 1)
	p.mu.Lock()
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, id); p.mu.Unlock() }()
	if err := p.send(frame{Type: "request", ID: id, Request: &req}); err != nil {
		return Response{}, err
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		_ = p.send(frame{Type: "cancel", ID: id})
		return Response{}, ctx.Err()
	case <-p.done:
		return Response{}, errors.New("worker connection closed; operation was not replayed")
	}
}
func (p *Peer) read() {
	defer p.Close()
	for {
		var f frame
		if err := p.conn.ReadJSON(&f); err != nil {
			return
		}
		switch f.Type {
		case "response":
			if f.Response == nil {
				return
			}
			p.mu.Lock()
			ch := p.pending[f.ID]
			p.mu.Unlock()
			if ch != nil {
				select {
				case ch <- *f.Response:
				default:
				}
			}
		case "cancel":
			p.mu.Lock()
			cancel := p.active[f.ID]
			p.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		case "request":
			if f.Request == nil || f.ID == "" {
				return
			}
			select {
			case p.slots <- struct{}{}:
			default:
				resp := Response{Status: http.StatusTooManyRequests, Body: []byte(`{"error":"too many active RPC calls"}`)}
				if p.send(frame{Type: "response", ID: f.ID, Response: &resp}) != nil {
					return
				}
				continue
			}
			ctx, cancel := context.WithCancel(context.Background())
			p.mu.Lock()
			select {
			case <-p.done:
				p.mu.Unlock()
				cancel()
				<-p.slots
				return
			default:
			}
			if _, exists := p.active[f.ID]; exists {
				p.mu.Unlock()
				cancel()
				<-p.slots
				return
			}
			p.active[f.ID] = cancel
			p.mu.Unlock()
			go func(f frame) {
				defer func() { cancel(); <-p.slots; p.mu.Lock(); delete(p.active, f.ID); p.mu.Unlock() }()
				resp := p.handler(ctx, *f.Request)
				_ = p.send(frame{Type: "response", ID: f.ID, Response: &resp})
			}(f)
		default:
			return
		}
	}
}
func (p *Peer) heartbeat() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			if err := p.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				p.Close()
				return
			}
		}
	}
}

type responseBuffer struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *responseBuffer) Header() http.Header { return w.header }
func (w *responseBuffer) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *responseBuffer) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(data)
}

func HTTPHandler(handler http.Handler) Handler {
	return func(ctx context.Context, req Request) Response {
		r, err := http.NewRequestWithContext(ctx, req.Method, req.Path, bytes.NewReader(req.Body))
		if err != nil {
			data, _ := json.Marshal(map[string]string{"error": err.Error()})
			return Response{400, data}
		}
		w := &responseBuffer{header: make(http.Header)}
		handler.ServeHTTP(w, r)
		if w.status == 0 {
			w.status = 200
		}
		return Response{w.status, w.body.Bytes()}
	}
}

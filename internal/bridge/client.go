package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"AgentRelay/internal/protocol"
	"AgentRelay/internal/state"
	"AgentRelay/internal/transport"
)

const DefaultURL = "https://localhost:8764"

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func NewClient(base string) *Client {
	c := &Client{BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 30 * time.Minute}}
	var local registry
	if state.Load(state.Path("bridge"), &local) == nil && local.Identity.Certificate != "" {
		c.HTTP.Transport = &http.Transport{TLSClientConfig: transport.PinnedTLS(local.Identity.Fingerprint())}
	}
	return c
}
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, transport.MaxMessage))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e protocol.ErrorResponse
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
func devicePath(id string) string { return "/v1/devices/" + url.PathEscape(id) }
func (c *Client) Devices(ctx context.Context) (out []protocol.Device, err error) {
	err = c.Do(ctx, "GET", "/v1/devices", nil, &out)
	return
}
func (c *Client) PairRequests(ctx context.Context) (out []protocol.PairingRequest, err error) {
	err = c.Do(ctx, "GET", "/v1/pair-requests", nil, &out)
	return
}
func (c *Client) DecidePairing(ctx context.Context, id string, approve bool) (out protocol.PairingStatus, err error) {
	action := "reject"
	if approve {
		action = "approve"
	}
	err = c.Do(ctx, "POST", "/v1/pair-requests/"+url.PathEscape(id)+"/"+action, nil, &out)
	return
}
func (c *Client) UpdateDevice(ctx context.Context, id string, req DeviceUpdate) (out protocol.Device, err error) {
	err = c.Do(ctx, "PATCH", devicePath(id), req, &out)
	return
}
func (c *Client) Exec(ctx context.Context, id string, req protocol.ExecRequest) (out protocol.ExecResponse, err error) {
	err = c.Do(ctx, "POST", devicePath(id)+"/exec", req, &out)
	return
}
func (c *Client) Sync(ctx context.Context, id string, req protocol.SyncRequest) (out protocol.SyncResponse, err error) {
	if req.SourcePath != "" {
		req.SourcePath, err = filepath.Abs(req.SourcePath)
		if err != nil {
			return
		}
	}
	err = c.Do(ctx, "POST", devicePath(id)+"/files/sync", req, &out)
	return
}
func (c *Client) StartTask(ctx context.Context, id string, req protocol.TaskRequest) (out protocol.Task, err error) {
	err = c.Do(ctx, "POST", devicePath(id)+"/tasks", req, &out)
	return
}
func (c *Client) ListTasks(ctx context.Context, id string) (out []protocol.Task, err error) {
	err = c.Do(ctx, "GET", devicePath(id)+"/tasks", nil, &out)
	return
}
func (c *Client) GetTask(ctx context.Context, id, task string) (out protocol.Task, err error) {
	err = c.Do(ctx, "GET", devicePath(id)+"/tasks/"+url.PathEscape(task), nil, &out)
	return
}
func (c *Client) RetrieveTask(ctx context.Context, id, task string) (out protocol.Task, err error) {
	err = c.Do(ctx, "POST", devicePath(id)+"/tasks/"+url.PathEscape(task)+"/retrieve", nil, &out)
	return
}
func (c *Client) CancelTask(ctx context.Context, id, task string) error {
	return c.Do(ctx, "DELETE", devicePath(id)+"/tasks/"+url.PathEscape(task), nil, nil)
}
func (c *Client) ReadFile(ctx context.Context, id, path string) (out protocol.ReadResponse, err error) {
	err = c.Do(ctx, "GET", devicePath(id)+"/files/read?path="+url.QueryEscape(path), nil, &out)
	return
}
func (c *Client) CreateSession(ctx context.Context, id string, req protocol.SessionRequest) (out protocol.Session, err error) {
	err = c.Do(ctx, "POST", devicePath(id)+"/sessions", req, &out)
	return
}
func (c *Client) ListSessions(ctx context.Context, id string) (out []protocol.Session, err error) {
	err = c.Do(ctx, "GET", devicePath(id)+"/sessions", nil, &out)
	return
}
func (c *Client) GetSession(ctx context.Context, id, session string) (out protocol.Session, err error) {
	err = c.Do(ctx, "GET", devicePath(id)+"/sessions/"+url.PathEscape(session), nil, &out)
	return
}
func (c *Client) WriteSession(ctx context.Context, id, session, input string) (out protocol.Session, err error) {
	err = c.Do(ctx, "POST", devicePath(id)+"/sessions/"+url.PathEscape(session)+"/input", protocol.SessionInput{Input: input}, &out)
	return
}
func (c *Client) CloseSession(ctx context.Context, id, session string) (out protocol.Session, err error) {
	err = c.Do(ctx, "DELETE", devicePath(id)+"/sessions/"+url.PathEscape(session), nil, &out)
	return
}

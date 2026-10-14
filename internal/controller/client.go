package controller

import (
	"DevFreight/internal/protocol"
	"DevFreight/internal/state"
	"DevFreight/internal/transport"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultURL = "https://localhost:8764"

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

type responseError struct {
	status int
	err    error
}

func (e *responseError) Error() string { return e.err.Error() }
func (e *responseError) Unwrap() error { return e.err }

func NewClient(base string) *Client {
	c := &Client{BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 30 * time.Minute}}
	var local registry
	if state.Load(state.Path("controller"), &local) == nil && local.Identity.Certificate != "" {
		c.HTTP.Transport = &http.Transport{TLSClientConfig: transport.PinnedTLS(local.Identity.Fingerprint())}
	}
	return c
}
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var data []byte
	if in != nil {
		var err error
		data, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	return c.do(ctx, method, path, "application/json", data, out)
}

func (c *Client) do(ctx context.Context, method, path, contentType string, body []byte, out any) error {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
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
			return &responseError{status: resp.StatusCode, err: errors.New(e.Error)}
		}
		return &responseError{status: resp.StatusCode, err: fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(data)))}
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
func (c *Client) CreateDevice(ctx context.Context, req protocol.DeviceCreate) (out protocol.Device, err error) {
	err = c.Do(ctx, "POST", "/v1/devices", req, &out)
	return
}
func (c *Client) ReplaceConnection(ctx context.Context, id string, req protocol.ConnectionReplacement) (out protocol.Device, err error) {
	err = c.Do(ctx, "POST", devicePath(id)+"/connection/replace", req, &out)
	return
}
func (c *Client) TestConnection(ctx context.Context, id string) (out protocol.Connection, err error) {
	err = c.Do(ctx, "POST", devicePath(id)+"/connection/test", nil, &out)
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

func (c *Client) UpdateConnection(ctx context.Context, id string, req protocol.ConnectionSpec) (out protocol.Connection, err error) {
	err = c.Do(ctx, "PATCH", devicePath(id)+"/connection", req, &out)
	return
}

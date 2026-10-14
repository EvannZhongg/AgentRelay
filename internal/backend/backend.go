// Package backend defines independently optional device capabilities. Neither
// the capability implementations nor their transports contain an agent loop.
package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"DevFreight/internal/protocol"
	"DevFreight/internal/transport"
)

type Exec interface {
	Execute(context.Context, protocol.ExecRequest) (protocol.ExecResponse, error)
}
type Files interface {
	Read(context.Context, string) (protocol.ReadResponse, error)
}

// Stateful resource protocols retain their method, resource ID and binary chunks.
type Sync interface {
	Synchronize(context.Context, transport.Request) (transport.Response, error)
}
type Tasks interface {
	Task(context.Context, transport.Request) (transport.Response, error)
}
type Sessions interface {
	Session(context.Context, transport.Request) (transport.Response, error)
}
type Desktop interface {
	Info(context.Context) (protocol.DesktopInfo, error)
	Capture(context.Context, protocol.CaptureRequest) (protocol.CaptureResponse, error)
	Input(context.Context, protocol.DesktopInput) (protocol.DesktopInputResult, error)
}

type Set struct {
	Exec     Exec
	Files    Files
	Sync     Sync
	Tasks    Tasks
	Sessions Sessions
	Desktop  Desktop
}
type Call func(context.Context, transport.Request) (transport.Response, error)
type rpc struct{ call Call }
type execRPC struct{ rpc }
type filesRPC struct{ rpc }
type syncRPC struct{ rpc }
type tasksRPC struct{ rpc }
type sessionsRPC struct{ rpc }
type desktopRPC struct{ rpc }

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func (r rpc) json(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	resp, err := r.call(ctx, transport.Request{Method: method, Path: path, Body: body})
	if err != nil {
		return err
	}
	if resp.Status >= 300 {
		var failure protocol.ErrorResponse
		_ = json.Unmarshal(resp.Body, &failure)
		if failure.Error == "" {
			failure.Error = fmt.Sprintf("remote operation returned HTTP %d", resp.Status)
		}
		return &Error{resp.Status, failure.Error}
	}
	return json.Unmarshal(resp.Body, out)
}
func (r execRPC) Execute(ctx context.Context, in protocol.ExecRequest) (out protocol.ExecResponse, err error) {
	err = r.json(ctx, "POST", "/v1/exec", in, &out)
	return
}
func (r filesRPC) Read(ctx context.Context, path string) (out protocol.ReadResponse, err error) {
	err = r.json(ctx, "GET", "/v1/files/read?path="+url.QueryEscape(path), nil, &out)
	return
}
func (r syncRPC) Synchronize(ctx context.Context, req transport.Request) (transport.Response, error) {
	return r.call(ctx, req)
}
func (r tasksRPC) Task(ctx context.Context, req transport.Request) (transport.Response, error) {
	return r.call(ctx, req)
}
func (r sessionsRPC) Session(ctx context.Context, req transport.Request) (transport.Response, error) {
	return r.call(ctx, req)
}
func (r desktopRPC) Info(ctx context.Context) (out protocol.DesktopInfo, err error) {
	err = r.json(ctx, "GET", "/v1/desktop", nil, &out)
	return
}
func (r desktopRPC) Capture(ctx context.Context, in protocol.CaptureRequest) (out protocol.CaptureResponse, err error) {
	err = r.json(ctx, "POST", "/v1/desktop/capture", in, &out)
	return
}
func (r desktopRPC) Input(ctx context.Context, in protocol.DesktopInput) (out protocol.DesktopInputResult, err error) {
	err = r.json(ctx, "POST", "/v1/desktop/input", in, &out)
	return
}

func FromRPC(call Call, c protocol.Capabilities) Set {
	r := rpc{call}
	var s Set
	if c.Exec {
		s.Exec = execRPC{r}
	}
	if c.Files {
		s.Files = filesRPC{r}
	}
	if c.Sync {
		s.Sync = syncRPC{r}
	}
	if c.Tasks {
		s.Tasks = tasksRPC{r}
	}
	if c.Sessions {
		s.Sessions = sessionsRPC{r}
	}
	if c.Desktop != nil && c.Desktop.Supported {
		s.Desktop = desktopRPC{r}
	}
	return s
}

// Capability maps operations to the capability required for execution.
func Capability(path string) string {
	path = strings.Split(path, "?")[0]
	switch {
	case path == "/v1/exec":
		return "exec"
	case path == "/v1/files/read":
		return "files"
	case strings.HasPrefix(path, "/v1/files/sync/"):
		return "sync"
	case path == "/v1/tasks" || strings.HasPrefix(path, "/v1/tasks/"):
		return "tasks"
	case path == "/v1/sessions" || strings.HasPrefix(path, "/v1/sessions/"):
		return "sessions"
	case path == "/v1/desktop" || strings.HasPrefix(path, "/v1/desktop/"):
		return "desktop"
	}
	return ""
}
func Supported(c protocol.Capabilities, name string) bool {
	switch name {
	case "exec":
		return c.Exec
	case "files":
		return c.Files
	case "sync":
		return c.Sync
	case "tasks":
		return c.Tasks
	case "sessions":
		return c.Sessions
	case "desktop":
		return c.Desktop != nil && c.Desktop.Supported
	}
	return false
}

func (s Set) Dispatch(ctx context.Context, req transport.Request) (transport.Response, error) {
	u, err := url.Parse(req.Path)
	if err != nil {
		return transport.Response{}, err
	}
	var out any
	switch Capability(req.Path) {
	case "exec":
		if s.Exec == nil {
			break
		}
		var in protocol.ExecRequest
		if err = json.Unmarshal(req.Body, &in); err == nil {
			out, err = s.Exec.Execute(ctx, in)
		}
	case "files":
		if s.Files == nil {
			break
		}
		out, err = s.Files.Read(ctx, u.Query().Get("path"))
	case "sync":
		if s.Sync != nil {
			return s.Sync.Synchronize(ctx, req)
		}
	case "tasks":
		if s.Tasks != nil {
			return s.Tasks.Task(ctx, req)
		}
	case "sessions":
		if s.Sessions != nil {
			return s.Sessions.Session(ctx, req)
		}
	case "desktop":
		if s.Desktop == nil {
			break
		}
		switch u.Path {
		case "/v1/desktop":
			out, err = s.Desktop.Info(ctx)
		case "/v1/desktop/capture":
			var in protocol.CaptureRequest
			if err = json.Unmarshal(req.Body, &in); err == nil {
				out, err = s.Desktop.Capture(ctx, in)
			}
		case "/v1/desktop/input":
			var in protocol.DesktopInput
			if err = json.Unmarshal(req.Body, &in); err == nil {
				out, err = s.Desktop.Input(ctx, in)
			}
		}
	}
	if err != nil {
		return transport.Response{}, err
	}
	if out == nil {
		return transport.Response{}, &Error{501, "device does not provide the requested capability"}
	}
	body, err := json.Marshal(out)
	return transport.Response{Status: 200, Body: body}, err
}

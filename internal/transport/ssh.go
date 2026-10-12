package transport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"AgentRelay/internal/protocol"
)

type SSH struct {
	Config    protocol.SSHConfig
	Workspace string
	Program   string
}

func ValidateSSH(c protocol.SSHConfig, workspace string) error {
	if c.Host == "" || strings.HasPrefix(c.Host, "-") || strings.ContainsAny(c.Host, " \t\r\n/@\\") {
		return errors.New("SSH host must be a hostname or IP address")
	}
	if c.User == "" || strings.HasPrefix(c.User, "-") || strings.ContainsAny(c.User, " \t\r\n/@\\") {
		return errors.New("SSH user is required and cannot contain whitespace or separators")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("SSH port must be between 1 and 65535")
	}
	if !strings.HasPrefix(workspace, "/") || strings.ContainsRune(workspace, '\x00') {
		return errors.New("SSH workspace must be an absolute POSIX path")
	}
	for _, p := range []string{c.IdentityFile, c.KnownHostsFile} {
		if strings.ContainsAny(p, "\r\n\x00") {
			return errors.New("invalid SSH authentication path")
		}
	}
	return nil
}

func (s SSH) args() []string {
	a := []string{"-F", "none", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-p", strconv.Itoa(s.Config.Port), "-l", s.Config.User}
	if s.Config.IdentityFile != "" {
		a = append(a, "-i", s.Config.IdentityFile)
	}
	if s.Config.KnownHostsFile != "" {
		a = append(a, "-o", "UserKnownHostsFile=\""+strings.ReplaceAll(strings.ReplaceAll(s.Config.KnownHostsFile, "\\", "/"), "\"", "\\\"")+"\"")
	}
	command := "python3 -u -c 'import base64;exec(base64.b64decode(\"" + base64.StdEncoding.EncodeToString([]byte(s.Program)) + "\"))'"
	return append(a, s.Config.Host, command)
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := max(0, b.limit-b.Len())
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

// Call sends one RPC over an SSH channel. The Python standard-library helper
// exists only in memory for this invocation; no Worker or listener is installed.
func (s SSH) Call(ctx context.Context, req Request) (Response, error) {
	if err := ValidateSSH(s.Config, s.Workspace); err != nil {
		return Response{}, err
	}
	if s.Program == "" {
		return Response{}, errors.New("SSH RPC executor program is required")
	}
	if len(req.Body) > 32<<20 {
		return Response{}, errors.New("RPC payload exceeds 32 MiB")
	}
	input, err := json.Marshal(struct {
		Workspace string  `json:"workspace"`
		Request   Request `json:"request"`
	}{s.Workspace, req})
	if err != nil {
		return Response{}, err
	}
	cmd := exec.CommandContext(ctx, "ssh", s.args()...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Response{}, err
	}
	defer stdin.Close()
	stdout := &boundedBuffer{limit: 32 << 20}
	stderr := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Start(); err != nil {
		return Response{}, fmt.Errorf("start OpenSSH: %w", err)
	}
	_, writeErr := stdin.Write(append(input, '\n'))
	if writeErr != nil {
		_ = cmd.Process.Kill()
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	if writeErr != nil {
		return Response{}, writeErr
	}
	if err != nil {
		return Response{}, fmt.Errorf("SSH RPC failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.overflow {
		return Response{}, errors.New("SSH response exceeds 32 MiB")
	}
	var out Response
	if err = json.Unmarshal(stdout.Bytes(), &out); err != nil || out.Status < 100 || out.Status > 599 {
		return Response{}, errors.New("invalid SSH RPC response; remote requires Python 3 and a POSIX shell")
	}
	return out, nil
}

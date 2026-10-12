package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"AgentRelay/internal/protocol"
)

type SSH struct{ Config protocol.SSHConfig }

func ValidateSSH(c protocol.SSHConfig) error {
	if c.Host == "" || strings.HasPrefix(c.Host, "-") || strings.ContainsAny(c.Host, " \t\r\n/@\\") {
		return errors.New("SSH host must be a hostname or IP address")
	}
	if c.User == "" || strings.HasPrefix(c.User, "-") || strings.ContainsAny(c.User, " \t\r\n/@\\") {
		return errors.New("SSH user is required and cannot contain whitespace or separators")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("SSH port must be between 1 and 65535")
	}
	for _, p := range []string{c.IdentityFile, c.KnownHostsFile} {
		if strings.ContainsAny(p, "\r\n\x00") {
			return errors.New("invalid SSH authentication path")
		}
	}
	return nil
}

func (s SSH) args(command string) []string {
	a := []string{"-F", "none", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-p", strconv.Itoa(s.Config.Port), "-l", s.Config.User}
	if s.Config.IdentityFile != "" {
		a = append(a, "-i", s.Config.IdentityFile)
	}
	if s.Config.KnownHostsFile != "" {
		a = append(a, "-o", "UserKnownHostsFile=\""+strings.ReplaceAll(strings.ReplaceAll(s.Config.KnownHostsFile, "\\", "/"), "\"", "\\\"")+"\"")
	}
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

// Run carries opaque native executor input/output over one OpenSSH channel.
// Keep stdin open until completion so the executor observes channel loss.
func (s SSH) Run(ctx context.Context, command string, input []byte) ([]byte, error) {
	if err := ValidateSSH(s.Config); err != nil {
		return nil, err
	}
	if command == "" {
		return nil, errors.New("SSH remote command is required")
	}
	if len(input) > 48<<20 {
		return nil, errors.New("SSH input exceeds 48 MiB")
	}
	cmd := exec.CommandContext(ctx, "ssh", s.args(command)...)
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer stdin.Close()
	stdout := &boundedBuffer{limit: 32 << 20}
	stderr := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("start OpenSSH: %w", err)
	}
	_, writeErr := stdin.Write(input)
	if writeErr != nil {
		_ = cmd.Process.Kill()
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if writeErr != nil {
		return nil, fmt.Errorf("SSH input failed: %w: %s", writeErr, strings.TrimSpace(stderr.String()))
	}
	if err != nil {
		return nil, fmt.Errorf("SSH executor failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.overflow {
		return nil, errors.New("SSH response exceeds 32 MiB")
	}
	return stdout.Bytes(), nil
}

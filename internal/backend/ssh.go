package backend

import (
	"AgentRelay/internal/protocol"
	"AgentRelay/internal/transport"
	"AgentRelay/internal/workspace"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

//go:embed ssh_posix.sh
var sshPOSIX string

//go:embed ssh_windows.ps1
var sshWindows string

// SSHPlatform uses target configuration/path syntax, never the controller OS.
func SSHPlatform(c protocol.SSHConfig, root string) (string, error) {
	if c.Platform != "" && c.Platform != "auto" && c.Platform != "windows" && c.Platform != "posix" {
		return "", errors.New("SSH platform must be auto, windows or posix")
	}
	p := c.Platform
	if p == "" || p == "auto" {
		p = "posix"
		if (len(root) >= 2 && root[1] == ':') || strings.HasPrefix(root, `\\`) || strings.HasPrefix(root, "//") {
			p = "windows"
		}
	}
	if strings.ContainsAny(root, "\x00\r\n") {
		return "", errors.New("invalid SSH workspace root")
	}
	if p == "windows" {
		drive := len(root) >= 3 && ((root[0] >= 'A' && root[0] <= 'Z') || (root[0] >= 'a' && root[0] <= 'z')) && root[1] == ':' && (root[2] == '/' || root[2] == '\\')
		normalized := strings.ReplaceAll(root, `\`, "/")
		unc := strings.HasPrefix(normalized, "//") && len(strings.Split(strings.Trim(normalized, "/"), "/")) >= 2
		if !drive && !unc {
			return "", errors.New("Windows SSH workspace needs an absolute drive or UNC path")
		}
	} else if !strings.HasPrefix(root, "/") || strings.Contains(root, `\`) {
		return "", errors.New("POSIX SSH workspace needs an absolute POSIX path")
	}
	return p, nil
}
func ValidateSSH(c protocol.SSHConfig, root string) error {
	if err := transport.ValidateSSH(c); err != nil {
		return err
	}
	_, err := SSHPlatform(c, root)
	return err
}

type sshInvocation struct {
	command   string
	input     []byte
	operation string
	filePath  string
}

func quoteSH(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func encodedPowerShell(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, v := range u {
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	return base64.StdEncoding.EncodeToString(b)
}
func prepareSSH(c protocol.SSHConfig, root string, r transport.Request) (sshInvocation, error) {
	p, err := SSHPlatform(c, root)
	if err != nil {
		return sshInvocation{}, err
	}
	if len(r.Body) > 32<<20 {
		return sshInvocation{}, errors.New("RPC payload exceeds 32 MiB")
	}
	u, err := url.Parse(r.Path)
	if err != nil {
		return sshInvocation{}, err
	}
	operation, relative := "", ""
	var exec protocol.ExecRequest
	switch {
	case r.Method == "GET" && u.Path == "/probe":
		operation = "probe"
	case r.Method == "GET" && u.Path == "/v1/files/read":
		operation = "read"
		relative = u.Query().Get("path")
	case r.Method == "POST" && u.Path == "/v1/exec":
		operation = "exec"
		if err = json.Unmarshal(r.Body, &exec); err != nil {
			return sshInvocation{}, err
		}
		if strings.TrimSpace(exec.Command) == "" || strings.ContainsRune(exec.Command, '\x00') {
			return sshInvocation{}, errors.New("command is required and cannot contain NUL")
		}
		if exec.TimeoutMS < 0 || exec.TimeoutMS > 1800000 {
			return sshInvocation{}, errors.New("timeout_ms must be between 0 and 1800000")
		}
		if exec.TimeoutMS == 0 {
			exec.TimeoutMS = 1800000
		}
		relative = exec.WorkingDir
	default:
		return sshInvocation{}, &Error{Status: 501, Message: "capability unavailable on SSH connection"}
	}
	if !workspace.Relative(relative) {
		return sshInvocation{}, errors.New("SSH operation path must be workspace-relative")
	}
	for k, v := range exec.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, '\x00') {
			return sshInvocation{}, errors.New("invalid environment variable")
		}
	}
	inv := sshInvocation{operation: operation, filePath: relative}
	if p == "windows" {
		// Small ASCII bootstrap supports cmd/PowerShell default shells without
		// command-line size or quoting dependencies. The script travels via stdin.
		bootstrap := `[Console]::InputEncoding=New-Object System.Text.UTF8Encoding($false);[Console]::OutputEncoding=New-Object System.Text.UTF8Encoding($false);$s=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadLine()));& ([ScriptBlock]::Create($s))`
		inv.command = "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + encodedPowerShell(bootstrap)
		body, _ := json.Marshal(struct {
			Workspace string               `json:"workspace"`
			Operation string               `json:"operation"`
			Path      string               `json:"path"`
			Exec      protocol.ExecRequest `json:"exec"`
		}{root, operation, relative, exec})
		inv.input = []byte(base64.StdEncoding.EncodeToString([]byte(sshWindows)) + "\n" + string(body) + "\n")
	} else {
		inv.command = "sh -s"
		vars := "relay_root=" + quoteSH(root) + "\nrelay_operation=" + quoteSH(operation) + "\nrelay_path=" + quoteSH(strings.ReplaceAll(relative, `\`, "/")) + "\nrelay_command=" + quoteSH(exec.Command) + "\nrelay_timeout=" + quoteSH(fmt.Sprintf("%.3f", float64(exec.TimeoutMS)/1000)) + "\n"
		keys := make([]string, 0, len(exec.Env))
		for k := range exec.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		env := "env"
		for _, k := range keys {
			env += " " + quoteSH(k+"="+exec.Env[k])
		}
		vars += "relay_env_run() { " + env + " sh \"$relay_temp/command\"; }\n"
		// The complete function is parsed before the EOF watcher reads stdin.
		inv.input = []byte("relay_main() {\n" + vars + sshPOSIX + "\n}\nrelay_main; exit $?\n")
	}
	return inv, nil
}
func decodeSSH(inv sshInvocation, data []byte, duration time.Duration) (transport.Response, error) {
	// Only remove final record terminators: empty base64 values end in a tab.
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	if len(lines) < 2 || strings.TrimSuffix(lines[0], "\r") != "AgentRelaySSH1" {
		return transport.Response{}, errors.New("invalid native SSH executor response")
	}
	fields := map[string]string{}
	for _, line := range lines[1:] {
		k, v, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "\t")
		if !ok {
			return transport.Response{}, errors.New("malformed SSH response field")
		}
		if _, ok := fields[k]; ok {
			return transport.Response{}, errors.New("duplicate SSH response field")
		}
		fields[k] = v
	}
	status, err := strconv.Atoi(fields["status"])
	if err != nil || status < 100 || status > 599 {
		return transport.Response{}, errors.New("invalid SSH response status")
	}
	decode := func(k string) ([]byte, error) {
		s, ok := fields[k]
		if !ok {
			return nil, fmt.Errorf("missing SSH field %s", k)
		}
		return base64.StdEncoding.DecodeString(s)
	}
	var out any
	if status >= 300 {
		b, e := decode("error")
		if e != nil {
			return transport.Response{}, e
		}
		out = protocol.ErrorResponse{Error: string(b)}
	} else {
		switch inv.operation {
		case "probe":
			os, e := decode("os")
			if e != nil {
				return transport.Response{}, e
			}
			arch, e := decode("architecture")
			if e != nil {
				return transport.Response{}, e
			}
			shell := "sh"
			if string(os) == "windows" {
				shell = "powershell"
			}
			out = protocol.Device{OS: string(os), Architecture: string(arch), Capabilities: protocol.Capabilities{Exec: true, Files: true, Shells: []string{shell}, MaxPayload: 32 << 20}}
		case "read":
			b, e := decode("content")
			if e != nil {
				return transport.Response{}, e
			}
			if len(b) > 16<<20 {
				return transport.Response{}, errors.New("SSH file exceeds 16 MiB")
			}
			out = protocol.ReadResponse{Path: inv.filePath, ContentBase64: base64.StdEncoding.EncodeToString(b), Bytes: int64(len(b))}
		case "exec":
			stdout, e := decode("stdout")
			if e != nil {
				return transport.Response{}, e
			}
			stderr, e := decode("stderr")
			if e != nil {
				return transport.Response{}, e
			}
			code, e := strconv.Atoi(fields["exit_code"])
			if e != nil {
				return transport.Response{}, errors.New("invalid SSH exit code")
			}
			if len(stdout) > 1<<20 || len(stderr) > 1<<20 {
				return transport.Response{}, errors.New("SSH command output exceeds limit")
			}
			out = protocol.ExecResponse{Stdout: strings.ToValidUTF8(string(stdout), "�"), Stderr: strings.ToValidUTF8(string(stderr), "�"), ExitCode: code, TimedOut: fields["timed_out"] == "1", OutputTruncated: fields["truncated"] == "1", DurationMS: duration.Milliseconds()}
		}
	}
	body, err := json.Marshal(out)
	return transport.Response{Status: status, Body: body}, err
}
func SSHCaller(config protocol.SSHConfig, root string) Call {
	return func(ctx context.Context, r transport.Request) (transport.Response, error) {
		if err := ValidateSSH(config, root); err != nil {
			return transport.Response{}, &Error{400, err.Error()}
		}
		inv, err := prepareSSH(config, root, r)
		if err != nil {
			var remote *Error
			if errors.As(err, &remote) {
				return transport.Response{}, err
			}
			return transport.Response{}, &Error{400, err.Error()}
		}
		started := time.Now()
		data, err := (transport.SSH{Config: config}).Run(ctx, inv.command, inv.input)
		if err != nil {
			return transport.Response{}, err
		}
		return decodeSSH(inv, data, time.Since(started))
	}
}

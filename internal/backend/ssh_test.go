package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"DevFreight/internal/protocol"
	"DevFreight/internal/transport"
)

func TestSSHPlatformValidationIndependentOfController(t *testing.T) {
	for _, tc := range []struct{ platform, root, want string }{
		{"auto", `C:\work space`, "windows"}, {"auto", `\\server\share\repo`, "windows"}, {"auto", "//server/share/repo", "windows"}, {"auto", "/srv/repo", "posix"}, {"windows", `D:\repo`, "windows"}, {"posix", "/Users/dev/repo", "posix"},
	} {
		got, err := SSHPlatform(protocol.SSHConfig{Platform: tc.platform}, tc.root)
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %s %v", tc, got, err)
		}
	}
	for _, tc := range []struct{ platform, root string }{{"windows", "/repo"}, {"posix", `C:\repo`}, {"invalid", "/repo"}, {"windows", "//server"}, {"auto", "repo"}, {"auto", "/repo\x00"}} {
		if _, err := SSHPlatform(protocol.SSHConfig{Platform: tc.platform}, tc.root); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}

func TestNativeSSHExecutors(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native platform executor")
	}
	root := filepath.Join(t.TempDir(), "space ' Unicode 中")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	platform := "posix"
	if runtime.GOOS == "windows" {
		platform = "windows"
	}
	run := func(r transport.Request) transport.Response {
		t.Helper()
		inv, err := prepareSSH(protocol.SSHConfig{Platform: platform}, root, r)
		if err != nil {
			t.Fatal(err)
		}
		data := runNative(t, inv, "")
		resp, err := decodeSSH(inv, data, time.Millisecond)
		if err != nil {
			t.Fatalf("decode: %v\n%s", err, data)
		}
		return resp
	}
	execute := func(command string, env map[string]string, timeout int64) protocol.ExecResponse {
		t.Helper()
		body, _ := json.Marshal(protocol.ExecRequest{Command: command, Env: env, TimeoutMS: timeout})
		r := run(transport.Request{Method: "POST", Path: "/v1/exec", Body: body})
		if r.Status != 200 {
			t.Fatalf("exec: %s", r.Body)
		}
		var out protocol.ExecResponse
		if err := json.Unmarshal(r.Body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	probe := run(transport.Request{Method: "GET", Path: "/probe"})
	var device protocol.Device
	_ = json.Unmarshal(probe.Body, &device)
	if probe.Status != 200 || device.OS != runtime.GOOS || !device.Capabilities.Files {
		t.Fatalf("probe: %s", probe.Body)
	}
	command := "printf '%s' \"$RELAY_VALUE\"; printf error >&2; exit 7"
	if platform == "windows" {
		command = "[Console]::Write($env:RELAY_VALUE); [Console]::Error.Write('error'); exit 7"
	}
	result := execute(command, map[string]string{"RELAY_VALUE": "quoted ' 中文 $() ` value"}, 5000)
	if result.ExitCode != 7 || result.Stdout != "quoted ' 中文 $() ` value" || result.Stderr != "error" {
		t.Fatalf("native streams: %+v", result)
	}
	if platform == "windows" {
		native := execute("cmd.exe /c exit 9", nil, 5000)
		if native.ExitCode != 9 {
			t.Fatalf("native program exit code: %+v", native)
		}
	}
	command = "sleep 30"
	if platform == "windows" {
		command = "Start-Sleep -Seconds 30"
	}
	result = execute(command, nil, 100)
	if !result.TimedOut || result.ExitCode == 0 {
		t.Fatalf("timeout: %+v", result)
	}
	file := "quote ' 中文 [literal].bin"
	payload := []byte{0, 1, 2, 255, 10, 13}
	if err := os.WriteFile(filepath.Join(root, file), payload, 0600); err != nil {
		t.Fatal(err)
	}
	read := run(transport.Request{Method: "GET", Path: "/v1/files/read?path=" + url.QueryEscape(file)})
	var data protocol.ReadResponse
	_ = json.Unmarshal(read.Body, &data)
	decoded, _ := base64.StdEncoding.DecodeString(data.ContentBase64)
	if read.Status != 200 || !bytes.Equal(decoded, payload) {
		t.Fatalf("binary read: %s", read.Body)
	}
	command = `head -c 1200000 /dev/zero | tr "\000" x`
	if platform == "windows" {
		command = "[Console]::Write(('x' * 1200000))"
	}
	result = execute(command, nil, 5000)
	if !result.OutputTruncated || len(result.Stdout) != 1<<20 {
		t.Fatalf("bounded output: len=%d truncated=%v", len(result.Stdout), result.OutputTruncated)
	}
	// Commands travel in stdin, never through command-line quoting or limits.
	command = strings.Repeat("# long request\n", 5000)
	if platform == "windows" {
		command += "[Console]::Write('large-command')"
	} else {
		command += "printf large-command"
	}
	result = execute(command, nil, 5000)
	if result.Stdout != "large-command" || result.ExitCode != 0 {
		t.Fatalf("large command: %+v", result)
	}
}

func runNative(t *testing.T, inv sshInvocation, disconnectMarker string, defaultShell ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if len(defaultShell) > 0 {
		cmd = exec.CommandContext(ctx, defaultShell[0], append(defaultShell[1:], inv.command)...)
	} else if runtime.GOOS == "windows" {
		parts := strings.Fields(inv.command)
		cmd = exec.CommandContext(ctx, parts[0], parts[1:]...)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-s")
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write(inv.input); err != nil {
		t.Fatal(err)
	}
	if disconnectMarker != "" {
		deadline := time.Now().Add(8 * time.Second)
		for {
			if _, err := os.Stat(disconnectMarker); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("command did not create disconnect marker")
			}
			time.Sleep(20 * time.Millisecond)
		}
		_ = stdin.Close()
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("native executor: %v\n%s\n%s", err, out.String(), errOut.String())
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	return out.Bytes()
}

func TestWindowsSSHDefaultShellBootstraps(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows default shells")
	}
	body, _ := json.Marshal(protocol.ExecRequest{Command: "[Console]::Write('default-shell-ok')", TimeoutMS: 5000})
	inv, err := prepareSSH(protocol.SSHConfig{Platform: "windows"}, t.TempDir(), transport.Request{Method: "POST", Path: "/v1/exec", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range [][]string{{"cmd.exe", "/d", "/s", "/c"}, {"powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}} {
		t.Run(shell[0], func(t *testing.T) {
			resp, err := decodeSSH(inv, runNative(t, inv, "", shell...), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			var result protocol.ExecResponse
			_ = json.Unmarshal(resp.Body, &result)
			if resp.Status != 200 || result.ExitCode != 0 || result.Stdout != "default-shell-ok" {
				t.Fatalf("default shell: %s", resp.Body)
			}
		})
	}
}

func TestNativeSSHDisconnectTerminatesChild(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native executor")
	}
	root := t.TempDir()
	platform := "posix"
	command := "sleep 30 & echo $! > child.pid; wait"
	if runtime.GOOS == "windows" {
		platform = "windows"
		command = `$p=Start-Process (Join-Path $PSHOME 'powershell.exe') -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 30' -PassThru -WindowStyle Hidden; [IO.File]::WriteAllText('child.pid',[string]$p.Id); Start-Sleep -Seconds 30`
	}
	body, _ := json.Marshal(protocol.ExecRequest{Command: command})
	inv, err := prepareSSH(protocol.SSHConfig{Platform: platform}, root, transport.Request{Method: "POST", Path: "/v1/exec", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	data := runNative(t, inv, filepath.Join(root, "child.pid"))
	resp, err := decodeSSH(inv, data, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var result protocol.ExecResponse
	_ = json.Unmarshal(resp.Body, &result)
	if resp.Status != 200 || result.ExitCode == 0 || result.TimedOut {
		t.Fatalf("disconnect: %s", resp.Body)
	}
	pidBytes, err := os.ReadFile(filepath.Join(root, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	var check *exec.Cmd
	if platform == "windows" {
		check = exec.Command("powershell.exe", "-NoProfile", "-Command", fmt.Sprintf("if (Get-Process -Id %d -ErrorAction SilentlyContinue) { exit 1 }", pid))
	} else {
		check = exec.Command("sh", "-c", fmt.Sprintf("ps -p %d -o stat= | grep -qv Z", pid))
		// POSIX check succeeds while the process is alive; invert below.
	}
	err = check.Run()
	if (platform == "windows" && err != nil) || (platform != "windows" && err == nil) {
		t.Fatal("child process survived channel EOF")
	}
}

func TestNativeSSHRejectsSymlinkEscapes(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native executor")
	}
	root, outside := t.TempDir(), t.TempDir()
	platform := "posix"
	if runtime.GOOS == "windows" {
		platform = "windows"
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if platform == "windows" {
		// Junctions exercise the Windows reparse-point policy without requiring
		// the separate privilege used to create symbolic links.
		out, err := exec.Command("cmd.exe", "/c", "mklink", "/J", filepath.Join(root, "link"), outside).CombinedOutput()
		if err != nil {
			t.Fatalf("create junction: %v %s", err, out)
		}
	} else if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	body, _ := json.Marshal(protocol.ExecRequest{Command: "echo forbidden", WorkingDir: "link"})
	for _, req := range []transport.Request{{Method: "POST", Path: "/v1/exec", Body: body}, {Method: "GET", Path: "/v1/files/read?path=link/secret"}} {
		inv, err := prepareSSH(protocol.SSHConfig{Platform: platform}, root, req)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := decodeSSH(inv, runNative(t, inv, ""), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != 400 {
			t.Fatalf("escaped workspace: %s", resp.Body)
		}
	}
}

func TestSSHRejectsPathTraversalAndUnsupportedOperations(t *testing.T) {
	for _, r := range []transport.Request{
		{Method: "GET", Path: "/v1/files/read?path=..%2Foutside"},
		{Method: "POST", Path: "/v1/exec", Body: []byte(`{"command":"echo ok","working_dir":"../outside"}`)},
		{Method: "POST", Path: "/v1/exec", Body: []byte(`{"command":"echo ok","timeout_ms":1800001}`)},
		{Method: "POST", Path: "/v1/tasks"},
	} {
		if _, err := prepareSSH(protocol.SSHConfig{}, "/repo", r); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}

func TestSSHRejectsMalformedNativeFrames(t *testing.T) {
	for _, s := range []string{"banner\nDevFreightSSH1\nstatus\t200\n", "DevFreightSSH1\nstatus\t200\nstatus\t400\n", "DevFreightSSH1\nstatus\t200\nstdout\tbad"} {
		if _, err := decodeSSH(sshInvocation{operation: "exec"}, []byte(s), time.Second); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}

// The embedded POSIX executor is sent to sh over stdin, where a trailing CR
// becomes part of the command word. Keep it LF-only on every checkout.
func TestEmbeddedPOSIXScriptUsesUnixLineEndings(t *testing.T) {
	if index := strings.IndexByte(sshPOSIX, '\r'); index >= 0 {
		t.Fatalf("ssh_posix.sh contains a carriage return at byte %d", index)
	}
}

package worker

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
)

const nativeShell = "powershell"

const shellPreamble = "$OutputEncoding = [Console]::OutputEncoding = [Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false); "

func shellCommand(ctx context.Context, command string, session bool) *exec.Cmd {
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command"}
	if session {
		command = "-"
	} else {
		command = shellPreamble + command
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", append(args, command)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Cancel = func() error { return killProcessTree(cmd) }
	return cmd
}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := kill.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

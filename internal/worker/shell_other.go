//go:build !windows

package worker

import (
	"context"
	"os/exec"
	"syscall"
)

const nativeShell = "sh"

const shellPreamble = ""

func shellCommand(ctx context.Context, command string, session bool) *exec.Cmd {
	var cmd *exec.Cmd
	if session {
		cmd = exec.CommandContext(ctx, "sh")
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessTree(cmd) }
	return cmd
}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

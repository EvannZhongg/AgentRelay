//go:build !windows

package worker

import (
	"context"
	"os/exec"
)

// Experimental fallback; Windows is the currently validated platform.
const nativeShell = "sh"

const shellPreamble = ""

func shellCommand(ctx context.Context, command string, session bool) *exec.Cmd {
	if session {
		return exec.CommandContext(ctx, "sh")
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

//go:build linux

package monitor

import (
	"os/exec"
	"syscall"
)

func killProcessGroup(cmd *exec.Cmd) {
	// pty.StartWithSize uses Setsid: true, so cmd.Process.Pid is already the PGID.
	if cmd.Process != nil && cmd.Process.Pid > 0 {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

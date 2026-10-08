//go:build !windows

package tool

import (
	"context"
	"os/exec"
	"syscall"
)

// newShellCommand builds the platform shell invocation. The command runs
// in its own process group and the whole group is killed on timeout, so
// children (a test runner's compiler, say) don't linger.
func newShellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", command) //nolint:gosec // G204: running the agent's command is the purpose; approval-gated upstream
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return cmd
}

//go:build windows

package tool

import (
	"context"
	"os/exec"
)

// newShellCommand builds the platform shell invocation. On Windows only
// the direct cmd.exe process is killed on timeout; grandchildren may
// linger (acceptable v1 — matching the PTY layer's semantics).
func newShellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd", "/c", command) //nolint:gosec // G204: running the agent's command is the purpose; approval-gated upstream
}

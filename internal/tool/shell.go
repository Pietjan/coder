package tool

// One-off shell commands for agents, copied from toto's internal/shell:
// the escape hatch until a dedicated tool exists for the job. Commands run
// through the platform shell (sh -c / cmd /c) in a fixed working directory
// with a hard timeout; output is combined stdout+stderr, capped so a
// chatty command cannot flood a model's context.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

const (
	// DefaultTimeout applies when the caller passes no timeout.
	DefaultTimeout = 2 * time.Minute
	// MaxTimeout is the hard ceiling on any command's runtime.
	MaxTimeout = 10 * time.Minute

	// output cap: keep the head (the command usually says what it's
	// doing) and a bigger tail (errors come last), drop the middle.
	headBytes = 8 << 10
	tailBytes = 40 << 10
)

// Result is one finished command.
type Result struct {
	Output    string // combined stdout+stderr, possibly truncated
	ExitCode  int
	TimedOut  bool
	Truncated bool
}

// Run executes command through the platform shell in dir, bounded by
// timeout (clamped to (0, MaxTimeout]; <= 0 means DefaultTimeout). A
// non-zero exit is reported in the Result, not as an error — err is only
// for failures to run at all.
func Run(ctx context.Context, dir, command string, timeout time.Duration) (Result, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := newShellCommand(ctx, command)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	res := Result{Output: buf.String()}
	res.Output, res.Truncated = capOutput(res.Output)

	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	case err == nil:
		return res, nil
	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return Result{}, fmt.Errorf("shell: run: %w", err)
	}
}

// capOutput keeps the first headBytes and last tailBytes of oversized
// output with a marker in between.
func capOutput(out string) (string, bool) {
	if len(out) <= headBytes+tailBytes {
		return out, false
	}
	dropped := len(out) - headBytes - tailBytes
	return fmt.Sprintf("%s\n... [%d bytes truncated] ...\n%s",
		out[:headBytes], dropped, out[len(out)-tailBytes:]), true
}

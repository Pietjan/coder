package tool

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunEchoAndCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "here.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// `dir` on cmd, `ls` on sh — echo + a file listing works on both
	cmd := "echo hello-from-shell"
	res, err := Run(context.Background(), dir, cmd, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Output, "hello-from-shell") {
		t.Errorf("res = %+v", res)
	}

	// the working directory is the given dir
	listCmd := "ls"
	if runtime.GOOS == "windows" {
		listCmd = "dir /b"
	}
	res, err = Run(context.Background(), dir, listCmd, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "here.txt") {
		t.Errorf("cwd listing = %+v", res)
	}
}

func TestRunExitCode(t *testing.T) {
	res, err := Run(context.Background(), t.TempDir(), "exit 3", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || res.TimedOut {
		t.Errorf("res = %+v", res)
	}
}

func TestRunTimeoutKills(t *testing.T) {
	slow := "sleep 30"
	if runtime.GOOS == "windows" {
		slow = "ping -n 31 127.0.0.1 > NUL"
	}
	start := time.Now()
	res, err := Run(context.Background(), t.TempDir(), slow, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.ExitCode != -1 {
		t.Errorf("res = %+v", res)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("kill took %s", elapsed)
	}
}

func TestRunBadCommandReportsExitNotError(t *testing.T) {
	res, err := Run(context.Background(), t.TempDir(), "definitely-not-a-command-xyz", 0)
	if err != nil {
		t.Fatalf("shell-level failures should be a Result, got %v", err)
	}
	if res.ExitCode == 0 {
		t.Errorf("res = %+v, want non-zero exit", res)
	}
}

func TestCapOutput(t *testing.T) {
	small, truncated := capOutput("tiny")
	if small != "tiny" || truncated {
		t.Errorf("small output mangled: %q %v", small, truncated)
	}

	big := strings.Repeat("h", headBytes) + strings.Repeat("m", 100<<10) + strings.Repeat("t", tailBytes)
	out, truncated := capOutput(big)
	if !truncated {
		t.Fatal("big output not truncated")
	}
	if !strings.HasPrefix(out, "hhhh") || !strings.HasSuffix(out, "tttt") || !strings.Contains(out, "bytes truncated") {
		t.Errorf("cap kept the wrong parts: head=%q tail=%q", out[:8], out[len(out)-8:])
	}
	if len(out) > headBytes+tailBytes+100 {
		t.Errorf("capped output still %d bytes", len(out))
	}
}

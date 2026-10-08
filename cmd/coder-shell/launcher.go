package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"
)

// Daemon is the coder process running inside WSL.
type Daemon struct {
	cfg  Config
	cmd  *exec.Cmd
	done chan error
}

// StartDaemon runs `coder serve` in the distro, with its output on logw.
func StartDaemon(ctx context.Context, cfg Config, logw io.Writer) (*Daemon, error) {
	cmd := exec.CommandContext(ctx, "wsl.exe", //nolint:gosec // the configured binary, by design
		"-d", cfg.Distro, "--", cfg.CoderPath, "serve",
		"--listen", cfg.UIAddr,
		"--llm", "http://"+cfg.LLMAddr+"/v1",
		"--project", cfg.Project,
	)
	cmd.Stdout = logw
	cmd.Stderr = logw
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s in %s: %w", cfg.CoderPath, cfg.Distro, err)
	}
	d := &Daemon{cfg: cfg, cmd: cmd, done: make(chan error, 1)}
	go func() { d.done <- cmd.Wait() }()
	return d, nil
}

// WaitUp polls the daemon's health endpoint until it answers, the
// process exits, or the deadline passes.
func (d *Daemon) WaitUp(ctx context.Context, timeout time.Duration) error {
	return waitUp(ctx, "http://"+d.cfg.UIAddr+"/healthz", timeout, d.done)
}

// Stop ends the daemon: the context cancellation kills wsl.exe, and a
// pkill inside the distro catches the Linux process if the session
// teardown did not.
func (d *Daemon) Stop() {
	if d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	kill := exec.CommandContext(ctx, "wsl.exe", "-d", d.cfg.Distro, "--", //nolint:gosec // our own process name
		"pkill", "-f", d.cfg.CoderPath+" serve --listen "+d.cfg.UIAddr)
	hideWindow(kill)
	_ = kill.Run()
}

// waitUp polls url every 250 ms. exited, when it delivers, ends the wait
// early with that error.
func waitUp(ctx context.Context, url string, timeout time.Duration, exited <-chan error) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-exited:
			if err == nil {
				err = errors.New("exited")
			}
			return fmt.Errorf("the daemon stopped before it answered on %s: %w", url, err)
		case <-time.After(250 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not answer within %s", url, timeout)
		}
	}
}

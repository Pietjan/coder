//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps wsl.exe from opening a console next to the app.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

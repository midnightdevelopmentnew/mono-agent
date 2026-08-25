//go:build windows

package main

import (
	"os/exec"
)

// setChatProcessGroup is a no-op pending the dedicated Windows pass.
func setChatProcessGroup(cmd *exec.Cmd) {}

// killChatProcessGroup kills the direct child only on Windows.
func killChatProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// setChatProcessGroup isolates the chat subprocess (monoagentcli → monomind
// → agent CLI) in its own process group so a UI stop reaps the whole tree.
func setChatProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killChatProcessGroup SIGTERMs then SIGKILLs the whole chat process group.
func killChatProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

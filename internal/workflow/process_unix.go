//go:build !windows

package workflow

import "syscall"

// processAlive reports whether a process with the given pid is currently running.
// Signal 0 probes for existence without affecting the target; EPERM means the
// process exists but is owned by another user.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

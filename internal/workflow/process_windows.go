//go:build windows

package workflow

// processAlive is a conservative fallback on Windows: it reports processes as
// not alive so stale executions still recover on restart. Multi-process
// daemon/CLI coordination via pid liveness is a POSIX-oriented feature.
func processAlive(pid int) bool {
	return false
}

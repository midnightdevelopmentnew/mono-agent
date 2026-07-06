package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"time"
)

// reportCrash is called from a deferred recover() in main(). It never panics
// itself and never blocks longer than a few seconds — it's an observability
// side effect, not something that should change crash behavior for the user.
//
// Shells out to `monomind report-crash` (from the sibling monomind CLI) so
// redaction, dedup against existing GitHub issues, and auth (gh CLI /
// GITHUB_TOKEN) logic live in one place instead of being reimplemented here.
// If monomind isn't installed, the crash is saved to a local log file instead.
func reportCrash(panicValue interface{}, stack []byte) {
	title := fmt.Sprintf("panic: %v", panicValue)
	body := fmt.Sprintf("Uncaught panic in `monoagentcli`.\n\nVersion: %s (built %s)\n\n```\n%s\n```\n", version, buildDate, stack)

	if monomindPath, err := exec.LookPath("monomind"); err == nil {
		if reportViaMonomind(monomindPath, title, body) {
			return
		}
	}
	if npxPath, err := exec.LookPath("npx"); err == nil {
		if reportViaNpx(npxPath, title, body) {
			return
		}
	}
	saveCrashLocally(title, body)
}

// reportViaMonomind returns false if the invocation failed (e.g. an older
// monomind on PATH predates the `report-crash` command) so the caller can
// fall back rather than silently swallowing the crash report.
func reportViaMonomind(monomindPath, title, body string) bool {
	cmd := exec.Command(monomindPath, "report-crash", "--repo", "monoes/mono-agent", "--title", title, "--body", body)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return runWithTimeout(cmd, 15*time.Second) == nil
}

func reportViaNpx(npxPath, title, body string) bool {
	cmd := exec.Command(npxPath, "-y", "monomind", "report-crash", "--repo", "monoes/mono-agent", "--title", title, "--body", body)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return runWithTimeout(cmd, 20*time.Second) == nil
}

func runWithTimeout(cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return fmt.Errorf("crash report timed out after %s", timeout)
	}
}

func saveCrashLocally(title, body string) {
	u, err := user.Current()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[monoagentcli] crash occurred, and couldn't save a report (no home dir): %s\n", title)
		return
	}
	crashDir := filepath.Join(u.HomeDir, ".monoagent", "crashes")
	if err := os.MkdirAll(crashDir, 0o755); err != nil {
		return
	}
	path := filepath.Join(crashDir, fmt.Sprintf("%d.md", time.Now().Unix()))
	content := fmt.Sprintf("# %s\n\n%s\n", title, body)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[monoagentcli] crash report saved to %s (install monomind to auto-file: npm i -g monomind)\n", path)
}

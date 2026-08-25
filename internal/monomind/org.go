package monomind

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Org observe/action commands (protocol §7): thin proxies over
// `monomind org <sub> [<name>] --format json`. Every subcommand resolves
// its project by cwd (§7.1), so callers pass projectRoot explicitly — this
// client manages multiple project roots, unlike the CLI's own cwd.
//
// Read-only/action results are returned as json.RawMessage rather than
// decoded into typed structs: the org JSON shapes are still evolving on the
// monomind side and callers here (the CLI, then the Wails bindings, then
// the frontend) only need to pass the payload through, not manipulate it in
// Go. This avoids silently dropping fields we didn't anticipate.

// orgTimeout bounds one-shot org observe/action calls.
var orgTimeout = 60 * time.Second

// runOrgJSON runs `monomind org <args...> --format json` with cwd=projectRoot
// and returns the raw stdout payload.
func runOrgJSON(ctx context.Context, projectRoot string, args ...string) (json.RawMessage, error) {
	bin, _, err := Ensure(ctx)
	if err != nil {
		return nil, err
	}
	full := append(append([]string{"org"}, args...), "--format", "json")

	cctx, cancel := context.WithTimeout(ctx, orgTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, full...)
	cmd.Dir = projectRoot

	out, err := cmd.Output()
	if err != nil {
		return nil, orgCommandError(args, err)
	}
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("monomind org %s: empty output", strings.Join(args, " "))
	}
	return json.RawMessage(trimmed), nil
}

// orgCommandError extracts the actionable message from a failed org
// subprocess. Exit codes are not reliably distinguishable between usage and
// runtime errors (protocol §7.1 caveat verified against org.ts), so this
// always surfaces stderr text rather than branching on exit status.
func orgCommandError(args []string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		msg := strings.TrimSpace(string(ee.Stderr))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("monomind org %s: %s", strings.Join(args, " "), msg)
	}
	return fmt.Errorf("monomind org %s: %w", strings.Join(args, " "), err)
}

// OrgList returns every org in the project (`org list`).
func OrgList(ctx context.Context, projectRoot string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "list")
}

// OrgStatus returns one org's status, or every org's status when name=="".
func OrgStatus(ctx context.Context, projectRoot, name string) (json.RawMessage, error) {
	args := []string{"status"}
	if name != "" {
		args = append(args, name)
	}
	return runOrgJSON(ctx, projectRoot, args...)
}

// OrgLogs returns the org's bus event log (`org logs <name>`). There is no
// --tail flag on the live monomind CLI despite the plan doc's claim.
func OrgLogs(ctx context.Context, projectRoot, name string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "logs", name)
}

// OrgReport returns the org's run report; all=true requests every run
// (`org report <name> --all`) instead of just the latest.
func OrgReport(ctx context.Context, projectRoot, name string, all bool) (json.RawMessage, error) {
	args := []string{"report", name}
	if all {
		args = append(args, "--all")
	}
	return runOrgJSON(ctx, projectRoot, args...)
}

// OrgCosts returns per-role token/cost totals (`org costs <name>`).
func OrgCosts(ctx context.Context, projectRoot, name string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "costs", name)
}

// OrgFlow returns the org's role communication graph (`org flow <name>`).
func OrgFlow(ctx context.Context, projectRoot, name string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "flow", name)
}

// OrgQuestions returns pending human-input questions (`org questions <name>`).
func OrgQuestions(ctx context.Context, projectRoot, name string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "questions", name)
}

// OrgGates returns pending decision gates (`org gates <name>`).
func OrgGates(ctx context.Context, projectRoot, name string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "gates", name)
}

// OrgDecisions returns the org's decision trace (`org decisions <name>`).
func OrgDecisions(ctx context.Context, projectRoot, name string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "decisions", name)
}

// OrgMemoryStats returns org memory statistics (`org memory <name> stats`).
func OrgMemoryStats(ctx context.Context, projectRoot, name string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "memory", name, "stats")
}

// OrgAnswer answers a pending human-input question
// (`org answer <name> <questionID> <answer...>`).
func OrgAnswer(ctx context.Context, projectRoot, name, questionID, answer string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "answer", name, questionID, answer)
}

// OrgApprove approves a pending tool-approval request
// (`org approve <name> <role> <action>` — role+action, not an id).
func OrgApprove(ctx context.Context, projectRoot, name, role, action string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "approve", name, role, action)
}

// OrgDeny denies a pending tool-approval request (`org deny <name> <role> <action>`).
func OrgDeny(ctx context.Context, projectRoot, name, role, action string) (json.RawMessage, error) {
	return runOrgJSON(ctx, projectRoot, "deny", name, role, action)
}

// OrgGateApprove approves a decision gate
// (`org gate-approve <name> <gateID> [resolution...]`).
func OrgGateApprove(ctx context.Context, projectRoot, name, gateID, resolution string) (json.RawMessage, error) {
	args := []string{"gate-approve", name, gateID}
	if resolution != "" {
		args = append(args, resolution)
	}
	return runOrgJSON(ctx, projectRoot, args...)
}

// OrgGateReject rejects a decision gate (`org gate-reject <name> <gateID> [resolution...]`).
func OrgGateReject(ctx context.Context, projectRoot, name, gateID, resolution string) (json.RawMessage, error) {
	args := []string{"gate-reject", name, gateID}
	if resolution != "" {
		args = append(args, resolution)
	}
	return runOrgJSON(ctx, projectRoot, args...)
}

// OrgEventsOptions configures OrgEvents.
type OrgEventsOptions struct {
	Run    string // --run <id>, empty = current run
	Follow bool   // --follow: keep streaming (like tail -f)
	Since  string // --since <eventId|iso>
}

// OrgEvents streams the org's bus.jsonl as NDJSON (`org events <name>`).
// NDJSON is the command's only output mode — no --format flag applies here.
// onLine is invoked once per raw JSON line, in arrival order. OrgEvents
// blocks until the subprocess exits or ctx is cancelled (in which case the
// process group is killed so no `monomind` or agent-CLI grandchild survives
// the caller, matching Exec's cancellation contract).
func OrgEvents(ctx context.Context, projectRoot, name string, opts OrgEventsOptions, onLine func(line []byte)) error {
	bin, _, err := Ensure(ctx)
	if err != nil {
		return err
	}
	args := []string{"org", "events", name}
	if opts.Run != "" {
		args = append(args, "--run", opts.Run)
	}
	if opts.Follow {
		args = append(args, "--follow")
	}
	if opts.Since != "" {
		args = append(args, "--since", opts.Since)
	}

	cmd := exec.Command(bin, args...)
	cmd.Dir = projectRoot
	setProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start monomind org events: %w", err)
	}

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			lineCopy := make([]byte, len(line))
			copy(lineCopy, line)
			onLine(lineCopy)
		}
	}()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		killProcessGroup(cmd, cmd.Process.Pid)
		<-readerDone
		<-waitCh
		return ctx.Err()
	case err := <-waitCh:
		<-readerDone
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return fmt.Errorf("monomind org events %s: %s", name, msg)
		}
		return nil
	}
}

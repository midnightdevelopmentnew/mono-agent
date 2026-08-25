package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ─────────────────────────────────────────────────────────────────────────────
// Orgs (monomind Org Runtime v2, via the Agent Exec Protocol's org surface)
//
// Same doctrine as the agent chat bindings in app_ai.go: this file never
// imports internal/monomind — every call shells out to `monoagentcli org
// <sub> ...`, whose stdout JSON is the UI contract. The Wails methods here
// return that JSON verbatim as a string; the frontend parses it (same
// pattern as ScanAgentRuntimes/StreamAgentChat).
// ─────────────────────────────────────────────────────────────────────────────

// orgCLITimeout bounds one-shot org observe/action calls from the UI.
const orgCLITimeout = 60 * time.Second

// runOrgCLI runs `monoagentcli org <args...>` and returns its stdout JSON
// verbatim, or an {"error":"..."} payload on failure (aiError shape,
// app_ai.go) so the frontend's existing error-guard idiom applies unchanged.
func (a *App) runOrgCLI(args ...string) string {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return aiError(err)
	}
	ctx, cancel := context.WithTimeout(a.ctx, orgCLITimeout)
	defer cancel()
	fullArgs := append([]string{"org"}, args...)
	cmd := exec.CommandContext(ctx, cliBin, fullArgs...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return aiError(fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr))))
		}
		return aiError(err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return aiError(fmt.Errorf("org %s: empty output", strings.Join(args, " ")))
	}
	return trimmed
}

func (a *App) ListOrgs() string {
	return a.runOrgCLI("list")
}

func (a *App) GetOrgStatus(name string) string {
	if name == "" {
		return a.runOrgCLI("status")
	}
	return a.runOrgCLI("status", name)
}

func (a *App) GetOrgLogs(name string) string {
	return a.runOrgCLI("logs", name)
}

func (a *App) GetOrgReport(name string, all bool) string {
	if all {
		return a.runOrgCLI("report", name, "--all")
	}
	return a.runOrgCLI("report", name)
}

func (a *App) GetOrgCosts(name string) string {
	return a.runOrgCLI("costs", name)
}

func (a *App) GetOrgFlow(name string) string {
	return a.runOrgCLI("flow", name)
}

func (a *App) GetOrgQuestions(name string) string {
	return a.runOrgCLI("questions", name)
}

func (a *App) GetOrgGates(name string) string {
	return a.runOrgCLI("gates", name)
}

func (a *App) GetOrgDecisions(name string) string {
	return a.runOrgCLI("decisions", name)
}

func (a *App) GetOrgMemoryStats(name string) string {
	return a.runOrgCLI("memory", name)
}

func (a *App) AnswerOrgQuestion(name, questionID, answer string) string {
	return a.runOrgCLI("answer", name, questionID, answer)
}

func (a *App) ApproveOrgAction(name, role, action string) string {
	return a.runOrgCLI("approve", name, role, action)
}

func (a *App) DenyOrgAction(name, role, action string) string {
	return a.runOrgCLI("deny", name, role, action)
}

func (a *App) GateApproveOrgAction(name, gateID, resolution string) string {
	if resolution == "" {
		return a.runOrgCLI("gate-approve", name, gateID)
	}
	return a.runOrgCLI("gate-approve", name, gateID, resolution)
}

func (a *App) GateRejectOrgAction(name, gateID, resolution string) string {
	if resolution == "" {
		return a.runOrgCLI("gate-reject", name, gateID)
	}
	return a.runOrgCLI("gate-reject", name, gateID, resolution)
}

// StreamOrgEvents starts a live tail of an org's bus event log. Each event
// line is re-emitted as a Wails "org:event" event {orgName, event}; the
// subprocess is tracked the same way agent chat is (a.runningCmds, keyed so
// a new stream for the same org supersedes the previous one) and killed as
// a process group on Stop/supersede/shutdown — matching StreamAgentChat.
func (a *App) StreamOrgEvents(orgName string) string {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return aiError(err)
	}
	cmd := exec.Command(cliBin, "org", "events", orgName, "--follow")
	setChatProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return aiError(err)
	}
	cmd.Stderr = a.chatLogWriter()
	if err := cmd.Start(); err != nil {
		return aiError(fmt.Errorf("start org events: %w", err))
	}

	key := "orgevents:" + orgName
	a.runningMu.Lock()
	if prev, ok := a.runningCmds[key]; ok {
		killChatProcessGroup(prev)
		delete(a.runningCmds, key)
	}
	a.runningCmds[key] = cmd
	a.runningMu.Unlock()

	go func() {
		defer func() {
			a.runningMu.Lock()
			delete(a.runningCmds, key)
			a.runningMu.Unlock()
		}()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			runtime.EventsEmit(a.ctx, "org:event", map[string]interface{}{
				"orgName": orgName,
				"event":   json.RawMessage(append([]byte(nil), line...)),
			})
		}
		_ = cmd.Wait()
		runtime.EventsEmit(a.ctx, "org:eventsClosed", map[string]interface{}{"orgName": orgName})
	}()
	return `{"ok":true}`
}

// StopOrgEvents kills the in-flight org event tail subprocess for an org.
func (a *App) StopOrgEvents(orgName string) string {
	key := "orgevents:" + orgName
	a.runningMu.Lock()
	cmd, ok := a.runningCmds[key]
	if ok {
		delete(a.runningCmds, key)
	}
	a.runningMu.Unlock()
	if !ok {
		return `{"ok":false,"error":"no active event stream"}`
	}
	killChatProcessGroup(cmd)
	return `{"ok":true}`
}

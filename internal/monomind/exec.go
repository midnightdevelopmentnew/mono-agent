package monomind

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// ToolHandler executes one bridged tool call (protocol §4). The returned
// string is what the agent sees; an error becomes an ok:false frame.
type ToolHandler func(ctx context.Context, name string, args json.RawMessage) (string, error)

// ExecOptions configures one `monomind agent exec` turn (protocol §3.1).
type ExecOptions struct {
	Runtime string
	Prompt  string
	Model   string
	Cwd     string
	Resume  string
	// SystemPrompt, when set, is written to a temp file and passed via
	// --system-file (avoids argv limits).
	SystemPrompt string
	// Tools enables the stdio bridge (§4); empty means --tools none.
	Tools []ToolSpec
	// OnToolCall executes bridged tool calls; required when Tools is set.
	OnToolCall ToolHandler
	// ToolTimeout bounds each tool round-trip (--tool-timeout).
	ToolTimeout time.Duration
	// Timeout is the overall wall-clock cap (--timeout); zero = none.
	Timeout time.Duration
	// BudgetUSD is the optional spend cap (--budget-usd).
	BudgetUSD float64
	// Env adds KEY=VALUE entries to the monomind process environment.
	Env map[string]string
	// Bin overrides the monomind binary (tests); empty = discovery.
	Bin string
}

// TurnResult is the terminal state of one exec turn.
type TurnResult struct {
	ExitCode   int
	SessionID  string
	ResultText string
	Err        *ProtocolError
}

// toolResultFrame is the client→monomind reply for a tool_call (§4.3).
type toolResultFrame struct {
	V      int    `json:"v"`
	Type   string `json:"type"`
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Result struct {
		Text string `json:"text"`
	} `json:"result"`
}

// KillGrace bounds the window between a graceful cancel (cancel frame +
// stdin EOF) and the process-group kill on ctx cancellation. Overridable
// for tests.
var KillGrace = 5 * time.Second

// Exec runs one agent turn and invokes onEvent for every protocol event in
// arrival order. It returns the turn's terminal state: a *ProtocolError for
// error turns (also mirrored in the error event the handler received).
//
// Lifecycle contract (protocol §3.2/§3.4): the stream always ends in done;
// ctx cancellation sends a cancel frame first (best-effort graceful), then
// escalates to a process-group kill after a grace window so neither
// monomind nor an agent-CLI grandchild survives the caller.
func Exec(ctx context.Context, opts ExecOptions, onEvent func(Event)) (*TurnResult, error) {
	bin := opts.Bin
	if bin == "" {
		var err error
		bin, err = Find()
		if err != nil {
			return nil, err
		}
	}

	if opts.Prompt == "" {
		return nil, fmt.Errorf("ExecOptions.Prompt is required")
	}
	if len(opts.Tools) > 0 && opts.OnToolCall == nil {
		return nil, fmt.Errorf("ExecOptions.OnToolCall is required when Tools is set")
	}

	args := []string{"agent", "exec", "--runtime", opts.Runtime, "--prompt", opts.Prompt}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Cwd != "" {
		args = append(args, "--cwd", opts.Cwd)
	}
	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	}
	if opts.Timeout > 0 {
		args = append(args, "--timeout", formatDuration(opts.Timeout))
	}
	if opts.BudgetUSD > 0 {
		args = append(args, "--budget-usd", fmt.Sprintf("%g", opts.BudgetUSD))
	}
	if opts.ToolTimeout > 0 {
		args = append(args, "--tool-timeout", formatDuration(opts.ToolTimeout))
	}

	cleanup := []func(){}
	defer func() {
		for _, f := range cleanup {
			f()
		}
	}()

	if opts.SystemPrompt != "" {
		f, err := os.CreateTemp("", "monoagent-system-*.md")
		if err != nil {
			return nil, fmt.Errorf("write system prompt: %w", err)
		}
		if _, err := f.WriteString(opts.SystemPrompt); err != nil {
			f.Close()
			os.Remove(f.Name())
			return nil, fmt.Errorf("write system prompt: %w", err)
		}
		f.Close()
		name := f.Name()
		cleanup = append(cleanup, func() { os.Remove(name) })
		args = append(args, "--system-file", name)
	}

	if len(opts.Tools) > 0 {
		b, err := json.Marshal(opts.Tools)
		if err != nil {
			return nil, fmt.Errorf("marshal tools: %w", err)
		}
		f, err := os.CreateTemp("", "monoagent-tools-*.json")
		if err != nil {
			return nil, fmt.Errorf("write tools file: %w", err)
		}
		if _, err := f.Write(b); err != nil {
			f.Close()
			os.Remove(f.Name())
			return nil, fmt.Errorf("write tools file: %w", err)
		}
		f.Close()
		name := f.Name()
		cleanup = append(cleanup, func() { os.Remove(name) })
		args = append(args, "--tools", "stdio", "--tools-file", name)
	}

	cmd := exec.Command(bin, args...)
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), envSlice(opts.Env)...)
	}
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr // monomind keeps diagnostics off stdout (§3)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start monomind: %w", err)
	}

	res := &TurnResult{}
	events := make(chan Event, 64)
	var stdinMu sync.Mutex
	var stdinOnce sync.Once

	writeLine := func(b []byte) {
		stdinMu.Lock()
		defer stdinMu.Unlock()
		_, _ = stdin.Write(append(b, '\n'))
	}
	closeStdin := func() {
		stdinOnce.Do(func() {
			stdinMu.Lock()
			defer stdinMu.Unlock()
			_ = stdin.Close()
		})
	}

	// stdout reader: one JSON object per line (§3); closes events on exit.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(events)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue // tolerate stray non-JSON output
			}
			lineCopy := make([]byte, len(line))
			copy(lineCopy, line) // sc.Bytes() is reused across scans
			var ev Event
			if err := json.Unmarshal(lineCopy, &ev); err != nil {
				continue // malformed line: skip, keep streaming
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Event loop: fan out to the caller, bridge tool calls, capture state.
	var toolWg sync.WaitGroup
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		for ev := range events {
			if onEvent != nil {
				onEvent(ev)
			}
			switch ev.Type {
			case EventSession:
				if ev.SessionID != "" {
					res.SessionID = ev.SessionID
				}
			case EventResult:
				res.ResultText = ev.Text
			case EventError:
				res.Err = &ProtocolError{Code: ev.Code, Message: ev.ErrMessage, Fatal: ev.Fatal}
			case EventDone:
				res.ExitCode = ev.ExitCode
			case EventToolCall:
				toolWg.Add(1)
				go func(ev Event) {
					defer toolWg.Done()
					text, err := opts.OnToolCall(ctx, ev.Name, ev.Args)
					frame := toolResultFrame{V: ProtocolVersion, Type: "tool_result", ID: ev.ID}
					if err != nil {
						frame.OK = false
						frame.Result.Text = err.Error()
					} else {
						frame.OK = true
						frame.Result.Text = text
					}
					if b, err := json.Marshal(frame); err == nil {
						writeLine(b)
					}
				}(ev)
			}
		}
	}()

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		// Graceful first (cancel frame + EOF), bounded by a group kill.
		writeLine([]byte(`{"v":1,"type":"cancel"}`))
		closeStdin()
		killTimer := time.AfterFunc(KillGrace, func() { killProcessGroup(cmd, cmd.Process.Pid) })
		defer killTimer.Stop()
		<-loopDone
		<-waitCh
		toolWg.Wait()
		if res.ExitCode == 0 && res.Err == nil {
			res.Err = &ProtocolError{Code: ErrCancelled, Message: "cancelled by caller", ExitCode: 130}
			res.ExitCode = 130
		}
		return res, nil

	case err := <-waitCh:
		// Process exited — drain buffered events, then finish up.
		<-readerDone
		<-loopDone
		toolWg.Wait()
		closeStdin()
		if res.ExitCode == 0 && res.Err == nil && err != nil {
			res.Err = &ProtocolError{Code: ErrRunnerError, Message: fmt.Sprintf("monomind exited: %v", err), ExitCode: 1}
			res.ExitCode = 1
		}
		return res, nil
	}
}

// envSlice flattens a map into KEY=VALUE strings.
func envSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// formatDuration renders a duration in the protocol's duration syntax
// (sub-second is ms-suffixed; everything else whole seconds).
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%ds", int((d+time.Second/2)/time.Second))
}

// Scan proxies `monomind agent scan --json` (protocol §6). Scanning always
// exits 0 on the monomind side; errors here mean monomind is missing/broken.
//
// Handshakes via Ensure() first: an installed-but-too-old monomind answers
// an unrecognized `agent scan --json` invocation with human help text
// instead of JSON (exit 0), which would otherwise surface as a confusing
// "unparseable output" JSON error instead of the actionable "monomind X is
// too old" message every other entry point already gives.
func Scan(ctx context.Context) (*ScanResult, error) {
	bin, _, err := Ensure(ctx)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "agent", "scan", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("agent scan failed: %w", err)
	}
	var res ScanResult
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("agent scan: unparseable output: %w", err)
	}
	return &res, nil
}

package monomind

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func fakeBin(t *testing.T, name string) string {
	t.Helper()
	bin := filepath.Join("testdata", name)
	abs, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return abs
	}
	if err := os.Chmod(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		got, min string
		want     bool
	}{
		{"2.10.0", "2.10.0", true},
		{"2.11.3", "2.10.0", true},
		{"2.9.25", "2.10.0", false},
		{"3.0.0", "2.10.0", true},
		{"2.10.0-beta.1", "2.10.0", true},
		{"v2.10.0", "2.10.0", true},
		{"10.0.0", "9.9.9", true},
		{"2.10", "2.10.0", true},
	}
	for _, c := range cases {
		if got := versionAtLeast(c.got, c.min); got != c.want {
			t.Errorf("versionAtLeast(%q, %q) = %v, want %v", c.got, c.min, got, c.want)
		}
	}
}

func TestHandshakeAgainstFake(t *testing.T) {
	vi, err := Handshake(context.Background(), fakeBin(t, "fake-monomind.sh"))
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if vi.Version != "2.10.0" {
		t.Errorf("version = %q", vi.Version)
	}
	for _, cap := range RequiredCapabilities {
		if !vi.HasCapability(cap) {
			t.Errorf("missing capability %q", cap)
		}
	}
}

func TestScanAgainstFake(t *testing.T) {
	old := os.Getenv(EnvOverride)
	os.Setenv(EnvOverride, fakeBin(t, "fake-monomind.sh"))
	defer os.Setenv(EnvOverride, old)

	res, err := Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(res.Agents))
	}
	if e := res.Find("claude"); e == nil || !e.Installed {
		t.Errorf("claude entry = %+v", e)
	}
}

// TestExecToolBridgeRoundTrip drives the full bidirectional path through a
// real subprocess: events in, tool_call bridged to Go, tool_result frame
// written back on stdin, turn completes.
func TestExecToolBridgeRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	var mu atomic.Value // []string of event types
	mu.Store([]string{})
	var toolArgs json.RawMessage

	res, err := Exec(context.Background(), ExecOptions{
		Bin:     fakeBin(t, "fake-monomind.sh"),
		Runtime: "claude",
		Prompt:  "build it",
		Tools: []ToolSpec{{
			Name:        "create_nodes",
			Description: "Create workflow nodes",
			Schema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"count": map[string]interface{}{"type": "number"}},
				"required":   []string{"count"},
			},
		}},
		OnToolCall: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			if name != "create_nodes" {
				t.Errorf("tool name = %q", name)
			}
			toolArgs = args
			return "created 2 nodes", nil
		},
	}, func(ev Event) {
		prev, _ := mu.Load().([]string)
		mu.Store(append(prev, ev.Type))
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("turn error: %+v", res.Err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d", res.ExitCode)
	}
	if res.SessionID != "th_fake" {
		t.Errorf("session id = %q", res.SessionID)
	}
	var gotCount float64
	var direct map[string]interface{}
	if err := json.Unmarshal(toolArgs, &direct); err != nil {
		t.Fatalf("tool args unparseable: %v (raw %s)", err, toolArgs)
	}
	if c, ok := direct["count"].(float64); ok {
		gotCount = c
	}
	if gotCount != 2 {
		t.Errorf("tool args count = %v, want 2 (raw: %s)", gotCount, toolArgs)
	}
	types, _ := mu.Load().([]string)
	wantOrder := []string{EventStart, EventSession, EventAssistant, EventToolCall, EventToolResult, EventAssistant, EventUsage, EventResult, EventDone}
	if len(types) != len(wantOrder) {
		t.Fatalf("event order %v, want %v", types, wantOrder)
	}
	for i := range wantOrder {
		if types[i] != wantOrder[i] {
			t.Errorf("event[%d] = %s, want %s (%v)", i, types[i], wantOrder[i], types)
		}
	}
}

// TestExecCancelGroupKill verifies the plan's Phase-1 gate: cancelling a
// hung monomind leaves ZERO orphan processes — monomind AND its spawned
// grandchild are reaped by the group kill.
func TestExecCancelGroupKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("group kill is unix-only in this build")
	}
	oldGrace := KillGrace
	KillGrace = 500 * time.Millisecond
	defer func() { KillGrace = oldGrace }()

	bin := fakeBin(t, "fake-monolith.sh")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resCh := make(chan *TurnResult, 1)
	go func() {
		res, _ := Exec(ctx, ExecOptions{
			Bin:     bin,
			Runtime: "claude",
			Prompt:  "hang forever",
		}, nil)
		resCh <- res
	}()

	// Wait for the monolith (and its sleep child) to exist.
	var pids []int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(pids) < 2 {
		pids = pids[:0]
		out, _ := exec.Command("pgrep", "-f", "fake-monolith").Output()
		for _, s := range fields(string(out)) {
			pids = append(pids, atoi(s))
		}
		if len(pids) > 0 {
			sleepOut, _ := exec.Command("sh", "-c", "pgrep -P "+itoa(pids[0])).Output()
			for _, s := range fields(string(sleepOut)) {
				pids = append(pids, atoi(s))
			}
		}
		if len(pids) < 2 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if len(pids) < 2 {
		t.Fatalf("expected monolith + child, found pids %v", pids)
	}

	cancel()
	select {
	case res := <-resCh:
		if res == nil || res.Err == nil || res.Err.Code != ErrCancelled {
			t.Fatalf("turn result = %+v, want cancelled", res)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Exec did not return after cancel")
	}

	// The gate: zero orphaned processes from the group.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		alive := 0
		for _, pid := range pids {
			if err := syscall.Kill(pid, 0); err == nil {
				alive++
			}
		}
		if alive == 0 {
			return // success — group fully reaped
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); err == nil {
			t.Errorf("orphan process %d survived the group kill", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// TestExecErrorTurnMapsProtocolError checks fatal error turns surface as
// TurnResult.Err with the protocol error code.
func TestExecErrorTurnMapsProtocolError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	// Reuse fake-monomind's exec path but force an auth failure through a
	// tiny inline script.
	dir := t.TempDir()
	bin := filepath.Join(dir, "monomind")
	script := "#!/bin/sh\ncase \"$1 $2\" in \"--version --json\") echo '{\"v\":1,\"version\":\"2.10.0\",\"min_caller\":\"1.0.0\",\"capabilities\":[\"agent-exec\",\"agent-scan\"]}'; exit 0;; esac\n" +
		"echo '{\"v\":1,\"type\":\"start\",\"runtime\":\"codex\",\"cwd\":\"/app\",\"pid\":1}'\n" +
		"echo '{\"v\":1,\"type\":\"error\",\"code\":\"auth\",\"fatal\":true,\"message\":\"not logged in\"}'\n" +
		"echo '{\"v\":1,\"type\":\"done\",\"exit_code\":1}'\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "codex", Prompt: "hi"}, nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Err == nil {
		t.Fatal("expected protocol error")
	}
	if res.Err.Code != ErrAuth || !res.Err.Fatal {
		t.Errorf("err = %+v", res.Err)
	}
	if res.ExitCode != 1 {
		t.Errorf("exit = %d", res.ExitCode)
	}
}

func fields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

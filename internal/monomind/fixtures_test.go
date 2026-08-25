package monomind

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fixtureEvents loads one golden transcript from testdata/fixtures —
// verbatim copies of monomind's published protocol goldens (plan gate:
// Phase-1 contract tests consume the Phase-0 fixtures).
func fixtureEvents(t *testing.T, name string) []Event {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "fixtures", name+".ndjson"))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	var evs []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("%s: unparseable golden line %q: %v", name, line, err)
		}
		evs = append(evs, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("%s: read: %v", name, err)
	}
	return evs
}

func TestFixtureEveryLineParsesAsV1Event(t *testing.T) {
	for _, name := range []string{"success", "tool-loop", "fatal-auth", "timeout", "cancel", "bad-frame"} {
		evs := fixtureEvents(t, name)
		if len(evs) == 0 {
			t.Fatalf("%s: empty fixture", name)
		}
		for _, ev := range evs {
			if ev.V != 1 {
				t.Errorf("%s: event type %s has v=%d, want 1", name, ev.Type, ev.V)
			}
		}
	}
}

func TestFixtureStartFirstDoneLastExactlyOnce(t *testing.T) {
	for _, name := range []string{"success", "tool-loop", "fatal-auth", "timeout", "cancel", "bad-frame"} {
		evs := fixtureEvents(t, name)
		if evs[0].Type != EventStart {
			t.Errorf("%s: first event is %s, want start", name, evs[0].Type)
		}
		if evs[len(evs)-1].Type != EventDone {
			t.Errorf("%s: last event is %s, want done", name, evs[len(evs)-1].Type)
		}
		var done int
		for _, ev := range evs {
			if ev.Type == EventDone {
				done++
			}
		}
		if done != 1 {
			t.Errorf("%s: %d done events, want 1", name, done)
		}
	}
}

func TestFixtureExitCodesMatchContract(t *testing.T) {
	want := map[string]int{
		"success":    0,
		"tool-loop":  0,
		"bad-frame":  0,
		"fatal-auth": 1,
		"timeout":    124,
		"cancel":     130,
	}
	for name, code := range want {
		evs := fixtureEvents(t, name)
		if got := evs[len(evs)-1].ExitCode; got != code {
			t.Errorf("%s: done.exit_code=%d, want %d", name, got, code)
		}
	}
}

func TestFixtureSuccessShape(t *testing.T) {
	evs := fixtureEvents(t, "success")
	types := make([]string, len(evs))
	for i, ev := range evs {
		types[i] = ev.Type
	}
	want := []string{EventStart, EventSession, EventAssistant, EventAssistant, EventUsage, EventResult, EventDone}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("success: event[%d]=%s, want %s (full: %v)", i, types[i], want[i], types)
		}
	}
	var result *Event
	for i := range evs {
		if evs[i].Type == EventResult {
			result = &evs[i]
		}
	}
	if result == nil {
		t.Fatal("success: no result event")
	}
	if result.Subtype != "success" || result.IsError || result.StopReason != StopEndTurn {
		t.Errorf("success: result = %+v", result)
	}
	if result.InputTokens != 1842 || result.OutputTokens != 96 || result.CostUSD != 0.0041 {
		t.Errorf("success: result usage = in:%d out:%d $%v", result.InputTokens, result.OutputTokens, result.CostUSD)
	}
}

func TestFixtureToolLoopEcho(t *testing.T) {
	evs := fixtureEvents(t, "tool-loop")
	var call, echo *Event
	for i := range evs {
		switch evs[i].Type {
		case EventToolCall:
			call = &evs[i]
		case EventToolResult:
			echo = &evs[i]
		}
	}
	if call == nil || echo == nil {
		t.Fatalf("tool-loop: missing call/echo: call=%v echo=%v", call, echo)
	}
	if call.Name != "create_nodes" || echo.ID != call.ID || echo.OK == nil || !*echo.OK {
		t.Errorf("tool-loop: call=%+v echo=%+v", call, echo)
	}
	if echo.Result == nil || echo.Result.Text != "created 2 nodes" {
		t.Errorf("tool-loop: echo result = %+v", echo.Result)
	}
}

func TestFixtureErrorSemantics(t *testing.T) {
	auth := fixtureEvents(t, "fatal-auth")
	for _, ev := range auth {
		if ev.Type == EventError {
			if ev.Code != ErrAuth || !ev.Fatal {
				t.Errorf("fatal-auth: error event = %+v", ev)
			}
			if ev.ExitCode != 0 { // error uses exit_code only on done
				t.Errorf("fatal-auth: error event carries exit_code %d", ev.ExitCode)
			}
		}
	}
	for _, name := range []string{"timeout", "cancel", "bad-frame"} {
		for _, ev := range fixtureEvents(t, name) {
			if ev.Type == EventError && ev.Fatal {
				t.Errorf("%s: error %q must not be fatal", name, ev.Code)
			}
		}
	}
}

func TestScanResultHelpers(t *testing.T) {
	res := &ScanResult{
		V: 1,
		Agents: []ScanEntry{
			{ID: "claude", Installed: true},
			{ID: "codex", Installed: false, InstallHint: "npm install -g @openai/codex"},
		},
	}
	if got := len(res.Installed()); got != 1 {
		t.Errorf("Installed() = %d entries, want 1", got)
	}
	if e := res.Find("codex"); e == nil || e.Installed {
		t.Errorf("Find(codex) = %+v", e)
	}
	if e := res.Find("ghost"); e != nil {
		t.Errorf("Find(ghost) = %+v, want nil", e)
	}
}

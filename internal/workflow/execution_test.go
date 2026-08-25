package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/rs/zerolog"
)

// stubStore is a no-op WorkflowStore that only records the execution-node rows
// written during a run, so tests can assert which nodes ran and with what input.
type stubStore struct {
	mu           sync.Mutex
	nodes        []*WorkflowExecutionNode
	waitingState string // captured by SetExecutionWaiting
}

func (s *stubStore) CreateExecutionNode(ctx context.Context, en *WorkflowExecutionNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *en
	s.nodes = append(s.nodes, &cp)
	return nil
}

func (s *stubStore) CreateWorkflow(context.Context, *Workflow) error              { return nil }
func (s *stubStore) GetWorkflow(context.Context, string) (*Workflow, error)       { return nil, nil }
func (s *stubStore) ListWorkflows(context.Context, string) ([]Workflow, error)    { return nil, nil }
func (s *stubStore) UpdateWorkflow(context.Context, *Workflow) error              { return nil }
func (s *stubStore) DeleteWorkflow(context.Context, string) error                 { return nil }
func (s *stubStore) SetWorkflowActive(context.Context, string, bool) error        { return nil }
func (s *stubStore) SaveWorkflowNodes(context.Context, string, []WorkflowNode) error {
	return nil
}
func (s *stubStore) SaveWorkflowConnections(context.Context, string, []WorkflowConnection) error {
	return nil
}
func (s *stubStore) CreateExecution(context.Context, *WorkflowExecution) error { return nil }
func (s *stubStore) GetExecution(context.Context, string) (*WorkflowExecution, error) {
	return nil, nil
}
func (s *stubStore) ListExecutions(context.Context, string, int) ([]WorkflowExecution, error) {
	return nil, nil
}
func (s *stubStore) UpdateExecutionStatus(context.Context, string, string, string) error { return nil }
func (s *stubStore) SetExecutionStarted(context.Context, string) error                   { return nil }
func (s *stubStore) SetExecutionFinished(context.Context, string, string, string) error  { return nil }
func (s *stubStore) UpdateExecutionNode(context.Context, *WorkflowExecutionNode) error    { return nil }
func (s *stubStore) SetExecutionNodeFinished(context.Context, string, string, []Item, string) error {
	return nil
}
func (s *stubStore) CreateCredential(context.Context, *Credential) error         { return nil }
func (s *stubStore) GetCredential(context.Context, string) (*Credential, error)  { return nil, nil }
func (s *stubStore) ListCredentials(context.Context, string) ([]Credential, error) {
	return nil, nil
}
func (s *stubStore) UpdateCredential(context.Context, *Credential) error { return nil }
func (s *stubStore) DeleteCredential(context.Context, string) error      { return nil }
func (s *stubStore) RecoverStaleExecutions(context.Context) error        { return nil }
func (s *stubStore) CancelQueuedExecution(context.Context, string) (bool, error) { return false, nil }
func (s *stubStore) SetExecutionWaiting(_ context.Context, _ string, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waitingState = state
	return nil
}
func (s *stubStore) ListResumableExecutions(context.Context) ([]string, error) { return nil, nil }
func (s *stubStore) ResumeWaitingExecution(context.Context, string) (bool, error) { return true, nil }
func (s *stubStore) PruneExecutions(context.Context, string, int) error  { return nil }
func (s *stubStore) RawDB() *sql.DB                                      { return nil }

// fakeFailNode always fails, to exercise on_error handling.
type fakeFailNode struct{}

func (fakeFailNode) Type() string { return "test.fail" }
func (fakeFailNode) Execute(context.Context, NodeInput, map[string]interface{}) ([]NodeOutput, error) {
	return nil, errTestNodeFail
}

var errTestNodeFail = fmt.Errorf("test node failure")

// TestRunExecution_PartialFailure verifies a node failing under
// on_error=continue makes RunExecution return a *PartialFailureError (which the
// engine maps to SUCCESS_WITH_ERRORS) rather than nil (a misleading success).
func TestRunExecution_PartialFailure(t *testing.T) {
	reg := NewNodeTypeRegistry()
	reg.Register("test.fail", func() NodeExecutor { return fakeFailNode{} })

	wf := &Workflow{
		ID:   "wf-pf",
		Name: "partial",
		Nodes: []WorkflowNode{
			{ID: "t1", WorkflowID: "wf-pf", Type: "trigger.manual", Name: "Trigger"},
			{ID: "n1", WorkflowID: "wf-pf", Type: "test.fail", Name: "Flaky", Config: map[string]interface{}{"on_error": "continue"}},
		},
		Connections: []WorkflowConnection{
			{SourceNodeID: "t1", SourceHandle: "main", TargetNodeID: "n1", TargetHandle: "main"},
		},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	exec := &WorkflowExecution{ID: "e-pf", WorkflowID: "wf-pf", TriggerNodeID: "t1"}
	err = RunExecution(context.Background(), exec, wf, dag, reg, &stubStore{}, nil, NewExpressionEngine(), zerolog.Nop())

	var pf *PartialFailureError
	if !errors.As(err, &pf) {
		t.Fatalf("RunExecution error = %v, want *PartialFailureError", err)
	}
	if len(pf.Nodes) != 1 || pf.Nodes[0] != "Flaky" {
		t.Errorf("PartialFailureError.Nodes = %v, want [Flaky]", pf.Nodes)
	}
}

// countNode counts its executions and passes items through — used to assert a
// node does NOT re-run on resume.
type countNode struct{ count *int }

func (c countNode) Type() string { return "test.count" }
func (c countNode) Execute(_ context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	*c.count++
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// gateNode pauses (ErrNodePaused) until *open is true, then passes items through
// — a stand-in for a Human-in-Loop node awaiting approval.
type gateNode struct{ open *bool }

func (g gateNode) Type() string { return "test.gate" }
func (g gateNode) Execute(_ context.Context, in NodeInput, _ map[string]interface{}) ([]NodeOutput, error) {
	if !*g.open {
		return nil, ErrNodePaused
	}
	return []NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// TestRunExecution_PauseAndResume verifies the HIL restart-resume core: a node
// pausing suspends the run (ErrExecutionPaused) with captured state, and on
// resume the pre-pause node does NOT re-execute while the post-pause node runs.
func TestRunExecution_PauseAndResume(t *testing.T) {
	preCount, postCount := 0, 0
	gateOpen := false

	reg := NewNodeTypeRegistry()
	reg.Register("test.count.pre", func() NodeExecutor { return countNode{&preCount} })
	reg.Register("test.gate", func() NodeExecutor { return gateNode{&gateOpen} })
	reg.Register("test.count.post", func() NodeExecutor { return countNode{&postCount} })

	wf := &Workflow{
		ID:   "wf-pr",
		Name: "pause-resume",
		Nodes: []WorkflowNode{
			{ID: "t", WorkflowID: "wf-pr", Type: "trigger.manual", Name: "T"},
			{ID: "pre", WorkflowID: "wf-pr", Type: "test.count.pre", Name: "Pre"},
			{ID: "gate", WorkflowID: "wf-pr", Type: "test.gate", Name: "Gate"},
			{ID: "post", WorkflowID: "wf-pr", Type: "test.count.post", Name: "Post"},
		},
		Connections: []WorkflowConnection{
			{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "pre", TargetHandle: "main"},
			{SourceNodeID: "pre", SourceHandle: "main", TargetNodeID: "gate", TargetHandle: "main"},
			{SourceNodeID: "gate", SourceHandle: "main", TargetNodeID: "post", TargetHandle: "main"},
		},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	// First run: gate closed → pause.
	store := &stubStore{}
	exec := &WorkflowExecution{ID: "e", WorkflowID: "wf-pr", TriggerNodeID: "t", TriggerData: map[string]interface{}{"x": 1}}
	err = RunExecution(context.Background(), exec, wf, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop())
	if !errors.Is(err, ErrExecutionPaused) {
		t.Fatalf("first run error = %v, want ErrExecutionPaused", err)
	}
	if preCount != 1 {
		t.Fatalf("pre node ran %d times before pause, want 1", preCount)
	}
	if postCount != 0 {
		t.Fatalf("post node ran %d times before resume, want 0", postCount)
	}
	if store.waitingState == "" {
		t.Fatal("no resume state was captured on pause")
	}

	// Resume: gate open, seeded with the captured state.
	gateOpen = true
	exec2 := &WorkflowExecution{ID: "e", WorkflowID: "wf-pr", TriggerData: map[string]interface{}{"x": 1}, ResumeState: store.waitingState}
	if err := RunExecution(context.Background(), exec2, wf, dag, reg, store, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("resume run error: %v", err)
	}
	if preCount != 1 {
		t.Fatalf("pre node re-ran on resume (count=%d) — side effects would double", preCount)
	}
	if postCount != 1 {
		t.Fatalf("post node ran %d times after resume, want 1", postCount)
	}
}

// TestRunExecution_OnlyFiredTriggerReceivesPayload is a regression test for the
// multi-trigger fan-out bug: a single trigger firing must only feed its own
// trigger node, not every trigger node in the workflow.
func TestRunExecution_OnlyFiredTriggerReceivesPayload(t *testing.T) {
	wf := &Workflow{
		ID:   "wf-1",
		Name: "multi-trigger",
		Nodes: []WorkflowNode{
			{ID: "t1", WorkflowID: "wf-1", Type: "trigger.webhook", Name: "Webhook"},
			{ID: "t2", WorkflowID: "wf-1", Type: "trigger.schedule", Name: "Schedule"},
		},
	}
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	inputLen := func(store *stubStore) map[string]int {
		out := map[string]int{}
		for _, n := range store.nodes {
			out[n.NodeID] = len(n.InputItems)
		}
		return out
	}

	// Only t1 fired: t1 gets the payload item, t2 gets none.
	fired := &stubStore{}
	exec := &WorkflowExecution{
		ID:            "ex-1",
		WorkflowID:    "wf-1",
		TriggerNodeID: "t1",
		TriggerData:   map[string]interface{}{"src": "webhook"},
	}
	if err := RunExecution(context.Background(), exec, wf, dag, NewNodeTypeRegistry(), fired, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("RunExecution: %v", err)
	}
	got := inputLen(fired)
	if got["t1"] != 1 {
		t.Errorf("fired trigger t1: got %d input items, want 1", got["t1"])
	}
	if got["t2"] != 0 {
		t.Errorf("non-fired trigger t2: got %d input items, want 0", got["t2"])
	}

	// Empty TriggerNodeID (manual/retry): legacy fan-out — every trigger fires.
	all := &stubStore{}
	execAll := &WorkflowExecution{
		ID:          "ex-2",
		WorkflowID:  "wf-1",
		TriggerData: map[string]interface{}{"src": "manual"},
	}
	if err := RunExecution(context.Background(), execAll, wf, dag, NewNodeTypeRegistry(), all, nil, NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("RunExecution (manual): %v", err)
	}
	got = inputLen(all)
	if got["t1"] != 1 || got["t2"] != 1 {
		t.Errorf("manual run: both triggers should fire, got t1=%d t2=%d", got["t1"], got["t2"])
	}
}

func TestParsePerItemFieldSpec(t *testing.T) {
	cases := []struct {
		spec         string
		wantArrayKey string
		wantKey      string
	}{
		{"condition", "", "condition"},
		{"assignments[].value", "assignments", "value"},
	}
	for _, c := range cases {
		arrayKey, key := parsePerItemFieldSpec(c.spec)
		if arrayKey != c.wantArrayKey || key != c.wantKey {
			t.Errorf("parsePerItemFieldSpec(%q) = (%q, %q), want (%q, %q)", c.spec, arrayKey, key, c.wantArrayKey, c.wantKey)
		}
	}
}

// TestExtractRestorePerItemFields_TopLevel verifies a top-level field spec
// (e.g. "condition") survives a real ResolveConfig pass unresolved, while an
// unrelated field in the same config is resolved normally.
func TestExtractRestorePerItemFields_TopLevel(t *testing.T) {
	config := map[string]interface{}{
		"condition": "{{$json.status}}",
		"other":     "{{$json.name}}",
	}
	state := extractPerItemFields(config, []string{"condition"})

	if _, ok := config["condition"]; ok {
		t.Fatal("condition should have been removed from config before resolution")
	}
	if _, ok := config["other"]; !ok {
		t.Fatal("other should remain in config for normal resolution")
	}

	engine := NewExpressionEngine()
	resolvedConfig, err := engine.ResolveConfig(config, ExpressionContext{JSON: map[string]interface{}{"status": "open", "name": "alice"}})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	restorePerItemFields(resolvedConfig, state)

	if resolvedConfig["condition"] != "{{$json.status}}" {
		t.Errorf("condition = %v, want raw template preserved", resolvedConfig["condition"])
	}
	if resolvedConfig["other"] != "alice" {
		t.Errorf("other = %v, want resolved to \"alice\"", resolvedConfig["other"])
	}
}

// TestExtractRestorePerItemFields_NestedArray verifies an "arrayKey[].subKey"
// spec (core.set's "assignments[].value") protects only that sub-key of
// every element, while sibling keys in each element still resolve normally
// and element order/count survive the round trip.
func TestExtractRestorePerItemFields_NestedArray(t *testing.T) {
	config := map[string]interface{}{
		"assignments": []interface{}{
			map[string]interface{}{"field": "a", "value": "{{$json.x}}"},
			map[string]interface{}{"field": "b", "value": "{{$json.y}}"},
		},
	}
	state := extractPerItemFields(config, []string{"assignments[].value"})

	for i, raw := range config["assignments"].([]interface{}) {
		elem := raw.(map[string]interface{})
		if _, ok := elem["value"]; ok {
			t.Fatalf("assignments[%d].value should have been removed before resolution", i)
		}
		if _, ok := elem["field"]; !ok {
			t.Fatalf("assignments[%d].field should remain for normal resolution", i)
		}
	}

	engine := NewExpressionEngine()
	resolvedConfig, err := engine.ResolveConfig(config, ExpressionContext{JSON: map[string]interface{}{"x": 1, "y": 2}})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	restorePerItemFields(resolvedConfig, state)

	resolvedAssignments, ok := resolvedConfig["assignments"].([]interface{})
	if !ok || len(resolvedAssignments) != 2 {
		t.Fatalf("resolved assignments = %#v, want a 2-element slice", resolvedConfig["assignments"])
	}
	elem0 := resolvedAssignments[0].(map[string]interface{})
	if elem0["field"] != "a" || elem0["value"] != "{{$json.x}}" {
		t.Errorf("assignments[0] = %+v, want field=a value={{$json.x}} (raw)", elem0)
	}
	elem1 := resolvedAssignments[1].(map[string]interface{})
	if elem1["field"] != "b" || elem1["value"] != "{{$json.y}}" {
		t.Errorf("assignments[1] = %+v, want field=b value={{$json.y}} (raw)", elem1)
	}
}

// TestExtractPerItemFields_AbsentFieldsAreNoop verifies a spec naming a field
// that isn't present in config (e.g. a node registered but not using one of
// its optional per-item fields) is silently skipped rather than erroring or
// fabricating an entry.
func TestExtractPerItemFields_AbsentFieldsAreNoop(t *testing.T) {
	config := map[string]interface{}{"other": "x"}
	state := extractPerItemFields(config, []string{"condition", "assignments[].value"})
	if len(state.topLevel) != 0 || len(state.nested) != 0 {
		t.Fatalf("extractPerItemFields state = %+v, want empty (no matching fields present)", state)
	}
	if len(config) != 1 {
		t.Fatalf("config = %v, want untouched", config)
	}
}

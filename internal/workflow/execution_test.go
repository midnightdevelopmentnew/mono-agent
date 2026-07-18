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
	mu    sync.Mutex
	nodes []*WorkflowExecutionNode
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

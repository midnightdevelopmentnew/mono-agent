package workflow_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"monoagent/internal/nodes/control"
	httpnodes "monoagent/internal/nodes/http"
	"monoagent/internal/workflow"
)

// These are regression tests for the per-item config resolution bug: before
// PerItemConfigResolver, RunExecution resolved a node's whole config once,
// using only the first input item's JSON, so every item in a batch silently
// got item[0]'s resolved values. A node-level unit test can't reproduce
// this — the bug lived in the interaction between RunExecution and the node,
// not in either alone — so these drive the real core.set and http.request
// node implementations through the real RunExecution path with 2+ items and
// assert the two items produce DIFFERENT results. A weaker "N items in, N
// items out" assertion would also pass against the unfixed code.
//
// This file is package workflow_test (not workflow) specifically so it can
// import the real node packages (internal/nodes/control, internal/nodes/http)
// without an import cycle — those packages import internal/workflow.

// fakeStore is a minimal no-op workflow.WorkflowStore, just enough for
// RunExecution to run without a real DB.
type fakeStore struct{}

func (fakeStore) CreateWorkflow(context.Context, *workflow.Workflow) error        { return nil }
func (fakeStore) GetWorkflow(context.Context, string) (*workflow.Workflow, error) { return nil, nil }
func (fakeStore) ListWorkflows(context.Context, string) ([]workflow.Workflow, error) {
	return nil, nil
}
func (fakeStore) UpdateWorkflow(context.Context, *workflow.Workflow) error { return nil }
func (fakeStore) DeleteWorkflow(context.Context, string) error             { return nil }
func (fakeStore) SetWorkflowActive(context.Context, string, bool) error    { return nil }
func (fakeStore) SaveWorkflowNodes(context.Context, string, []workflow.WorkflowNode) error {
	return nil
}
func (fakeStore) SaveWorkflowConnections(context.Context, string, []workflow.WorkflowConnection) error {
	return nil
}
func (fakeStore) CreateExecution(context.Context, *workflow.WorkflowExecution) error { return nil }
func (fakeStore) GetExecution(context.Context, string) (*workflow.WorkflowExecution, error) {
	return nil, nil
}
func (fakeStore) ListExecutions(context.Context, string, int) ([]workflow.WorkflowExecution, error) {
	return nil, nil
}
func (fakeStore) UpdateExecutionStatus(context.Context, string, string, string) error { return nil }
func (fakeStore) SetExecutionStarted(context.Context, string) error                   { return nil }
func (fakeStore) SetExecutionFinished(context.Context, string, string, string) error  { return nil }
func (fakeStore) UpdateExecutionNode(context.Context, *workflow.WorkflowExecutionNode) error {
	return nil
}
func (fakeStore) CreateExecutionNode(context.Context, *workflow.WorkflowExecutionNode) error {
	return nil
}
func (fakeStore) SetExecutionNodeFinished(context.Context, string, string, []workflow.Item, string) error {
	return nil
}
func (fakeStore) CreateCredential(context.Context, *workflow.Credential) error { return nil }
func (fakeStore) GetCredential(context.Context, string) (*workflow.Credential, error) {
	return nil, nil
}
func (fakeStore) ListCredentials(context.Context, string) ([]workflow.Credential, error) {
	return nil, nil
}
func (fakeStore) UpdateCredential(context.Context, *workflow.Credential) error { return nil }
func (fakeStore) DeleteCredential(context.Context, string) error               { return nil }
func (fakeStore) RecoverStaleExecutions(context.Context) error                 { return nil }
func (fakeStore) CancelQueuedExecution(context.Context, string) (bool, error)  { return false, nil }
func (fakeStore) SetExecutionWaiting(context.Context, string, string) error    { return nil }
func (fakeStore) ListResumableExecutions(context.Context) ([]string, error)    { return nil, nil }
func (fakeStore) ResumeWaitingExecution(context.Context, string) (bool, error) { return true, nil }
func (fakeStore) PruneExecutions(context.Context, string, int) error           { return nil }
func (fakeStore) RawDB() *sql.DB                                               { return nil }

// fanOutItemsNode ignores its input and always emits the same fixed items —
// a stand-in for any upstream node that produces more than one item (e.g. a
// list/query node). RunExecution's trigger mechanism only ever seeds a
// single item, so this is how these tests get 2+ items flowing into the
// node under test.
type fanOutItemsNode struct{ items []workflow.Item }

func (f fanOutItemsNode) Type() string { return "test.fanout" }
func (f fanOutItemsNode) Execute(context.Context, workflow.NodeInput, map[string]interface{}) ([]workflow.NodeOutput, error) {
	return []workflow.NodeOutput{{Handle: "main", Items: f.items}}, nil
}

// captureNode records the items it receives and passes them through unchanged.
type captureNode struct{ got *[]workflow.Item }

func (c captureNode) Type() string { return "test.capture" }
func (c captureNode) Execute(_ context.Context, in workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
	*c.got = in.Items
	return []workflow.NodeOutput{{Handle: "main", Items: in.Items}}, nil
}

// TestRunExecution_SetNode_PerItemValueResolution is the core.set half of
// the per-item config resolution regression: two items with different $json
// values must produce different assigned values, not both collapse to
// item[0]'s.
func TestRunExecution_SetNode_PerItemValueResolution(t *testing.T) {
	reg := workflow.NewNodeTypeRegistry()
	reg.Register("test.fanout", func() workflow.NodeExecutor {
		return fanOutItemsNode{items: []workflow.Item{
			{JSON: map[string]interface{}{"n": "one"}},
			{JSON: map[string]interface{}{"n": "two"}},
		}}
	})
	reg.Register("core.set", func() workflow.NodeExecutor { return &control.SetNode{} })
	var captured []workflow.Item
	reg.Register("test.capture", func() workflow.NodeExecutor { return captureNode{got: &captured} })

	wf := &workflow.Workflow{
		ID:   "wf-set-peritem",
		Name: "set-per-item",
		Nodes: []workflow.WorkflowNode{
			{ID: "t", WorkflowID: "wf-set-peritem", Type: "trigger.manual", Name: "Trigger"},
			{ID: "f", WorkflowID: "wf-set-peritem", Type: "test.fanout", Name: "Fanout"},
			{ID: "s", WorkflowID: "wf-set-peritem", Type: "core.set", Name: "Set", Config: map[string]interface{}{
				"assignments": []interface{}{
					map[string]interface{}{"field": "out", "value": "{{$json.n}}", "type": "string"},
				},
			}},
			{ID: "c", WorkflowID: "wf-set-peritem", Type: "test.capture", Name: "Capture"},
		},
		Connections: []workflow.WorkflowConnection{
			{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "f", TargetHandle: "main"},
			{SourceNodeID: "f", SourceHandle: "main", TargetNodeID: "s", TargetHandle: "main"},
			{SourceNodeID: "s", SourceHandle: "main", TargetNodeID: "c", TargetHandle: "main"},
		},
	}
	dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	exec := &workflow.WorkflowExecution{ID: "e-set-peritem", WorkflowID: wf.ID, TriggerNodeID: "t"}
	if err := workflow.RunExecution(context.Background(), exec, wf, dag, reg, fakeStore{}, nil, workflow.NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("RunExecution: %v", err)
	}

	if len(captured) != 2 {
		t.Fatalf("captured %d items, want 2", len(captured))
	}
	got0, _ := captured[0].JSON["out"].(string)
	got1, _ := captured[1].JSON["out"].(string)
	if got0 != "one" {
		t.Errorf(`item[0].out = %q, want "one"`, got0)
	}
	if got1 != "two" {
		t.Errorf(`item[1].out = %q, want "two"`, got1)
	}
	if got0 == got1 {
		t.Fatalf("item[0].out == item[1].out == %q — per-item resolution collapsed to a single item's value (the bug this test guards against)", got0)
	}
}

// TestRunExecution_RequestNode_PerItemURLResolution is the http.request half
// of the per-item config resolution regression: two items with different
// $json values must produce two requests to different URLs, not two
// identical requests both built from item[0]'s data.
func TestRunExecution_RequestNode_PerItemURLResolution(t *testing.T) {
	var mu sync.Mutex
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPaths = append(gotPaths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	reg := workflow.NewNodeTypeRegistry()
	reg.Register("test.fanout", func() workflow.NodeExecutor {
		return fanOutItemsNode{items: []workflow.Item{
			{JSON: map[string]interface{}{"id": "1"}},
			{JSON: map[string]interface{}{"id": "2"}},
		}}
	})
	reg.Register("http.request", func() workflow.NodeExecutor { return &httpnodes.RequestNode{} })

	wf := &workflow.Workflow{
		ID:   "wf-req-peritem",
		Name: "request-per-item",
		Nodes: []workflow.WorkflowNode{
			{ID: "t", WorkflowID: "wf-req-peritem", Type: "trigger.manual", Name: "Trigger"},
			{ID: "f", WorkflowID: "wf-req-peritem", Type: "test.fanout", Name: "Fanout"},
			{ID: "r", WorkflowID: "wf-req-peritem", Type: "http.request", Name: "Request", Config: map[string]interface{}{
				"method": "GET",
				"url":    server.URL + "/items/{{$json.id}}",
			}},
		},
		Connections: []workflow.WorkflowConnection{
			{SourceNodeID: "t", SourceHandle: "main", TargetNodeID: "f", TargetHandle: "main"},
			{SourceNodeID: "f", SourceHandle: "main", TargetNodeID: "r", TargetHandle: "main"},
		},
	}
	dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	exec := &workflow.WorkflowExecution{ID: "e-req-peritem", WorkflowID: wf.ID, TriggerNodeID: "t"}
	if err := workflow.RunExecution(context.Background(), exec, wf, dag, reg, fakeStore{}, nil, workflow.NewExpressionEngine(), zerolog.Nop()); err != nil {
		t.Fatalf("RunExecution: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotPaths) != 2 {
		t.Fatalf("server received %d requests, want 2 (got %v)", len(gotPaths), gotPaths)
	}
	if gotPaths[0] != "/items/1" {
		t.Errorf("request[0] path = %q, want %q", gotPaths[0], "/items/1")
	}
	if gotPaths[1] != "/items/2" {
		t.Errorf("request[1] path = %q, want %q", gotPaths[1], "/items/2")
	}
	if gotPaths[0] == gotPaths[1] {
		t.Fatalf("both requests hit path %q — per-item URL resolution collapsed to a single item's value (the bug this test guards against)", gotPaths[0])
	}
}

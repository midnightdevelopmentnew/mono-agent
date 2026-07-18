package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"

	"monoagent/internal/vault"
	"monoagent/internal/workflow"
)

func newHILTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE hil_pending (
		id TEXT PRIMARY KEY, execution_id TEXT NOT NULL, workflow_id TEXT NOT NULL,
		node_id TEXT NOT NULL, node_name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
		readonly_data TEXT NOT NULL DEFAULT '{}', editable_data TEXT NOT NULL DEFAULT '{}',
		edited_data TEXT NOT NULL DEFAULT '{}', node_config TEXT NOT NULL DEFAULT '{}',
		profile_id TEXT NOT NULL DEFAULT 'default',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func hilInput() workflow.NodeInput {
	return workflow.NodeInput{
		ExecutionID: "e1", WorkflowID: "w1", NodeID: "n1", NodeName: "Review",
		Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"caption": "original"})},
	}
}

func TestHIL_PausesThenResumesOnApproval(t *testing.T) {
	db := newHILTestDB(t)
	ctx := vault.ContextWithDB(context.Background(), db)
	n := &HumanInLoopNode{}

	// First run: creates a pending row and pauses.
	_, err := n.Execute(ctx, hilInput(), map[string]interface{}{})
	if !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("first run error = %v, want ErrNodePaused", err)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM hil_pending WHERE execution_id='e1' AND node_id='n1'`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 pending row, got %d", count)
	}

	// Running again while still pending must NOT create a duplicate row.
	if _, err := n.Execute(ctx, hilInput(), map[string]interface{}{}); !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("second run (still pending) = %v, want ErrNodePaused", err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM hil_pending WHERE execution_id='e1' AND node_id='n1'`).Scan(&count)
	if count != 1 {
		t.Fatalf("duplicate rows created on re-run: got %d, want 1", count)
	}

	// Approve with edited data.
	if _, err := db.Exec(`UPDATE hil_pending SET status='approved', edited_data='{"caption":"edited"}' WHERE execution_id='e1'`); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Resume run: returns the item with edited_data applied.
	out, err := n.Execute(ctx, hilInput(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("resume run error: %v", err)
	}
	if len(out) != 1 || len(out[0].Items) != 1 {
		t.Fatalf("unexpected output shape: %+v", out)
	}
	if got := out[0].Items[0].JSON["caption"]; got != "edited" {
		t.Fatalf("edited_data not applied: caption=%v, want edited", got)
	}
}

func TestHIL_RejectReturnsError(t *testing.T) {
	db := newHILTestDB(t)
	ctx := vault.ContextWithDB(context.Background(), db)
	n := &HumanInLoopNode{}

	if _, err := n.Execute(ctx, hilInput(), map[string]interface{}{}); !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("first run = %v, want ErrNodePaused", err)
	}
	if _, err := db.Exec(`UPDATE hil_pending SET status='rejected' WHERE execution_id='e1'`); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if _, err := n.Execute(ctx, hilInput(), map[string]interface{}{}); err == nil || errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("expected a rejection error, got %v", err)
	}
}

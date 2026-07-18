package workflow

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newExecStore(t *testing.T) *SQLiteWorkflowStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE workflow_executions (
		id TEXT PRIMARY KEY, workflow_id TEXT, status TEXT, trigger_type TEXT,
		trigger_data TEXT DEFAULT '{}', started_at TIMESTAMP, finished_at TIMESTAMP,
		error_message TEXT DEFAULT '', created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		pid INTEGER, resume_state TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE hil_pending (id TEXT PRIMARY KEY, execution_id TEXT, status TEXT DEFAULT 'pending')`); err != nil {
		t.Fatalf("create hil table: %v", err)
	}
	return NewSQLiteWorkflowStore(db)
}

func insertExec(t *testing.T, s *SQLiteWorkflowStore, id, status, resume string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, resume_state) VALUES (?,?,?,?)`,
		id, "wf", status, resume); err != nil {
		t.Fatalf("insert exec: %v", err)
	}
}

func statusOf(t *testing.T, s *SQLiteWorkflowStore, id string) string {
	t.Helper()
	var st string
	if err := s.db.QueryRow(`SELECT status FROM workflow_executions WHERE id=?`, id).Scan(&st); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return st
}

// TestCancelQueuedExecution_CoversWaiting is the regression guard for: a paused
// (WAITING) execution must be cancellable (previously a silent no-op).
func TestCancelQueuedExecution_CoversWaiting(t *testing.T) {
	s := newExecStore(t)
	ctx := context.Background()
	insertExec(t, s, "w", "WAITING", `{"pendingInputs":{}}`)
	insertExec(t, s, "q", "QUEUED", "")
	insertExec(t, s, "r", "RUNNING", "")

	for _, id := range []string{"w", "q"} {
		ok, err := s.CancelQueuedExecution(ctx, id)
		if err != nil || !ok {
			t.Fatalf("CancelQueuedExecution(%s) = %v,%v; want true,nil", id, ok, err)
		}
		if got := statusOf(t, s, id); got != "CANCELLED" {
			t.Errorf("exec %s status = %s, want CANCELLED", id, got)
		}
	}
	// resume_state of the cancelled WAITING row is cleared so it can't resume.
	var rs string
	_ = s.db.QueryRow(`SELECT resume_state FROM workflow_executions WHERE id='w'`).Scan(&rs)
	if rs != "" {
		t.Errorf("cancelled WAITING resume_state = %q, want empty", rs)
	}
	// RUNNING is not cancellable via this status flip.
	if ok, _ := s.CancelQueuedExecution(ctx, "r"); ok {
		t.Error("CancelQueuedExecution should not flip a RUNNING execution")
	}
}

// TestResumeWaitingExecution_CASExclusive verifies the atomic flip: only the
// first caller wins (guards against double-resume under concurrent engines).
func TestResumeWaitingExecution_CASExclusive(t *testing.T) {
	s := newExecStore(t)
	ctx := context.Background()
	insertExec(t, s, "e", "WAITING", "{}")

	first, err := s.ResumeWaitingExecution(ctx, "e")
	if err != nil || !first {
		t.Fatalf("first ResumeWaitingExecution = %v,%v; want true,nil", first, err)
	}
	if got := statusOf(t, s, "e"); got != "QUEUED" {
		t.Fatalf("status = %s, want QUEUED", got)
	}
	// A second attempt (another engine) must lose the CAS.
	second, err := s.ResumeWaitingExecution(ctx, "e")
	if err != nil {
		t.Fatalf("second ResumeWaitingExecution err: %v", err)
	}
	if second {
		t.Error("second ResumeWaitingExecution won the CAS; want false (already resumed)")
	}
}

// TestListResumableExecutions_OnlyWaiting confirms cancelled/finished rows are
// never resumed.
func TestListResumableExecutions_OnlyWaiting(t *testing.T) {
	s := newExecStore(t)
	ctx := context.Background()
	insertExec(t, s, "wait", "WAITING", "{}")
	insertExec(t, s, "canc", "CANCELLED", "")
	insertExec(t, s, "done", "SUCCESS", "")

	ids, err := s.ListResumableExecutions(ctx)
	if err != nil {
		t.Fatalf("ListResumableExecutions: %v", err)
	}
	if len(ids) != 1 || ids[0] != "wait" {
		t.Fatalf("resumable = %v, want [wait]", ids)
	}
}

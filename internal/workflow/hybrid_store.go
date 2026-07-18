package workflow

import (
	"context"
	"database/sql"
)

// HybridWorkflowStore delegates workflow-definition operations to a
// WorkflowFileStore (JSON files) and all execution/credential operations to a
// SQLiteWorkflowStore.
//
// If the file store is nil, all workflow CRUD falls back to SQLite only.
// This is the canonical store — used by both the CLI and the Wails GUI.
type HybridWorkflowStore struct {
	files *WorkflowFileStore
	sql   *SQLiteWorkflowStore
}

// NewHybridWorkflowStore creates a HybridWorkflowStore.
// files may be nil, in which case all workflow CRUD uses SQLite only.
func NewHybridWorkflowStore(files *WorkflowFileStore, sql *SQLiteWorkflowStore) *HybridWorkflowStore {
	return &HybridWorkflowStore{files: files, sql: sql}
}

// ---------------------------------------------------------------------------
// Workflow CRUD — file store preferred, SQLite fallback
// ---------------------------------------------------------------------------

func (h *HybridWorkflowStore) CreateWorkflow(ctx context.Context, w *Workflow) error {
	if h.files != nil {
		return h.files.SaveWorkflow(ctx, w)
	}
	return h.sql.CreateWorkflow(ctx, w)
}

func (h *HybridWorkflowStore) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	// Try file store first (Wails-created workflows live here).
	if h.files != nil {
		wf, err := h.files.GetWorkflow(ctx, id)
		if err != nil {
			return nil, err
		}
		if wf != nil {
			return wf, nil
		}
	}
	// Fall back to SQLite (imported/legacy workflows).
	return h.sql.GetWorkflow(ctx, id)
}

func (h *HybridWorkflowStore) ListWorkflows(ctx context.Context, profileID string) ([]Workflow, error) {
	seen := make(map[string]bool)
	var result []Workflow

	// Collect file-store workflows first (file store has no profile concept).
	if h.files != nil {
		filePtrs, err := h.files.ListWorkflows(ctx)
		if err != nil {
			return nil, err
		}
		for _, wf := range filePtrs {
			seen[wf.ID] = true
			result = append(result, *wf)
		}
	}

	// Append SQLite workflows not already present in the file store.
	sqlWFs, err := h.sql.ListWorkflows(ctx, profileID)
	if err != nil {
		return nil, err
	}
	for _, wf := range sqlWFs {
		if !seen[wf.ID] {
			result = append(result, wf)
		}
	}
	return result, nil
}

func (h *HybridWorkflowStore) UpdateWorkflow(ctx context.Context, w *Workflow) error {
	if h.files != nil {
		return h.files.SaveWorkflow(ctx, w)
	}
	return h.sql.UpdateWorkflow(ctx, w)
}

// SaveWorkflow writes a workflow to the file store (create or update).
func (h *HybridWorkflowStore) SaveWorkflow(ctx context.Context, w *Workflow) error {
	if h.files != nil {
		return h.files.SaveWorkflow(ctx, w)
	}
	return h.sql.UpdateWorkflow(ctx, w)
}

func (h *HybridWorkflowStore) DeleteWorkflow(ctx context.Context, id string) error {
	if h.files != nil {
		_ = h.files.DeleteWorkflow(ctx, id) // ignore not-found
	}
	_ = h.sql.DeleteWorkflow(ctx, id) // ignore not-found
	return nil
}

func (h *HybridWorkflowStore) SetWorkflowActive(ctx context.Context, id string, active bool) error {
	// Try to update in file store.
	if h.files != nil {
		wf, err := h.files.GetWorkflow(ctx, id)
		if err == nil && wf != nil {
			wf.IsActive = active
			return h.files.SaveWorkflow(ctx, wf)
		}
	}
	return h.sql.SetWorkflowActive(ctx, id, active)
}

// ---------------------------------------------------------------------------
// Node / Connection save — SQLite only (executions need them there)
// ---------------------------------------------------------------------------

// ensureSQLWorkflow guarantees a minimal metadata row exists in SQLite for
// workflowID, mirroring it from the file store if necessary. Nodes,
// connections, and executions all have hard FK references to workflows(id)
// in SQLite even when the workflow's canonical definition lives only in the
// file store (e.g. created via the CLI's `workflow create`).
func (h *HybridWorkflowStore) ensureSQLWorkflow(ctx context.Context, workflowID string) {
	existing, err := h.sql.GetWorkflow(ctx, workflowID)
	if err != nil || existing != nil {
		return
	}
	if h.files == nil {
		return
	}
	wf, ferr := h.files.GetWorkflow(ctx, workflowID)
	if ferr != nil || wf == nil {
		return
	}
	// Insert a minimal stub row — only metadata, no nodes/connections.
	stub := *wf
	stub.Nodes = nil
	stub.Connections = nil
	_ = h.sql.CreateWorkflow(ctx, &stub)
}

// syncFileNodes mirrors the current SQL-stored nodes/connections for
// workflowID back onto the file-store copy. GetWorkflow prefers the file
// store, so without this, nodes/connections written via SaveWorkflowNodes/
// SaveWorkflowConnections would be invisible to every read path.
func (h *HybridWorkflowStore) syncFileNodes(ctx context.Context, workflowID string) {
	if h.files == nil {
		return
	}
	wf, err := h.files.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		return
	}
	sqlWF, err := h.sql.GetWorkflow(ctx, workflowID)
	if err != nil || sqlWF == nil {
		return
	}
	wf.Nodes = sqlWF.Nodes
	wf.Connections = sqlWF.Connections
	_ = h.files.SaveWorkflow(ctx, wf)
}

func (h *HybridWorkflowStore) SaveWorkflowNodes(ctx context.Context, workflowID string, nodes []WorkflowNode) error {
	h.ensureSQLWorkflow(ctx, workflowID)
	if err := h.sql.SaveWorkflowNodes(ctx, workflowID, nodes); err != nil {
		return err
	}
	h.syncFileNodes(ctx, workflowID)
	return nil
}

func (h *HybridWorkflowStore) SaveWorkflowConnections(ctx context.Context, workflowID string, conns []WorkflowConnection) error {
	h.ensureSQLWorkflow(ctx, workflowID)
	if err := h.sql.SaveWorkflowConnections(ctx, workflowID, conns); err != nil {
		return err
	}
	h.syncFileNodes(ctx, workflowID)
	return nil
}

// ---------------------------------------------------------------------------
// Executions — always SQLite
// ---------------------------------------------------------------------------

func (h *HybridWorkflowStore) CreateExecution(ctx context.Context, e *WorkflowExecution) error {
	h.ensureSQLWorkflow(ctx, e.WorkflowID)
	return h.sql.CreateExecution(ctx, e)
}

func (h *HybridWorkflowStore) GetExecution(ctx context.Context, id string) (*WorkflowExecution, error) {
	return h.sql.GetExecution(ctx, id)
}

func (h *HybridWorkflowStore) ListExecutions(ctx context.Context, workflowID string, limit int) ([]WorkflowExecution, error) {
	return h.sql.ListExecutions(ctx, workflowID, limit)
}

func (h *HybridWorkflowStore) UpdateExecutionStatus(ctx context.Context, id string, status string, errMsg string) error {
	return h.sql.UpdateExecutionStatus(ctx, id, status, errMsg)
}

func (h *HybridWorkflowStore) SetExecutionStarted(ctx context.Context, id string) error {
	return h.sql.SetExecutionStarted(ctx, id)
}

func (h *HybridWorkflowStore) SetExecutionFinished(ctx context.Context, id string, status string, errMsg string) error {
	return h.sql.SetExecutionFinished(ctx, id, status, errMsg)
}

func (h *HybridWorkflowStore) CreateExecutionNode(ctx context.Context, en *WorkflowExecutionNode) error {
	return h.sql.CreateExecutionNode(ctx, en)
}

func (h *HybridWorkflowStore) UpdateExecutionNode(ctx context.Context, en *WorkflowExecutionNode) error {
	return h.sql.UpdateExecutionNode(ctx, en)
}

func (h *HybridWorkflowStore) SetExecutionNodeFinished(ctx context.Context, id string, status string, outputItems []Item, errMsg string) error {
	return h.sql.SetExecutionNodeFinished(ctx, id, status, outputItems, errMsg)
}

// ---------------------------------------------------------------------------
// Credentials — always SQLite
// ---------------------------------------------------------------------------

func (h *HybridWorkflowStore) CreateCredential(ctx context.Context, c *Credential) error {
	return h.sql.CreateCredential(ctx, c)
}

func (h *HybridWorkflowStore) GetCredential(ctx context.Context, id string) (*Credential, error) {
	return h.sql.GetCredential(ctx, id)
}

func (h *HybridWorkflowStore) ListCredentials(ctx context.Context, credType string) ([]Credential, error) {
	return h.sql.ListCredentials(ctx, credType)
}

func (h *HybridWorkflowStore) UpdateCredential(ctx context.Context, c *Credential) error {
	return h.sql.UpdateCredential(ctx, c)
}

func (h *HybridWorkflowStore) DeleteCredential(ctx context.Context, id string) error {
	return h.sql.DeleteCredential(ctx, id)
}

// ---------------------------------------------------------------------------
// Maintenance — SQLite
// ---------------------------------------------------------------------------

func (h *HybridWorkflowStore) RecoverStaleExecutions(ctx context.Context) error {
	return h.sql.RecoverStaleExecutions(ctx)
}

func (h *HybridWorkflowStore) CancelQueuedExecution(ctx context.Context, id string) (bool, error) {
	return h.sql.CancelQueuedExecution(ctx, id)
}

func (h *HybridWorkflowStore) SetExecutionWaiting(ctx context.Context, id string, resumeState string) error {
	return h.sql.SetExecutionWaiting(ctx, id, resumeState)
}

func (h *HybridWorkflowStore) ListResumableExecutions(ctx context.Context) ([]string, error) {
	return h.sql.ListResumableExecutions(ctx)
}

func (h *HybridWorkflowStore) PruneExecutions(ctx context.Context, workflowID string, keepCount int) error {
	return h.sql.PruneExecutions(ctx, workflowID, keepCount)
}

// RawDB delegates to the SQLite store's underlying DB.
func (h *HybridWorkflowStore) RawDB() *sql.DB { return h.sql.RawDB() }

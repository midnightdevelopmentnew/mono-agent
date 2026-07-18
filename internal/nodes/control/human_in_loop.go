package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"monoagent/internal/vault"
	"monoagent/internal/workflow"
)

// HumanInLoopNode pauses workflow execution and waits for a human to review
// and optionally edit the incoming data before approving or rejecting it.
//
// Config fields:
//
//	"readonly_fields"  ([]string): item keys shown in the read-only info section
//	"editable_fields"  ([]string): item keys shown in the editable section
//	"timeout_minutes"  (float64, optional): max wait time, default 0 = unlimited
type HumanInLoopNode struct{}

func (n *HumanInLoopNode) Type() string { return "core.human_in_loop" }

func (n *HumanInLoopNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	db := vault.DBFromContext(ctx)
	if db == nil {
		return nil, fmt.Errorf("human_in_loop: database not available in context")
	}

	profileID := vault.ProfileIDFromContext(ctx)
	if profileID == "" {
		profileID = "default"
	}

	// Optional timeout: pending items older than this are auto-rejected.
	timeoutMinutes := 0.0
	if v, ok := config["timeout_minutes"]; ok {
		switch t := v.(type) {
		case float64:
			timeoutMinutes = t
		case int:
			timeoutMinutes = float64(t)
		}
	}

	// Non-blocking: on first encounter, create one pending row per input item
	// and pause the execution (ErrNodePaused) — the engine persists resume state
	// and suspends without holding a goroutine. On resume, this node re-runs and
	// evaluates the (now possibly resolved) rows. Idempotent by (execution,
	// node): duplicate rows are never created.
	existing, err := loadHILRows(ctx, db, input.ExecutionID, input.NodeID)
	if err != nil {
		return nil, err
	}

	if len(existing) == 0 {
		if err := n.createRows(ctx, db, input, config, profileID); err != nil {
			return nil, err
		}
		return nil, workflow.ErrNodePaused
	}

	// Expire stale pending rows if a timeout is configured, then re-read.
	if timeoutMinutes > 0 {
		_, _ = db.ExecContext(ctx,
			`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP
			 WHERE execution_id=? AND node_id=? AND status='pending' AND created_at < datetime('now', ?)`,
			input.ExecutionID, input.NodeID, fmt.Sprintf("-%d minutes", int(timeoutMinutes)))
		if existing, err = loadHILRows(ctx, db, input.ExecutionID, input.NodeID); err != nil {
			return nil, err
		}
	}

	anyPending, anyRejected := false, false
	for _, r := range existing {
		switch r.status {
		case "pending":
			anyPending = true
		case "rejected":
			anyRejected = true
		}
	}
	if anyRejected {
		return nil, fmt.Errorf("human_in_loop: item rejected by human reviewer")
	}
	if anyPending {
		return nil, workflow.ErrNodePaused // still awaiting approval
	}

	// All approved — emit each input item with its row's edited_data applied.
	// Rows are ordered by insertion, matching the input item order.
	var approvedItems []workflow.Item
	for i, item := range input.Items {
		out := copyMap(item.JSON)
		if i < len(existing) {
			for k, v := range existing[i].edited {
				out[k] = v
			}
		}
		approvedItems = append(approvedItems, workflow.NewItem(out))
	}
	return []workflow.NodeOutput{{Handle: "main", Items: approvedItems}}, nil
}

type hilRow struct {
	status string
	edited map[string]interface{}
}

// loadHILRows returns the HIL rows for one (execution, node) in insertion order.
func loadHILRows(ctx context.Context, db *sql.DB, executionID, nodeID string) ([]hilRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT status, edited_data FROM hil_pending WHERE execution_id=? AND node_id=? ORDER BY rowid`,
		executionID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("human_in_loop: load rows: %w", err)
	}
	defer rows.Close()
	var out []hilRow
	for rows.Next() {
		var status, editedRaw string
		if err := rows.Scan(&status, &editedRaw); err != nil {
			return nil, fmt.Errorf("human_in_loop: scan row: %w", err)
		}
		r := hilRow{status: status}
		if editedRaw != "" && editedRaw != "{}" {
			_ = json.Unmarshal([]byte(editedRaw), &r.edited)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// createRows inserts one pending HIL row per input item.
func (n *HumanInLoopNode) createRows(ctx context.Context, db *sql.DB, input workflow.NodeInput, config map[string]interface{}, profileID string) error {
	readonlyFields := stringsFromConfig(config, "readonly_fields")
	editableFields := stringsFromConfig(config, "editable_fields")
	configJSON, _ := json.Marshal(map[string]interface{}{
		"readonly_fields": readonlyFields,
		"editable_fields": editableFields,
	})
	for _, item := range input.Items {
		roData := extractFields(item.JSON, readonlyFields)
		edData := extractFields(item.JSON, editableFields)
		if len(readonlyFields) == 0 && len(editableFields) == 0 {
			edData = copyMap(item.JSON)
		}
		roJSON, _ := json.Marshal(roData)
		edJSON, _ := json.Marshal(edData)
		if _, err := db.ExecContext(ctx,
			`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, edited_data, node_config, profile_id)
			 VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, '{}', ?, ?)`,
			uuid.New().String(), input.ExecutionID, input.WorkflowID, input.NodeID, input.NodeName,
			string(roJSON), string(edJSON), string(configJSON), profileID,
		); err != nil {
			return fmt.Errorf("human_in_loop: insert pending record: %w", err)
		}
	}
	return nil
}

func stringsFromConfig(config map[string]interface{}, key string) []string {
	raw, ok := config[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, s := range v {
			if str, ok := s.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func extractFields(src map[string]interface{}, keys []string) map[string]interface{} {
	out := make(map[string]interface{}, len(keys))
	for _, k := range keys {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	return out
}

func copyMap(src map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

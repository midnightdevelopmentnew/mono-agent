package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

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

	readonlyFields := stringsFromConfig(config, "readonly_fields")
	editableFields := stringsFromConfig(config, "editable_fields")

	timeoutMinutes := 0.0
	if v, ok := config["timeout_minutes"]; ok {
		switch t := v.(type) {
		case float64:
			timeoutMinutes = t
		case int:
			timeoutMinutes = float64(t)
		}
	}

	// Process each input item — create a separate HIL record per item.
	// Items flow out in order after each is approved.
	var approvedItems []workflow.Item

	for _, item := range input.Items {
		roData := extractFields(item.JSON, readonlyFields)
		edData := extractFields(item.JSON, editableFields)

		// If no specific fields configured, put everything in editable.
		if len(readonlyFields) == 0 && len(editableFields) == 0 {
			edData = copyMap(item.JSON)
		}

		configJSON, _ := json.Marshal(map[string]interface{}{
			"readonly_fields": readonlyFields,
			"editable_fields": editableFields,
		})
		roJSON, _ := json.Marshal(roData)
		edJSON, _ := json.Marshal(edData)

		profileID := vault.ProfileIDFromContext(ctx)
		if profileID == "" {
			profileID = "default"
		}

		id := uuid.New().String()
		_, err := db.ExecContext(ctx,
			`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, edited_data, node_config, profile_id)
			 VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, '{}', ?, ?)`,
			id, input.ExecutionID, input.WorkflowID, input.NodeID, input.NodeName,
			string(roJSON), string(edJSON), string(configJSON), profileID,
		)
		if err != nil {
			return nil, fmt.Errorf("human_in_loop: insert pending record: %w", err)
		}

		// Set up optional timeout.
		var timeoutCh <-chan time.Time
		if timeoutMinutes > 0 {
			timeoutCh = time.After(time.Duration(timeoutMinutes * float64(time.Minute)))
		}

		// Poll until the record is approved or rejected.
		approved, err := waitForHILDecision(ctx, db, id, item.JSON, timeoutCh)
		if err != nil {
			return nil, err
		}
		approvedItems = append(approvedItems, workflow.NewItem(approved))
	}

	return []workflow.NodeOutput{
		{Handle: "main", Items: approvedItems},
	}, nil
}

func waitForHILDecision(ctx context.Context, db *sql.DB, id string, original map[string]interface{}, timeoutCh <-chan time.Time) (map[string]interface{}, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			db.ExecContext(context.Background(), //nolint:errcheck
				`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP WHERE id=?`, id)
			return nil, workflow.ErrExecutionCancelled
		case <-timeoutCh:
			db.ExecContext(context.Background(), //nolint:errcheck
				`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP WHERE id=?`, id)
			return nil, fmt.Errorf("human_in_loop: timed out waiting for human approval")
		case <-ticker.C:
			var status, editedRaw string
			if err := db.QueryRowContext(ctx, `SELECT status, edited_data FROM hil_pending WHERE id=?`, id).Scan(&status, &editedRaw); err != nil {
				continue
			}
			switch status {
			case "approved":
				out := copyMap(original)
				if editedRaw != "" && editedRaw != "{}" {
					var edited map[string]interface{}
					if json.Unmarshal([]byte(editedRaw), &edited) == nil {
						for k, v := range edited {
							out[k] = v
						}
					}
				}
				return out, nil
			case "rejected":
				return nil, fmt.Errorf("human_in_loop: item rejected by human reviewer")
			}
		}
	}
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

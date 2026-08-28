package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"monoagent/internal/workflow"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ─────────────────────────────────────────────────────────────────────────────
// Workflow types
// ─────────────────────────────────────────────────────────────────────────────

type WorkflowSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsActive    bool   `json:"is_active"`
	Version     int    `json:"version"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type WorkflowNodeData struct {
	ID        string                 `json:"id"`
	NodeType  string                 `json:"node_type"`
	Name      string                 `json:"name"`
	Config    map[string]interface{} `json:"config"`
	PositionX float64                `json:"position_x"`
	PositionY float64                `json:"position_y"`
	Disabled  bool                   `json:"disabled"`
	Schema    *workflow.NodeSchema   `json:"schema,omitempty"`
}

type WorkflowConnectionData struct {
	ID           string `json:"id"`
	SourceNodeID string `json:"source_node_id"`
	SourceHandle string `json:"source_handle"`
	TargetNodeID string `json:"target_node_id"`
	TargetHandle string `json:"target_handle"`
	Position     int    `json:"position"`
}

type WorkflowDetail struct {
	WorkflowSummary
	Nodes       []WorkflowNodeData       `json:"nodes"`
	Connections []WorkflowConnectionData `json:"connections"`
}

type SaveWorkflowRequest struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	IsActive    bool                     `json:"is_active"`
	Nodes       []WorkflowNodeData       `json:"nodes"`
	Connections []WorkflowConnectionData `json:"connections"`
}

type WorkflowExecutionSummary struct {
	ID           string `json:"id"`
	WorkflowID   string `json:"workflow_id"`
	WorkflowName string `json:"workflow_name"`
	Status       string `json:"status"`
	TriggerType  string `json:"trigger_type"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
	Error        string `json:"error"`
	CreatedAt    string `json:"created_at"`
}

type CredentialSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ServiceType string `json:"service_type"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type SaveCredentialRequest struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	ServiceType string                 `json:"service_type"`
	Data        map[string]interface{} `json:"data"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Workflow CRUD
// ─────────────────────────────────────────────────────────────────────────────

// workflowToDetail converts a *workflow.Workflow into a *WorkflowDetail for the frontend.
func workflowToDetail(wf *workflow.Workflow) *WorkflowDetail {
	detail := &WorkflowDetail{
		WorkflowSummary: WorkflowSummary{
			ID:          wf.ID,
			Name:        wf.Name,
			Description: wf.Description,
			IsActive:    wf.IsActive,
			Version:     wf.Version,
			CreatedAt:   wf.CreatedAt.Format(time.RFC3339),
			UpdatedAt:   wf.UpdatedAt.Format(time.RFC3339),
		},
		Nodes:       []WorkflowNodeData{},
		Connections: []WorkflowConnectionData{},
	}
	for _, n := range wf.Nodes {
		detail.Nodes = append(detail.Nodes, WorkflowNodeData{
			ID:        n.ID,
			NodeType:  n.Type,
			Name:      n.Name,
			Config:    n.Config,
			PositionX: n.PositionX,
			PositionY: n.PositionY,
			Disabled:  n.Disabled,
			Schema:    n.Schema,
		})
	}
	for _, c := range wf.Connections {
		detail.Connections = append(detail.Connections, WorkflowConnectionData{
			ID:           c.ID,
			SourceNodeID: c.SourceNodeID,
			SourceHandle: c.SourceHandle,
			TargetNodeID: c.TargetNodeID,
			TargetHandle: c.TargetHandle,
			Position:     c.Position,
		})
	}
	return detail
}

// ListWorkflowTemplates returns metadata for all bundled, ready-to-use
// workflow templates (e.g. "Outlook Email Sync") shipped with the app.
func (a *App) ListWorkflowTemplates() []workflow.Template {
	return workflow.ListTemplates()
}

// CreateWorkflowFromTemplate instantiates a bundled template as a new,
// editable workflow for the active profile. Node IDs from the template are
// remapped to fresh UUIDs so multiple instantiations never collide, then
// saved via the same path as a normal SaveWorkflow call.
func (a *App) CreateWorkflowFromTemplate(templateID string) (*WorkflowSummary, error) {
	tmpl, ok := workflow.GetTemplate(templateID)
	if !ok {
		return nil, fmt.Errorf("unknown template %q", templateID)
	}

	idMap := make(map[string]string, len(tmpl.Nodes))
	for _, n := range tmpl.Nodes {
		idMap[n.ID] = uuid.New().String()
	}

	req := SaveWorkflowRequest{Name: tmpl.Name, Description: tmpl.Description, IsActive: false}
	for _, n := range tmpl.Nodes {
		config := n.Config
		if config == nil {
			config = map[string]interface{}{}
		}
		// people.sync_outlook_message scopes synced people/messages by its
		// own "profile_id" config field, independent of the workflow's
		// profile — default it to the active profile so the template works
		// correctly out of the box.
		if n.Type == "people.sync_outlook_message" {
			config["profile_id"] = a.getActiveProfileID()
		}
		req.Nodes = append(req.Nodes, WorkflowNodeData{
			ID:        idMap[n.ID],
			NodeType:  n.Type,
			Name:      n.Name,
			Config:    config,
			PositionX: n.Position.X,
			PositionY: n.Position.Y,
			Disabled:  n.Disabled,
		})
	}
	for _, c := range tmpl.Connections {
		req.Connections = append(req.Connections, WorkflowConnectionData{
			ID:           uuid.New().String(),
			SourceNodeID: idMap[c.Source],
			SourceHandle: c.SourceHandle,
			TargetNodeID: idMap[c.Target],
			TargetHandle: c.TargetHandle,
		})
	}

	return a.SaveWorkflow(req)
}

func (a *App) ListWorkflows() ([]WorkflowSummary, error) {
	// Read through the hybrid store, NOT raw SQL. SetWorkflowActive writes the
	// active flag through a.wfStore, which prefers the JSON file store, while
	// this listing used to SELECT is_active straight out of SQLite. The two
	// diverge the moment anything is toggled or edited from the CLI, and the
	// GUI then shows an active workflow as switched off forever -- observed
	// 2026-08-28 with four scheduled workflows displaying as off while the
	// daemon was running all of them on cron. A stale toggle is worse than a
	// missing one: switching an "off" workflow on is harmless, but the same
	// staleness can present a running workflow as off and invite a click that
	// genuinely deactivates it.
	if a.wfStore == nil {
		return nil, fmt.Errorf("workflow store not available")
	}
	wfs, err := a.wfStore.ListWorkflows(context.Background(), a.getActiveProfileID())
	if err != nil {
		return nil, err
	}
	summaries := make([]WorkflowSummary, 0, len(wfs))
	for _, wf := range wfs {
		summaries = append(summaries, WorkflowSummary{
			ID:          wf.ID,
			Name:        wf.Name,
			Description: wf.Description,
			IsActive:    wf.IsActive,
			Version:     wf.Version,
			CreatedAt:   wf.CreatedAt.Format(time.RFC3339),
			UpdatedAt:   wf.UpdatedAt.Format(time.RFC3339),
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})
	return summaries, nil
}

func (a *App) GetWorkflow(id string) (*WorkflowDetail, error) {
	if a.wfStore == nil {
		return nil, fmt.Errorf("workflow store not available")
	}
	ctx := context.Background()
	wf, err := a.wfStore.GetWorkflow(ctx, id)
	if err != nil {
		return nil, err
	}
	if wf == nil {
		return nil, fmt.Errorf("workflow %s not found", id)
	}
	// Verify caller owns this workflow.
	if a.db != nil {
		var wfProfile string
		_ = a.db.QueryRow(`SELECT profile_id FROM workflows WHERE id = ?`, id).Scan(&wfProfile)
		if wfProfile != "" && wfProfile != a.getActiveProfileID() {
			return nil, fmt.Errorf("workflow %s not found", id)
		}
	}
	return workflowToDetail(wf), nil
}

func (a *App) SaveWorkflow(req SaveWorkflowRequest) (*WorkflowSummary, error) {
	if a.wfStore == nil {
		return nil, fmt.Errorf("workflow store not available")
	}
	if a.db != nil && req.ID != "" {
		var wfProfile string
		_ = a.db.QueryRow(`SELECT profile_id FROM workflows WHERE id = ?`, req.ID).Scan(&wfProfile)
		if wfProfile != "" && wfProfile != a.getActiveProfileID() {
			return nil, fmt.Errorf("workflow %s not found", req.ID)
		}
	}
	ctx := context.Background()
	wf := &workflow.Workflow{
		ID:          req.ID,
		Name:        req.Name,
		Description: req.Description,
		IsActive:    req.IsActive,
		ProfileID:   a.getActiveProfileID(),
	}
	for _, n := range req.Nodes {
		node := workflow.WorkflowNode{
			ID:        n.ID,
			Type:      n.NodeType,
			Name:      n.Name,
			PositionX: n.PositionX,
			PositionY: n.PositionY,
			Disabled:  n.Disabled,
			Config:    n.Config,
			Schema:    n.Schema,
		}
		if node.Schema == nil {
			schema, _ := workflow.LoadDefaultSchema(node.Type)
			node.Schema = schema
		}
		wf.Nodes = append(wf.Nodes, node)
	}
	for _, c := range req.Connections {
		wf.Connections = append(wf.Connections, workflow.WorkflowConnection{
			ID:           c.ID,
			SourceNodeID: c.SourceNodeID,
			SourceHandle: c.SourceHandle,
			TargetNodeID: c.TargetNodeID,
			TargetHandle: c.TargetHandle,
			Position:     c.Position,
		})
	}
	if err := a.wfStore.SaveWorkflow(ctx, wf); err != nil {
		return nil, err
	}
	// Tag the workflow with the active profile. The store doesn't know about profiles,
	// so we set it with a follow-up UPDATE.
	if a.db != nil {
		_, _ = a.db.Exec(`UPDATE workflows SET profile_id = ? WHERE id = ?`, a.getActiveProfileID(), wf.ID)
	}
	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Saved workflow: %s [%s]", wf.Name, wf.ID))
	return &WorkflowSummary{
		ID:          wf.ID,
		Name:        wf.Name,
		Description: wf.Description,
		IsActive:    wf.IsActive,
		Version:     wf.Version,
		CreatedAt:   wf.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   wf.UpdatedAt.Format(time.RFC3339),
	}, nil
}

func (a *App) DeleteWorkflow(id string) error {
	if a.wfStore == nil {
		return fmt.Errorf("workflow store not available")
	}
	if a.db != nil {
		var wfProfile string
		_ = a.db.QueryRow(`SELECT profile_id FROM workflows WHERE id = ?`, id).Scan(&wfProfile)
		if wfProfile != "" && wfProfile != a.getActiveProfileID() {
			return fmt.Errorf("workflow %s not found", id)
		}
	}
	err := a.wfStore.DeleteWorkflow(context.Background(), id)
	if err == nil {
		a.emitLog("WORKFLOW", "WARN", "Deleted workflow: "+id)
	}
	return err
}

func (a *App) SetWorkflowActive(id string, active bool) error {
	if a.wfStore == nil {
		return fmt.Errorf("workflow store not available")
	}
	ctx := context.Background()
	if a.db != nil {
		var wfProfile string
		_ = a.db.QueryRow(`SELECT profile_id FROM workflows WHERE id = ?`, id).Scan(&wfProfile)
		if wfProfile != "" && wfProfile != a.getActiveProfileID() {
			return fmt.Errorf("workflow %s not found", id)
		}
	}
	wf, err := a.wfStore.GetWorkflow(ctx, id)
	if err != nil || wf == nil {
		return fmt.Errorf("workflow %s not found", id)
	}
	wf.IsActive = active
	return a.wfStore.SaveWorkflow(ctx, wf)
}

// ─────────────────────────────────────────────────────────────────────────────
// Workflow execution (subprocess)
// ─────────────────────────────────────────────────────────────────────────────

// RunWorkflow spawns `monoagentcli workflow run <id>` as a subprocess.
// Stdout/stderr stream to the UI. The subprocess can be killed via CancelWorkflow.
func (a *App) RunWorkflow(id string) error {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}

	// Ensure workflow is active so the engine doesn't reject it.
	if a.db != nil {
		_, _ = a.db.Exec("UPDATE workflows SET is_active = 1 WHERE id = ? AND profile_id = ?", id, a.getActiveProfileID())
	}

	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Starting workflow %s", id))

	cmd := exec.CommandContext(a.ctx, cliBin, "--profile", a.getActiveProfileID(), "workflow", "run", id)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start workflow: %w", err)
	}
	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Workflow %s started (pid %d)", id, cmd.Process.Pid))

	a.runningMu.Lock()
	a.runningCmds[id] = cmd
	a.runningMu.Unlock()

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			a.emitLog("WORKFLOW", "INFO", line)
			// Detect execution ID from CLI output and notify the frontend
			if strings.HasPrefix(line, "Execution started: ") {
				execID := strings.TrimPrefix(line, "Execution started: ")
				runtime.EventsEmit(a.ctx, "workflow:exec-started", map[string]interface{}{
					"workflow_id":  id,
					"execution_id": strings.TrimSpace(execID),
				})
			}
		}
	}()
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			a.emitLog("WORKFLOW", "INFO", scanner.Text())
		}
	}()
	go func() {
		defer func() {
			a.runningMu.Lock()
			delete(a.runningCmds, id)
			a.runningMu.Unlock()
		}()
		waitErr := cmd.Wait()
		if waitErr != nil {
			a.emitLog("WORKFLOW", "ERROR", fmt.Sprintf("Workflow %s failed: %v", id, waitErr))
			runtime.EventsEmit(a.ctx, "workflow:complete", map[string]interface{}{"workflow_id": id, "success": false})
		} else {
			a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Workflow %s completed", id))
			runtime.EventsEmit(a.ctx, "workflow:complete", map[string]interface{}{"workflow_id": id, "success": true})
		}
	}()
	return nil
}

func (a *App) GetWorkflowExecutions(workflowID string, limit int) ([]WorkflowExecutionSummary, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := a.db.Query(`SELECT we.id, we.workflow_id, we.status, we.trigger_type,
	                                 COALESCE(we.started_at, '') as started_at,
	                                 COALESCE(we.finished_at, '') as finished_at,
	                                 COALESCE(we.error_message, '') as error,
	                                 we.created_at
	                          FROM workflow_executions we
	                          JOIN workflows w ON w.id = we.workflow_id
	                          WHERE we.workflow_id = ? AND w.profile_id = ?
	                          ORDER BY we.created_at DESC
	                          LIMIT ?`, workflowID, a.getActiveProfileID(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var execs []WorkflowExecutionSummary
	for rows.Next() {
		var e WorkflowExecutionSummary
		if rows.Scan(&e.ID, &e.WorkflowID, &e.Status, &e.TriggerType,
			&e.StartedAt, &e.FinishedAt, &e.Error, &e.CreatedAt) == nil {
			execs = append(execs, e)
		}
	}
	if execs == nil {
		execs = []WorkflowExecutionSummary{}
	}
	return execs, rows.Err()
}

func (a *App) GetRecentExecutions(limit int) ([]WorkflowExecutionSummary, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := a.db.Query(`SELECT e.id, e.workflow_id, COALESCE(w.name,'') as workflow_name,
	                                 e.status, COALESCE(e.trigger_type,''),
	                                 COALESCE(e.started_at,'') as started_at,
	                                 COALESCE(e.finished_at,'') as finished_at,
	                                 COALESCE(e.error_message,'') as error,
	                                 e.created_at
	                          FROM workflow_executions e
	                          LEFT JOIN workflows w ON e.workflow_id = w.id
	                          WHERE w.profile_id = ?
	                          ORDER BY e.created_at DESC
	                          LIMIT ?`, a.getActiveProfileID(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var execs []WorkflowExecutionSummary
	for rows.Next() {
		var e WorkflowExecutionSummary
		if rows.Scan(&e.ID, &e.WorkflowID, &e.WorkflowName, &e.Status, &e.TriggerType,
			&e.StartedAt, &e.FinishedAt, &e.Error, &e.CreatedAt) == nil {
			execs = append(execs, e)
		}
	}
	if execs == nil {
		execs = []WorkflowExecutionSummary{}
	}
	return execs, rows.Err()
}

// GetExecutionDetail returns a full execution record with per-node status.
func (a *App) GetExecutionDetail(executionID string) (map[string]interface{}, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}

	// Fetch the execution itself.
	var execID, wfID, status, triggerType, startedAt, finishedAt, errMsg, createdAt string
	err := a.db.QueryRow(`SELECT id, workflow_id, status,
	                              COALESCE(trigger_type,'') as trigger_type,
	                              COALESCE(started_at,'') as started_at,
	                              COALESCE(finished_at,'') as finished_at,
	                              COALESCE(error_message,'') as error_message,
	                              created_at
	                       FROM workflow_executions WHERE id = ? AND profile_id = ?`, executionID, a.getActiveProfileID()).
		Scan(&execID, &wfID, &status, &triggerType, &startedAt, &finishedAt, &errMsg, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("execution not found: %w", err)
	}

	// Fetch per-node execution rows.
	rows, err := a.db.Query(`SELECT id, node_id, node_name, status,
	                                COALESCE(error_message,'') as error_message,
	                                COALESCE(started_at,'') as started_at,
	                                COALESCE(finished_at,'') as finished_at,
	                                COALESCE(input_items,'[]') as input_items,
	                                COALESCE(output_items,'[]') as output_items,
	                                retry_count
	                         FROM workflow_execution_nodes
	                         WHERE execution_id = ?
	                         ORDER BY started_at`, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodesList []map[string]interface{}
	for rows.Next() {
		var nID, nodeID, nodeName, nStatus, nErr, nStarted, nFinished, inputItems, outputItems string
		var retryCount int
		if err := rows.Scan(&nID, &nodeID, &nodeName, &nStatus, &nErr, &nStarted, &nFinished, &inputItems, &outputItems, &retryCount); err != nil {
			continue
		}
		nodesList = append(nodesList, map[string]interface{}{
			"id":            nID,
			"node_id":       nodeID,
			"node_name":     nodeName,
			"status":        nStatus,
			"error_message": nErr,
			"started_at":    nStarted,
			"finished_at":   nFinished,
			"input_items":   inputItems,
			"output_items":  outputItems,
			"retry_count":   retryCount,
		})
	}
	if nodesList == nil {
		nodesList = []map[string]interface{}{}
	}

	return map[string]interface{}{
		"id":          execID,
		"workflow_id": wfID,
		"status":      status,
		"trigger_type": triggerType,
		"started_at":  startedAt,
		"finished_at": finishedAt,
		"error":       errMsg,
		"created_at":  createdAt,
		"nodes":       nodesList,
	}, nil
}

// CancelWorkflow cancels a running workflow execution.
func (a *App) CancelWorkflow(executionID string) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}

	// Look up the workflow_id and pid for this execution, scoped to the active
	// profile so one profile cannot resolve (and kill) another's subprocess.
	var workflowID string
	var pid int
	_ = a.db.QueryRow(`SELECT workflow_id, COALESCE(pid,0) FROM workflow_executions WHERE id = ? AND profile_id = ?`, executionID, a.getActiveProfileID()).Scan(&workflowID, &pid)

	// Kill the subprocess if tracked by Wails (started via RunWorkflow).
	a.runningMu.Lock()
	killed := false
	if workflowID != "" {
		if cmd, ok := a.runningCmds[workflowID]; ok && cmd.Process != nil {
			_ = cmd.Process.Kill()
			delete(a.runningCmds, workflowID)
			killed = true
		}
	}
	if !killed {
		if cmd, ok := a.runningCmds[executionID]; ok && cmd.Process != nil {
			_ = cmd.Process.Kill()
			delete(a.runningCmds, executionID)
			killed = true
		}
	}
	a.runningMu.Unlock()

	// Kill external CLI process via PID stored in the DB.
	if !killed && pid > 0 {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}

	// Mark cancelled in DB — scoped to the active profile for safety.
	_, _ = a.db.Exec(`UPDATE workflow_executions SET status = 'CANCELLED', finished_at = CURRENT_TIMESTAMP WHERE id = ? AND profile_id = ?`, executionID, a.getActiveProfileID())
	// Reject any pending HIL items for this execution so they don't stay blocked forever.
	_, _ = a.db.Exec(`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP WHERE execution_id=? AND status='pending' AND profile_id = ?`, executionID, a.getActiveProfileID())
	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Execution %s cancelled", executionID))
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────

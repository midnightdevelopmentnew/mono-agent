package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"monoagent/internal/connections"
	"monoagent/internal/vault"
	"github.com/rs/zerolog"
)

// WorkflowEngine is the central coordinator for workflow management and execution.
// It owns the queue, trigger manager, webhook server, node registry, and store.
type WorkflowEngine struct {
	store          WorkflowStore
	connStore      *connections.Store
	registry       *NodeTypeRegistry
	queue          *ExecutionQueue
	triggerMgr     *TriggerManager
	webhookServer  *WebhookServer
	expr           *ExpressionEngine
	logger         zerolog.Logger
	mu             sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
	pruneInterval    time.Duration
	maxExecHistory   int
	profileID        string
	allowAllProfiles bool
}

// EngineConfig holds WorkflowEngine configuration.
type EngineConfig struct {
	WebhookAddr    string        // e.g. ":9321"
	MaxConcurrent  int           // default 3, max 20
	QueueCapacity  int           // default 1000
	PruneInterval  time.Duration // default 1h
	MaxExecHistory int           // default 500
	ProfileID      string        // active profile for vault image registration
	// AllowAllProfiles disables the single-profile guard in
	// checkWorkflowProfile, letting this engine activate/run workflows
	// belonging to any profile. Intended for a long-running daemon process
	// that keeps every active workflow's triggers alive regardless of which
	// profile is selected in the UI.
	AllowAllProfiles bool
}

// NewWorkflowEngine creates a fully wired engine. Call Start() to begin processing.
// NewWorkflowEngineWithStore creates a WorkflowEngine using a caller-supplied
// WorkflowStore. Use this when you need a HybridWorkflowStore (file store +
// SQLite). db is still required for the connections store.
func NewWorkflowEngineWithStore(store WorkflowStore, db *sql.DB, scheduler SchedulerInterface, registry *NodeTypeRegistry, cfg EngineConfig, logger zerolog.Logger) *WorkflowEngine {
	applyEngineDefaults(&cfg)
	connStore := connections.NewStore(db)
	webhookServer := NewWebhookServer(cfg.WebhookAddr, logger)
	profileID := cfg.ProfileID
	if profileID == "" {
		profileID = "default"
	}
	e := &WorkflowEngine{
		store:            store,
		connStore:        connStore,
		registry:         registry,
		webhookServer:    webhookServer,
		expr:             NewExpressionEngine(),
		logger:           logger,
		pruneInterval:    cfg.PruneInterval,
		maxExecHistory:   cfg.MaxExecHistory,
		profileID:        profileID,
		allowAllProfiles: cfg.AllowAllProfiles,
	}
	e.triggerMgr = NewTriggerManager(store, webhookServer, scheduler,
		func(workflowID string, nodeID string, items []Item) { e.handleTrigger(workflowID, nodeID, items) },
		logger,
	)
	e.queue = NewExecutionQueue(cfg.QueueCapacity, cfg.MaxConcurrent,
		func(ctx context.Context, req ExecutionRequest) { e.handleExecution(ctx, req) },
		logger,
	)
	return e
}

func applyEngineDefaults(cfg *EngineConfig) {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 3
	}
	if cfg.MaxConcurrent > 20 {
		cfg.MaxConcurrent = 20
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = 1000
	}
	if cfg.PruneInterval <= 0 {
		cfg.PruneInterval = time.Hour
	}
	if cfg.MaxExecHistory <= 0 {
		cfg.MaxExecHistory = 500
	}
	if cfg.WebhookAddr == "" {
		// Bind loopback by default so webhook triggers aren't reachable from the
		// LAN without auth; operators can opt into ":9321" explicitly.
		cfg.WebhookAddr = "127.0.0.1:9321"
	}
}

func NewWorkflowEngine(db *sql.DB, scheduler SchedulerInterface, registry *NodeTypeRegistry, cfg EngineConfig, logger zerolog.Logger) *WorkflowEngine {
	applyEngineDefaults(&cfg)

	store := NewSQLiteWorkflowStore(db)
	connStore := connections.NewStore(db)
	webhookServer := NewWebhookServer(cfg.WebhookAddr, logger)

	e := &WorkflowEngine{
		store:          store,
		connStore:      connStore,
		registry:       registry,
		webhookServer:  webhookServer,
		expr:           NewExpressionEngine(),
		logger:         logger,
		pruneInterval:  cfg.PruneInterval,
		maxExecHistory: cfg.MaxExecHistory,
	}

	// Wire trigger manager with a handleTrigger closure.
	e.triggerMgr = NewTriggerManager(
		store,
		webhookServer,
		scheduler,
		func(workflowID string, nodeID string, items []Item) {
			e.handleTrigger(workflowID, nodeID, items)
		},
		logger,
	)

	// Wire execution queue with handleExecution as the handler.
	e.queue = NewExecutionQueue(
		cfg.QueueCapacity,
		cfg.MaxConcurrent,
		func(ctx context.Context, req ExecutionRequest) {
			e.handleExecution(ctx, req)
		},
		logger,
	)

	return e
}

// Start initializes the engine: starts the queue workers, webhook server,
// recovers stale executions, re-registers triggers for active workflows,
// and starts the prune loop.
func (e *WorkflowEngine) Start(ctx context.Context) error {
	e.ctx, e.cancel = context.WithCancel(ctx)

	// 1. Start webhook server. The webhook port is a single fixed port shared by
	// every monoagent process, so a second engine (a CLI run started while the
	// daemon or the app is up) can never bind it. That must not stop the engine:
	// webhook deliveries are already being served by whoever owns the port, and
	// everything else — manual runs, schedules, node execution — is unaffected.
	if err := e.webhookServer.Start(); err != nil {
		if !isAddrInUse(err) {
			return fmt.Errorf("engine: start webhook server: %w", err)
		}
		e.logger.Warn().Err(err).
			Msg("engine: webhook port already in use by another monoagent process — this engine will not serve webhooks, everything else runs normally")
	}

	// 2. Start queue workers.
	e.queue.Start(e.ctx)

	// 3. Recover stale executions left over from a prior crash or restart.
	if err := e.store.RecoverStaleExecutions(e.ctx); err != nil {
		e.logger.Warn().Err(err).Msg("engine: failed to recover stale executions")
	}

	// 3a. Reject HIL items orphaned by a crash — their execution is no longer
	// running, so their in-process waiter is gone and approving them is a no-op.
	// This clears zombie 'pending' rows from the approvals queue.
	if db := e.store.RawDB(); db != nil {
		if _, err := db.ExecContext(e.ctx,
			`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP
			 WHERE status='pending' AND execution_id IN (
			     SELECT id FROM workflow_executions WHERE status NOT IN ('RUNNING','QUEUED','WAITING'))`,
		); err != nil {
			e.logger.Warn().Err(err).Msg("engine: failed to clean up orphaned HIL items")
		}
	}

	// 4. Re-register triggers for all active workflows.
	if err := e.reregisterTriggers(e.ctx); err != nil {
		e.logger.Warn().Err(err).Msg("engine: failed to re-register some triggers on startup")
	}

	// 5. Start prune and resume loops.
	go e.pruneLoop(e.ctx)
	go e.resumeLoop(e.ctx)

	e.logger.Info().Msg("workflow engine started")
	return nil
}

// resumeLoop periodically re-enqueues WAITING executions whose pause points
// (Human-in-Loop items) have all been resolved, so they continue from where
// they paused. This is what makes an approval — from the GUI or the CLI, in any
// process — actually resume the run, and lets paused runs survive a restart.
func (e *WorkflowEngine) resumeLoop(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ids, err := e.store.ListResumableExecutions(ctx)
			if err != nil {
				e.logger.Warn().Err(err).Msg("engine: listing resumable executions")
				continue
			}
			for _, id := range ids {
				if err := e.ResumeExecution(id); err != nil {
					e.logger.Warn().Err(err).Str("execution_id", id).Msg("engine: failed to resume execution")
				}
			}
		}
	}
}

// ResumeExecution re-enqueues a paused (WAITING) execution. handleExecution
// reloads its persisted resume_state so RunExecution skips completed nodes and
// continues from the pause point.
func (e *WorkflowEngine) ResumeExecution(executionID string) error {
	dctx, cancel := dbCtx()
	defer cancel()
	exec, err := e.store.GetExecution(dctx, executionID)
	if err != nil {
		return fmt.Errorf("engine: resume execution: %w", err)
	}
	if exec == nil {
		return fmt.Errorf("engine: resume execution: %w", ErrExecutionNotFound)
	}
	if exec.Status != "WAITING" {
		return nil // already resumed or finished by another tick
	}
	// Atomically flip WAITING → QUEUED (preserves resume_state). The guarded
	// CAS ensures that among concurrent engines sharing the DB exactly one
	// resumes the execution — the losers get flipped=false and stop.
	flipped, err := e.store.ResumeWaitingExecution(dctx, executionID)
	if err != nil {
		return fmt.Errorf("engine: resume execution: %w", err)
	}
	if !flipped {
		return nil // another engine won the resume, or it's no longer WAITING
	}
	req := ExecutionRequest{
		WorkflowID:  exec.WorkflowID,
		ExecutionID: executionID,
		TriggerType: exec.TriggerType,
		TriggerData: exec.TriggerData,
	}
	if err := e.queue.Enqueue(req); err != nil {
		// Transient (queue full / closing): revert to WAITING preserving the
		// resume state so the next resume tick retries — never destroy the run.
		_ = e.store.SetExecutionWaiting(dctx, executionID, exec.ResumeState)
		return fmt.Errorf("engine: resume execution: %w", err)
	}
	return nil
}

// Stop gracefully shuts down all components.
func (e *WorkflowEngine) Stop() error {
	e.logger.Info().Msg("workflow engine stopping")

	// Signal all goroutines driven by e.ctx.
	if e.cancel != nil {
		e.cancel()
	}

	// Deactivate all triggers (stops cron jobs and deregisters webhooks).
	e.triggerMgr.DeactivateAll()

	// Stop the queue — drains in-flight work and waits for workers to exit.
	e.queue.Stop()

	// Shut down the webhook HTTP server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := e.webhookServer.Stop(shutdownCtx); err != nil {
		e.logger.Warn().Err(err).Msg("engine: webhook server shutdown error")
	}

	e.logger.Info().Msg("workflow engine stopped")
	return nil
}

// pruneLoop runs on the prune interval and culls old execution history for
// every workflow.
func (e *WorkflowEngine) pruneLoop(ctx context.Context) {
	ticker := time.NewTicker(e.pruneInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.runPrune(ctx)
		}
	}
}

// runPrune iterates over all workflows and prunes execution history.
func (e *WorkflowEngine) runPrune(ctx context.Context) {
	workflows, err := e.store.ListWorkflows(ctx, e.profileID)
	if err != nil {
		e.logger.Warn().Err(err).Msg("engine: prune: failed to list workflows")
		return
	}
	for _, wf := range workflows {
		if err := e.store.PruneExecutions(ctx, wf.ID, e.maxExecHistory); err != nil {
			e.logger.Warn().Err(err).
				Str("workflow_id", wf.ID).
				Msg("engine: prune: failed to prune executions")
		}
	}
}

// reregisterTriggers loads all active workflows and activates their triggers.
func (e *WorkflowEngine) reregisterTriggers(ctx context.Context) error {
	workflows, err := e.store.ListWorkflows(ctx, e.profileID)
	if err != nil {
		return fmt.Errorf("list workflows: %w", err)
	}

	var firstErr error
	for _, wf := range workflows {
		if !wf.IsActive {
			continue
		}
		// Load the full workflow (with nodes) for trigger registration.
		full, err := e.store.GetWorkflow(ctx, wf.ID)
		if err != nil {
			e.logger.Warn().Err(err).
				Str("workflow_id", wf.ID).
				Msg("engine: reregister triggers: failed to load workflow")
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if full == nil {
			continue
		}
		if err := e.triggerMgr.ActivateWorkflow(ctx, full); err != nil {
			e.logger.Warn().Err(err).
				Str("workflow_id", wf.ID).
				Msg("engine: reregister triggers: failed to activate workflow triggers")
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// handleTrigger is called by TriggerManager whenever a trigger fires.
// It creates a WorkflowExecution record and enqueues it for execution.
func (e *WorkflowEngine) handleTrigger(workflowID string, nodeID string, items []Item) {
	ctx := e.ctx

	// Determine trigger type from the node.  We load the workflow to find the
	// node's Type field.
	triggerType := "unknown"
	wf, err := e.store.GetWorkflow(ctx, workflowID)
	if err == nil && wf != nil {
		if !wf.IsActive {
			e.logger.Warn().
				Str("workflow_id", workflowID).
				Msg("engine: handleTrigger: workflow is not active, ignoring trigger")
			return
		}
		for _, n := range wf.Nodes {
			if n.ID == nodeID {
				triggerType = n.Type
				break
			}
		}
	}

	// Build trigger data from the first item (if any).
	triggerData := make(map[string]interface{})
	if len(items) > 0 && items[0].JSON != nil {
		triggerData = items[0].JSON
	}

	exec := &WorkflowExecution{
		WorkflowID:  workflowID,
		Status:      "QUEUED",
		TriggerType: triggerType,
		TriggerData: triggerData,
	}

	if err := e.store.CreateExecution(ctx, exec); err != nil {
		e.logger.Error().Err(err).
			Str("workflow_id", workflowID).
			Str("node_id", nodeID).
			Msg("engine: handleTrigger: failed to create execution record")
		return
	}

	req := ExecutionRequest{
		WorkflowID:    workflowID,
		ExecutionID:   exec.ID,
		TriggerType:   triggerType,
		TriggerNodeID: nodeID,
		TriggerData:   triggerData,
	}

	if err := e.queue.Enqueue(req); err != nil {
		// Queue is full — mark the execution as FAILED immediately.
		e.logger.Warn().
			Str("execution_id", exec.ID).
			Str("workflow_id", workflowID).
			Msg("engine: handleTrigger: queue full, execution will not run")
		if updateErr := e.store.SetExecutionFinished(ctx, exec.ID, "FAILED", "queue full"); updateErr != nil {
			e.logger.Error().Err(updateErr).
				Str("execution_id", exec.ID).
				Msg("engine: handleTrigger: failed to mark execution as FAILED after queue full")
		}
	}
}

// handleExecution is called by a queue worker for each execution request.
func (e *WorkflowEngine) handleExecution(ctx context.Context, req ExecutionRequest) {
	log := e.logger.With().
		Str("execution_id", req.ExecutionID).
		Str("workflow_id", req.WorkflowID).
		Logger()

	// 0. Honor a cancellation that landed while this request sat in the queue.
	if exec, err := e.store.GetExecution(ctx, req.ExecutionID); err == nil && exec != nil && exec.Status == "CANCELLED" {
		log.Info().Msg("engine: handleExecution: execution cancelled before dispatch; skipping")
		return
	}

	// 1. Load the workflow.
	wf, err := e.store.GetWorkflow(ctx, req.WorkflowID)
	if err != nil {
		log.Error().Err(err).Msg("engine: handleExecution: failed to load workflow")
		_ = e.store.SetExecutionFinished(ctx, req.ExecutionID, "FAILED", fmt.Sprintf("load workflow: %s", err.Error()))
		return
	}
	if wf == nil {
		log.Error().Msg("engine: handleExecution: workflow not found")
		_ = e.store.SetExecutionFinished(ctx, req.ExecutionID, "FAILED", "workflow not found")
		return
	}

	// 2. Update execution status to RUNNING, record started_at.
	if err := e.store.SetExecutionStarted(ctx, req.ExecutionID); err != nil {
		log.Error().Err(err).Msg("engine: handleExecution: failed to mark execution as RUNNING")
		// Non-fatal — attempt to continue.
	}

	// 3. Build the DAG.
	dag, err := BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		log.Error().Err(err).Msg("engine: handleExecution: failed to build DAG")
		_ = e.store.SetExecutionFinished(ctx, req.ExecutionID, "FAILED", fmt.Sprintf("build dag: %s", err.Error()))
		return
	}

	// Load the full execution record (with trigger data) so runExecution has it.
	exec, err := e.store.GetExecution(ctx, req.ExecutionID)
	if err != nil || exec == nil {
		log.Error().Err(err).Msg("engine: handleExecution: failed to load execution record")
		_ = e.store.SetExecutionFinished(ctx, req.ExecutionID, "FAILED", "could not load execution record")
		return
	}
	// TriggerNodeID isn't persisted; carry it from the request so RunExecution
	// only fires the branch of the trigger node that actually fired.
	exec.TriggerNodeID = req.TriggerNodeID

	// 4. Execute via runExecution (defined in execution.go).
	runErr := e.runExecution(ctx, exec, wf, dag)

	// 5. On completion: update execution status to SUCCESS or FAILED.
	// Use a detached context so DB writes succeed even if the execution context
	// was cancelled (e.g. by engine shutdown or a browser panic).
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer persistCancel()
	var partialErr *PartialFailureError
	switch {
	case runErr == nil:
		log.Info().Msg("engine: execution finished successfully")
		_ = e.store.SetExecutionFinished(persistCtx, req.ExecutionID, "SUCCESS", "")
	case errors.Is(runErr, ErrExecutionPaused):
		// The run suspended at a pause point (e.g. Human-in-Loop). RunExecution
		// already persisted WAITING + resume state; leave it for the resume loop.
		log.Info().Msg("engine: execution paused, awaiting resume")
	case errors.As(runErr, &partialErr):
		// The run completed but nodes failed under on_error=continue/skip/error_branch.
		// Surface that rather than a misleading green SUCCESS.
		log.Warn().Err(runErr).Msg("engine: execution finished with non-fatal node failures")
		_ = e.store.SetExecutionFinished(persistCtx, req.ExecutionID, "SUCCESS_WITH_ERRORS", runErr.Error())
	case errors.Is(runErr, ErrExecutionCancelled):
		log.Warn().Err(runErr).Str("final_status", "CANCELLED").Msg("engine: execution cancelled")
		_ = e.store.SetExecutionFinished(persistCtx, req.ExecutionID, "CANCELLED", runErr.Error())
	default:
		log.Warn().Err(runErr).Str("final_status", "FAILED").Msg("engine: execution finished with error")
		_ = e.store.SetExecutionFinished(persistCtx, req.ExecutionID, "FAILED", runErr.Error())
	}
}

// ---------------------------------------------------------------------------
// Workflow lifecycle
// ---------------------------------------------------------------------------

// CreateWorkflow saves a new workflow (inactive by default).
func (e *WorkflowEngine) CreateWorkflow(ctx context.Context, w *Workflow) error {
	w.IsActive = false
	if w.ProfileID == "" {
		w.ProfileID = e.profileID
	}
	if err := e.store.CreateWorkflow(ctx, w); err != nil {
		return fmt.Errorf("engine: create workflow: %w", err)
	}
	if len(w.Nodes) > 0 {
		if err := e.store.SaveWorkflowNodes(ctx, w.ID, w.Nodes); err != nil {
			return fmt.Errorf("engine: create workflow nodes: %w", err)
		}
	}
	if len(w.Connections) > 0 {
		if err := e.store.SaveWorkflowConnections(ctx, w.ID, w.Connections); err != nil {
			return fmt.Errorf("engine: create workflow connections: %w", err)
		}
	}
	e.logger.Info().Str("workflow_id", w.ID).Str("name", w.Name).Msg("engine: workflow created")
	return nil
}

// SaveWorkflow updates a workflow's definition (nodes + connections).
// If the workflow is currently active, it is deactivated first, then saved,
// then reactivated.
func (e *WorkflowEngine) SaveWorkflow(ctx context.Context, w *Workflow) error {
	// Load current state to check IsActive.
	existing, err := e.store.GetWorkflow(ctx, w.ID)
	if err != nil {
		return fmt.Errorf("engine: save workflow: load existing: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("engine: save workflow: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(existing); err != nil {
		return fmt.Errorf("engine: save workflow: %w", err)
	}

	wasActive := existing.IsActive

	// Deactivate triggers if currently active.
	if wasActive {
		e.triggerMgr.DeactivateWorkflow(w.ID)
		if err := e.store.SetWorkflowActive(ctx, w.ID, false); err != nil {
			return fmt.Errorf("engine: save workflow: deactivate: %w", err)
		}
	}

	// Preserve the active flag from the existing record; caller controls it
	// via Activate/DeactivateWorkflow.
	w.IsActive = false

	if err := e.store.UpdateWorkflow(ctx, w); err != nil {
		return fmt.Errorf("engine: save workflow: update: %w", err)
	}
	if err := e.store.SaveWorkflowNodes(ctx, w.ID, w.Nodes); err != nil {
		return fmt.Errorf("engine: save workflow: save nodes: %w", err)
	}
	if err := e.store.SaveWorkflowConnections(ctx, w.ID, w.Connections); err != nil {
		return fmt.Errorf("engine: save workflow: save connections: %w", err)
	}

	// Reactivate if it was active before the save.
	if wasActive {
		if err := e.ActivateWorkflow(ctx, w.ID); err != nil {
			return fmt.Errorf("engine: save workflow: reactivate: %w", err)
		}
	}

	e.logger.Info().Str("workflow_id", w.ID).Msg("engine: workflow saved")
	return nil
}

// checkWorkflowProfile returns an error if wf belongs to a different profile
// than the engine's active profile, preventing cross-profile access to a
// workflow whose ID was guessed or leaked from another profile.
func (e *WorkflowEngine) checkWorkflowProfile(wf *Workflow) error {
	if e.allowAllProfiles {
		return nil
	}
	if wf.ProfileID != "" && wf.ProfileID != e.profileID {
		return fmt.Errorf("workflow belongs to a different profile")
	}
	return nil
}

// RestoreActiveWorkflows re-activates every workflow across all profiles
// that is marked active in the store, registering their triggers with this
// engine. Intended to be called once after Start() by a long-running daemon
// process, so scheduled/webhook triggers survive process restarts. Requires
// AllowAllProfiles (set via EngineConfig) since it spans every profile.
func (e *WorkflowEngine) RestoreActiveWorkflows(ctx context.Context) error {
	if !e.allowAllProfiles {
		return fmt.Errorf("engine: RestoreActiveWorkflows requires AllowAllProfiles")
	}
	workflows, err := e.store.ListWorkflows(ctx, "")
	if err != nil {
		return fmt.Errorf("engine: restore active workflows: list: %w", err)
	}
	var restored, failed int
	for _, wf := range workflows {
		if !wf.IsActive {
			continue
		}
		full, err := e.store.GetWorkflow(ctx, wf.ID)
		if err != nil || full == nil {
			failed++
			e.logger.Warn().Str("workflow_id", wf.ID).Err(err).Msg("engine: restore: failed to load workflow")
			continue
		}
		if err := e.triggerMgr.ActivateWorkflow(ctx, full); err != nil {
			failed++
			e.logger.Warn().Str("workflow_id", wf.ID).Err(err).Msg("engine: restore: failed to activate triggers")
			continue
		}
		restored++
	}
	e.logger.Info().Int("restored", restored).Int("failed", failed).Msg("engine: restored active workflows")
	return nil
}

// DeleteWorkflow deactivates and deletes a workflow.
func (e *WorkflowEngine) DeleteWorkflow(ctx context.Context, id string) error {
	existing, err := e.store.GetWorkflow(ctx, id)
	if err != nil {
		return fmt.Errorf("engine: delete workflow: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("engine: delete workflow: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(existing); err != nil {
		return fmt.Errorf("engine: delete workflow: %w", err)
	}

	if existing.IsActive {
		e.triggerMgr.DeactivateWorkflow(id)
	}

	if err := e.store.DeleteWorkflow(ctx, id); err != nil {
		return fmt.Errorf("engine: delete workflow: %w", err)
	}

	e.logger.Info().Str("workflow_id", id).Msg("engine: workflow deleted")
	return nil
}

// ActivateWorkflow enables a workflow and registers its triggers.
func (e *WorkflowEngine) ActivateWorkflow(ctx context.Context, id string) error {
	wf, err := e.store.GetWorkflow(ctx, id)
	if err != nil {
		return fmt.Errorf("engine: activate workflow: %w", err)
	}
	if wf == nil {
		return fmt.Errorf("engine: activate workflow: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(wf); err != nil {
		return fmt.Errorf("engine: activate workflow: %w", err)
	}

	if err := e.store.SetWorkflowActive(ctx, id, true); err != nil {
		return fmt.Errorf("engine: activate workflow: %w", err)
	}
	wf.IsActive = true

	if err := e.triggerMgr.ActivateWorkflow(ctx, wf); err != nil {
		// Revert the active flag so the DB stays consistent.
		_ = e.store.SetWorkflowActive(ctx, id, false)
		return fmt.Errorf("engine: activate workflow: register triggers: %w", err)
	}

	e.logger.Info().Str("workflow_id", id).Msg("engine: workflow activated")
	return nil
}

// DeactivateWorkflow disables a workflow and unregisters its triggers.
func (e *WorkflowEngine) DeactivateWorkflow(ctx context.Context, id string) error {
	existing, err := e.store.GetWorkflow(ctx, id)
	if err != nil {
		return fmt.Errorf("engine: deactivate workflow: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("engine: deactivate workflow: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(existing); err != nil {
		return fmt.Errorf("engine: deactivate workflow: %w", err)
	}

	e.triggerMgr.DeactivateWorkflow(id)

	if err := e.store.SetWorkflowActive(ctx, id, false); err != nil {
		return fmt.Errorf("engine: deactivate workflow: %w", err)
	}

	e.logger.Info().Str("workflow_id", id).Msg("engine: workflow deactivated")
	return nil
}

// ---------------------------------------------------------------------------
// Execution management
// ---------------------------------------------------------------------------

// TriggerWorkflow manually triggers a workflow (for manual trigger nodes).
// Returns the new execution ID.
func (e *WorkflowEngine) TriggerWorkflow(ctx context.Context, workflowID string, data map[string]interface{}) (string, error) {
	wf, err := e.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return "", fmt.Errorf("engine: trigger workflow: %w", err)
	}
	if wf == nil {
		return "", fmt.Errorf("engine: trigger workflow: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(wf); err != nil {
		return "", fmt.Errorf("engine: trigger workflow: %w", err)
	}
	if !wf.IsActive {
		return "", fmt.Errorf("engine: trigger workflow: %w", ErrWorkflowInactive)
	}

	if data == nil {
		data = make(map[string]interface{})
	}

	exec := &WorkflowExecution{
		WorkflowID:  workflowID,
		Status:      "QUEUED",
		TriggerType: "trigger.manual",
		TriggerData: data,
	}

	if err := e.store.CreateExecution(ctx, exec); err != nil {
		return "", fmt.Errorf("engine: trigger workflow: create execution: %w", err)
	}

	req := ExecutionRequest{
		WorkflowID:  workflowID,
		ExecutionID: exec.ID,
		TriggerType: "trigger.manual",
		TriggerData: data,
	}

	if err := e.queue.Enqueue(req); err != nil {
		_ = e.store.SetExecutionFinished(ctx, exec.ID, "FAILED", "queue full")
		return exec.ID, fmt.Errorf("engine: trigger workflow: %w", ErrQueueFull)
	}

	e.logger.Info().
		Str("workflow_id", workflowID).
		Str("execution_id", exec.ID).
		Msg("engine: manual trigger queued")
	return exec.ID, nil
}

// CancelExecution signals an in-flight execution to stop.
func (e *WorkflowEngine) CancelExecution(executionID string) {
	// Authoritatively cancel a still-queued execution so it doesn't run once a
	// worker picks it up (queued requests have no cancel func yet). handleExecution
	// re-checks status before running, so this makes the cancel stick.
	dctx, cancel := dbCtx()
	defer cancel()
	// Covers QUEUED (not yet dispatched) and WAITING (paused at a HIL node with
	// no live goroutine) — a status flip is enough for both.
	if cancelled, err := e.store.CancelQueuedExecution(dctx, executionID); err == nil && cancelled {
		// Reject any still-pending HIL items so a cancelled paused run can't be
		// resumed and doesn't leave zombie items in the approvals queue.
		if db := e.store.RawDB(); db != nil {
			_, _ = db.ExecContext(dctx,
				`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP WHERE execution_id=? AND status='pending'`,
				executionID)
		}
		e.logger.Info().Str("execution_id", executionID).Msg("engine: queued/waiting execution cancelled")
	}
	// Signal cancellation for an already-dispatched (running) execution.
	e.queue.Cancel(executionID)
	e.logger.Info().Str("execution_id", executionID).Msg("engine: execution cancel requested")
}

// RetryExecution re-queues a failed execution as a new execution.
func (e *WorkflowEngine) RetryExecution(ctx context.Context, executionID string) (string, error) {
	orig, err := e.store.GetExecution(ctx, executionID)
	if err != nil {
		return "", fmt.Errorf("engine: retry execution: %w", err)
	}
	if orig == nil {
		return "", fmt.Errorf("engine: retry execution: %w", ErrExecutionNotFound)
	}

	wf, err := e.store.GetWorkflow(ctx, orig.WorkflowID)
	if err != nil {
		return "", fmt.Errorf("engine: retry execution: load workflow: %w", err)
	}
	if wf == nil {
		return "", fmt.Errorf("engine: retry execution: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(wf); err != nil {
		return "", fmt.Errorf("engine: retry execution: %w", err)
	}

	exec := &WorkflowExecution{
		WorkflowID:  orig.WorkflowID,
		Status:      "QUEUED",
		TriggerType: orig.TriggerType,
		TriggerData: orig.TriggerData,
	}

	if err := e.store.CreateExecution(ctx, exec); err != nil {
		return "", fmt.Errorf("engine: retry execution: create new execution: %w", err)
	}

	req := ExecutionRequest{
		WorkflowID:  orig.WorkflowID,
		ExecutionID: exec.ID,
		TriggerType: orig.TriggerType,
		TriggerData: orig.TriggerData,
	}

	if err := e.queue.Enqueue(req); err != nil {
		_ = e.store.SetExecutionFinished(ctx, exec.ID, "FAILED", "queue full")
		return exec.ID, fmt.Errorf("engine: retry execution: %w", ErrQueueFull)
	}

	e.logger.Info().
		Str("original_execution_id", executionID).
		Str("new_execution_id", exec.ID).
		Msg("engine: execution retry queued")
	return exec.ID, nil
}

// ---------------------------------------------------------------------------
// Query helpers
// ---------------------------------------------------------------------------

// GetWorkflow loads a workflow with all nodes and connections.
func (e *WorkflowEngine) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	wf, err := e.store.GetWorkflow(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("engine: get workflow: %w", err)
	}
	if wf == nil {
		return nil, fmt.Errorf("engine: get workflow: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(wf); err != nil {
		return nil, fmt.Errorf("engine: get workflow: %w", err)
	}
	return wf, nil
}

// ListWorkflows returns all workflows.
func (e *WorkflowEngine) ListWorkflows(ctx context.Context) ([]Workflow, error) {
	workflows, err := e.store.ListWorkflows(ctx, e.profileID)
	if err != nil {
		return nil, fmt.Errorf("engine: list workflows: %w", err)
	}
	return workflows, nil
}

// GetExecution loads a workflow execution with all node results.
func (e *WorkflowEngine) GetExecution(ctx context.Context, id string) (*WorkflowExecution, error) {
	exec, err := e.store.GetExecution(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("engine: get execution: %w", err)
	}
	if exec == nil {
		return nil, fmt.Errorf("engine: get execution: %w", ErrExecutionNotFound)
	}
	return exec, nil
}

// ListExecutions returns recent executions for a workflow.
func (e *WorkflowEngine) ListExecutions(ctx context.Context, workflowID string, limit int) ([]WorkflowExecution, error) {
	executions, err := e.store.ListExecutions(ctx, workflowID, limit)
	if err != nil {
		return nil, fmt.Errorf("engine: list executions: %w", err)
	}
	return executions, nil
}

// runExecution delegates to RunExecution defined in execution.go.
func (e *WorkflowEngine) runExecution(ctx context.Context, exec *WorkflowExecution, wf *Workflow, dag *DAG) error {
	ctx = vault.ContextWithDB(ctx, e.store.RawDB())
	ctx = vault.ContextWithProfileID(ctx, e.profileID)
	return RunExecution(ctx, exec, wf, dag, e.registry, e.store, e.connStore, e.expr, e.logger)
}

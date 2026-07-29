package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	browserpkg "monoagent/internal/browser"
	cfgpkg "monoagent/internal/config"
	"monoagent/internal/connections"
	"monoagent/internal/nodes"
	"monoagent/internal/scheduler"
	"monoagent/internal/storage"
	"monoagent/internal/workflow"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// newHybridStore creates a HybridWorkflowStore that reads from both
// the JSON file store (~/.monoagent/workflows/) and the SQLite database.
// This ensures workflows created by both the CLI and the Wails GUI are visible.
func newHybridStore(db *storage.Database) *workflow.HybridWorkflowStore {
	sqlStore := workflow.NewSQLiteWorkflowStore(db.DB)
	fileStore, err := workflow.NewWorkflowFileStore(expandPath("~/.monoagent/workflows"))
	if err != nil {
		// If file store can't be created, wrap SQLite-only in a hybrid shell
		// so callers always get the same type.
		return workflow.NewHybridWorkflowStore(nil, sqlStore)
	}
	return workflow.NewHybridWorkflowStore(fileStore, sqlStore)
}

// buildEngine constructs a fully wired WorkflowEngine suitable for CLI use.
// It creates its own scheduler (no action executor or store needed for workflow triggers).
// The returned cleanup func closes any browser session (extension tabs)
// opened by nodes the engine ran; callers should defer it alongside
// engine.Stop().
func buildEngine(cfg *globalConfig, allowAllProfiles bool) (*workflow.WorkflowEngine, func(), error) {
	db, err := initDB(cfg)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open database: %w", err)
	}

	registry := buildNodeRegistry(cfg.Verbose, db.DB)

	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
	if !cfg.Verbose {
		logger = logger.Level(zerolog.WarnLevel)
	}

	// Set up browser session provider, bot registry, and config manager
	// so browser/social nodes work in workflows. Chrome extension only —
	// no Rod/Chromium fallback, sharing another local process's connection
	// when one already exists (e.g. the daemon).
	extLogger := logger.With().Str("component", "extension").Logger()
	extBridge := setupExtensionBridge(extLogger, 30*time.Second)
	if !extBridge.IsConnected() {
		// No throwaway automation browser — launch the user's real Chrome
		// (same mechanism as `login`) so the extension can attach, then wait.
		if err := ensureExtensionConnected(extBridge, 30*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: %v\n", err)
		}
	}

	hybridProvider := &browserpkg.HybridSessionProvider{
		ExtBridge: extBridge,
		Logger:    extLogger,
	}
	nodes.SetGlobalSessionProvider(hybridProvider)
	nodes.SetGlobalBotRegistry(&cliBotRegistry{})
	nodes.SetGlobalCredentialStore(connections.NewStore(db.DB))

	cfgLogger := zerolog.New(os.Stderr).Level(zerolog.WarnLevel)
	cfgStore := cfgpkg.ConfigStore(&cfgpkg.DBConfigStore{DB: db})
	apiClient := cfgpkg.NewAPIClient(cfgLogger)
	rawCfgMgr := cfgpkg.NewConfigManager(expandPath("~/.monoagent/configs"), cfgStore, apiClient, cfgLogger)
	nodes.SetGlobalConfigMgr(&cfgpkg.ConfigManagerAdapter{Mgr: rawCfgMgr})

	sched := scheduler.NewScheduler(logger)
	sched.Start()

	engCfg := workflow.EngineConfig{
		MaxConcurrent:    5,
		QueueCapacity:    1000,
		PruneInterval:    time.Hour,
		MaxExecHistory:   500,
		ProfileID:        cfg.ProfileID,
		AllowAllProfiles: allowAllProfiles,
	}

	hybridStore := newHybridStore(db)
	engine := workflow.NewWorkflowEngineWithStore(hybridStore, db.DB, sched, registry, engCfg, logger)
	return engine, hybridProvider.Close, nil
}

// newWorkflowCmd returns the parent `workflow` cobra command with all subcommands attached.
func newWorkflowCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Manage and run workflows",
		Long: "Create, list, run, activate, deactivate, delete, and inspect executions of workflows.\n\n" +
			"For scheduled (trigger.schedule) or webhook (trigger.webhook) workflows to fire on their " +
			"own over time, `monoagentcli daemon` must be running continuously — `workflow activate` " +
			"alone only registers triggers for as long as that one command is running.",
	}

	cmd.AddCommand(
		newWorkflowListCmd(cfg),
		newWorkflowGetCmd(cfg),
		newWorkflowCreateCmd(cfg),
		newWorkflowImportCmd(cfg),
		newWorkflowExportCmd(cfg),
		newWorkflowRunCmd(cfg),
		newWorkflowActivateCmd(cfg),
		newWorkflowDeactivateCmd(cfg),
		newWorkflowDeleteCmd(cfg),
		newWorkflowExecutionsCmd(cfg),
		newWorkflowNodeCmd(cfg),
		newWorkflowConnectCmd(cfg),
		newWorkflowDisconnectCmd(cfg),
		newWorkflowMigrateCmd(cfg),
		newWorkflowTemplatesCmd(cfg),
		newWorkflowSearchCmd(cfg),
	)

	return cmd
}

// newWorkflowTemplatesCmd manages bundled, ready-to-use workflow templates
// (e.g. "Outlook Email Sync") that ship with the CLI/app and can be turned
// into a real, editable workflow with one command.
func newWorkflowTemplatesCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "templates",
		Short: "Browse and instantiate bundled ready-to-use workflow templates",
	}
	cmd.AddCommand(
		newWorkflowTemplatesListCmd(cfg),
		newWorkflowTemplatesShowCmd(cfg),
		newWorkflowTemplatesUseCmd(cfg),
		newWorkflowTemplatesRunCmd(cfg),
	)
	return cmd
}

// newWorkflowSearchCmd searches bundled templates AND the user's saved
// workflows in one place. This is the "what can this thing already do for me?"
// entry point — an agent that knows nothing but the binary name can run it,
// find something relevant, and get the exact command to run it.
func newWorkflowSearchCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "search [query]",
		Short: "Search bundled templates and saved workflows by name, description, or node type",
		Long: "Search everything runnable — bundled templates and the workflows saved for this profile — " +
			"by name, description, or the node types involved. Omit the query to list everything.\n\n" +
			"Each hit comes with the command that runs it.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = strings.ToLower(args[0])
			}
			matches := func(fields ...string) bool {
				if query == "" {
					return true
				}
				for _, f := range fields {
					if strings.Contains(strings.ToLower(f), query) {
						return true
					}
				}
				return false
			}

			type hit struct {
				Source  string   `json:"source"` // "template" or "workflow"
				ID      string   `json:"id"`
				Name    string   `json:"name"`
				Inputs  []string `json:"inputs,omitempty"`
				Active  bool     `json:"active,omitempty"`
				Command string   `json:"command"`
			}
			var hits []hit

			for _, t := range workflow.ListTemplates() {
				if !matches(t.ID, t.Name, t.Description) {
					continue
				}
				runCmd := "monoagentcli workflow templates run " + t.ID
				if ex := exampleInputJSON(t.Inputs); ex != "" {
					runCmd += " --input '" + ex + "'"
				}
				hits = append(hits, hit{Source: "template", ID: t.ID, Name: t.Name, Inputs: t.Inputs, Command: runCmd})
			}

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			ctx := context.Background()
			store := newHybridStore(db)
			saved, err := store.ListWorkflows(ctx, cfg.ProfileID)
			if err != nil {
				return fmt.Errorf("list workflows: %w", err)
			}
			for _, wf := range saved {
				nodeTypes := make([]string, 0, len(wf.Nodes))
				for _, n := range wf.Nodes {
					nodeTypes = append(nodeTypes, n.Type)
				}
				if !matches(append(nodeTypes, wf.ID, wf.Name, wf.Description)...) {
					continue
				}
				hits = append(hits, hit{
					Source:  "workflow",
					ID:      wf.ID,
					Name:    wf.Name,
					Active:  wf.IsActive,
					Command: "monoagentcli workflow run " + wf.ID,
				})
			}

			if cfg.JSONOutput {
				if hits == nil {
					hits = []hit{}
				}
				return json.NewEncoder(os.Stdout).Encode(hits)
			}

			if len(hits) == 0 {
				fmt.Fprintf(os.Stdout, "Nothing matches %q.\n", query)
				fmt.Fprintln(os.Stdout, "Try `monoagentcli workflow search` with no query, or `monoagentcli ref templates`.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SOURCE\tNAME\tRUN WITH")
			for _, h := range hits {
				fmt.Fprintf(w, "%s\t%s\t%s\n", h.Source, h.Name, h.Command)
			}
			return w.Flush()
		},
	}
}

// instantiateTemplate turns a bundled template into a concrete, unsaved
// Workflow owned by profileID. Node IDs in the template are stable,
// human-readable placeholders (e.g. "trigger") shared across every
// instantiation, so they are remapped to fresh UUIDs — that is what lets the
// same template be instantiated (and run) many times concurrently without the
// copies colliding.
func instantiateTemplate(tmplFile workflow.WorkflowFile, profileID string) workflow.Workflow {
	idMap := make(map[string]string, len(tmplFile.Nodes))
	for _, n := range tmplFile.Nodes {
		idMap[n.ID] = uuid.New().String()
	}

	now := time.Now().UTC()
	wf := workflow.Workflow{
		ID:          uuid.New().String(),
		Name:        tmplFile.Name,
		Description: tmplFile.Description,
		IsActive:    false,
		Version:     1,
		ProfileID:   profileID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	for _, fn := range tmplFile.Nodes {
		config := fn.Config
		if config == nil {
			config = map[string]interface{}{}
		}
		// people.sync_outlook_message scopes synced people/messages by
		// its own "profile_id" config field, independent of the
		// workflow's ProfileID — default it to this profile so the
		// template works correctly out of the box for non-default profiles.
		if fn.Type == "people.sync_outlook_message" {
			config["profile_id"] = profileID
		}
		wf.Nodes = append(wf.Nodes, workflow.WorkflowNode{
			ID:         idMap[fn.ID],
			WorkflowID: wf.ID,
			Type:       fn.Type,
			Name:       fn.Name,
			PositionX:  fn.Position.X,
			PositionY:  fn.Position.Y,
			Disabled:   fn.Disabled,
			Config:     config,
		})
	}
	for _, fe := range tmplFile.Connections {
		wf.Connections = append(wf.Connections, workflow.WorkflowConnection{
			ID:           uuid.New().String(),
			WorkflowID:   wf.ID,
			SourceNodeID: idMap[fe.Source],
			SourceHandle: fe.SourceHandle,
			TargetNodeID: idMap[fe.Target],
			TargetHandle: fe.TargetHandle,
		})
	}
	return wf
}

// inputKeyList renders a template's trigger-data keys for table output.
func inputKeyList(inputs []string) string {
	if len(inputs) == 0 {
		return "(none)"
	}
	return strings.Join(inputs, ",")
}

// exampleInputJSON builds a copy-pasteable --input value for a template, so an
// agent can run it without reading the node configs first.
func exampleInputJSON(inputs []string) string {
	if len(inputs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(inputs))
	for _, key := range inputs {
		// Plural keys are arrays in every bundled template (prompts, urls, …).
		if strings.HasSuffix(key, "s") {
			parts = append(parts, fmt.Sprintf("%q:[\"...\",\"...\"]", key))
		} else {
			parts = append(parts, fmt.Sprintf("%q:\"...\"", key))
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// newWorkflowTemplatesShowCmd prints everything needed to run one template.
func newWorkflowTemplatesShowCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "show <template-id>",
		Short: "Show a template's description, inputs, nodes, and the exact command to run it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tmplFile, ok := workflow.GetTemplate(args[0])
			if !ok {
				return fmt.Errorf("unknown template %q. Run `monoagentcli workflow templates list` to see available IDs", args[0])
			}
			var meta workflow.Template
			for _, t := range workflow.ListTemplates() {
				if t.ID == args[0] {
					meta = t
					break
				}
			}

			if cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
					"id":          meta.ID,
					"name":        tmplFile.Name,
					"description": tmplFile.Description,
					"inputs":      meta.Inputs,
					"definition":  tmplFile,
				})
			}

			fmt.Fprintf(os.Stdout, "%s  (%s)\n\n%s\n\n", tmplFile.Name, args[0], tmplFile.Description)
			fmt.Fprintf(os.Stdout, "INPUT KEYS: %s\n\n", inputKeyList(meta.Inputs))

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NODE\tTYPE")
			for _, n := range tmplFile.Nodes {
				fmt.Fprintf(w, "%s\t%s\n", n.Name, n.Type)
			}
			if err := w.Flush(); err != nil {
				return err
			}

			fmt.Fprintf(os.Stdout, "\nRun it:\n  monoagentcli workflow templates run %s", args[0])
			if ex := exampleInputJSON(meta.Inputs); ex != "" {
				fmt.Fprintf(os.Stdout, " --input '%s'", ex)
			}
			fmt.Fprintln(os.Stdout)
			fmt.Fprintf(os.Stdout, "\nSave an editable copy instead:\n  monoagentcli workflow templates use %s\n", args[0])
			fmt.Fprintln(os.Stdout, "\nNode config details:  monoagentcli ref node <type>")
			return nil
		},
	}
}

func newWorkflowTemplatesListCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List bundled workflow templates",
		RunE: func(cmd *cobra.Command, args []string) error {
			templates := workflow.ListTemplates()
			if cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(templates)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tINPUT KEYS\tNAME\tDESCRIPTION")
			for _, t := range templates {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.ID, inputKeyList(t.Inputs), t.Name, t.Description)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, "\nRun one:     monoagentcli workflow templates run <id> --input '{\"<key>\":\"...\"}'")
			fmt.Fprintln(os.Stdout, "Inspect one: monoagentcli workflow templates show <id>")
			return nil
		},
	}
	return cmd
}

func newWorkflowTemplatesUseCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "use <template-id>",
		Short: "Instantiate a bundled template as a new, editable workflow",
		Long: "Creates a new workflow for the current profile from a bundled template " +
			"(see `workflow templates list` for IDs). The workflow starts inactive — " +
			"fill in any required credentials (e.g. the Outlook account) via `workflow node set` " +
			"or the app, then `workflow activate` it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tmplFile, ok := workflow.GetTemplate(args[0])
			if !ok {
				return fmt.Errorf("unknown template %q. Run `monoagentcli workflow templates list` to see available IDs", args[0])
			}

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			wf := instantiateTemplate(tmplFile, cfg.ProfileID)

			store := newHybridStore(db)
			ctx := context.Background()
			if err := store.CreateWorkflow(ctx, &wf); err != nil {
				return fmt.Errorf("create workflow: %w", err)
			}
			for i := range wf.Nodes {
				if err := wf.Nodes[i].MarshalConfig(); err != nil {
					return fmt.Errorf("marshal node config: %w", err)
				}
			}
			if err := store.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes); err != nil {
				return fmt.Errorf("save nodes: %w", err)
			}
			if err := store.SaveWorkflowConnections(ctx, wf.ID, wf.Connections); err != nil {
				return fmt.Errorf("save connections: %w", err)
			}

			if cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(wf)
			}
			fmt.Fprintf(os.Stdout, "Created workflow %q from template %q  (id: %s, %d nodes, %d connections)\n",
				wf.Name, args[0], wf.ID, len(wf.Nodes), len(wf.Connections))
			fmt.Fprintln(os.Stdout, "It starts inactive — set any required credentials, then `monoagentcli workflow activate "+wf.ID+"`.")
			return nil
		},
	}
	return cmd
}

// newWorkflowListCmd lists all workflows.
func newWorkflowListCmd(cfg *globalConfig) *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all workflows",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := newHybridStore(db)
			ctx := context.Background()

			workflows, err := store.ListWorkflows(ctx, cfg.ProfileID)
			if err != nil {
				return fmt.Errorf("list workflows: %w", err)
			}

			if jsonOut || cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(workflows)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tACTIVE\tVERSION\tUPDATED AT")
			for _, wf := range workflows {
				active := "false"
				if wf.IsActive {
					active = "true"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n",
					wf.ID, wf.Name, active, wf.Version,
					wf.UpdatedAt.Format(time.RFC3339),
				)
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

// newWorkflowGetCmd prints a single workflow as JSON.
func newWorkflowGetCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Print a workflow as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := newHybridStore(db)
			ctx := context.Background()

			wf, err := store.GetWorkflow(ctx, args[0])
			if err != nil {
				return fmt.Errorf("get workflow: %w", err)
			}
			if wf == nil {
				return fmt.Errorf("workflow %q not found", args[0])
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(wf)
		},
	}
}

// waitForExecution polls until the execution leaves RUNNING/QUEUED, printing
// its final status, or returns an error once timeout elapses.
func waitForExecution(ctx context.Context, engine *workflow.WorkflowEngine, executionID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			exec, err := engine.GetExecution(ctx, executionID)
			if err != nil {
				return fmt.Errorf("poll execution: %w", err)
			}
			switch exec.Status {
			case "RUNNING", "QUEUED":
				if time.Now().After(deadline) {
					return fmt.Errorf("timed out waiting for execution %s (still %s)", executionID, exec.Status)
				}
				// keep polling
			default:
				if exec.ErrorMessage != "" {
					fmt.Fprintf(os.Stdout, "Status: %s\nError:  %s\n", exec.Status, exec.ErrorMessage)
				} else {
					fmt.Fprintf(os.Stdout, "Status: %s\n", exec.Status)
				}
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// newWorkflowTemplatesRunCmd instantiates a bundled template into a throwaway
// workflow, runs it once, and deletes it. Because each invocation gets its own
// workflow (fresh IDs), the same template can be run many times side by side —
// e.g. several image generations at once — without the runs interfering, and
// without leaving saved workflows behind.
func newWorkflowTemplatesRunCmd(cfg *globalConfig) *cobra.Command {
	var inputJSON string
	var keep bool

	cmd := &cobra.Command{
		Use:   "run <template-id>",
		Short: "Run a bundled template once, without saving a workflow",
		Long: "Instantiates a bundled template as a temporary workflow, triggers it, waits for it " +
			"to finish, then deletes it. Safe to run several times concurrently — each run is a " +
			"separate throwaway workflow. Use --keep to leave the instantiated workflow behind for editing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tmplFile, ok := workflow.GetTemplate(args[0])
			if !ok {
				return fmt.Errorf("unknown template %q. Run `monoagentcli workflow templates list` to see available IDs", args[0])
			}

			triggerData := map[string]interface{}{}
			if inputJSON != "" {
				if err := json.Unmarshal([]byte(inputJSON), &triggerData); err != nil {
					return fmt.Errorf("invalid --input JSON (expected a JSON object): %w", err)
				}
			}

			engine, closeBrowsers, err := buildEngine(cfg, false)
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			ctx := context.Background()
			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			wf := instantiateTemplate(tmplFile, cfg.ProfileID)
			for i := range wf.Nodes {
				if err := wf.Nodes[i].MarshalConfig(); err != nil {
					return fmt.Errorf("marshal node config: %w", err)
				}
			}
			if err := engine.CreateWorkflow(ctx, &wf); err != nil {
				return fmt.Errorf("create workflow from template: %w", err)
			}
			if !keep {
				defer func() {
					if err := engine.DeleteWorkflow(context.Background(), wf.ID); err != nil {
						fmt.Fprintf(os.Stderr, "warning: could not delete temporary workflow %s: %v\n", wf.ID, err)
					}
				}()
			}
			if err := engine.ActivateWorkflow(ctx, wf.ID); err != nil {
				return fmt.Errorf("activate workflow: %w", err)
			}

			executionID, err := engine.TriggerWorkflow(ctx, wf.ID, triggerData)
			if err != nil {
				return fmt.Errorf("trigger workflow: %w", err)
			}
			fmt.Fprintf(os.Stdout, "Running template %q (workflow %s, execution %s)\n", args[0], wf.ID, executionID)

			runErr := waitForExecution(ctx, engine, executionID, 15*time.Minute)
			if keep {
				fmt.Fprintf(os.Stdout, "Kept workflow %s\n", wf.ID)
			}
			return runErr
		},
	}

	cmd.Flags().StringVar(&inputJSON, "input", "", `Trigger data as a JSON object, e.g. --input '{"prompts":["a wizard","the same wizard on a dragon"]}'`)
	cmd.Flags().BoolVar(&keep, "keep", false, "Keep the instantiated workflow instead of deleting it after the run")
	return cmd
}

// newWorkflowRunCmd manually triggers a workflow and polls for completion.
func newWorkflowRunCmd(cfg *globalConfig) *cobra.Command {
	var inputJSON string

	cmd := &cobra.Command{
		Use:   "run <id>",
		Short: "Manually trigger a workflow and wait for it to complete",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := args[0]

			triggerData := map[string]interface{}{}
			if inputJSON != "" {
				if err := json.Unmarshal([]byte(inputJSON), &triggerData); err != nil {
					return fmt.Errorf("invalid --input JSON (expected a JSON object): %w", err)
				}
			}

			engine, closeBrowsers, err := buildEngine(cfg, false)
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			ctx := context.Background()
			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			executionID, err := engine.TriggerWorkflow(ctx, workflowID, triggerData)
			if err != nil {
				return fmt.Errorf("trigger workflow: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Execution started: %s\n", executionID)
			return waitForExecution(ctx, engine, executionID, 15*time.Minute)
		},
	}

	cmd.Flags().StringVar(&inputJSON, "input", "", `Trigger data as a JSON object, e.g. --input '{"prompt":"a red bicycle"}' (available downstream as {{ $json.<field> }})`)
	return cmd
}

// newWorkflowActivateCmd enables a workflow and registers its triggers.
func newWorkflowActivateCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "activate <id>",
		Short: "Activate a workflow and start its triggers",
		Long: "Activate a workflow and start its triggers.\n\n" +
			"IMPORTANT: activation only registers triggers for the lifetime of this command. " +
			"For a trigger.schedule or trigger.webhook to actually fire later, `monoagentcli daemon` " +
			"must be running continuously (it restores every active workflow's triggers on startup).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			engine, closeBrowsers, err := buildEngine(cfg, false)
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			ctx := context.Background()
			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			if err := engine.ActivateWorkflow(ctx, args[0]); err != nil {
				return fmt.Errorf("activate workflow: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Workflow %s activated.\n", args[0])
			fmt.Fprintln(os.Stdout, "Note: run `monoagentcli daemon` (kept running continuously) for this workflow's triggers to actually fire — activation alone only registers them for this command's lifetime.")
			return nil
		},
	}
}

// newWorkflowDeactivateCmd disables a workflow and unregisters its triggers.
func newWorkflowDeactivateCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "deactivate <id>",
		Short: "Deactivate a workflow and stop its triggers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			engine, closeBrowsers, err := buildEngine(cfg, false)
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			ctx := context.Background()
			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			if err := engine.DeactivateWorkflow(ctx, args[0]); err != nil {
				return fmt.Errorf("deactivate workflow: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Workflow %s deactivated.\n", args[0])
			return nil
		},
	}
}

// newWorkflowDeleteCmd deletes a workflow (with confirmation unless --force).
func newWorkflowDeleteCmd(cfg *globalConfig) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := args[0]

			if !force {
				fmt.Fprintf(os.Stdout, "Delete workflow %q? This is irreversible. [y/N] ", workflowID)
				reader := bufio.NewReader(os.Stdin)
				answer, _ := reader.ReadString('\n')
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer != "y" && answer != "yes" {
					fmt.Fprintln(os.Stdout, "Aborted.")
					return nil
				}
			}

			engine, closeBrowsers, err := buildEngine(cfg, false)
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			ctx := context.Background()
			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			if err := engine.DeleteWorkflow(ctx, workflowID); err != nil {
				return fmt.Errorf("delete workflow: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Workflow %s deleted.\n", workflowID)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	return cmd
}

// newWorkflowExecutionsCmd lists recent executions for a workflow.
func newWorkflowExecutionsCmd(cfg *globalConfig) *cobra.Command {
	var limit int
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "executions <workflow-id>",
		Short: "List recent executions for a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := newHybridStore(db)
			ctx := context.Background()

			executions, err := store.ListExecutions(ctx, args[0], limit)
			if err != nil {
				return fmt.Errorf("list executions: %w", err)
			}

			if jsonOut || cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(executions)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATUS\tTRIGGER TYPE\tSTARTED AT\tFINISHED AT\tERROR")
			for _, e := range executions {
				startedAt := ""
				if e.StartedAt != nil {
					startedAt = e.StartedAt.Format(time.RFC3339)
				}
				finishedAt := ""
				if e.FinishedAt != nil {
					finishedAt = e.FinishedAt.Format(time.RFC3339)
				}
				errStr := e.ErrorMessage
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					e.ID, e.Status, e.TriggerType,
					startedAt, finishedAt, errStr,
				)
			}
			return w.Flush()
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of executions to show (0 = all)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

// newWorkflowCreateCmd creates a blank workflow and prints its ID.
func newWorkflowCreateCmd(cfg *globalConfig) *cobra.Command {
	var description string
	var active bool

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new blank workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			now := time.Now().UTC()
			wf := &workflow.Workflow{
				ID:          uuid.New().String(),
				Name:        args[0],
				Description: description,
				IsActive:    active,
				Version:     1,
				ProfileID:   cfg.ProfileID,
				CreatedAt:   now,
				UpdatedAt:   now,
			}

			store := newHybridStore(db)
			ctx := context.Background()
			if err := store.CreateWorkflow(ctx, wf); err != nil {
				return fmt.Errorf("create workflow: %w", err)
			}

			if cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(wf)
			}
			fmt.Fprintf(os.Stdout, "Created workflow: %s  (id: %s)\n", wf.Name, wf.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&description, "description", "", "Workflow description")
	cmd.Flags().BoolVar(&active, "active", false, "Mark workflow as active immediately")
	return cmd
}

// newWorkflowImportCmd imports a full workflow definition from a JSON file.
func newWorkflowImportCmd(cfg *globalConfig) *cobra.Command {
	var inputFile string
	var overwrite bool

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a workflow from a JSON file (--file or stdin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw []byte
			var err error
			if inputFile != "" {
				raw, err = os.ReadFile(inputFile)
			} else {
				raw, err = io.ReadAll(os.Stdin)
			}
			if err != nil {
				return fmt.Errorf("read input: %w", err)
			}

			// Try to parse as a WorkflowFile (new JSON file format with "type",
			// "source", "target" keys). Fall back to direct Workflow unmarshal for
			// legacy exports that already use "node_type", "source_node_id" etc.
			wf, parseErr := workflow.ParseWorkflowFileBytes(raw)
			if parseErr != nil {
				// Legacy format fallback.
				var legacyWF workflow.Workflow
				if err2 := json.Unmarshal(raw, &legacyWF); err2 != nil {
					return fmt.Errorf("parse JSON: %w", parseErr)
				}
				wf = legacyWF
			}

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := newHybridStore(db)
			ctx := context.Background()

			now := time.Now().UTC()

			// Assign a fresh ID unless --overwrite is requested and one is present.
			if !overwrite || wf.ID == "" {
				wf.ID = uuid.New().String()
			}
			wf.CreatedAt = now
			wf.UpdatedAt = now

			if err := store.CreateWorkflow(ctx, &wf); err != nil {
				return fmt.Errorf("save workflow: %w", err)
			}

			// Parse config for each node before saving.
			nodes := wf.Nodes
			for i := range nodes {
				nodes[i].WorkflowID = wf.ID
				if nodes[i].ID == "" {
					nodes[i].ID = uuid.New().String()
				}
				if nodes[i].Config != nil {
					if err := nodes[i].MarshalConfig(); err != nil {
						return fmt.Errorf("marshal node config: %w", err)
					}
				}
			}
			if err := store.SaveWorkflowNodes(ctx, wf.ID, nodes); err != nil {
				return fmt.Errorf("save nodes: %w", err)
			}

			conns := wf.Connections
			for i := range conns {
				conns[i].WorkflowID = wf.ID
				if conns[i].ID == "" {
					conns[i].ID = uuid.New().String()
				}
			}
			if err := store.SaveWorkflowConnections(ctx, wf.ID, conns); err != nil {
				return fmt.Errorf("save connections: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Imported workflow %q as id: %s  (%d nodes, %d connections)\n",
				wf.Name, wf.ID, len(nodes), len(conns))
			return nil
		},
	}

	cmd.Flags().StringVarP(&inputFile, "file", "f", "", "Path to JSON file (default: stdin)")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Keep the id from the file instead of generating a new one")
	return cmd
}

// newWorkflowExportCmd exports a workflow as JSON.
func newWorkflowExportCmd(cfg *globalConfig) *cobra.Command {
	var outputFile string

	cmd := &cobra.Command{
		Use:   "export <id>",
		Short: "Export a workflow as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := newHybridStore(db)
			ctx := context.Background()

			wf, err := store.GetWorkflow(ctx, args[0])
			if err != nil {
				return fmt.Errorf("get workflow: %w", err)
			}
			if wf == nil {
				return fmt.Errorf("workflow %q not found", args[0])
			}

			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if outputFile != "" {
				f, err := os.Create(outputFile)
				if err != nil {
					return fmt.Errorf("create output file: %w", err)
				}
				defer f.Close()
				enc = json.NewEncoder(f)
				enc.SetIndent("", "  ")
				if err := enc.Encode(wf); err != nil {
					return err
				}
				fmt.Fprintf(os.Stdout, "Exported workflow %q to %s\n", wf.Name, outputFile)
				return nil
			}
			return enc.Encode(wf)
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Write to file instead of stdout")
	return cmd
}

// ── node subcommand group ────────────────────────────────────────────────────

// newWorkflowNodeCmd is the `workflow node` parent command.
func newWorkflowNodeCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Manage nodes within a workflow",
	}
	cmd.AddCommand(
		newWorkflowNodeAddCmd(cfg),
		newWorkflowNodeListCmd(cfg),
		newWorkflowNodeSetCmd(cfg),
		newWorkflowNodeRemoveCmd(cfg),
	)
	return cmd
}

// newWorkflowNodeAddCmd adds a node to a workflow.
func newWorkflowNodeAddCmd(cfg *globalConfig) *cobra.Command {
	var nodeType, name, configJSON string
	var posX, posY float64

	cmd := &cobra.Command{
		Use:   "add <workflow-id>",
		Short: "Add a node to a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := args[0]

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := newHybridStore(db)
			ctx := context.Background()

			// Ensure workflow exists.
			wf, err := store.GetWorkflow(ctx, workflowID)
			if err != nil || wf == nil {
				return fmt.Errorf("workflow %q not found", workflowID)
			}

			// Fetch existing nodes so we can append.
			existing := wf.Nodes

			// Parse --config JSON.
			var config map[string]interface{}
			if configJSON != "" {
				if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
					return fmt.Errorf("parse --config JSON: %w", err)
				}
			} else {
				config = make(map[string]interface{})
			}

			newNode := workflow.WorkflowNode{
				ID:         uuid.New().String(),
				WorkflowID: workflowID,
				Type:       nodeType,
				Name:       name,
				Config:     config,
				PositionX:  posX,
				PositionY:  posY,
			}
			if err := newNode.MarshalConfig(); err != nil {
				return fmt.Errorf("marshal config: %w", err)
			}

			existing = append(existing, newNode)
			if err := store.SaveWorkflowNodes(ctx, workflowID, existing); err != nil {
				return fmt.Errorf("save nodes: %w", err)
			}

			if cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(newNode)
			}
			fmt.Fprintf(os.Stdout, "Added node %s (type: %s, id: %s)\n", newNode.Name, newNode.Type, newNode.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&nodeType, "type", "", "Node type (e.g. core.if, trigger.schedule) [required]")
	cmd.Flags().StringVar(&name, "name", "", "Display name for the node [required]")
	cmd.Flags().StringVar(&configJSON, "config", "", "Node configuration as JSON object")
	cmd.Flags().Float64Var(&posX, "x", 0, "Canvas X position")
	cmd.Flags().Float64Var(&posY, "y", 0, "Canvas Y position")
	_ = cmd.MarkFlagRequired("type")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

// newWorkflowNodeListCmd lists the nodes in a workflow.
func newWorkflowNodeListCmd(cfg *globalConfig) *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list <workflow-id>",
		Short: "List nodes in a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := newHybridStore(db)
			ctx := context.Background()

			wf, err := store.GetWorkflow(ctx, args[0])
			if err != nil || wf == nil {
				return fmt.Errorf("workflow %q not found", args[0])
			}

			if jsonOut || cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(wf.Nodes)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTYPE\tNAME\tX\tY\tCONFIG")
			for _, n := range wf.Nodes {
				configStr := n.ConfigRaw
				if len(configStr) > 60 {
					configStr = configStr[:57] + "..."
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%.0f\t%.0f\t%s\n",
					n.ID, n.Type, n.Name, n.PositionX, n.PositionY, configStr)
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

// newWorkflowNodeSetCmd updates a node's configuration and/or position.
func newWorkflowNodeSetCmd(cfg *globalConfig) *cobra.Command {
	var configJSON, name string
	var posX, posY float64
	var setPosX, setPosY bool

	cmd := &cobra.Command{
		Use:   "set <workflow-id> <node-id>",
		Short: "Update a node's config, name, or position",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID, nodeID := args[0], args[1]

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := newHybridStore(db)
			ctx := context.Background()

			wf, err := store.GetWorkflow(ctx, workflowID)
			if err != nil || wf == nil {
				return fmt.Errorf("workflow %q not found", workflowID)
			}

			found := false
			for i := range wf.Nodes {
				if wf.Nodes[i].ID != nodeID {
					continue
				}
				found = true
				if name != "" {
					wf.Nodes[i].Name = name
				}
				if configJSON != "" {
					var config map[string]interface{}
					if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
						return fmt.Errorf("parse --config JSON: %w", err)
					}
					wf.Nodes[i].Config = config
					if err := wf.Nodes[i].MarshalConfig(); err != nil {
						return fmt.Errorf("marshal config: %w", err)
					}
				}
				if setPosX {
					wf.Nodes[i].PositionX = posX
				}
				if setPosY {
					wf.Nodes[i].PositionY = posY
				}
				break
			}
			if !found {
				return fmt.Errorf("node %q not found in workflow %q", nodeID, workflowID)
			}

			if err := store.SaveWorkflowNodes(ctx, workflowID, wf.Nodes); err != nil {
				return fmt.Errorf("save nodes: %w", err)
			}
			fmt.Fprintf(os.Stdout, "Node %s updated.\n", nodeID)
			return nil
		},
	}

	cmd.Flags().StringVar(&configJSON, "config", "", "New configuration as JSON object")
	cmd.Flags().StringVar(&name, "name", "", "New display name")
	cmd.Flags().Float64Var(&posX, "x", 0, "Canvas X position")
	cmd.Flags().Float64Var(&posY, "y", 0, "Canvas Y position")
	// Detect whether --x/--y were actually provided.
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		setPosX = cmd.Flags().Changed("x")
		setPosY = cmd.Flags().Changed("y")
		return nil
	}
	return cmd
}

// newWorkflowNodeRemoveCmd removes a node (and its connections) from a workflow.
func newWorkflowNodeRemoveCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <workflow-id> <node-id>",
		Short: "Remove a node from a workflow",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID, nodeID := args[0], args[1]

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := newHybridStore(db)
			ctx := context.Background()

			wf, err := store.GetWorkflow(ctx, workflowID)
			if err != nil || wf == nil {
				return fmt.Errorf("workflow %q not found", workflowID)
			}

			newNodes := wf.Nodes[:0]
			found := false
			for _, n := range wf.Nodes {
				if n.ID == nodeID {
					found = true
					continue
				}
				newNodes = append(newNodes, n)
			}
			if !found {
				return fmt.Errorf("node %q not found in workflow %q", nodeID, workflowID)
			}

			// Drop connections that reference the removed node.
			newConns := wf.Connections[:0]
			for _, c := range wf.Connections {
				if c.SourceNodeID == nodeID || c.TargetNodeID == nodeID {
					continue
				}
				newConns = append(newConns, c)
			}

			if err := store.SaveWorkflowNodes(ctx, workflowID, newNodes); err != nil {
				return fmt.Errorf("save nodes: %w", err)
			}
			if err := store.SaveWorkflowConnections(ctx, workflowID, newConns); err != nil {
				return fmt.Errorf("save connections: %w", err)
			}

			fmt.Fprintf(os.Stdout, "Node %s removed.\n", nodeID)
			return nil
		},
	}
}

// ── connect / disconnect ─────────────────────────────────────────────────────

// newWorkflowConnectCmd adds an edge between two nodes.
func newWorkflowConnectCmd(cfg *globalConfig) *cobra.Command {
	var fromStr, toStr string

	cmd := &cobra.Command{
		Use:   "connect <workflow-id>",
		Short: "Connect two nodes  (--from nodeID:handle --to nodeID:handle)",
		Long: `Add a connection between two nodes.

  --from and --to accept "nodeID:handle" or just "nodeID" (handle defaults to "main").

  Example:
    monoagentcli workflow connect wf1 --from abc123:main --to def456:main`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := args[0]

			srcID, srcHandle, err := parseNodeHandle(fromStr)
			if err != nil {
				return fmt.Errorf("--from: %w", err)
			}
			dstID, dstHandle, err := parseNodeHandle(toStr)
			if err != nil {
				return fmt.Errorf("--to: %w", err)
			}

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := newHybridStore(db)
			ctx := context.Background()

			wf, err := store.GetWorkflow(ctx, workflowID)
			if err != nil || wf == nil {
				return fmt.Errorf("workflow %q not found", workflowID)
			}

			conn := workflow.WorkflowConnection{
				ID:           uuid.New().String(),
				WorkflowID:   workflowID,
				SourceNodeID: srcID,
				SourceHandle: srcHandle,
				TargetNodeID: dstID,
				TargetHandle: dstHandle,
				Position:     len(wf.Connections),
			}
			wf.Connections = append(wf.Connections, conn)

			if err := store.SaveWorkflowConnections(ctx, workflowID, wf.Connections); err != nil {
				return fmt.Errorf("save connections: %w", err)
			}

			if cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(conn)
			}
			fmt.Fprintf(os.Stdout, "Connected %s:%s → %s:%s  (id: %s)\n",
				srcID, srcHandle, dstID, dstHandle, conn.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&fromStr, "from", "", "Source node: nodeID[:handle]  (required)")
	cmd.Flags().StringVar(&toStr, "to", "", "Target node: nodeID[:handle]  (required)")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

// newWorkflowDisconnectCmd removes a connection by its ID.
func newWorkflowDisconnectCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect <workflow-id> <connection-id>",
		Short: "Remove a connection from a workflow",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID, connID := args[0], args[1]

			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := newHybridStore(db)
			ctx := context.Background()

			wf, err := store.GetWorkflow(ctx, workflowID)
			if err != nil || wf == nil {
				return fmt.Errorf("workflow %q not found", workflowID)
			}

			newConns := wf.Connections[:0]
			found := false
			for _, c := range wf.Connections {
				if c.ID == connID {
					found = true
					continue
				}
				newConns = append(newConns, c)
			}
			if !found {
				return fmt.Errorf("connection %q not found in workflow %q", connID, workflowID)
			}

			if err := store.SaveWorkflowConnections(ctx, workflowID, newConns); err != nil {
				return fmt.Errorf("save connections: %w", err)
			}
			fmt.Fprintf(os.Stdout, "Connection %s removed.\n", connID)
			return nil
		},
	}
}

// newWorkflowMigrateCmd migrates workflows from SQLite to JSON files.
func newWorkflowMigrateCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Migrate workflows from SQLite to JSON files",
		RunE:  runWorkflowMigrate(cfg),
	}
}

func runWorkflowMigrate(cfg *globalConfig) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		db, err := initDB(cfg)
		if err != nil {
			return fmt.Errorf("open sqlite store: %w", err)
		}
		defer db.Close()

		sqliteStore := workflow.NewSQLiteWorkflowStore(db.DB)

		wfDir := filepath.Join(os.Getenv("HOME"), ".monoagent", "workflows")
		fileStore, err := workflow.NewWorkflowFileStore(wfDir)
		if err != nil {
			return fmt.Errorf("open file store: %w", err)
		}

		workflows, err := sqliteStore.ListWorkflows(ctx, "")
		if err != nil {
			return fmt.Errorf("list workflows from sqlite: %w", err)
		}

		fmt.Printf("Found %d workflows in SQLite. Migrating to %s...\n", len(workflows), wfDir)

		var migrated, skipped int
		for _, wf := range workflows {
			full, err := sqliteStore.GetWorkflow(ctx, wf.ID)
			if err != nil {
				fmt.Printf("  SKIP %s (%s): load error: %v\n", wf.ID, wf.Name, err)
				skipped++
				continue
			}
			if full == nil {
				skipped++
				continue
			}

			for i, n := range full.Nodes {
				if n.Schema == nil {
					schema, err := workflow.LoadDefaultSchema(n.Type)
					if err != nil {
						schema = &workflow.NodeSchema{Fields: []workflow.NodeSchemaField{}}
					}
					full.Nodes[i].Schema = schema
				}
			}

			if err := fileStore.SaveWorkflow(ctx, full); err != nil {
				fmt.Printf("  SKIP %s (%s): write error: %v\n", wf.ID, wf.Name, err)
				skipped++
				continue
			}
			fmt.Printf("  OK   %s (%s)\n", full.ID, full.Name)
			migrated++
		}

		fmt.Printf("\nMigration complete: %d migrated, %d skipped.\n", migrated, skipped)
		fmt.Println("SQLite data was NOT modified — safe to roll back.")
		return nil
	}
}

// parseNodeHandle parses "nodeID:handle" or "nodeID" (defaulting handle to "main").
func parseNodeHandle(s string) (nodeID, handle string, err error) {
	if s == "" {
		return "", "", fmt.Errorf("value is required")
	}
	parts := strings.SplitN(s, ":", 2)
	nodeID = parts[0]
	if len(parts) == 2 && parts[1] != "" {
		handle = parts[1]
	} else {
		handle = "main"
	}
	return nodeID, handle, nil
}

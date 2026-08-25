package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"monoagent/internal/ai"
	aichat "monoagent/internal/ai/chat"
	"monoagent/internal/monomind"
	"monoagent/internal/noderegistry"
)

// parseDurationFlag accepts plain seconds ("90") or suffixed ("90s", "10m", "2h").
func parseDurationFlag(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	var secs float64
	if _, err := fmt.Sscanf(s, "%f", &secs); err == nil {
		return time.Duration(secs * float64(time.Second)), nil
	}
	return 0, fmt.Errorf("invalid duration %q (use e.g. 90s, 10m, 2h)", s)
}

func newChatCmd(cfg *globalConfig) *cobra.Command {
	var (
		runtime  string
		model    string
		resume   string
		canvasID string
		timeoutS string
		budget   float64
	)
	cmd := &cobra.Command{
		Use:   "chat [prompt]",
		Short: "Chat with a locally-installed AI agent (via the monomind engine)",
		Long: "One agent turn delegated through monomind's Agent Exec Protocol. Streams NDJSON " +
			"events to stdout (start, session, assistant, tool_call, tool_result, usage, result, " +
			"done) — this stream is the interface the desktop app consumes.\n\n" +
			"With --canvas <workflowID> the turn runs as a workflow-builder assistant: the eight " +
			"canvas tools (create workflow/nodes/connections, …) execute against this machine's " +
			"database over the stdio tool bridge, and the conversation is persisted to the chat " +
			"history table.",
		Args: cobra.MinimumNArgs(1),
		Example: `  monoagentcli chat --runtime claude "summarize the output folder"
  monoagentcli chat --runtime codex --canvas general "build a gmail digest workflow"
  monoagentcli chat --runtime claude --resume th_9f2a "continue"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime == "" {
				return fmt.Errorf("--runtime is required (see `agent scan --installed`)")
			}
			prompt := strings.Join(args, " ")
			timeout, err := parseDurationFlag(timeoutS)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			bin, _, err := monomind.Ensure(ctx)
			if err != nil {
				return err
			}

			var (
				store     *ai.AIStore
				closeDB   func()
				canvas    *aichat.CanvasTools
				nodeTypes []aichat.NodeTypeInfo
				profileID string
			)
			if canvasID != "" {
				store, closeDB, err = openAIStore(cfg)
				if err != nil {
					return err
				}
				defer closeDB()
				db, err := initDB(cfg)
				if err != nil {
					return fmt.Errorf("initializing database: %w", err)
				}
				defer db.Close()
				canvas = aichat.NewCanvasTools(db.DB)
				profileID, err = resolveProfileID(db.DB, cfg.ProfileID)
				if err != nil {
					return fmt.Errorf("resolving profile: %w", err)
				}
				canvas.SetProfileID(profileID)
				nodeTypes = registryNodeTypes(db.DB)
				canvas.SetNodeTypes(nodeTypes)
				if err := canvas.CheckWorkflowOwnership(canvasID); err != nil {
					return err
				}
				if err := store.SaveChatMessage(ai.ChatMessage{
					ID:         uuid.NewString(),
					WorkflowID: canvasID,
					Role:       "user",
					Content:    prompt,
					ProviderID: runtime,
					Model:      model,
				}); err != nil {
					return fmt.Errorf("saving chat message: %w", err)
				}
			}

			opts := monomind.ExecOptions{
				Bin:       bin,
				Runtime:   runtime,
				Prompt:    prompt,
				Model:     model,
				Resume:    resume,
				Timeout:   timeout,
				BudgetUSD: budget,
			}
			if canvas != nil {
				opts.SystemPrompt = canvasSystemPrompt(canvasID, nodeTypes)
				opts.Tools = canvasToolSpecs(canvas)
				opts.ToolTimeout = 2 * time.Minute
				opts.OnToolCall = func(ctx context.Context, name string, args json.RawMessage) (string, error) {
					return canvas.Execute(name, string(args))
				}
			}

			emit := func(v interface{}) {
				b, _ := json.Marshal(v)
				fmt.Println(string(b))
			}
			// Subprocess runners' result events carry usage but not always
			// text — accumulate assistant prose for history persistence.
			var assistantText strings.Builder
			res, err := monomind.Exec(ctx, opts, func(ev monomind.Event) {
				emit(ev)
				if ev.Type == monomind.EventAssistant && ev.Text != "" {
					assistantText.WriteString(ev.Text)
					if !strings.HasSuffix(ev.Text, "\n") {
						assistantText.WriteString("\n")
					}
				}
			})
			if err != nil {
				return err
			}
			if res.Err != nil {
				// Error events already streamed; surface the failure on stderr
				// and exit with the turn's protocol code.
				fmt.Fprintf(os.Stderr, "chat: %v\n", res.Err)
				os.Exit(res.Err.ExitCode)
			}
			if canvas != nil {
				final := res.ResultText
				if final == "" {
					final = assistantText.String()
				}
				if final != "" {
					if err := store.SaveChatMessage(ai.ChatMessage{
						ID:         uuid.NewString(),
						WorkflowID: canvasID,
						Role:       "assistant",
						Content:    final,
						ProviderID: runtime,
						Model:      model,
					}); err != nil {
						fmt.Fprintf(os.Stderr, "warning: saving chat message: %v\n", err)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "", "Agent runtime id (claude, codex, kimi, … — see `agent scan`)")
	cmd.Flags().StringVar(&model, "model", "", "Model override for the runtime")
	cmd.Flags().StringVar(&resume, "resume", "", "Session/thread id to resume (from the session event)")
	cmd.Flags().StringVar(&canvasID, "canvas", "", "Workflow-builder mode for this workflow id")
	cmd.Flags().StringVar(&timeoutS, "timeout", "", "Overall timeout (e.g. 90s, 10m)")
	cmd.Flags().Float64Var(&budget, "budget-usd", 0, "Spend cap for this turn")
	return cmd
}

// canvasSystemPrompt rebuilds the workflow-builder prompt with a node
// catalog generated from the live node registry — it cannot drift from the
// registry the way a hardcoded list can.
func canvasSystemPrompt(workflowID string, types []aichat.NodeTypeInfo) string {
	var b strings.Builder
	b.WriteString(`You are a workflow builder AI inside Mono Agent. You ONLY communicate by calling tool functions. NEVER describe what you would do — ALWAYS call the tools directly.

RULES:
1. If workflow_id is "general" or "draft", call create_workflow FIRST.
2. Then call create_nodes with all needed nodes in ONE call.
3. Then call connect_nodes for each connection.
4. Respond with a brief summary ONLY after all tool calls are done.
5. Use "main" as source_handle and target_handle for connections.
6. Space nodes: increment position_x by 250 per node.

NODE TYPES (use these exact type values in create_nodes):
`)
	byCat := map[string][]string{}
	for _, t := range types {
		byCat[t.Category] = append(byCat[t.Category], t.Type)
	}
	cats := make([]string, 0, len(byCat))
	for c := range byCat {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	for _, c := range cats {
		list := byCat[c]
		sort.Strings(list)
		fmt.Fprintf(&b, "%s: %s\n", c, strings.Join(list, ", "))
	}
	fmt.Fprintf(&b, "\nCurrent workflow_id: %s", workflowID)
	return b.String()
}

// canvasToolSpecs converts the CanvasTools OpenAI-shaped definitions to
// protocol ToolSpecs (§4.1) — schemas pass through as JSON Schema.
func canvasToolSpecs(ct *aichat.CanvasTools) []monomind.ToolSpec {
	defs := ct.ToolDefs()
	specs := make([]monomind.ToolSpec, 0, len(defs))
	for _, d := range defs {
		schema, _ := d.Function.Parameters.(map[string]interface{})
		specs = append(specs, monomind.ToolSpec{
			Name:        d.Function.Name,
			Description: d.Function.Description,
			Schema:      schema,
		})
	}
	return specs
}

// registryNodeTypes derives the canvas node catalog from the node registry.
func registryNodeTypes(db *sql.DB) []aichat.NodeTypeInfo {
	reg := noderegistry.Build(db)
	var out []aichat.NodeTypeInfo
	for _, t := range reg.Types() {
		parts := strings.SplitN(t, ".", 2)
		cat := "other"
		if len(parts) == 2 {
			cat = parts[0]
		}
		out = append(out, aichat.NodeTypeInfo{Type: t, Label: t, Category: cat, Description: ""})
	}
	return out
}

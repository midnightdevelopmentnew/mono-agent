// Package agent provides workflow nodes backed by locally-installed AI
// agent runtimes, delegated through monomind's Agent Exec Protocol. This is
// the local-agent replacement for the deprecated ai.* provider nodes
// (docs/plans/local-agent-monomind-delegation.md D5).
package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"monoagent/internal/monomind"
	"monoagent/internal/workflow"
)

// templatePattern matches {{$json.FIELD}} placeholders in prompt templates —
// same syntax as the deprecated ai.* nodes so migrated prompts keep working.
var templatePattern = regexp.MustCompile(`\{\{\$json\.(\w+)\}\}`)

// AskNode sends each input item through one local AI agent turn.
//
// Config fields:
//
//	"runtime" (string, required): agent runtime id — claude, codex, kimi, …
//	  (list with `monoagentcli agent scan --installed`).
//	"prompt" (string, required): user prompt template. Supports {{$json.FIELD}} placeholders.
//	"model" (string): model override for the runtime.
//	"system_prompt" (string): optional system prompt.
//	"output_key" (string): key to store the agent's answer under (default "agent_response").
//	"timeout" (number): overall per-item timeout in seconds (default 300).
type AskNode struct{}

func (n *AskNode) Type() string { return "agent.ask" }

func (n *AskNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	runtime := configString(config, "runtime", "")
	if runtime == "" {
		return nil, fmt.Errorf("%w: agent.ask node requires \"runtime\" (see `monoagentcli agent scan --installed`)", workflow.ErrInvalidConfig)
	}
	promptTemplate := configString(config, "prompt", "")
	if promptTemplate == "" {
		return nil, fmt.Errorf("%w: agent.ask node requires \"prompt\"", workflow.ErrInvalidConfig)
	}
	systemPrompt := configString(config, "system_prompt", "")
	model := configString(config, "model", "")
	outputKey := configString(config, "output_key", "agent_response")
	timeoutSec := configInt(config, "timeout", 300)
	if timeoutSec <= 0 {
		timeoutSec = 300
	}

	// Handshake once per node execution — catches a missing or
	// protocol-incompatible monomind up front with an actionable error,
	// instead of every item silently returning an empty answer.
	bin, _, err := monomind.Ensure(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent.ask: %w", err)
	}

	items := make([]workflow.Item, 0, len(input.Items))
	for _, item := range input.Items {
		prompt := expandTemplate(promptTemplate, item)

		// Subprocess runners' result events carry usage but not always text —
		// accumulate assistant prose as the fallback answer.
		var assistant strings.Builder
		res, err := monomind.Exec(ctx, monomind.ExecOptions{
			Bin:          bin,
			Runtime:      runtime,
			Model:        model,
			Prompt:       prompt,
			SystemPrompt: systemPrompt,
			Timeout:      time.Duration(timeoutSec) * time.Second,
		}, func(ev monomind.Event) {
			if ev.Type == monomind.EventAssistant && ev.Text != "" {
				assistant.WriteString(ev.Text)
				if !strings.HasSuffix(ev.Text, "\n") {
					assistant.WriteString("\n")
				}
			}
		})
		if err != nil {
			return nil, fmt.Errorf("agent.ask (%s): %w", runtime, err)
		}
		if res.Err != nil {
			return nil, fmt.Errorf("agent.ask (%s) turn failed: %s", runtime, res.Err.Error())
		}
		answer := strings.TrimSpace(res.ResultText)
		if answer == "" {
			answer = strings.TrimSpace(assistant.String())
		}

		outJSON := copyItemJSON(item)
		outJSON[outputKey] = answer
		outJSON["_agent_session_id"] = res.SessionID
		items = append(items, workflow.Item{JSON: outJSON})
	}

	return []workflow.NodeOutput{
		{Handle: "main", Items: items},
	}, nil
}

// RegisterAll registers the agent node types into the given registry.
func RegisterAll(r *workflow.NodeTypeRegistry) {
	r.Register("agent.ask", func() workflow.NodeExecutor { return &AskNode{} })
}

// expandTemplate replaces {{$json.KEY}} placeholders in template with values from item.JSON.
func expandTemplate(template string, item workflow.Item) string {
	return templatePattern.ReplaceAllStringFunc(template, func(match string) string {
		parts := templatePattern.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		key := parts[1]
		if item.JSON == nil {
			return match
		}
		val, ok := item.JSON[key]
		if !ok {
			return match
		}
		return fmt.Sprintf("%v", val)
	})
}

// configString extracts a non-empty string value from config.
func configString(config map[string]interface{}, key, defaultVal string) string {
	if v, ok := config[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return defaultVal
}

// configInt extracts an int value from config (JSON numbers arrive as float64).
func configInt(config map[string]interface{}, key string, defaultVal int) int {
	if v, ok := config[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return defaultVal
}

// copyItemJSON creates a shallow copy of an item's JSON map.
func copyItemJSON(item workflow.Item) map[string]interface{} {
	m := make(map[string]interface{}, len(item.JSON)+2)
	for k, v := range item.JSON {
		m[k] = v
	}
	return m
}

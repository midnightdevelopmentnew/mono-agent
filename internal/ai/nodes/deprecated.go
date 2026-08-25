package ainodes

import (
	"context"
	"fmt"

	"monoagent/internal/workflow"
)

// DeprecatedNodeExecutor fails every execution with the migration hint.
// Registered under the historical ai.* type names so saved workflows that
// still reference them get an actionable error instead of "unknown node
// type". workflow.ValidateForSave/ValidateForActivation also reject these
// types, but neither is currently wired into a save/activate call path
// (pre-existing gap, unrelated to this change) — this executor-level
// fail-fast is what actually fires today.
type DeprecatedNodeExecutor struct {
	TypeName string
}

func (n *DeprecatedNodeExecutor) Type() string { return n.TypeName }

func (n *DeprecatedNodeExecutor) Execute(_ context.Context, _ workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
	hint, _ := workflow.IsDeprecatedNodeType(n.TypeName)
	return nil, fmt.Errorf("%s node %q is deprecated by the local-agent transition — %s (docs/plans/local-agent-monomind-delegation.md)",
		"ai", n.TypeName, hint)
}

// RegisterDeprecated registers fail-fast stubs for every historical ai.*
// provider node type. Replaces RegisterAll in the production registry — the
// provider-backed implementations remain for their unit tests until the
// Phase-5 rip-out.
func RegisterDeprecated(r *workflow.NodeTypeRegistry) {
	for _, typeName := range []string{
		"ai.chat", "ai.extract", "ai.classify", "ai.transform", "ai.embed", "ai.agent",
	} {
		t := typeName
		r.Register(t, func() workflow.NodeExecutor { return &DeprecatedNodeExecutor{TypeName: t} })
	}
}

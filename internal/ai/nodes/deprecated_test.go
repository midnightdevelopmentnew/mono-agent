package ainodes

import (
	"context"
	"strings"
	"testing"

	"monoagent/internal/workflow"
)

func TestRegisterDeprecatedCoversAllHistoricalTypes(t *testing.T) {
	r := workflow.NewNodeTypeRegistry()
	RegisterDeprecated(r)

	for _, typeName := range []string{
		"ai.chat", "ai.extract", "ai.classify", "ai.transform", "ai.embed", "ai.agent",
	} {
		factory, ok := r.Get(typeName)
		if !ok {
			t.Fatalf("Get(%q): not registered", typeName)
		}
		exec := factory()
		if got := exec.Type(); got != typeName {
			t.Errorf("Type() = %q, want %q", got, typeName)
		}
	}
}

func TestDeprecatedNodeExecutorFailsFastWithMigrationHint(t *testing.T) {
	node := &DeprecatedNodeExecutor{TypeName: "ai.chat"}
	_, err := node.Execute(context.Background(), workflow.NodeInput{}, nil)
	if err == nil {
		t.Fatal("Execute() = nil error, want a migration-hint error")
	}
	if !strings.Contains(err.Error(), "agent.ask") {
		t.Errorf("Execute() error = %q, want it to mention the agent.ask replacement", err.Error())
	}
}

func TestDeprecatedNodeExecutorEmbedHasNoEquivalentHint(t *testing.T) {
	node := &DeprecatedNodeExecutor{TypeName: "ai.embed"}
	_, err := node.Execute(context.Background(), workflow.NodeInput{}, nil)
	if err == nil {
		t.Fatal("Execute() = nil error, want a migration-hint error")
	}
	if !strings.Contains(err.Error(), "no local-agent equivalent") {
		t.Errorf("Execute() error = %q, want it to say no local-agent equivalent exists", err.Error())
	}
}

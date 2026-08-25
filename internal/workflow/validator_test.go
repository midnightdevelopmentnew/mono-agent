package workflow

import (
	"strings"
	"testing"
)

func TestIsDeprecatedNodeType(t *testing.T) {
	for _, typeName := range []string{
		"ai.chat", "ai.extract", "ai.classify", "ai.transform", "ai.embed", "ai.agent",
	} {
		hint, ok := IsDeprecatedNodeType(typeName)
		if !ok {
			t.Errorf("IsDeprecatedNodeType(%q) = false, want true", typeName)
		}
		if hint == "" {
			t.Errorf("IsDeprecatedNodeType(%q) returned an empty hint", typeName)
		}
	}
	if _, ok := IsDeprecatedNodeType("agent.ask"); ok {
		t.Error("IsDeprecatedNodeType(\"agent.ask\") = true, want false — the replacement node must not be flagged")
	}
}

func TestValidateForSaveRejectsDeprecatedNodeTypes(t *testing.T) {
	w := &Workflow{
		Name: "uses ai.chat",
		Nodes: []WorkflowNode{
			{ID: "n1", Type: "ai.chat"},
		},
	}
	err := ValidateForSave(w)
	if err == nil {
		t.Fatal("ValidateForSave() = nil, want an error for a deprecated node type")
	}
	if !strings.Contains(err.Error(), "agent.ask") {
		t.Errorf("ValidateForSave() error = %q, want it to mention the agent.ask replacement", err.Error())
	}
}

func TestValidateForSaveRejectsDeprecatedEmbedWithNoEquivalentHint(t *testing.T) {
	w := &Workflow{
		Name: "uses ai.embed",
		Nodes: []WorkflowNode{
			{ID: "n1", Type: "ai.embed"},
		},
	}
	err := ValidateForSave(w)
	if err == nil {
		t.Fatal("ValidateForSave() = nil, want an error for ai.embed")
	}
	if !strings.Contains(err.Error(), "no local-agent equivalent") {
		t.Errorf("ValidateForSave() error = %q, want it to say no local-agent equivalent exists", err.Error())
	}
}

func TestValidateForSaveAllowsAgentAsk(t *testing.T) {
	w := &Workflow{
		Name: "uses agent.ask",
		Nodes: []WorkflowNode{
			{ID: "n1", Type: "agent.ask"},
		},
	}
	if err := ValidateForSave(w); err != nil {
		t.Errorf("ValidateForSave() = %v, want nil for a non-deprecated node type", err)
	}
}

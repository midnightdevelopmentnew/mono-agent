package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"monoagent/internal/monomind"
	"monoagent/internal/workflow"
)

// writeFakeIncompatibleMonomind writes a script that mimics a pre-protocol
// monomind install: it responds to unknown subcommands (including
// `--version --json`) with human-readable help text instead of JSON, exit 0
// — reproducing the real installed-but-too-old case.
func writeFakeIncompatibleMonomind(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "monomind")
	script := "#!/bin/sh\necho 'Agent Management Commands'\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake monomind: %v", err)
	}
	return path
}

func TestAskNodeType(t *testing.T) {
	node := &AskNode{}
	if got := node.Type(); got != "agent.ask" {
		t.Errorf("Type() = %q, want %q", got, "agent.ask")
	}
}

func TestAskNodeRequiresRuntime(t *testing.T) {
	node := &AskNode{}
	_, err := node.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"prompt": "hello",
	})
	if err == nil {
		t.Fatal("Execute() = nil error, want a missing-runtime error")
	}
	if !errors.Is(err, workflow.ErrInvalidConfig) {
		t.Errorf("Execute() error = %v, want it to wrap workflow.ErrInvalidConfig", err)
	}
}

func TestAskNodeRequiresPrompt(t *testing.T) {
	node := &AskNode{}
	_, err := node.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"runtime": "claude",
	})
	if err == nil {
		t.Fatal("Execute() = nil error, want a missing-prompt error")
	}
	if !errors.Is(err, workflow.ErrInvalidConfig) {
		t.Errorf("Execute() error = %v, want it to wrap workflow.ErrInvalidConfig", err)
	}
}

func TestRegisterAllRegistersAgentAsk(t *testing.T) {
	r := workflow.NewNodeTypeRegistry()
	RegisterAll(r)
	factory, ok := r.Get("agent.ask")
	if !ok {
		t.Fatal("Get(\"agent.ask\"): not registered")
	}
	if got := factory().Type(); got != "agent.ask" {
		t.Errorf("factory().Type() = %q, want %q", got, "agent.ask")
	}
}

func TestExpandTemplate(t *testing.T) {
	item := workflow.Item{JSON: map[string]interface{}{"text": "hello world", "count": 3}}

	got := expandTemplate("say: {{$json.text}} ({{$json.count}} times)", item)
	want := "say: hello world (3 times)"
	if got != want {
		t.Errorf("expandTemplate() = %q, want %q", got, want)
	}
}

func TestExpandTemplateLeavesUnknownFieldsUntouched(t *testing.T) {
	item := workflow.Item{JSON: map[string]interface{}{"text": "hi"}}
	got := expandTemplate("{{$json.missing}}", item)
	if got != "{{$json.missing}}" {
		t.Errorf("expandTemplate() = %q, want the placeholder left as-is", got)
	}
}

// TestAskNodeFailsFastOnIncompatibleMonomind guards against a regression
// where Execute called monomind.Exec directly (skipping the Ensure
// handshake): against a too-old/incompatible monomind that answers unknown
// flags with exit-0 help text instead of JSON, the old code silently
// returned a "successful" empty answer instead of an actionable error.
func TestAskNodeFailsFastOnIncompatibleMonomind(t *testing.T) {
	t.Setenv(monomind.EnvOverride, writeFakeIncompatibleMonomind(t))

	node := &AskNode{}
	out, err := node.Execute(context.Background(), workflow.NodeInput{
		Items: []workflow.Item{{JSON: map[string]interface{}{}}},
	}, map[string]interface{}{
		"runtime": "claude",
		"prompt":  "say hi",
	})
	if err == nil {
		t.Fatalf("Execute() = %+v, nil error — want a handshake error against an incompatible monomind", out)
	}
	if !strings.Contains(err.Error(), "handshake") {
		t.Errorf("Execute() error = %q, want it to mention the handshake failure", err.Error())
	}
}

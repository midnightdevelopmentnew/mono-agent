package action

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
)

// TestStepConditionBranchCanTriggerLoop is a regression test: condition steps
// whose then/else branch references a loop id (rather than a regular step id)
// must actually run that loop instead of silently dropping the reference.
func TestStepConditionBranchCanTriggerLoop(t *testing.T) {
	ae := NewActionExecutor(context.Background(), nil, nil, nil, nil, nil, zerolog.Nop())
	ae.action = &StorageAction{ID: "test-action"}
	ae.actionDef = &ActionDef{
		Steps: []StepDef{
			{ID: "mark_ran", Type: "set_variable", Variable: "loopRan", Value: true},
		},
		Loops: []LoopDef{
			{ID: "fallback_loop", Iterator: "items", IndexVar: "i", Steps: []string{"mark_ran"}},
		},
	}
	ae.SetVariable("items", []interface{}{"a", "b"})

	condStep := StepDef{
		ID:        "check_flag",
		Type:      "condition",
		Condition: ConditionDef{Variable: "flag", Operator: "equals", Value: "yes"},
		Then:      []string{"fallback_loop"},
	}
	ae.SetVariable("flag", "yes")

	result, err := ae.stepCondition(context.Background(), condStep)
	if err != nil {
		t.Fatalf("stepCondition returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected condition step to succeed, got failure: %v", result.Error)
	}

	if v, ok := ae.execCtx.GetVariable("loopRan"); !ok || v != true {
		t.Fatalf("expected fallback_loop to run and set loopRan=true, got %v (present=%v)", v, ok)
	}
}

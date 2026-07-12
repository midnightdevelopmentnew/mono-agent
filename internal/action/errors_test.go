package action

import (
	"context"
	"errors"
	"testing"
)

// TestHandleTryAlternativeCapsRetries verifies that the "try_alternative"
// error action does not loop forever: it retries up to MaxRetries and then
// falls through to onFailure instead of returning Retry=true indefinitely.
func TestHandleTryAlternativeCapsRetries(t *testing.T) {
	eh := NewErrorHandler()
	def := &ErrorHandlerDef{Action: ErrorActionTryAlternative, MaxRetries: 2}
	result := &StepResult{Success: false, StepID: "step-1", Error: errors.New("boom")}

	retries := 0
	// Simulate the executor loop: keep re-handling while Retry is true.
	for i := 0; i < 100; i++ {
		handled := eh.Handle(context.Background(), def, result, nil)
		if !handled.Retry {
			if !handled.Skip {
				t.Fatalf("expected Skip after retries exhausted, got %+v", handled)
			}
			break
		}
		retries++
		if retries > 10 {
			t.Fatal("try_alternative retried without bound (infinite loop)")
		}
	}

	if retries != def.MaxRetries {
		t.Fatalf("expected %d retries, got %d", def.MaxRetries, retries)
	}
}

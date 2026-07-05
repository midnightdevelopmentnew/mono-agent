package action

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/browser"
	"github.com/rs/zerolog"
)

// TestRunAllStopsSubmissionOnCancel verifies that once ctx is cancelled,
// RunAll stops submitting new actions to the worker pool instead of
// submitting every remaining action anyway (regression test for a `break`
// inside `select` only breaking the select, not the enclosing for-loop).
func TestRunAllStopsSubmissionOnCancel(t *testing.T) {
	runner := NewActionRunner(2, nil, nil, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: submission loop should stop immediately

	actions := make([]StorageAction, 5)
	for i := range actions {
		actions[i] = StorageAction{ID: "a"}
	}

	var pageProviderCalls int32
	results := runner.RunAll(ctx, actions, func(action StorageAction) (browser.PageInterface, BotAdapter, error) {
		atomic.AddInt32(&pageProviderCalls, 1)
		return nil, nil, context.Canceled
	})

	if calls := atomic.LoadInt32(&pageProviderCalls); calls != 0 {
		t.Fatalf("expected pageProvider to never be called after cancellation, got %d calls", calls)
	}
	for i, res := range results {
		if len(res.FailedItems) == 0 {
			t.Fatalf("result[%d] expected a cancellation FailedItem, got none", i)
		}
	}
}

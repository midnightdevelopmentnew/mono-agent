package workflow

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// TestExecutionQueue_BoundsConcurrency verifies the gate caps the number of
// executions actively running at once.
func TestExecutionQueue_BoundsConcurrency(t *testing.T) {
	started := make(chan string, 10)
	release := make(chan struct{})
	handler := func(ctx context.Context, req ExecutionRequest) {
		started <- req.ExecutionID
		<-release
	}
	q := NewExecutionQueue(10, 2, handler, zerolog.Nop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	for i := 0; i < 4; i++ {
		if err := q.Enqueue(ExecutionRequest{ExecutionID: fmt.Sprint(i)}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	// Exactly two should start; a third must not while the gate is full.
	<-started
	<-started
	select {
	case id := <-started:
		t.Fatalf("third execution %s started; concurrency bound of 2 violated", id)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	cancel()
	q.Stop()
}

// TestExecutionQueue_SlotYieldAllowsProgress verifies that releasing a
// ConcurrencySlot (what a Human-in-Loop node does while waiting) frees the gate
// so another execution runs even at a concurrency bound of 1 — the core fix for
// HIL worker starvation.
func TestExecutionQueue_SlotYieldAllowsProgress(t *testing.T) {
	started := make(chan string, 10)
	proceed := make(chan struct{})
	handler := func(ctx context.Context, req ExecutionRequest) {
		started <- req.ExecutionID
		if req.ExecutionID == "A" {
			slot, ok := SlotFromContext(ctx)
			if !ok {
				t.Error("no ConcurrencySlot in execution context")
				return
			}
			slot.Release() // yield while "waiting" for approval
			<-proceed
			_ = slot.Acquire(ctx)
		}
	}
	q := NewExecutionQueue(10, 1, handler, zerolog.Nop()) // bound = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	if err := q.Enqueue(ExecutionRequest{ExecutionID: "A"}); err != nil {
		t.Fatalf("enqueue A: %v", err)
	}
	if got := <-started; got != "A" {
		t.Fatalf("expected A to start first, got %s", got)
	}

	// A has yielded its slot; B must be able to run despite the bound of 1.
	if err := q.Enqueue(ExecutionRequest{ExecutionID: "B"}); err != nil {
		t.Fatalf("enqueue B: %v", err)
	}
	select {
	case got := <-started:
		if got != "B" {
			t.Fatalf("expected B to start, got %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("B never started; a waiting execution failed to free its concurrency slot")
	}

	close(proceed)
	cancel()
	q.Stop()
}

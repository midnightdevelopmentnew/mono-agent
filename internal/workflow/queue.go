package workflow

import (
	"context"
	"sync"

	"github.com/rs/zerolog"
)

// ExecutionRequest is enqueued when a workflow execution is triggered.
type ExecutionRequest struct {
	WorkflowID  string
	ExecutionID string
	TriggerType string
	// TriggerNodeID identifies which trigger node fired. Empty means "all trigger
	// nodes" (manual/retry runs), preserving legacy fan-out behaviour.
	TriggerNodeID string
	TriggerData   map[string]interface{}
}

// ConcurrencySlot represents an execution's hold on the engine's bounded
// concurrency. A node that must wait a long time (e.g. Human-in-Loop) can
// Release its slot so other executions can run while it waits, then Acquire
// it again before continuing. Held for the whole execution otherwise.
type ConcurrencySlot interface {
	Release()
	Acquire(ctx context.Context) error
}

type slotCtxKey struct{}

// ContextWithSlot attaches a ConcurrencySlot to a context.
func ContextWithSlot(ctx context.Context, slot ConcurrencySlot) context.Context {
	return context.WithValue(ctx, slotCtxKey{}, slot)
}

// SlotFromContext returns the ConcurrencySlot attached to ctx, if any.
func SlotFromContext(ctx context.Context) (ConcurrencySlot, bool) {
	s, ok := ctx.Value(slotCtxKey{}).(ConcurrencySlot)
	return s, ok
}

// gate bounds the number of executions actively doing work via a token channel.
type gate struct {
	tokens chan struct{}
}

func newGate(n int) *gate {
	if n < 1 {
		n = 1
	}
	return &gate{tokens: make(chan struct{}, n)}
}

func (g *gate) acquire(ctx context.Context) error {
	select {
	case g.tokens <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *gate) release() {
	select {
	case <-g.tokens:
	default:
	}
}

// gateSlot is a per-execution ConcurrencySlot backed by the shared gate. It
// tracks whether it currently holds a token so Release/Acquire are idempotent
// and a double release can never free another execution's token.
type gateSlot struct {
	g    *gate
	mu   sync.Mutex
	held bool
}

func (s *gateSlot) Acquire(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held {
		return nil
	}
	if err := s.g.acquire(ctx); err != nil {
		return err
	}
	s.held = true
	return nil
}

func (s *gateSlot) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.held {
		return
	}
	s.g.release()
	s.held = false
}

// ExecutionQueue buffers pending execution requests and runs each in its own
// goroutine, bounded by a concurrency gate. A single dispatcher drains the
// channel; using per-execution goroutines (rather than a fixed worker pool)
// lets a long-waiting node yield its concurrency slot without parking a worker.
type ExecutionQueue struct {
	ch          chan ExecutionRequest
	gate        *gate
	wg          sync.WaitGroup
	cancelFuncs sync.Map // executionID → context.CancelFunc
	handler     func(ctx context.Context, req ExecutionRequest)
	logger      zerolog.Logger

	mu     sync.Mutex // guards closed and serialises sends against Stop's close
	closed bool
}

// NewExecutionQueue creates a queue with the given buffer capacity and a
// maximum number of concurrently-executing (non-waiting) executions.
func NewExecutionQueue(capacity int, maxConcurrent int, handler func(ctx context.Context, req ExecutionRequest), logger zerolog.Logger) *ExecutionQueue {
	return &ExecutionQueue{
		ch:      make(chan ExecutionRequest, capacity),
		gate:    newGate(maxConcurrent),
		handler: handler,
		logger:  logger,
	}
}

// Start launches the dispatcher goroutine. Must be called before Enqueue.
// The provided context governs dispatcher and execution lifetime.
func (q *ExecutionQueue) Start(ctx context.Context) {
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		q.logger.Info().Msg("workflow queue dispatcher started")
		defer q.logger.Info().Msg("workflow queue dispatcher stopped")
		for {
			select {
			case <-ctx.Done():
				return
			case req, ok := <-q.ch:
				if !ok {
					return // channel closed by Stop()
				}
				q.wg.Add(1)
				go q.run(ctx, req)
			}
		}
	}()
}

// run executes a single request in its own goroutine: it creates a cancellable
// child context, acquires a concurrency slot (blocking until one is free),
// attaches the slot so long-waiting nodes can yield it, runs the handler, then
// cleans up.
func (q *ExecutionQueue) run(ctx context.Context, req ExecutionRequest) {
	defer q.wg.Done()

	execCtx, cancel := context.WithCancel(ctx)
	q.cancelFuncs.Store(req.ExecutionID, cancel)

	slot := &gateSlot{g: q.gate}
	defer func() {
		q.cancelFuncs.Delete(req.ExecutionID)
		slot.Release()
		cancel()
		if r := recover(); r != nil {
			q.logger.Error().
				Str("execution_id", req.ExecutionID).
				Str("workflow_id", req.WorkflowID).
				Interface("panic", r).
				Msg("workflow handler panicked")
		}
	}()

	if err := slot.Acquire(execCtx); err != nil {
		// Context cancelled while waiting for a slot (shutdown/cancel).
		return
	}

	q.logger.Debug().
		Str("execution_id", req.ExecutionID).
		Str("workflow_id", req.WorkflowID).
		Str("trigger_type", req.TriggerType).
		Msg("dispatching execution request")

	q.handler(ContextWithSlot(execCtx, slot), req)
}

// Stop closes the channel and blocks until the dispatcher and all in-flight
// executions have exited. Callers should cancel the context passed to Start
// first so waiting executions unblock.
func (q *ExecutionQueue) Stop() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	close(q.ch)
	q.mu.Unlock()
	q.wg.Wait()
}

// Enqueue adds a request to the queue. Returns ErrQueueFull if the buffer is
// full, or ErrQueueClosed if the queue has been stopped. The closed check and
// the (non-blocking) send are serialised under mu against Stop's close, so a
// trigger firing during shutdown can never panic with send-on-closed-channel.
func (q *ExecutionQueue) Enqueue(req ExecutionRequest) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrQueueClosed
	}
	select {
	case q.ch <- req:
		q.mu.Unlock()
		q.logger.Debug().
			Str("execution_id", req.ExecutionID).
			Str("workflow_id", req.WorkflowID).
			Msg("execution request enqueued")
		return nil
	default:
		q.mu.Unlock()
		q.logger.Warn().
			Str("execution_id", req.ExecutionID).
			Str("workflow_id", req.WorkflowID).
			Msg("execution queue full, request rejected")
		return ErrQueueFull
	}
}

// Cancel signals cancellation for a specific execution.
func (q *ExecutionQueue) Cancel(executionID string) {
	if val, ok := q.cancelFuncs.Load(executionID); ok {
		if cancel, ok := val.(context.CancelFunc); ok {
			cancel()
			q.logger.Info().
				Str("execution_id", executionID).
				Msg("execution cancellation signalled")
		}
	}
}

// Len returns the current number of items waiting in the queue.
func (q *ExecutionQueue) Len() int {
	return len(q.ch)
}

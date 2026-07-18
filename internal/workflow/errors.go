package workflow

import (
	"errors"
	"fmt"
	"strings"
)

// PartialFailureError reports that an execution completed but one or more nodes
// failed under an on_error=continue/skip/error_branch policy. The engine maps it
// to a SUCCESS_WITH_ERRORS status so a run that had failures isn't shown as green.
type PartialFailureError struct {
	Nodes []string // names of nodes that failed non-fatally
}

func (e *PartialFailureError) Error() string {
	return fmt.Sprintf("%d node(s) failed but the workflow continued: %s",
		len(e.Nodes), strings.Join(e.Nodes, ", "))
}

var (
	ErrQueueFull          = errors.New("workflow: execution queue is full")
	ErrQueueClosed        = errors.New("workflow: execution queue is closed")
	ErrWorkflowNotFound   = errors.New("workflow: workflow not found")
	ErrExecutionNotFound  = errors.New("workflow: execution not found")
	ErrCycleDetected      = errors.New("workflow: cycle detected in workflow graph")
	ErrInvalidConfig      = errors.New("workflow: invalid node configuration")
	ErrNodeTypeUnknown    = errors.New("workflow: unknown node type")
	ErrNoTriggerNode      = errors.New("workflow: workflow has no trigger node")
	ErrWorkflowInactive   = errors.New("workflow: workflow is not active")
	ErrExecutionCancelled = errors.New("workflow: execution was cancelled")
	ErrExecutionTimeout   = errors.New("workflow: execution timed out")
	ErrTriggerActive      = errors.New("workflow: trigger already active for this workflow")

	// ErrNodePaused is returned by a node (e.g. Human-in-Loop) to suspend the
	// execution until an external event (an approval) lets it resume, without
	// holding a goroutine. RunExecution serializes its state and returns
	// ErrExecutionPaused; the engine marks the execution WAITING.
	ErrNodePaused      = errors.New("workflow: node paused, awaiting resume")
	ErrExecutionPaused = errors.New("workflow: execution paused, awaiting resume")
)

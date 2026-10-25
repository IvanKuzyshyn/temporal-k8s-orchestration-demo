// Package activities implements the worker's retry-safe example work.
package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// Activities binds local deployment configuration, never mutable workflow state.
type Activities struct {
	workerName string
	duration   time.Duration
}

// New defaults an unspecified duration to five seconds.
func New(workerName string, duration time.Duration) *Activities {
	if duration <= 0 {
		duration = 5 * time.Second
	}
	return &Activities{workerName: workerName, duration: duration}
}

// Example exposes misrouting as a non-retryable failure and cooperatively
// cancels simulated work. A real side effect would also need idempotency.
func (a *Activities) Example(ctx context.Context, input contracts.ChildInput) (contracts.ChildResult, error) {
	if a.workerName == "" || input.WorkerName != a.workerName {
		return contracts.ChildResult{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("worker identity mismatch: local %q, requested %q", a.workerName, input.WorkerName), "WorkerIdentityMismatch", nil)
	}
	if err := ctx.Err(); err != nil {
		return contracts.ChildResult{}, err
	}
	info := activity.GetInfo(ctx)
	activity.GetLogger(ctx).Info("example activity started", "worker", a.workerName, "workflowID", info.WorkflowExecution.ID, "runID", info.WorkflowExecution.RunID, "activityID", info.ActivityID, "taskQueue", info.TaskQueue)
	timer := time.NewTimer(a.duration)
	defer timer.Stop()
	heartbeat := time.NewTicker(2 * time.Second)
	defer heartbeat.Stop()
	activity.RecordHeartbeat(ctx, a.workerName)
	for {
		select {
		case <-ctx.Done():
			return contracts.ChildResult{}, ctx.Err()
		case <-heartbeat.C:
			activity.RecordHeartbeat(ctx, a.workerName)
		case <-timer.C:
			if err := ctx.Err(); err != nil {
				return contracts.ChildResult{}, err
			}
			return contracts.ChildResult{WorkerName: a.workerName, Message: input.Payload.Message}, nil
		}
	}
}

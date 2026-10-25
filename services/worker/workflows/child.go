// Package workflows contains the reusable worker workflow.
package workflows

import (
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// Child executes the fixed example activity on the child's own worker queue.
func Child(ctx workflow.Context, input contracts.ChildInput) (contracts.ChildResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:              workflow.GetInfo(ctx).TaskQueueName,
		StartToCloseTimeout:    60 * time.Second,
		ScheduleToCloseTimeout: 10 * time.Minute,
		HeartbeatTimeout:       15 * time.Second,
		WaitForCancellation:    true,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:    3,
			InitialInterval:    2 * time.Second,
			MaximumInterval:    10 * time.Second,
			BackoffCoefficient: 2,
		},
	})
	var result contracts.ChildResult
	err := workflow.ExecuteActivity(ctx, contracts.ExampleActivityName, input).Get(ctx, &result)
	return result, err
}

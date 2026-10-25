// Package workflows contains deterministic orchestration over immutable plans.
package workflows

import (
	"fmt"
	"sort"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/workflow"
)

type childState struct {
	future   workflow.ChildWorkflowFuture
	cancel   workflow.CancelFunc
	complete bool
	result   contracts.ChildResult
}

// Parent consumes a validated plan snapshot; discovery and parent start options
// (including the 30-minute execution timeout and no retry policy) belong to callers.
func Parent(ctx workflow.Context, input contracts.ParentInput) (contracts.ParentResult, error) {
	// Copy before sorting so workflow code never changes its serialized input.
	nodes := append([]plan.Node(nil), input.Plan.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	states := make([]childState, len(nodes))
	index := make(map[string]int, len(nodes))
	for i, node := range nodes {
		index[node.Name] = i
	}
	cancelUnfinished := func() {
		for i := range states {
			if states[i].cancel != nil && !states[i].complete {
				states[i].cancel()
			}
		}
	}
	defer cancelUnfinished()
	completed := 0
	for completed < len(nodes) {
		if err := ctx.Err(); err != nil {
			return contracts.ParentResult{}, err
		}
		// Consume all available completions before scheduling. In particular, an
		// already available terminal failure must not be hidden by a ready success.
		for i := range states {
			state := &states[i]
			if state.future == nil || state.complete || !state.future.IsReady() {
				continue
			}
			if err := state.future.Get(ctx, &state.result); err != nil {
				return contracts.ParentResult{}, err
			}
			state.complete = true
			completed++
		}
		if completed == len(nodes) {
			break
		}
		for i, node := range nodes {
			if states[i].future != nil {
				continue
			}
			ready := true
			for _, dependency := range node.Dependencies {
				dep, exists := index[dependency]
				if !exists || !states[dep].complete {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			childCtx, cancel := workflow.WithCancel(ctx)
			childCtx = workflow.WithChildOptions(childCtx, workflow.ChildWorkflowOptions{
				WorkflowID:               workflow.GetInfo(ctx).WorkflowExecution.ID + "/" + node.Name,
				TaskQueue:                node.TaskQueue,
				WorkflowExecutionTimeout: 15 * time.Minute,
				ParentClosePolicy:        enumspb.PARENT_CLOSE_POLICY_REQUEST_CANCEL,
				// Workflow retries are intentionally unset; activity retries handle recovery.
			})
			states[i].cancel = cancel
			states[i].future = workflow.ExecuteChildWorkflow(childCtx, node.WorkflowType, contracts.ChildInput{WorkerName: node.Name})
		}
		selector := workflow.NewSelector(ctx)
		running := false
		for i := range states {
			if states[i].future != nil && !states[i].complete {
				running = true
				selector.AddFuture(states[i].future, func(workflow.Future) {})
			}
		}
		if !running {
			return contracts.ParentResult{}, fmt.Errorf("plan has no runnable workers")
		}
		selector.Select(ctx)
	}
	result := contracts.ParentResult{Results: make([]contracts.ChildResult, 0, len(nodes))}
	for i := range states {
		result.Results = append(result.Results, states[i].result)
	}
	return result, nil
}

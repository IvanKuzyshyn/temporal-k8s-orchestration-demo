package runs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

// TemporalClient is the SDK subset needed here; client.Client satisfies it.
// Its owner retains connection lifecycle responsibility.
type TemporalClient interface {
	ExecuteWorkflow(context.Context, client.StartWorkflowOptions, interface{}, ...interface{}) (client.WorkflowRun, error)
	DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
	GetWorkflow(context.Context, string, string) client.WorkflowRun
}
type Temporal struct{ client TemporalClient }

func NewTemporal(c TemporalClient) *Temporal { return &Temporal{client: c} }
func (t *Temporal) Start(ctx context.Context, id string, input contracts.ParentInput) (Run, error) {
	handle, err := t.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: id, TaskQueue: "orchestrator.v1.temporal", WorkflowExecutionTimeout: 30 * time.Minute,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowIDConflictPolicy:                 enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
		Memo:                                     map[string]interface{}{"planHash": input.Plan.Hash},
	}, contracts.ParentWorkflowName, input)
	if err != nil {
		return Run{}, sdkError(err)
	}
	if handle == nil || handle.GetID() != id || handle.GetRunID() == "" {
		return Run{}, errors.New("invalid Temporal start response")
	}
	return Run{WorkflowID: handle.GetID(), RunID: handle.GetRunID(), PlanHash: input.Plan.Hash, Status: "running"}, nil
}
func (t *Temporal) Get(ctx context.Context, id string) (Execution, error) {
	description, err := t.client.DescribeWorkflowExecution(ctx, id, "")
	if err != nil {
		return Execution{}, sdkError(err)
	}
	invalid := func() (Execution, error) { return Execution{}, errors.New("invalid durable execution metadata") }
	if description == nil || description.WorkflowExecutionInfo == nil {
		return invalid()
	}
	info := description.WorkflowExecutionInfo
	if info.Execution == nil || info.Execution.WorkflowId != id || info.Execution.RunId == "" || info.Memo == nil {
		return invalid()
	}
	var planHash string
	dc := converter.GetDefaultDataConverter()
	p := info.Memo.Fields["planHash"]
	if p == nil {
		return invalid()
	}
	if err := dc.FromPayload(p, &planHash); err != nil || !validHash(planHash) {
		return invalid()
	}
	status := info.Status
	switch status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
	default:
		return invalid()
	}
	run := Run{WorkflowID: id, RunID: info.Execution.RunId, PlanHash: planHash, Status: strings.ToLower(status.String())}
	// Calling WorkflowRun.Get on an unfinished execution would wait for closure.
	// Pin the described run ID, never a mutable "latest execution" result handle.
	if status == enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED {
		var result contracts.ParentResult
		if err := t.client.GetWorkflow(ctx, id, run.RunID).GetWithOptions(ctx, &result, client.WorkflowRunGetOptions{DisableFollowingRuns: true}); err != nil {
			return Execution{}, sdkError(err)
		}
		run.Result = &result
	} else if status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		// Summaries intentionally do not expose application error causes or payloads.
		run.Failure = "Workflow " + run.Status
	}
	return Execution{Run: run}, nil
}
func sdkError(err error) error {
	var duplicate *serviceerror.WorkflowExecutionAlreadyStarted
	var missing *serviceerror.NotFound
	var namespaceMissing *serviceerror.NamespaceNotFound
	var unavailable *serviceerror.Unavailable
	var deadline *serviceerror.DeadlineExceeded
	var exhausted *serviceerror.ResourceExhausted
	var canceled *serviceerror.Canceled
	switch {
	case errors.As(err, &duplicate):
		return ErrAlreadyExists
	case errors.As(err, &missing):
		return ErrNotFound
	case errors.As(err, &namespaceMissing), errors.As(err, &unavailable), errors.As(err, &deadline), errors.As(err, &exhausted), errors.As(err, &canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return ErrUnavailable
	default:
		return err
	}
}

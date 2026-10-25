package runs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/runs"
	. "github.com/onsi/gomega"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

type sdkClient struct {
	options                 client.StartWorkflowOptions
	workflow                interface{}
	args                    []interface{}
	executeErr, describeErr error
	description             *workflowservice.DescribeWorkflowExecutionResponse
	handle                  *workflowRun
	gotID, gotRunID         string
}

func (f *sdkClient) ExecuteWorkflow(_ context.Context, o client.StartWorkflowOptions, w interface{}, a ...interface{}) (client.WorkflowRun, error) {
	f.options = o
	f.workflow = w
	f.args = a
	return f.handle, f.executeErr
}
func (f *sdkClient) DescribeWorkflowExecution(_ context.Context, id, rid string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	f.gotID = id
	f.gotRunID = rid
	return f.description, f.describeErr
}
func (f *sdkClient) GetWorkflow(_ context.Context, id, rid string) client.WorkflowRun {
	f.gotID = id
	f.gotRunID = rid
	return f.handle
}

type workflowRun struct {
	getCalls   int
	getOptions client.WorkflowRunGetOptions
	result     contracts.ParentResult
	err        error
}

func (*workflowRun) GetFirstExecutionRunID() string { return "original-run" }
func (*workflowRun) GetID() string                  { return "workflow-id" }
func (*workflowRun) GetRunID() string               { return "original-run" }
func (r *workflowRun) Get(_ context.Context, v interface{}) error {
	r.getCalls++
	if r.err != nil {
		return r.err
	}
	*(v.(*contracts.ParentResult)) = r.result
	return nil
}
func (r *workflowRun) GetWithOptions(ctx context.Context, v interface{}, options client.WorkflowRunGetOptions) error {
	r.getOptions = options
	return r.Get(ctx, v)
}

const requestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const planHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func description(t *testing.T, status enumspb.WorkflowExecutionStatus) *workflowservice.DescribeWorkflowExecutionResponse {
	t.Helper()
	g := NewWithT(t)
	r, e := converter.GetDefaultDataConverter().ToPayload(requestHash)
	g.Expect(e).NotTo(HaveOccurred())
	p, e := converter.GetDefaultDataConverter().ToPayload(planHash)
	g.Expect(e).NotTo(HaveOccurred())
	return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: "workflow-id", RunId: "original-run"}, Status: status, Memo: &commonpb.Memo{Fields: map[string]*commonpb.Payload{"requestHash": r, "planHash": p}}}}
}
func TestTemporalOutgoingStartPolicyAndMemo(t *testing.T) {
	g := NewWithT(t)
	f := &sdkClient{handle: &workflowRun{}}
	adapter := runs.NewTemporal(f)
	input := contracts.ParentInput{Plan: snapshot(t).Plan, Payload: contracts.Payload{Message: "hello"}, RequestHash: requestHash}
	r, e := adapter.Start(context.Background(), "workflow-id", input)
	g.Expect(e).NotTo(HaveOccurred())
	g.Expect(r.RunID).To(Equal("original-run"))
	g.Expect(r.PlanHash).To(Equal(input.Plan.Hash))
	g.Expect(f.options.ID).To(Equal("workflow-id"))
	g.Expect(f.options.TaskQueue).To(Equal("orchestrator.v1.temporal"))
	g.Expect(f.options.WorkflowExecutionTimeout).To(Equal(30 * time.Minute))
	g.Expect(f.options.RetryPolicy).To(BeNil())
	g.Expect(f.options.WorkflowIDReusePolicy).To(Equal(enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE))
	g.Expect(f.options.WorkflowIDConflictPolicy).To(Equal(enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL))
	g.Expect(f.options.WorkflowExecutionErrorWhenAlreadyStarted).To(BeTrue())
	g.Expect(f.workflow).To(Equal(contracts.ParentWorkflowName))
	g.Expect(f.args).To(Equal([]interface{}{input}))
	g.Expect(f.options.Memo).To(Equal(map[string]interface{}{"requestHash": requestHash, "planHash": input.Plan.Hash}))
}
func TestTemporalDescribeMemoAndNonblockingUnfinishedGet(t *testing.T) {
	for _, status := range []enumspb.WorkflowExecutionStatus{enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT} {
		t.Run(status.String(), func(t *testing.T) {
			g := NewWithT(t)
			f := &sdkClient{description: description(t, status), handle: &workflowRun{result: contracts.ParentResult{Results: []contracts.ChildResult{{WorkerName: "worker-a", Message: "hello"}}}}}
			r, e := runs.NewTemporal(f).Get(context.Background(), "workflow-id")
			g.Expect(e).NotTo(HaveOccurred())
			g.Expect(r.RequestHash).To(Equal(requestHash))
			g.Expect(r.Run.PlanHash).To(Equal(planHash))
			g.Expect(r.Run.RunID).To(Equal("original-run"))
			if status == enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED {
				g.Expect(f.handle.getCalls).To(Equal(1))
				g.Expect(r.Run.Result.Results).To(HaveLen(1))
				g.Expect(f.gotRunID).To(Equal("original-run"))
				g.Expect(f.handle.getOptions.DisableFollowingRuns).To(BeTrue())
			} else {
				g.Expect(f.handle.getCalls).To(BeZero())
			}
			if status == enumspb.WORKFLOW_EXECUTION_STATUS_FAILED {
				g.Expect(r.Run.Failure).NotTo(BeEmpty())
			}
		})
	}
}
func TestTemporalRejectsCorruptDurableMetadata(t *testing.T) {
	mutations := map[string]func(*workflowservice.DescribeWorkflowExecutionResponse){"nil": func(d *workflowservice.DescribeWorkflowExecutionResponse) { d.WorkflowExecutionInfo = nil }, "memo": func(d *workflowservice.DescribeWorkflowExecutionResponse) { d.WorkflowExecutionInfo.Memo = nil }, "request": func(d *workflowservice.DescribeWorkflowExecutionResponse) {
		delete(d.WorkflowExecutionInfo.Memo.Fields, "requestHash")
	}, "plan": func(d *workflowservice.DescribeWorkflowExecutionResponse) {
		d.WorkflowExecutionInfo.Memo.Fields["planHash"] = &commonpb.Payload{Data: []byte("bad")}
	}, "run": func(d *workflowservice.DescribeWorkflowExecutionResponse) {
		d.WorkflowExecutionInfo.Execution.RunId = ""
	}, "id": func(d *workflowservice.DescribeWorkflowExecutionResponse) {
		d.WorkflowExecutionInfo.Execution.WorkflowId = "wrong"
	}, "status": func(d *workflowservice.DescribeWorkflowExecutionResponse) {
		d.WorkflowExecutionInfo.Status = enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED
	}}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			d := description(t, enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING)
			mutate(d)
			_, e := runs.NewTemporal(&sdkClient{description: d}).Get(context.Background(), "workflow-id")
			g.Expect(e).To(HaveOccurred())
		})
	}
}
func TestTemporalCompletedResultFailuresRemainTruthful(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err, want error
	}{
		{"timeout", context.DeadlineExceeded, runs.ErrUnavailable},
		{"unexpected", errors.New("sensitive result error"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			f := &sdkClient{description: description(t, enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED), handle: &workflowRun{err: tc.err}}
			execution, err := runs.NewTemporal(f).Get(context.Background(), "workflow-id")
			g.Expect(err).To(HaveOccurred())
			g.Expect(execution).To(BeZero())
			if tc.want != nil {
				g.Expect(errors.Is(err, tc.want)).To(BeTrue())
			} else {
				g.Expect(err).To(MatchError(tc.err))
			}
		})
	}
}

func TestTemporalMapsSDKExceptions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err, want error
	}{
		{"active duplicate", serviceerror.NewWorkflowExecutionAlreadyStarted("secret", "request", "original-run"), runs.ErrAlreadyExists},
		{"completed duplicate", serviceerror.NewWorkflowExecutionAlreadyStarted("secret", "request", "original-run"), runs.ErrAlreadyExists},
		{"unavailable", serviceerror.NewUnavailable("secret"), runs.ErrUnavailable}, {"deadline", context.DeadlineExceeded, runs.ErrUnavailable}, {"cancel", context.Canceled, runs.ErrUnavailable}, {"not found", serviceerror.NewNotFound("secret"), runs.ErrNotFound},
		{"namespace missing", serviceerror.NewNamespaceNotFound("missing"), runs.ErrUnavailable},
		{"SDK deadline", serviceerror.NewDeadlineExceeded("secret"), runs.ErrUnavailable},
		{"SDK canceled", serviceerror.NewCanceled("secret"), runs.ErrUnavailable},
		{"throttled", serviceerror.NewResourceExhausted(enumspb.RESOURCE_EXHAUSTED_CAUSE_RPS_LIMIT, "secret"), runs.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			f := &sdkClient{executeErr: tc.err, describeErr: tc.err}
			_, e := runs.NewTemporal(f).Start(context.Background(), "workflow-id", contracts.ParentInput{})
			g.Expect(errors.Is(e, tc.want)).To(BeTrue())
			_, e = runs.NewTemporal(f).Get(context.Background(), "workflow-id")
			g.Expect(errors.Is(e, tc.want)).To(BeTrue())
		})
	}
	g := NewWithT(t)
	unexpected := errors.New("secret internal")
	_, e := runs.NewTemporal(&sdkClient{describeErr: unexpected}).Get(context.Background(), "workflow-id")
	g.Expect(e).To(MatchError(unexpected))
}

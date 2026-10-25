package workflows

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/worker/activities"
	. "github.com/onsi/gomega"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

type activityRecorder struct {
	interceptor.WorkerInterceptorBase
	name    string
	options workflow.ActivityOptions
	input   contracts.ChildInput
	// Tests may shorten heartbeat delivery without changing production defaults.
	heartbeatTimeout time.Duration
}
type activityInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	recorder *activityRecorder
}
type activityOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
	recorder *activityRecorder
}

func (r *activityRecorder) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &activityInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, recorder: r}
}
func (r *activityInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return r.Next.Init(&activityOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}, recorder: r.recorder})
}
func (r *activityOutbound) ExecuteActivity(ctx workflow.Context, name string, args ...any) workflow.Future {
	r.recorder.name = name
	r.recorder.options = workflow.GetActivityOptions(ctx)
	r.recorder.input = args[0].(contracts.ChildInput)
	if r.recorder.heartbeatTimeout > 0 {
		o := r.recorder.options
		o.HeartbeatTimeout = r.recorder.heartbeatTimeout
		ctx = workflow.WithActivityOptions(ctx, o)
	}
	return r.Next.ExecuteActivity(ctx, name, args...)
}

func TestChildOptionsAndResult(t *testing.T) {
	g := NewWithT(t)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	recorder := &activityRecorder{}
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{recorder}})
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{TaskQueue: "worker-b.v1.temporal"})
	env.RegisterWorkflowWithOptions(Child, workflow.RegisterOptions{Name: contracts.ChildWorkflowName})
	a := activities.New("worker-b", time.Millisecond)
	env.RegisterActivityWithOptions(a.Example, activity.RegisterOptions{Name: contracts.ExampleActivityName})
	input := contracts.ChildInput{WorkerName: "worker-b", Payload: contracts.Payload{Message: "hello"}}
	env.ExecuteWorkflow(contracts.ChildWorkflowName, input)
	g.Expect(env.GetWorkflowError()).NotTo(HaveOccurred())
	var result contracts.ChildResult
	g.Expect(env.GetWorkflowResult(&result)).To(Succeed())
	g.Expect(result).To(Equal(contracts.ChildResult{WorkerName: "worker-b", Message: "hello"}))
	g.Expect(recorder.name).To(Equal("ExampleActivity"))
	g.Expect(recorder.input).To(Equal(input))
	o := recorder.options
	g.Expect(o.TaskQueue).To(Equal("worker-b.v1.temporal"))
	g.Expect(o.StartToCloseTimeout).To(Equal(time.Minute))
	g.Expect(o.ScheduleToCloseTimeout).To(Equal(10 * time.Minute))
	g.Expect(o.HeartbeatTimeout).To(Equal(15 * time.Second))
	g.Expect(o.RetryPolicy).NotTo(BeNil())
	g.Expect(o.RetryPolicy.MaximumAttempts).To(Equal(int32(3)))
	g.Expect(o.RetryPolicy.InitialInterval).To(Equal(2 * time.Second))
	g.Expect(o.RetryPolicy.MaximumInterval).To(Equal(10 * time.Second))
	g.Expect(o.RetryPolicy.BackoffCoefficient).To(Equal(float64(2)))
}

// Actual SDK retries (not OnActivity mock returns) exercise the policy emitted by Child.
func TestChildActivityRetries(t *testing.T) {
	for _, succeed := range []bool{true, false} {
		t.Run(fmt.Sprintf("eventual success=%t", succeed), func(t *testing.T) {
			g := NewWithT(t)
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			var attempts atomic.Int32
			env.RegisterWorkflowWithOptions(Child, workflow.RegisterOptions{Name: contracts.ChildWorkflowName})
			env.RegisterActivityWithOptions(func(ctx context.Context, in contracts.ChildInput) (contracts.ChildResult, error) {
				attempts.Add(1)
				if succeed && activity.GetInfo(ctx).Attempt == 3 {
					return contracts.ChildResult{WorkerName: in.WorkerName, Message: in.Payload.Message}, nil
				}
				return contracts.ChildResult{}, fmt.Errorf("transient failure")
			}, activity.RegisterOptions{Name: contracts.ExampleActivityName})
			origin := env.Now()
			env.ExecuteWorkflow(contracts.ChildWorkflowName, contracts.ChildInput{WorkerName: "worker-a", Payload: contracts.Payload{Message: "hello"}})
			g.Expect(attempts.Load()).To(Equal(int32(3)))
			g.Expect(env.Now().Sub(origin)).To(Equal(6 * time.Second))
			if succeed {
				g.Expect(env.GetWorkflowError()).NotTo(HaveOccurred())
			} else {
				g.Expect(env.GetWorkflowError()).To(HaveOccurred())
			}
		})
	}
}

func TestChildMisroutingIsNotRetried(t *testing.T) {
	g := NewWithT(t)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var attempts atomic.Int32
	env.RegisterWorkflowWithOptions(Child, workflow.RegisterOptions{Name: contracts.ChildWorkflowName})
	a := activities.New("worker-b", time.Millisecond)
	env.RegisterActivityWithOptions(func(ctx context.Context, in contracts.ChildInput) (contracts.ChildResult, error) {
		attempts.Add(1)
		return a.Example(ctx, in)
	}, activity.RegisterOptions{Name: contracts.ExampleActivityName})
	env.ExecuteWorkflow(contracts.ChildWorkflowName, contracts.ChildInput{WorkerName: "worker-a"})
	g.Expect(env.GetWorkflowError()).To(HaveOccurred())
	g.Expect(attempts.Load()).To(Equal(int32(1)))
	var app *temporal.ApplicationError
	g.Expect(errors.As(env.GetWorkflowError(), &app)).To(BeTrue())
	g.Expect(app.NonRetryable()).To(BeTrue())
}

func TestChildCancellationReachesRunningActivity(t *testing.T) {
	g := NewWithT(t)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	stopped := make(chan error, 1)
	// SDK cancellation resolves the future before canceling the activity context;
	// context cancellation is delivered by the next non-throttled heartbeat.
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&activityRecorder{heartbeatTimeout: time.Second}}})
	env.RegisterWorkflowWithOptions(Child, workflow.RegisterOptions{Name: contracts.ChildWorkflowName})
	a := activities.New("worker-a", time.Hour)
	env.RegisterActivityWithOptions(func(ctx context.Context, in contracts.ChildInput) (contracts.ChildResult, error) {
		// Cancel only after an actual activity has started, not before scheduling it.
		env.RegisterDelayedCallback(env.CancelWorkflow, time.Millisecond)
		result, err := a.Example(ctx, in)
		stopped <- err
		return result, err
	}, activity.RegisterOptions{Name: contracts.ExampleActivityName})
	env.ExecuteWorkflow(contracts.ChildWorkflowName, contracts.ChildInput{WorkerName: "worker-a"})
	g.Expect(temporal.IsCanceledError(env.GetWorkflowError())).To(BeTrue())
	select {
	case err := <-stopped:
		g.Expect(err).To(MatchError(context.Canceled))
	case <-time.After(5 * time.Second):
		t.Fatal("activity did not observe SDK cancellation")
	}
}

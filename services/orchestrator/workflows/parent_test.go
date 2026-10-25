package workflows

import (
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	. "github.com/onsi/gomega"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// Observe actual outgoing SDK commands, not a duplicate options builder.
type childCommand struct {
	name    string
	input   contracts.ChildInput
	options workflow.ChildWorkflowOptions
}
type commandRecorder struct {
	interceptor.WorkerInterceptorBase
	commands []childCommand
}
type recordInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	recorder *commandRecorder
}
type recordOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
	recorder *commandRecorder
}

func (r *commandRecorder) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &recordInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, recorder: r}
}
func (r *recordInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return r.Next.Init(&recordOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}, recorder: r.recorder})
}
func (r *recordOutbound) ExecuteChildWorkflow(ctx workflow.Context, name string, args ...any) workflow.ChildWorkflowFuture {
	r.recorder.commands = append(r.recorder.commands, childCommand{name, args[0].(contracts.ChildInput), workflow.GetChildWorkflowOptions(ctx)})
	return r.Next.ExecuteChildWorkflow(ctx, name, args...)
}

func graph(nodes ...plan.Node) contracts.ParentInput {
	for i := range nodes {
		nodes[i].TaskQueue = nodes[i].Name + ".v1.temporal"
		nodes[i].WorkflowType = contracts.ChildWorkflowName
	}
	return contracts.ParentInput{Plan: plan.Plan{SchemaVersion: "1", Nodes: nodes, Hash: "immutable-plan-hash"}, Payload: contracts.Payload{Message: "hello"}, RequestHash: "request-hash"}
}

// The test child uses real workflow timers/cancellation, replacing only worker work
// so scheduler timing and terminal failures can be controlled without a server.
func TestParentScheduling(t *testing.T) {
	tests := []struct {
		name      string
		input     contracts.ParentInput
		durations map[string]time.Duration
		starts    map[string]time.Duration
	}{
		{"parallel roots and sorted results", graph(plan.Node{Name: "worker-a"}, plan.Node{Name: "worker-b"}), map[string]time.Duration{"worker-a": 10 * time.Second, "worker-b": time.Second}, map[string]time.Duration{"worker-a": 0, "worker-b": 0}},
		{"chain", graph(plan.Node{Name: "worker-a"}, plan.Node{Name: "worker-b", Dependencies: []string{"worker-a"}}, plan.Node{Name: "worker-c", Dependencies: []string{"worker-b"}}), map[string]time.Duration{"worker-a": time.Second, "worker-b": 2 * time.Second, "worker-c": time.Second}, map[string]time.Duration{"worker-a": 0, "worker-b": time.Second, "worker-c": 3 * time.Second}},
		{"diamond join", graph(plan.Node{Name: "worker-a"}, plan.Node{Name: "worker-b", Dependencies: []string{"worker-a"}}, plan.Node{Name: "worker-c", Dependencies: []string{"worker-a"}}, plan.Node{Name: "worker-d", Dependencies: []string{"worker-b", "worker-c"}}), map[string]time.Duration{"worker-a": time.Second, "worker-b": 2 * time.Second, "worker-c": 4 * time.Second, "worker-d": time.Second}, map[string]time.Duration{"worker-a": 0, "worker-b": time.Second, "worker-c": time.Second, "worker-d": 5 * time.Second}},
		{"fast branch progresses before unrelated root", graph(plan.Node{Name: "worker-a"}, plan.Node{Name: "worker-b"}, plan.Node{Name: "worker-c", Dependencies: []string{"worker-a"}}, plan.Node{Name: "worker-d", Dependencies: []string{"worker-c"}}), map[string]time.Duration{"worker-a": time.Second, "worker-b": 20 * time.Second, "worker-c": time.Second, "worker-d": time.Second}, map[string]time.Duration{"worker-a": 0, "worker-b": 0, "worker-c": time.Second, "worker-d": 2 * time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			recorder := &commandRecorder{}
			env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{recorder}})
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: "parent-identity", TaskQueue: "orchestrator.v1.temporal"})
			env.RegisterWorkflowWithOptions(Parent, workflow.RegisterOptions{Name: contracts.ParentWorkflowName})
			starts := map[string]time.Duration{}
			origin := env.Now()
			env.RegisterWorkflowWithOptions(func(ctx workflow.Context, in contracts.ChildInput) (contracts.ChildResult, error) {
				starts[in.WorkerName] = workflow.Now(ctx).Sub(origin)
				if err := workflow.Sleep(ctx, tt.durations[in.WorkerName]); err != nil {
					return contracts.ChildResult{}, err
				}
				return contracts.ChildResult{WorkerName: in.WorkerName, Message: in.Payload.Message}, nil
			}, workflow.RegisterOptions{Name: contracts.ChildWorkflowName})
			env.ExecuteWorkflow(contracts.ParentWorkflowName, tt.input)
			g.Expect(env.GetWorkflowError()).NotTo(HaveOccurred())
			g.Expect(starts).To(Equal(tt.starts))
			var result contracts.ParentResult
			g.Expect(env.GetWorkflowResult(&result)).To(Succeed())
			expected := make([]contracts.ChildResult, 0, len(tt.input.Plan.Nodes))
			for _, node := range tt.input.Plan.Nodes {
				expected = append(expected, contracts.ChildResult{WorkerName: node.Name, Message: "hello"})
			}
			g.Expect(result.Results).To(Equal(expected))
			g.Expect(recorder.commands).To(HaveLen(len(expected)))
			for _, cmd := range recorder.commands {
				g.Expect(cmd.name).To(Equal("ExampleChildWorkflow"))
				g.Expect(cmd.input.Payload.Message).To(Equal("hello"))
				g.Expect(cmd.options.TaskQueue).To(Equal(cmd.input.WorkerName + ".v1.temporal"))
				g.Expect(cmd.options.WorkflowID).To(Equal("parent-identity/" + cmd.input.WorkerName))
				g.Expect(cmd.options.WorkflowExecutionTimeout).To(Equal(15 * time.Minute))
				g.Expect(cmd.options.RetryPolicy).To(BeNil())
				g.Expect(cmd.options.ParentClosePolicy).To(Equal(enumspb.PARENT_CLOSE_POLICY_REQUEST_CANCEL))
			}
			// Root launches are ordered by canonical worker identity, not map traversal.
			g.Expect(recorder.commands[0].input.WorkerName).To(Equal("worker-a"))
		})
	}
}

func TestParentFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"root failure blocks diamond", "sibling failure cancels running child", "parent cancellation"} {
		t.Run(mode, func(t *testing.T) {
			g := NewWithT(t)
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			started := []string{}
			canceled := []string{}
			completed := []string{}
			// The suite stops processing queued listeners when its top-level
			// workflow returns. Keep only this test driver alive until cancellation
			// is observed, without making production wait for sibling completion.
			env.RegisterWorkflowWithOptions(func(ctx workflow.Context, in contracts.ParentInput) (contracts.ParentResult, error) {
				result, err := Parent(ctx, in)
				wantCanceled := 1
				if mode == "root failure blocks diamond" {
					wantCanceled = 0
				}
				if mode == "parent cancellation" {
					wantCanceled = 2
				}
				observationCtx, _ := workflow.NewDisconnectedContext(ctx)
				_, _ = workflow.AwaitWithTimeout(observationCtx, time.Minute, func() bool { return len(canceled) >= wantCanceled })
				return result, err
			}, workflow.RegisterOptions{Name: contracts.ParentWorkflowName})
			env.SetOnChildWorkflowCanceledListener(func(info *workflow.Info) { canceled = append(canceled, info.WorkflowExecution.ID) })
			env.SetOnChildWorkflowCompletedListener(func(info *workflow.Info, _ converter.EncodedValue, _ error) {
				completed = append(completed, info.WorkflowExecution.ID)
			})
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: "failure-parent"})
			env.RegisterWorkflowWithOptions(func(ctx workflow.Context, in contracts.ChildInput) (contracts.ChildResult, error) {
				started = append(started, in.WorkerName)
				if in.WorkerName == "worker-a" && mode != "parent cancellation" {
					if err := workflow.Sleep(ctx, time.Second); err != nil {
						return contracts.ChildResult{}, err
					}
					return contracts.ChildResult{}, temporal.NewNonRetryableApplicationError("terminal worker failure", "Terminal", nil)
				}
				if err := workflow.Sleep(ctx, time.Hour); err != nil {
					return contracts.ChildResult{}, err
				}
				return contracts.ChildResult{WorkerName: in.WorkerName, Message: in.Payload.Message}, nil
			}, workflow.RegisterOptions{Name: contracts.ChildWorkflowName})
			input := graph(plan.Node{Name: "worker-a"}, plan.Node{Name: "worker-b", Dependencies: []string{"worker-a"}}, plan.Node{Name: "worker-c", Dependencies: []string{"worker-a"}}, plan.Node{Name: "worker-d", Dependencies: []string{"worker-b", "worker-c"}})
			if mode != "root failure blocks diamond" {
				input = graph(plan.Node{Name: "worker-a"}, plan.Node{Name: "worker-b"}, plan.Node{Name: "worker-c", Dependencies: []string{"worker-a"}})
			}
			if mode == "parent cancellation" {
				env.RegisterDelayedCallback(env.CancelWorkflow, time.Second)
			}
			env.ExecuteWorkflow(contracts.ParentWorkflowName, input)
			g.Expect(env.GetWorkflowError()).To(HaveOccurred())
			if mode == "root failure blocks diamond" {
				g.Expect(started).To(Equal([]string{"worker-a"}))
				g.Expect(completed).To(Equal([]string{"failure-parent/worker-a"}))
			} else {
				g.Expect(started).To(ConsistOf("worker-a", "worker-b"))
				g.Expect(canceled).To(ContainElement("failure-parent/worker-b"))
				if mode == "parent cancellation" {
					g.Expect(temporal.IsCanceledError(env.GetWorkflowError())).To(BeTrue())
					g.Expect(canceled).To(ContainElement("failure-parent/worker-a"))
				}
			}
		})
	}
}

package activities

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	. "github.com/onsi/gomega"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
)

type heartbeatRecorder struct {
	interceptor.WorkerInterceptorBase
	mu         sync.Mutex
	times      []time.Time
	identities []any
}
type heartbeatInbound struct {
	interceptor.ActivityInboundInterceptorBase
	recorder *heartbeatRecorder
}
type heartbeatOutbound struct {
	interceptor.ActivityOutboundInterceptorBase
	recorder *heartbeatRecorder
}

func (r *heartbeatRecorder) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &heartbeatInbound{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, recorder: r}
}
func (r *heartbeatInbound) Init(next interceptor.ActivityOutboundInterceptor) error {
	return r.Next.Init(&heartbeatOutbound{ActivityOutboundInterceptorBase: interceptor.ActivityOutboundInterceptorBase{Next: next}, recorder: r.recorder})
}
func (r *heartbeatOutbound) RecordHeartbeat(ctx context.Context, details ...any) {
	r.recorder.mu.Lock()
	r.recorder.times = append(r.recorder.times, time.Now())
	r.recorder.identities = append(r.recorder.identities, details[0])
	r.recorder.mu.Unlock()
	r.Next.RecordHeartbeat(ctx, details...)
}

func TestExampleConfiguredDurationAndIdentity(t *testing.T) {
	g := NewWithT(t)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	a := New("worker-c", 20*time.Millisecond)
	env.RegisterActivityWithOptions(a.Example, activity.RegisterOptions{Name: contracts.ExampleActivityName})
	start := time.Now()
	encoded, err := env.ExecuteActivity(contracts.ExampleActivityName, contracts.ChildInput{WorkerName: "worker-c"})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(time.Since(start)).To(BeNumerically(">=", 20*time.Millisecond))
	var result contracts.ChildResult
	g.Expect(encoded.Get(&result)).To(Succeed())
	g.Expect(result).To(Equal(contracts.ChildResult{WorkerName: "worker-c"}))
}

func TestExampleDefaultDurationAndPeriodicHeartbeat(t *testing.T) {
	g := NewWithT(t)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	recorder := &heartbeatRecorder{}
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{recorder}})
	a := New("worker-a", 0)
	env.RegisterActivityWithOptions(a.Example, activity.RegisterOptions{Name: contracts.ExampleActivityName})
	start := time.Now()
	_, err := env.ExecuteActivity(contracts.ExampleActivityName, contracts.ChildInput{WorkerName: "worker-a"})
	elapsed := time.Since(start)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(elapsed).To(BeNumerically(">=", 5*time.Second))
	g.Expect(elapsed).To(BeNumerically("<", 8*time.Second))
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	g.Expect(recorder.times).To(HaveLen(3))
	g.Expect(recorder.identities).To(Equal([]any{"worker-a", "worker-a", "worker-a"}))
	g.Expect(recorder.times[1].Sub(recorder.times[0])).To(BeNumerically("~", 2*time.Second, 500*time.Millisecond))
	g.Expect(recorder.times[2].Sub(recorder.times[1])).To(BeNumerically("~", 2*time.Second, 500*time.Millisecond))
}

func TestExampleRejectsMisroutingAndEmptyIdentity(t *testing.T) {
	for _, local := range []string{"worker-b", ""} {
		t.Run("local="+local, func(t *testing.T) {
			g := NewWithT(t)
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestActivityEnvironment()
			a := New(local, time.Millisecond)
			env.RegisterActivityWithOptions(a.Example, activity.RegisterOptions{Name: contracts.ExampleActivityName})
			_, err := env.ExecuteActivity(contracts.ExampleActivityName, contracts.ChildInput{WorkerName: "worker-a"})
			g.Expect(err).To(HaveOccurred())
			var app *temporal.ApplicationError
			g.Expect(errors.As(err, &app)).To(BeTrue())
			g.Expect(app.NonRetryable()).To(BeTrue())
		})
	}
}

func TestExampleHonorsAlreadyCanceledContext(t *testing.T) {
	// The SDK-running cancellation case is in Child tests; here verify the activity
	// never returns success if cancellation predates its delay.
	g := NewWithT(t)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	a := New("worker-a", time.Hour)
	observed := make(chan error, 1)
	env.RegisterActivityWithOptions(func(ctx context.Context, in contracts.ChildInput) (contracts.ChildResult, error) {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		result, err := a.Example(ctx, in)
		observed <- err
		return result, err
	}, activity.RegisterOptions{Name: contracts.ExampleActivityName})
	_, err := env.ExecuteActivity(contracts.ExampleActivityName, contracts.ChildInput{WorkerName: "worker-a"})
	g.Expect(err).To(HaveOccurred())
	g.Expect(<-observed).To(MatchError(context.Canceled))
}

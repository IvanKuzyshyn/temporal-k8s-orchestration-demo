//go:build integration

// These tests mutate only the demo applications. scripts/scaling.sh snapshots
// and restores their static baseline even when a test fails.
package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/onsi/gomega"
	enumspb "go.temporal.io/api/enums/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

type harness struct {
	t    *testing.T
	g    *gomega.WithT
	c    client.Client
	root string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, g: gomega.NewWithT(t), root: os.Getenv("SCALING_ROOT")}
	h.g.Expect(h.root).NotTo(gomega.BeEmpty(), "use mise run test:scaling (explicit owned context required)")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var err error
	h.c, err = client.DialContext(ctx, client.Options{HostPort: h.forward("temporal", "temporal", 7233), Namespace: "default"})
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(h.c.Close)
	return h
}

func (h *harness) argv(args ...string) []string {
	return append([]string{"--kubeconfig", filepath.Join(h.root, ".local/kubeconfig"), "--context", "kind-temporal-demo", "--request-timeout=15s"}, args...)
}

func (h *harness) forward(ns, service string, port int) string {
	h.t.Helper()
	cmd := exec.Command("kubectl", h.argv("-n", ns, "port-forward", "--address", "127.0.0.1", "service/"+service, fmt.Sprintf(":%d", port))...)
	stdout, err := cmd.StdoutPipe()
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	// A file avoids concurrent reads of a buffer while kubectl is writing errors.
	log, err := os.CreateTemp(h.t.TempDir(), "forward-*.log")
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	cmd.Stderr = log
	h.g.Expect(cmd.Start()).To(gomega.Succeed())
	record := ""
	h.t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = log.Close()
		if record != "" {
			_ = os.Remove(record)
		}
	})
	if dir := os.Getenv("SCALING_FORWARD_DIR"); dir != "" {
		record = filepath.Join(dir, fmt.Sprint(cmd.Process.Pid))
		h.g.Expect(os.WriteFile(record, []byte(strings.Join(cmd.Args, " ")+"\n"), 0600)).To(gomega.Succeed())
	}
	address := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		sent := false
		for scanner.Scan() {
			if line := scanner.Text(); !sent && strings.HasPrefix(line, "Forwarding from 127.0.0.1:") {
				address <- strings.Fields(line)[2]
				sent = true
			}
		}
	}()
	select {
	case endpoint := <-address:
		return endpoint
	case <-time.After(15 * time.Second):
		h.t.Fatal("owned localhost tunnel timed out")
	}
	return ""
}

func (h *harness) k(args ...string) []byte {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	b, err := exec.CommandContext(ctx, "kubectl", h.argv(args...)...).CombinedOutput()
	h.g.Expect(err).NotTo(gomega.HaveOccurred(), string(b))
	return b
}

func (h *harness) preflight() {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r, err := h.c.CountWorkflow(ctx, &workflowservice.CountWorkflowExecutionsRequest{Namespace: "default", Query: "ExecutionStatus = 'Running'"})
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(r.GetCount()).To(gomega.BeZero(), "refuse fixture changes while unrelated executions are running")
	h.retainedHistory()
}

func TestScalingPreflight(t *testing.T) { newHarness(t).preflight() }

func (h *harness) zero(name string) bool {
	var pods struct{ Items []json.RawMessage }
	h.g.Expect(json.Unmarshal(h.k("-n", "temporal-demo-"+name, "get", "pods", "-l", "app="+name, "-o", "json"), &pods)).To(gomega.Succeed())
	return h.replicas(name) == "0" && len(pods.Items) == 0
}

func (h *harness) replicas(name string) string {
	return strings.TrimSpace(string(h.k("-n", "temporal-demo-"+name, "get", "deployment", name, "-o", "jsonpath={.spec.replicas}")))
}

func (h *harness) healthy(name string) {
	var so struct {
		Status struct {
			Conditions []struct{ Type, Status, Reason, Message string }
			Health     map[string]struct{ NumberOfFailures int }
		}
	}
	h.g.Expect(json.Unmarshal(h.k("-n", "temporal-demo-"+name, "get", "scaledobject", name, "-o", "json"), &so)).To(gomega.Succeed())
	ready := false
	for _, c := range so.Status.Conditions {
		if c.Type == "Ready" {
			h.g.Expect(c.Status).To(gomega.Equal("True"), "scaler error: %s: %s", c.Reason, c.Message)
			ready = true
		}
		if c.Type == "Active" {
			h.t.Logf("%s scaler Active=%s reason=%s", name, c.Status, c.Reason)
		}
	}
	h.g.Expect(ready).To(gomega.BeTrue(), "missing Ready condition is not inactivity")
	for metric, health := range so.Status.Health {
		h.g.Expect(health.NumberOfFailures).To(gomega.BeZero(), metric)
	}
}

// Pending controller state and read errors are returned to Eventually, never
// asserted inside its probe. Persistent errors retain their last diagnostics.
func (h *harness) zeroReady(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	read := func(args ...string) ([]byte, error) {
		b, err := exec.CommandContext(ctx, "kubectl", h.argv(append([]string{"-n", "temporal-demo-" + name}, args...)...)...).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("kubectl %v: %w: %s", args, err, b)
		}
		return b, nil
	}
	var deployment struct{ Spec struct{ Replicas *int } }
	b, err := read("get", "deployment", name, "-o", "json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &deployment); err != nil {
		return fmt.Errorf("Deployment %s: %w", name, err)
	}
	var pods struct{ Items []json.RawMessage }
	b, err = read("get", "pods", "-l", "app="+name, "-o", "json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &pods); err != nil {
		return fmt.Errorf("Pods %s: %w", name, err)
	}
	var scaler struct {
		Status struct {
			Conditions []struct{ Type, Status, Reason, Message string }
			Health     map[string]struct{ NumberOfFailures int }
		}
	}
	b, err = read("get", "scaledobject", name, "--ignore-not-found", "-o", "json")
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return fmt.Errorf("missing ScaledObject %s", name)
	}
	if err := json.Unmarshal(b, &scaler); err != nil {
		return fmt.Errorf("ScaledObject %s: %w", name, err)
	}
	ready, activeKnown, healthy := false, false, true
	for _, condition := range scaler.Status.Conditions {
		if condition.Type == "Ready" {
			ready = condition.Status == "True"
		}
		if condition.Type == "Active" {
			activeKnown = condition.Status == "True" || condition.Status == "False"
		}
	}
	for _, metric := range scaler.Status.Health {
		healthy = healthy && metric.NumberOfFailures == 0
	}
	replicas := -1
	if deployment.Spec.Replicas != nil {
		replicas = *deployment.Spec.Replicas
	}
	if replicas == 0 && len(pods.Items) == 0 && ready && activeKnown && healthy {
		return nil
	}
	return fmt.Errorf("waiting for %s zero real Pods and healthy Ready: replicas=%d pods=%d conditions=%+v health=%+v", name, replicas, len(pods.Items), scaler.Status.Conditions, scaler.Status.Health)
}

func (h *harness) waitZero(name string, bound time.Duration) {
	h.t.Helper()
	start := time.Now()
	if !h.g.Eventually(func() error { return h.zeroReady(name) }, bound, 2*time.Second).Should(gomega.Succeed()) {
		return
	}
	h.t.Logf("%s zero replicas AND zero real Pods, scaler Ready=True after %s; cached Temporal pollers are not live Pods", name, time.Since(start).Round(time.Millisecond))
}

// Render and validate the same buffered artifact that gets applied, retaining
// static discovery. The singleton uses plan.Build through validate-graph/API.
func (h *harness) graph(single bool) {
	h.t.Helper()
	image := strings.TrimSpace(string(h.k("-n", "temporal-demo-orchestrator", "get", "deployment", "orchestrator", "-o", "jsonpath={.spec.template.spec.containers[0].image}")))
	args := []string{"template", "temporal-demo", filepath.Join(h.root, "k8s/worker-helm-chart"), "-f", filepath.Join(h.root, "k8s/worker-helm-chart/values-static.yaml"), "-f", filepath.Join(h.root, "k8s/worker-helm-chart/values-scaling.yaml"), "--set-string", "images.tag=" + strings.Split(image, ":")[1], "--kube-version", "1.35.8"}
	if single {
		args = append(args, "--set-json", `workers=[{"name":"worker-a","runAfter":""}]`)
	}
	b, err := exec.Command("helm", args...).CombinedOutput()
	h.g.Expect(err).NotTo(gomega.HaveOccurred(), string(b))
	file := filepath.Join(h.t.TempDir(), "apps.yaml")
	h.g.Expect(os.WriteFile(file, b, 0600)).To(gomega.Succeed())
	cmd := exec.Command("go", "run", "./cmd/validate-graph")
	cmd.Dir = h.root
	cmd.Stdin = bytes.NewReader(b)
	out, err := cmd.CombinedOutput()
	h.g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
	h.t.Logf("fixture graph: %s", bytes.TrimSpace(out))
	h.k("apply", "--dry-run=server", "--validate=strict", "-f", file)
	h.k("apply", "-f", file)
	h.k("-n", "temporal-demo-orchestrator", "rollout", "status", "deployment/orchestrator", "--timeout=180s")
}

func (h *harness) own(run client.WorkflowRun) {
	h.t.Helper()
	h.t.Logf("owned execution %s/%s", run.GetID(), run.GetRunID())
	h.t.Cleanup(func() { h.finish(run) })
	// A temporary exact-ID list lets the shell trap repeat native SDK cleanup
	// after a test-process timeout; it never discovers or adopts unrelated runs.
	if path := os.Getenv("SCALING_RUNS_FILE"); path != "" {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		err = json.NewEncoder(file).Encode(executionRef{run.GetID(), run.GetRunID()})
		closeErr := file.Close()
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		h.g.Expect(closeErr).NotTo(gomega.HaveOccurred())
	}
}

func (h *harness) finish(run client.WorkflowRun) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d, err := h.c.DescribeWorkflowExecution(ctx, run.GetID(), run.GetRunID())
	if err != nil {
		h.t.Errorf("owned execution cleanup describe: %v", err)
		return
	}
	if d.GetWorkflowExecutionInfo().GetStatus() == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		if err := h.c.TerminateWorkflow(ctx, run.GetID(), run.GetRunID(), "owned Task7 cleanup"); err != nil {
			h.t.Errorf("terminate owned execution: %v", err)
		}
	}
	// ParentClosePolicy is REQUEST_CANCEL, not terminate. Explicitly finish
	// only children named in this owned parent's actual history on failure.
	iter := h.c.GetWorkflowHistory(ctx, run.GetID(), run.GetRunID(), false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iter.HasNext() {
		event, err := iter.Next()
		if err != nil {
			h.t.Errorf("owned history cleanup: %v", err)
			break
		}
		if child := event.GetChildWorkflowExecutionStartedEventAttributes(); child != nil {
			execution := child.WorkflowExecution
			info, err := h.c.DescribeWorkflowExecution(ctx, execution.WorkflowId, execution.RunId)
			if err != nil {
				h.t.Errorf("owned child cleanup: %v", err)
				continue
			}
			if info.GetWorkflowExecutionInfo().GetStatus() == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
				if err := h.c.TerminateWorkflow(ctx, execution.WorkflowId, execution.RunId, "owned Task7 child cleanup"); err != nil {
					h.t.Errorf("terminate owned child: %v", err)
				}
			}
		}
	}
}

func (h *harness) parent(names []string) {
	h.t.Helper()
	endpoint := "http://" + h.forward("temporal-demo-orchestrator", "orchestrator", 8080)
	req, err := http.NewRequest(http.MethodPost, endpoint+"/runs", nil)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	req.Header.Set("Idempotency-Key", fmt.Sprintf("task7-%s-%d", h.t.Name(), time.Now().UnixNano()))
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	defer response.Body.Close()
	h.g.Expect(response.StatusCode).To(gomega.Equal(http.StatusAccepted))
	var identity struct{ WorkflowID, RunID string }
	h.g.Expect(json.NewDecoder(response.Body).Decode(&identity)).To(gomega.Succeed())
	run := h.c.GetWorkflow(context.Background(), identity.WorkflowID, identity.RunID)
	h.own(run)
	start := time.Now()
	h.g.Eventually(func() string { return h.replicas(names[0]) }, 90*time.Second, time.Second).Should(gomega.Equal("1"))
	h.t.Logf("activation from zero in %s", time.Since(start).Round(time.Millisecond))
	h.healthy(names[0])
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var result contracts.ParentResult
	h.g.Expect(run.Get(ctx, &result)).To(gomega.Succeed())
	expected := make([]contracts.ChildResult, 0, len(names))
	for _, name := range names {
		expected = append(expected, contracts.ChildResult{WorkerName: name})
	}
	h.g.Expect(result.Results).To(gomega.Equal(expected))
	h.t.Logf("identity-only result %+v", result)
	for _, name := range names {
		h.waitZero(name, 3*time.Minute)
	}
}

func TestActivationFromZero(t *testing.T) {
	h := newHarness(t)
	h.preflight()
	h.waitZero("worker-a", 3*time.Minute)
	h.graph(true)
	h.waitZero("worker-a", 3*time.Minute)
	h.parent([]string{"worker-a"})
	h.graph(false)
	for _, name := range []string{"worker-a", "worker-b", "worker-c", "worker-d"} {
		h.waitZero(name, 3*time.Minute)
	}
	h.parent([]string{"worker-a", "worker-b", "worker-c", "worker-d"})
}

func TestReplicaHandoffReapply(t *testing.T) {
	h := newHarness(t)
	h.preflight()
	h.waitZero("worker-a", 3*time.Minute)
	// Deployment generation also changes on annotation-only updates. Watch
	// actual replica values instead, including any transient reset to one.
	watch := exec.Command("kubectl", h.argv("-n", "temporal-demo-worker-a", "get", "deployment", "worker-a", "--watch", "--output-watch-events", "-o", "json")...)
	stdout, err := watch.StdoutPipe()
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(watch.Start()).To(gomega.Succeed())
	t.Cleanup(func() { _ = watch.Process.Kill(); _ = watch.Wait() })
	replicas := make(chan int, 32)
	go func() {
		defer close(replicas)
		dec := json.NewDecoder(stdout)
		for {
			var event struct {
				Object struct{ Spec struct{ Replicas *int } }
			}
			if err := dec.Decode(&event); err != nil {
				return
			}
			if event.Object.Spec.Replicas == nil {
				replicas <- -1
			} else {
				replicas <- *event.Object.Spec.Replicas
			}
		}
	}()
	select {
	case value := <-replicas:
		h.g.Expect(value).To(gomega.BeZero())
	case <-time.After(15 * time.Second):
		t.Fatal("replica watch did not start")
	}
	cmd := exec.Command("bash", "scripts/scaling.sh", "deploy")
	cmd.Dir = h.root
	b, err := cmd.CombinedOutput()
	_ = watch.Process.Kill()
	h.g.Expect(err).NotTo(gomega.HaveOccurred(), string(b))
	observed := 0
	for value := range replicas {
		h.g.Expect(value).To(gomega.BeZero(), "reapply transiently reset KEDA's replicas")
		observed++
	}
	_ = watch.Wait()
	h.g.Expect(observed).To(gomega.BeNumerically(">", 0), "watch must observe the reapply updates")
	h.g.Expect(h.zero("worker-a")).To(gomega.BeTrue())
	h.t.Logf("steady-state native reapply: %d update events, every replica value remained zero", observed)
}

// Test-only workflow, independently polled on the host. Activity input/result
// stay identity-only; the production worker/config/contracts are unchanged.
// Keeping workflow tasks off worker-a isolates activity-only retry activation.
func activityProbe(ctx workflow.Context) (contracts.ChildResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: "worker-a.v1.temporal", StartToCloseTimeout: 2 * time.Minute, ScheduleToCloseTimeout: 10 * time.Minute, HeartbeatTimeout: 15 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{InitialInterval: 90 * time.Second, MaximumInterval: 90 * time.Second, BackoffCoefficient: 1, MaximumAttempts: 3},
	})
	var result contracts.ChildResult
	err := workflow.ExecuteActivity(ctx, contracts.ExampleActivityName, contracts.ChildInput{WorkerName: "worker-a"}).Get(ctx, &result)
	return result, err
}

func (h *harness) probe() client.WorkflowRun {
	queue := fmt.Sprintf("task7-probe-%d", time.Now().UnixNano())
	w := worker.New(h.c, queue, worker.Options{})
	w.RegisterWorkflow(activityProbe)
	h.g.Expect(w.Start()).To(gomega.Succeed())
	h.t.Cleanup(w.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run, err := h.c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: queue, TaskQueue: queue, WorkflowExecutionTimeout: 10 * time.Minute}, activityProbe)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.own(run)
	return run
}

func (h *harness) pending(run client.WorkflowRun) *workflowpb.PendingActivityInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := h.c.DescribeWorkflowExecution(ctx, run.GetID(), run.GetRunID())
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	if len(d.PendingActivities) == 0 {
		return nil
	}
	return d.PendingActivities[0]
}

func (h *harness) configure(cooldown int, count bool, duration string) {
	h.k("-n", "temporal-demo-worker-a", "patch", "scaledobject", "worker-a", "--type=merge", "-p", fmt.Sprintf(`{"spec":{"cooldownPeriod":%d,"triggers":[{"type":"temporal","metadata":{"endpoint":"temporal.temporal.svc.cluster.local:7233","namespace":"default","taskQueue":"worker-a.v1.temporal","queueTypes":"workflow,activity","activationTargetQueueSize":"0","targetQueueSize":"1","enableTLS":"false","includeRunningWorkflowCount":"%t"}}]}}`, cooldown, count))
	h.k("-n", "temporal-demo-worker-a", "set", "env", "deployment/worker-a", "ACTIVITY_DURATION="+duration)
}

func (h *harness) started(run client.WorkflowRun) {
	h.g.Eventually(func() bool { p := h.pending(run); return p != nil && p.State == enumspb.PENDING_ACTIVITY_STATE_STARTED }, 90*time.Second, 500*time.Millisecond).Should(gomega.BeTrue())
	p := h.pending(run)
	h.t.Logf("activity started attempt=%d identity=%s", p.GetAttempt(), p.GetLastWorkerIdentity())
}

func (h *harness) recovered(run client.WorkflowRun, requireRetry bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var result contracts.ChildResult
	h.g.Expect(run.Get(ctx, &result)).To(gomega.Succeed())
	h.g.Expect(result.WorkerName).To(gomega.Equal("worker-a"))
	iter := h.c.GetWorkflowHistory(ctx, run.GetID(), run.GetRunID(), false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	attempt := int32(0)
	for iter.HasNext() {
		event, err := iter.Next()
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		if a := event.GetActivityTaskStartedEventAttributes(); a != nil {
			attempt = a.Attempt
			h.t.Logf("history ActivityTaskStarted event=%d attempt=%d identity=%s", event.EventId, a.Attempt, a.Identity)
		}
	}
	if requireRetry {
		h.g.Expect(attempt).To(gomega.BeNumerically(">=", 2), "delayed recovery must include a real retry")
	} else {
		h.g.Expect(attempt).To(gomega.BeNumerically(">=", 1))
	}
	h.waitZero("worker-a", 3*time.Minute)
}

func TestInFlightScaleDown(t *testing.T) {
	if os.Getenv("SCALING_SKIP_LONG") == "1" {
		t.Skip("long in-flight experiment is manual-dispatch in CI; required locally")
	}
	h := newHarness(t)
	h.preflight()
	h.waitZero("worker-a", 3*time.Minute)
	h.configure(5, false, "50s")
	run := h.probe()
	h.started(run)
	pod := strings.TrimSpace(string(h.k("-n", "temporal-demo-worker-a", "get", "pods", "-l", "app=worker-a", "-o", "jsonpath={.items[0].metadata.name}")))
	// Temporal can dispatch before kubelet publishes Running. Wait for that
	// status before opening logs, including when KEDA already began shutdown.
	h.k("-n", "temporal-demo-worker-a", "wait", "--for=jsonpath={.status.containerStatuses[0].state.running.startedAt}", "pod/"+pod, "--timeout=30s")
	logs, err := os.CreateTemp(t.TempDir(), "shutdown-*.log")
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	cmd := exec.Command("kubectl", h.argv("--request-timeout=90s", "-n", "temporal-demo-worker-a", "logs", "-f", pod, "--timestamps", "--tail=100")...)
	cmd.Stdout = logs
	cmd.Stderr = logs
	h.g.Expect(cmd.Start()).To(gomega.Succeed())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = logs.Close() })
	h.waitZero("worker-a", 90*time.Second)
	// The baseline backlog scaler may interrupt. Give the retry a sufficient
	// window rather than making the invalid claim that backlog means in-flight.
	h.configure(120, false, "5s")
	h.g.Eventually(func() bool { p := h.pending(run); return p == nil || (p.Attempt >= 2 && p.LastFailure != nil) }, 30*time.Second, time.Second).Should(gomega.BeTrue())
	p := h.pending(run)
	if p != nil {
		h.t.Logf("interruption: attempt=%d failure=%s next=%s", p.Attempt, p.LastFailure.GetMessage(), p.NextAttemptScheduleTime.AsTime())
	} else {
		h.t.Log("baseline finished during shutdown; no interrupted retry observed")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	_ = logs.Close()
	b, err := os.ReadFile(logs.Name())
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(string(b)).To(gomega.ContainSubstring("shutdown requested"))
	h.t.Logf("actual shutdown logs:\n%s", b)
	h.recovered(run, false)
}

func (h *harness) backlog(qt enumspb.TaskQueueType) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := h.c.WorkflowService().DescribeTaskQueue(ctx, &workflowservice.DescribeTaskQueueRequest{Namespace: "default", TaskQueue: &taskqueuepb.TaskQueue{Name: "worker-a.v1.temporal", Kind: enumspb.TASK_QUEUE_KIND_NORMAL}, TaskQueueType: qt, ReportStats: true})
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(r.Stats).NotTo(gomega.BeNil(), "pinned native scaler requires ReportStats, not a poller count")
	return r.Stats.ApproximateBacklogCount
}

func TestDelayedRetryReactivation(t *testing.T) {
	h := newHarness(t)
	h.preflight()
	h.waitZero("worker-a", 3*time.Minute)
	h.configure(20, false, "50s")
	run := h.probe()
	h.started(run)
	// A deliberate, scoped application termination supplies a real failed attempt
	// without adding a failure switch or test payload to the production worker.
	h.k("-n", "temporal-demo-worker-a", "delete", "pod", "-l", "app=worker-a", "--grace-period=0", "--force", "--wait=true", "--timeout=30s")
	h.g.Eventually(func() bool {
		p := h.pending(run)
		return p != nil && p.Attempt == 2 && p.NextAttemptScheduleTime != nil
	}, 40*time.Second, time.Second).Should(gomega.BeTrue())
	p := h.pending(run)
	next := p.NextAttemptScheduleTime.AsTime()
	h.t.Logf("failed attempt1: %s; attempt=%d delayed until %s", p.LastFailure.GetMessage(), p.Attempt, next)
	h.waitZero("worker-a", 75*time.Second)
	h.g.Expect(time.Until(next)).To(gomega.BeNumerically(">", 10*time.Second), "retry must genuinely outlast scale-to-zero")
	h.configure(20, false, "5s")
	// Hold at zero ONLY to observe the retry backlog directly before releasing
	// KEDA. The workflow poller is on a different queue; worker-a has no pollers.
	h.k("-n", "temporal-demo-worker-a", "annotate", "scaledobject", "worker-a", "autoscaling.keda.sh/paused=true", "--overwrite")
	h.g.Eventually(func() int64 { return h.backlog(enumspb.TASK_QUEUE_TYPE_ACTIVITY) }, 2*time.Minute, time.Second).Should(gomega.BeNumerically(">", 0))
	h.g.Expect(h.zero("worker-a")).To(gomega.BeTrue())
	h.g.Expect(h.backlog(enumspb.TASK_QUEUE_TYPE_WORKFLOW)).To(gomega.BeZero())
	h.t.Logf("observed delayed ACTIVITY backlog=%d WORKFLOW backlog=0 with zero real Pods", h.backlog(enumspb.TASK_QUEUE_TYPE_ACTIVITY))
	h.k("-n", "temporal-demo-worker-a", "annotate", "scaledobject", "worker-a", "autoscaling.keda.sh/paused-")
	start := time.Now()
	h.g.Eventually(func() string { return h.replicas("worker-a") }, 60*time.Second, time.Second).Should(gomega.Equal("1"))
	h.t.Logf("activity-only retry reactivation after unpause: %s", time.Since(start).Round(time.Millisecond))
	h.healthy("worker-a")
	h.recovered(run, true)
	h.configure(120, false, "5s")
}

func TestRunningCountOptIn(t *testing.T) {
	h := newHarness(t)
	h.preflight()
	h.waitZero("worker-a", 3*time.Minute)
	// Real production child workflow stays open on this queue for the activity.
	// A short test cooldown exposes the flag's effect; it is not a guarantee.
	h.configure(5, true, "50s")
	p, err := plan.Build([]plan.Registration{{SchemaVersion: "1", WorkerName: "worker-a", TaskQueue: "worker-a.v1.temporal", WorkflowType: contracts.ChildWorkflowName, SourceServiceName: "worker-a", SourceServiceNamespace: "temporal-demo-worker-a"}})
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run, err := h.c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: fmt.Sprintf("task7-running-count-%d", time.Now().UnixNano()), TaskQueue: "orchestrator.v1.temporal", WorkflowExecutionTimeout: 30 * time.Minute}, contracts.ParentWorkflowName, contracts.ParentInput{Plan: p})
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.own(run)
	// Verify the actual visibility query used by pinned KEDA, including its
	// unversioned filter, rather than assuming a metadata key means support.
	query := "ExecutionStatus = 'Running' AND TaskQueue = 'worker-a.v1.temporal' AND TemporalWorkerDeploymentVersion IS NULL"
	h.g.Eventually(func() int64 {
		c, err := h.c.CountWorkflow(ctx, &workflowservice.CountWorkflowExecutionsRequest{Namespace: "default", Query: query})
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		return c.Count
	}, 60*time.Second, time.Second).Should(gomega.BeNumerically(">", 0))
	h.started(h.c.GetWorkflow(ctx, run.GetID()+"/worker-a", ""))
	h.g.Consistently(func() string { return h.replicas("worker-a") }, 20*time.Second, time.Second).Should(gomega.Equal("1"))
	h.healthy("worker-a")
	var result contracts.ParentResult
	h.g.Expect(run.Get(ctx, &result)).To(gomega.Succeed())
	h.waitZero("worker-a", 90*time.Second)
	h.t.Log("opt-in visibility running count observed; held one replica beyond test cooldown; not a safety guarantee")
	h.configure(120, false, "5s")
}

//go:build integration && (acceptance_integration || resilience_integration)

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/registry"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/runs"
	"github.com/onsi/gomega"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/encoding/protojson"
)

var diamondNames = []string{"worker-a", "worker-b", "worker-c", "worker-d"}

type acceptanceHarness struct {
	*harness
	endpoint string
	ctx      context.Context
}

func newAcceptanceHarness(t *testing.T) *acceptanceHarness {
	t.Helper()
	g := gomega.NewWithT(t)
	root, endpoint, address := os.Getenv("SCALING_ROOT"), os.Getenv("DEMO_API_URL"), os.Getenv("TEMPORAL_ADDRESS")
	g.Expect(root).NotTo(gomega.BeEmpty(), "use mise run smoke or test:resilience")
	g.Expect(endpoint).NotTo(gomega.BeEmpty(), "DEMO_API_URL is mandatory")
	g.Expect(address).NotTo(gomega.BeEmpty(), "TEMPORAL_ADDRESS is mandatory")
	g.Expect(os.Getenv("DEMO_KUBE_CONTEXT")).To(gomega.Equal("kind-temporal-demo"), "explicit owned context is mandatory")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	c, err := client.DialContext(ctx, client.Options{HostPort: address, Namespace: "default"})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(c.Close)
	h := &acceptanceHarness{harness: &harness{t: t, g: g, c: c, root: root}, endpoint: endpoint, ctx: ctx}
	// Fail promptly on missing combined wiring, rather than treating a static
	// registry and cached Temporal pollers as a live-discovery/scaling proof.
	mode := strings.TrimSpace(string(h.k("-n", "temporal-demo-orchestrator", "get", "deployment", "orchestrator", "-o", `jsonpath={.spec.template.spec.containers[0].env[?(@.name=="REGISTRY_MODE")].value}`)))
	g.Expect(mode).To(gomega.Equal("kubernetes"), "acceptance requires combined live registry and native scaling")
	// A preceding experiment may have replaced the Pod behind the initial
	// environment-supplied tunnel. Reopen only our own localhost tunnel.
	if code, _, err := h.request("GET", "/healthz", "", nil); err != nil || code != 200 {
		h.endpoint = "http://" + h.forward("temporal-demo-orchestrator", "orchestrator", 8080)
	}
	return h
}

func (h *acceptanceHarness) request(method, path, key string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(h.ctx, method, h.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, b, err
}

func (h *acceptanceHarness) expected(changed bool) plan.Plan {
	b, err := os.ReadFile(filepath.Join(h.root, "deploy/config/workers.json"))
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	var registrations []plan.Registration
	h.g.Expect(json.Unmarshal(b, &registrations)).To(gomega.Succeed())
	if changed {
		for i := range registrations {
			if registrations[i].WorkerName == "worker-b" {
				registrations[i].RunAfter = "worker-c"
			}
		}
	}
	p, err := plan.Build(registrations)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	return p
}

func (h *acceptanceHarness) waitPlan(p plan.Plan) {
	h.g.Eventually(func() error {
		code, b, err := h.request("GET", "/workers", "", nil)
		if err != nil {
			return err
		}
		var s registry.Snapshot
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if code != 200 || !s.Synced || len(s.Problems) != 0 || len(s.Plan.Nodes) != 4 || !reflect.DeepEqual(s.Plan, p) {
			return fmt.Errorf("registry %d: %s", code, b)
		}
		return nil
	}, 90*time.Second, time.Second).Should(gomega.Succeed())
	var registrations struct {
		Items []struct {
			Metadata struct{ Name, Namespace string }
			Data     map[string]string
		}
	}
	h.g.Expect(json.Unmarshal(h.k("get", "configmaps", "-A", "-l", "temporal-demo.io/registration=true", "-o", "json"), &registrations)).To(gomega.Succeed())
	h.g.Expect(registrations.Items).To(gomega.HaveLen(4))
	seen := map[string]bool{}
	for _, r := range registrations.Items {
		name := r.Data["workerName"]
		h.g.Expect(diamondNames).To(gomega.ContainElement(name))
		h.g.Expect(seen[name]).To(gomega.BeFalse())
		seen[name] = true
		h.g.Expect(r.Metadata.Namespace).To(gomega.Equal("temporal-demo-" + name))
		h.g.Expect(r.Metadata.Name).To(gomega.Equal("temporal-worker-" + name))
	}
	h.t.Logf("exact four registrations and canonical plan hash=%s", p.Hash)
}

func (h *acceptanceHarness) start(key string) runs.Run {
	h.g.Expect(runs.ValidKey(key)).To(gomega.BeTrue(), "acceptance starts require an exact test key")
	sum := sha256.Sum256([]byte(key))
	ref := executionRef{ID: fmt.Sprintf("run-%x", sum), Pending: true}
	path := os.Getenv("SCALING_RUNS_FILE")
	h.g.Expect(recordExecution(path, ref)).To(gomega.Succeed(), "persist exact start intent before POST")
	// Registered before any response assertion, including transport/decode errors.
	// A process timeout leaves the same pending intent for the shell fallback.
	h.t.Cleanup(func() { h.cleanupExecution(ref, path) })
	code, b, err := h.request("POST", "/runs", key, nil)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(code).To(gomega.Equal(202), string(b))
	var r runs.Run
	h.g.Expect(json.Unmarshal(b, &r)).To(gomega.Succeed())
	h.g.Expect(r.WorkflowID).To(gomega.Equal(ref.ID), "response must match our exact keyed start intent")
	h.g.Expect(r.RunID).NotTo(gomega.BeEmpty())
	ref.RunID = r.RunID
	ref.Pending = false
	h.g.Expect(recordExecution(path, ref)).To(gomega.Succeed(), "persist accepted execution pin")
	h.t.Logf("owned execution %s/%s", ref.ID, ref.RunID)
	return r
}
func (h *acceptanceHarness) replay(key string, original runs.Run) {
	code, b, err := h.request("POST", "/runs", key, nil)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(code).To(gomega.Equal(202), string(b))
	var got runs.Run
	h.g.Expect(json.Unmarshal(b, &got)).To(gomega.Succeed())
	h.g.Expect(got).To(gomega.Equal(original), "durable identity/plan/status/result changed")
}
func (h *acceptanceHarness) completed(r runs.Run) runs.Run {
	ctx, cancel := context.WithTimeout(h.ctx, 4*time.Minute)
	defer cancel()
	var result contracts.ParentResult
	h.g.Expect(h.c.GetWorkflow(ctx, r.WorkflowID, r.RunID).Get(ctx, &result)).To(gomega.Succeed())
	h.g.Expect(result).To(gomega.Equal(identityResult()))
	code, b, err := h.request("GET", "/runs/"+r.WorkflowID, "", nil)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(code).To(gomega.Equal(200), string(b))
	var got runs.Run
	h.g.Expect(json.Unmarshal(b, &got)).To(gomega.Succeed())
	h.g.Expect(got.WorkflowID).To(gomega.Equal(r.WorkflowID))
	h.g.Expect(got.RunID).To(gomega.Equal(r.RunID))
	h.g.Expect(got.PlanHash).To(gomega.Equal(r.PlanHash))
	h.g.Expect(got.Status).To(gomega.Equal("completed"))
	h.g.Expect(got.Result).To(gomega.Equal(&result))
	return got
}
func identityResult() contracts.ParentResult {
	r := contracts.ParentResult{}
	for _, name := range diamondNames {
		r.Results = append(r.Results, contracts.ChildResult{WorkerName: name})
	}
	return r
}
func keyID(key string) string { return fmt.Sprintf("run-%x", sha256.Sum256([]byte(key))) }
func (h *acceptanceHarness) missing(key string) {
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()
	_, err := h.c.DescribeWorkflowExecution(ctx, keyID(key), "")
	var missing *serviceerror.NotFound
	h.g.Expect(errors.As(err, &missing)).To(gomega.BeTrue(), "phantom start for %s: %v", keyID(key), err)
}
func (h *acceptanceHarness) restart() {
	// Snapshot restoration is owned by the shell fixture, including the rollout annotation.
	h.k("-n", "temporal-demo-orchestrator", "rollout", "restart", "deployment/orchestrator")
	h.k("-n", "temporal-demo-orchestrator", "rollout", "status", "deployment/orchestrator", "--timeout=180s")
	h.endpoint = "http://" + h.forward("temporal-demo-orchestrator", "orchestrator", 8080)
	h.waitPlan(h.expected(false))
}
func (h *acceptanceHarness) annotation(changed bool) {
	// The shell snapshot is the cleanup authority; never mutate a replacement
	// Service simply because it has the expected name.
	b, err := os.ReadFile(filepath.Join(os.Getenv("ACCEPTANCE_PROOF"), "service-worker-b.json"))
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	var original struct{ Metadata struct{ UID string } }
	h.g.Expect(json.Unmarshal(b, &original)).To(gomega.Succeed())
	h.g.Expect(strings.TrimSpace(string(h.k("-n", "temporal-demo-worker-b", "get", "service", "worker-b", "-o", "jsonpath={.metadata.uid}")))).To(gomega.Equal(original.Metadata.UID))
	deps := "worker-a"
	if changed {
		deps = "worker-c"
	}
	h.k("-n", "temporal-demo-worker-b", "annotate", "service", "worker-b", "temporal-worker-run-after="+deps, "--overwrite")
	h.waitPlan(h.expected(changed))
}
func (h *acceptanceHarness) history(id, run string) []*historypb.HistoryEvent {
	ctx, cancel := context.WithTimeout(h.ctx, 20*time.Second)
	defer cancel()
	iter := h.c.GetWorkflowHistory(ctx, id, run, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var events []*historypb.HistoryEvent
	for iter.HasNext() {
		e, err := iter.Next()
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		events = append(events, e)
	}
	if dir := os.Getenv("ACCEPTANCE_PROOF"); dir != "" {
		b, err := protojson.Marshal(&historypb.History{Events: events})
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		sum := sha256.Sum256([]byte(id + run))
		h.g.Expect(os.WriteFile(filepath.Join(dir, fmt.Sprintf("history-%x.json", sum[:8])), b, 0600)).To(gomega.Succeed())
	}
	return events
}

// Compare the complete JSON value, not a permissively decoded struct which
// would silently ignore a newly introduced business-data field.
func exactPayload(p *commonpb.Payloads, expected any) error {
	if len(p.GetPayloads()) != 1 {
		return fmt.Errorf("expected one payload: %v", p)
	}
	var got any
	if err := converter.GetDefaultDataConverter().FromPayload(p.Payloads[0], &got); err != nil {
		return err
	}
	b, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	var want any
	if err := json.Unmarshal(b, &want); err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("payload got %v want %v", got, want)
	}
	return nil
}

// Validate commands and order from parent event IDs, never log timestamps.
func checkParent(events []*historypb.HistoryEvent, id string, p plan.Plan) (map[string]string, error) {
	starts, finishes := map[string]int64{}, map[string]int64{}
	children := map[string]string{}
	initiated := map[int64]string{}
	seenInput, seenResult := false, false
	for _, e := range events {
		if a := e.GetWorkflowExecutionStartedEventAttributes(); a != nil {
			if seenInput {
				return nil, fmt.Errorf("duplicate parent input")
			}
			seenInput = true
			if err := exactPayload(a.Input, contracts.ParentInput{Plan: p}); err != nil {
				return nil, err
			}
			if len(a.Memo.GetFields()) != 1 {
				return nil, fmt.Errorf("unexpected parent memo")
			}
			var hash string
			if err := converter.GetDefaultDataConverter().FromPayload(a.Memo.Fields["planHash"], &hash); err != nil || hash != p.Hash {
				return nil, fmt.Errorf("memo hash mismatch")
			}
		}
		if a := e.GetStartChildWorkflowExecutionInitiatedEventAttributes(); a != nil {
			name := strings.TrimPrefix(a.WorkflowId, id+"/")
			var node *plan.Node
			for i := range p.Nodes {
				if p.Nodes[i].Name == name {
					node = &p.Nodes[i]
				}
			}
			if node == nil || starts[name] != 0 || a.WorkflowId != id+"/"+name || a.TaskQueue.GetName() != node.TaskQueue || a.WorkflowType.GetName() != contracts.ChildWorkflowName {
				return nil, fmt.Errorf("unexpected/duplicate child command %s", a.WorkflowId)
			}
			if len(a.Header.GetFields()) != 0 || len(a.Memo.GetFields()) != 0 || len(a.SearchAttributes.GetIndexedFields()) != 0 {
				return nil, fmt.Errorf("extra child command data")
			}
			if err := exactPayload(a.Input, contracts.ChildInput{WorkerName: name}); err != nil {
				return nil, err
			}
			starts[name] = e.EventId
			initiated[e.EventId] = name
			for _, dep := range node.Dependencies {
				if finishes[dep] == 0 || finishes[dep] >= e.EventId {
					return nil, fmt.Errorf("%s started before %s completion", name, dep)
				}
			}
		}
		if a := e.GetChildWorkflowExecutionStartedEventAttributes(); a != nil {
			name := initiated[a.InitiatedEventId]
			if name == "" || children[name] != "" || a.WorkflowExecution.GetWorkflowId() != id+"/"+name {
				return nil, fmt.Errorf("unexpected child start")
			}
			children[name] = a.WorkflowExecution.GetRunId()
		}
		if a := e.GetChildWorkflowExecutionCompletedEventAttributes(); a != nil {
			name := initiated[a.InitiatedEventId]
			if name == "" || finishes[name] != 0 {
				return nil, fmt.Errorf("unexpected child completion")
			}
			if err := exactPayload(a.Result, contracts.ChildResult{WorkerName: name}); err != nil {
				return nil, err
			}
			finishes[name] = e.EventId
		}
		if a := e.GetWorkflowExecutionCompletedEventAttributes(); a != nil {
			seenResult = true
			if err := exactPayload(a.Result, identityResult()); err != nil {
				return nil, err
			}
		}
		if e.GetActivityTaskScheduledEventAttributes() != nil {
			return nil, fmt.Errorf("parent scheduled activity")
		}
	}
	if !seenInput || !seenResult || len(starts) != 4 || len(children) != 4 || len(finishes) != 4 {
		return nil, fmt.Errorf("incomplete diamond: starts=%v children=%v finishes=%v", starts, children, finishes)
	}
	// In the diamond, B/C are both eligible immediately after A: neither
	// command may be held behind the other's completion (a global barrier).
	if reflect.DeepEqual(p.Nodes[1].Dependencies, p.Nodes[2].Dependencies) &&
		(starts["worker-b"] >= finishes["worker-c"] || starts["worker-c"] >= finishes["worker-b"]) {
		return nil, fmt.Errorf("eligible B/C were serialized")
	}
	return children, nil
}
func checkChild(events []*historypb.HistoryEvent, name string) error {
	input, scheduled, finished, result := 0, 0, 0, 0
	for _, e := range events {
		if a := e.GetWorkflowExecutionStartedEventAttributes(); a != nil {
			input++
			if a.TaskQueue.GetName() != name+".v1.temporal" {
				return fmt.Errorf("child queue mismatch")
			}
			if err := exactPayload(a.Input, contracts.ChildInput{WorkerName: name}); err != nil {
				return err
			}
		}
		if a := e.GetActivityTaskScheduledEventAttributes(); a != nil {
			scheduled++
			if a.TaskQueue.GetName() != name+".v1.temporal" || a.ActivityType.GetName() != contracts.ExampleActivityName || len(a.Header.GetFields()) != 0 {
				return fmt.Errorf("unexpected activity command")
			}
			if err := exactPayload(a.Input, contracts.ChildInput{WorkerName: name}); err != nil {
				return err
			}
		}
		if a := e.GetActivityTaskCompletedEventAttributes(); a != nil {
			finished++
			if err := exactPayload(a.Result, contracts.ChildResult{WorkerName: name}); err != nil {
				return err
			}
		}
		if a := e.GetWorkflowExecutionCompletedEventAttributes(); a != nil {
			result++
			if err := exactPayload(a.Result, contracts.ChildResult{WorkerName: name}); err != nil {
				return err
			}
		}
		if e.GetStartChildWorkflowExecutionInitiatedEventAttributes() != nil {
			return fmt.Errorf("child started nested workflow")
		}
	}
	if input != 1 || scheduled != 1 || finished != 1 || result != 1 {
		return fmt.Errorf("child lifecycle counts %d/%d/%d/%d", input, scheduled, finished, result)
	}
	return nil
}
func (h *acceptanceHarness) assertHistory(r runs.Run, p plan.Plan) {
	children, err := checkParent(h.history(r.WorkflowID, r.RunID), r.WorkflowID, p)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, name := range diamondNames {
		h.g.Expect(checkChild(h.history(r.WorkflowID+"/"+name, children[name]), name)).To(gomega.Succeed())
	}
	h.t.Logf("history commands, payloads, queues, once-only children and dependency completion ordering verified: %s/%s", r.WorkflowID, r.RunID)
}

func TestDiamondFromZero(t *testing.T) {
	h := newAcceptanceHarness(t)
	h.preflight()
	p := h.expected(false)
	h.waitPlan(p)
	for _, name := range diamondNames {
		h.waitZero(name, 3*time.Minute)
		for _, queueType := range []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_WORKFLOW, enumspb.TASK_QUEUE_TYPE_ACTIVITY} {
			ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
			description, err := h.c.DescribeTaskQueue(ctx, name+".v1.temporal", queueType)
			cancel()
			h.g.Expect(err).NotTo(gomega.HaveOccurred())
			t.Logf("%s %s cached Temporal pollers=%d; zero actual Pods establishes no live deployed pollers, not this stale cache", name, queueType, len(description.Pollers))
		}
	}
	key := fmt.Sprintf("task10-diamond-%d", time.Now().UnixNano())
	start := time.Now()
	r := h.start(key)
	h.g.Expect(r.PlanHash).To(gomega.Equal(p.Hash))
	h.g.Eventually(func() string { return h.replicas("worker-a") }, 90*time.Second, time.Second).Should(gomega.Equal("1"))
	t.Logf("A activation from zero=%s", time.Since(start).Round(time.Millisecond))
	r = h.completed(r)
	h.assertHistory(r, p)
	h.replay(key, r)
	bad := fmt.Sprintf("task10-body-%d", time.Now().UnixNano())
	code, b, err := h.request("POST", "/runs", bad, []byte(" "))
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(code).To(gomega.Equal(400), string(b))
	h.missing(bad)
	h.restart()
	h.replay(key, r)
	h.annotation(true)
	h.replay(key, r)
	h.assertHistory(r, p)
	h.annotation(false)
	finished := time.Now()
	for _, name := range diamondNames {
		h.waitZero(name, 3*time.Minute)
	}
	t.Logf("all-worker zero after completion/replay checks=%s; full smoke=%s", time.Since(finished).Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
}

// registerSnapshotIsolationCleanup is the snapshot experiment's cleanup only.
// Keeping registration separate lets offline tests exercise real FailNow flow.
func (h *acceptanceHarness) registerSnapshotIsolationCleanup(scalerUID string, scalerAnnotations map[string]string) {
	patch, err := json.Marshal([]map[string]any{{"op": "test", "path": "/metadata/uid", "value": scalerUID}, {"op": "add", "path": "/metadata/annotations", "value": scalerAnnotations}})
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	// Independent testing.T callbacks run even if another callback calls
	// FailNow. Registry convergence must not gate the scaler's rollback.
	h.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cleanup := *h
		cleanup.ctx = ctx // experiment deadline may already have expired
		cleanup.annotation(false)
	})
	h.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "kubectl", h.argv("-n", "temporal-demo-worker-a", "patch", "scaledobject", "worker-a", "--type=json", "-p", string(patch))...).CombinedOutput()
		if err != nil {
			h.t.Errorf("restore snapshot scaler: %v: %s", err, out)
		}
	})
}

func TestPlanSnapshotIsolation(t *testing.T) {
	h := newAcceptanceHarness(t)
	h.preflight()
	p := h.expected(false)
	h.waitPlan(p)
	// Pause at zero so the old execution is provably active across the update,
	// without adding a production delay/control to the immutable plan.
	h.waitZero("worker-a", 3*time.Minute)
	var scaler struct {
		Metadata struct {
			UID         string
			Annotations map[string]string
		}
	}
	h.g.Expect(json.Unmarshal(h.k("-n", "temporal-demo-worker-a", "get", "scaledobject", "worker-a", "-o", "json"), &scaler)).To(gomega.Succeed())
	h.registerSnapshotIsolationCleanup(scaler.Metadata.UID, scaler.Metadata.Annotations)
	h.k("-n", "temporal-demo-worker-a", "annotate", "scaledobject", "worker-a", "autoscaling.keda.sh/paused=true", "--overwrite")
	key := fmt.Sprintf("task10-snapshot-%d", time.Now().UnixNano())
	first := h.start(key)
	h.g.Eventually(func() bool {
		d, err := h.c.DescribeWorkflowExecution(h.ctx, first.WorkflowID, first.RunID)
		return err == nil && d.WorkflowExecutionInfo.Status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
	}, 15*time.Second, time.Second).Should(gomega.BeTrue())
	before := h.history(first.WorkflowID, first.RunID)
	h.annotation(true)
	d, err := h.c.DescribeWorkflowExecution(h.ctx, first.WorkflowID, first.RunID)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(d.WorkflowExecutionInfo.Status).To(gomega.Equal(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING), "original execution must still be active after registry change")
	changed := h.expected(true)
	var oldInput *commonpb.Payloads
	for _, e := range before {
		if a := e.GetWorkflowExecutionStartedEventAttributes(); a != nil {
			oldInput = a.Input
		}
	}
	h.g.Expect(exactPayload(oldInput, contracts.ParentInput{Plan: p})).To(gomega.Succeed())
	second := h.start(key + "-new")
	h.g.Expect(second.PlanHash).To(gomega.Equal(changed.Hash))
	h.g.Expect(second.PlanHash).NotTo(gomega.Equal(first.PlanHash))
	h.k("-n", "temporal-demo-worker-a", "annotate", "scaledobject", "worker-a", "autoscaling.keda.sh/paused-")
	first = h.completed(first)
	second = h.completed(second)
	h.assertHistory(first, p)
	h.assertHistory(second, changed)
	h.replay(key, first)
	h.annotation(false)
}

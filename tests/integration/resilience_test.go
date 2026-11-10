//go:build integration && resilience_integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onsi/gomega"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

// snapshotResource restores all changed fields and original ownership metadata
// after each experiment, not merely at the end of the shell suite.
func (h *acceptanceHarness) snapshotResource(ns, kind, name string) {
	b := h.k("-n", ns, "get", kind, name, "-o", "json")
	var obj struct {
		Metadata struct {
			UID                 string
			Annotations, Labels map[string]string
		}
		Spec json.RawMessage
	}
	h.g.Expect(json.Unmarshal(b, &obj)).To(gomega.Succeed())
	patch := []map[string]any{{"op": "test", "path": "/metadata/uid", "value": obj.Metadata.UID}, {"op": "replace", "path": "/spec", "value": obj.Spec}, {"op": "add", "path": "/metadata/annotations", "value": obj.Metadata.Annotations}, {"op": "add", "path": "/metadata/labels", "value": obj.Metadata.Labels}}
	p, err := json.Marshal(patch)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "kubectl", h.argv("-n", ns, "patch", kind, name, "--type=json", "-p", string(p))...).CombinedOutput()
		if err != nil {
			h.t.Errorf("restore %s %s/%s: %v: %s", kind, ns, name, err, out)
		}
		if kind == "deployment" {
			ctx, cancel := context.WithTimeout(context.Background(), 190*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "kubectl", h.argv("-n", ns, "rollout", "status", "deployment/"+name, "--timeout=180s")...).CombinedOutput()
			if err != nil {
				h.t.Errorf("restored rollout %s: %v: %s", name, err, out)
			}
		}
	})
}

func (h *acceptanceHarness) runningActivity(parent string) client.WorkflowRun {
	var runID string
	h.g.Eventually(func() error {
		ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
		defer cancel()
		d, err := h.c.DescribeWorkflowExecution(ctx, parent+"/worker-a", "")
		if err != nil {
			return err
		}
		if len(d.PendingActivities) != 1 || d.PendingActivities[0].State != enumspb.PENDING_ACTIVITY_STATE_STARTED {
			return fmt.Errorf("waiting for running A activity: %v", d.PendingActivities)
		}
		runID = d.WorkflowExecutionInfo.Execution.RunId
		h.t.Logf("A activity started attempt=%d identity=%s", d.PendingActivities[0].Attempt, d.PendingActivities[0].LastWorkerIdentity)
		return nil
	}, 90*time.Second, 500*time.Millisecond).Should(gomega.Succeed())
	return h.c.GetWorkflow(h.ctx, parent+"/worker-a", runID)
}

type workerPod struct{ Metadata struct{ Name, UID string } }

func activityPod(identity string, pods []workerPod) (workerPod, error) {
	parts := strings.Split(identity, "@")
	if len(parts) != 3 || parts[1] == "" {
		return workerPod{}, fmt.Errorf("unexpected SDK worker identity %q", identity)
	}
	for _, pod := range pods {
		if pod.Metadata.Name == parts[1] {
			return pod, nil
		}
	}
	return workerPod{}, fmt.Errorf("no actual Pod for running activity identity %q", identity)
}
func TestActivityPodSelection(t *testing.T) {
	g := gomega.NewWithT(t)
	var old, current workerPod
	old.Metadata.Name = "old"
	old.Metadata.UID = "old-uid"
	current.Metadata.Name = "current"
	current.Metadata.UID = "current-uid"
	got, err := activityPod("1@current@", []workerPod{old, current})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(got).To(gomega.Equal(current))
	_, err = activityPod("1@absent@", []workerPod{old, current})
	g.Expect(err).To(gomega.HaveOccurred())
}

func TestRecoveryWorkerTermination(t *testing.T) {
	h := newAcceptanceHarness(t)
	h.preflight()
	h.waitPlan(h.expected(false))
	h.waitZero("worker-a", 3*time.Minute)
	h.snapshotResource("temporal-demo-worker-a", "deployment", "worker-a")
	h.k("-n", "temporal-demo-worker-a", "set", "env", "deployment/worker-a", "ACTIVITY_DURATION=30s")
	h.k("-n", "temporal-demo-worker-a", "rollout", "status", "deployment/worker-a", "--timeout=180s")
	r := h.start(fmt.Sprintf("task10-termination-%d", time.Now().UnixNano()))
	child := h.runningActivity(r.WorkflowID)
	var pods struct{ Items []workerPod }
	h.g.Expect(json.Unmarshal(h.k("-n", "temporal-demo-worker-a", "get", "pods", "-l", "app=worker-a", "-o", "json"), &pods)).To(gomega.Succeed())
	actual, err := activityPod(h.pending(child).GetLastWorkerIdentity(), pods.Items)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	pod := actual.Metadata
	h.g.Expect(strings.TrimSpace(string(h.k("-n", "temporal-demo-worker-a", "get", "pod", pod.Name, "-o", "jsonpath={.metadata.uid}")))).To(gomega.Equal(pod.UID))
	h.k("-n", "temporal-demo-worker-a", "delete", "pod", pod.Name, "--grace-period=0", "--force", "--wait=true", "--timeout=30s")
	r = h.completed(r)
	h.assertHistory(r, h.expected(false))
	attempt := int32(0)
	for _, e := range h.history(child.GetID(), child.GetRunID()) {
		if a := e.GetActivityTaskStartedEventAttributes(); a != nil {
			attempt = a.Attempt
		}
	}
	h.g.Expect(attempt).To(gomega.BeNumerically(">=", 2), "worker termination must produce a genuine retried activity")
	t.Logf("forced exact Pod %s UID=%s; recovered activity attempt=%d", pod.Name, pod.UID, attempt)
}

func TestRecoveryOrchestratorRestart(t *testing.T) {
	h := newAcceptanceHarness(t)
	h.preflight()
	h.waitPlan(h.expected(false))
	h.waitZero("worker-a", 3*time.Minute)
	h.snapshotResource("temporal-demo-orchestrator", "deployment", "orchestrator")
	h.snapshotResource("temporal-demo-worker-a", "deployment", "worker-a")
	h.k("-n", "temporal-demo-worker-a", "set", "env", "deployment/worker-a", "ACTIVITY_DURATION=30s")
	h.k("-n", "temporal-demo-worker-a", "rollout", "status", "deployment/worker-a", "--timeout=180s")
	key := fmt.Sprintf("task10-restart-%d", time.Now().UnixNano())
	r := h.start(key)
	h.runningActivity(r.WorkflowID)
	h.restart()
	r = h.completed(r)
	h.replay(key, r)
	h.assertHistory(r, h.expected(false))
}

func TestRecoveryDelayedRetry(t *testing.T) {
	h := newAcceptanceHarness(t)
	h.preflight()
	h.snapshotResource("temporal-demo-worker-a", "deployment", "worker-a")
	h.snapshotResource("temporal-demo-worker-a", "scaledobject", "worker-a")
	// Keep the existing independent delayed-activity-backlog proof unchanged;
	// execute it against the combined deployment as well as its static suite.
	TestDelayedRetryReactivation(t)
}

// Reuse the integration test binary as the small relay fixture. No extra
// production binary, test switch, proxy framework or new base-image pin.
func TestTemporalRelayFixture(t *testing.T) {
	upstream := os.Getenv("TASK10_RELAY_UPSTREAM")
	if upstream == "" {
		t.Fatal("fixture requires TASK10_RELAY_UPSTREAM")
	}
	ln, err := net.Listen("tcp", ":7233")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	for {
		conn, err := ln.Accept()
		if err != nil {
			t.Fatal(err)
		}
		go relayConnection(conn, upstream)
	}
}
func relayConnection(downstream net.Conn, upstream string) {
	defer downstream.Close()
	upstreamConn, err := net.DialTimeout("tcp", upstream, 2*time.Second)
	if err != nil {
		return
	}
	defer upstreamConn.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstreamConn, downstream)
		_ = upstreamConn.Close()
		_ = downstream.Close()
	}()
	_, _ = io.Copy(downstream, upstreamConn)
	_ = downstream.Close()
	_ = upstreamConn.Close()
	wg.Wait()
}

func (h *acceptanceHarness) createRelay() {
	image := os.Getenv("TEMPORAL_RELAY_IMAGE")
	h.g.Expect(image).NotTo(gomega.BeEmpty())
	// Native handwritten YAML; the shell owns exact-UID final cleanup even if
	// this test process times out. create, not apply/adopt, protects prior objects.
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: task10-temporal-relay
  namespace: temporal-demo-orchestrator
spec:
  selector:
    app: task10-temporal-relay
  ports:
    - port: 7233
      targetPort: 7233
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: task10-temporal-relay
  namespace: temporal-demo-orchestrator
spec:
  replicas: 1
  selector:
    matchLabels:
      app: task10-temporal-relay
  template:
    metadata:
      labels:
        app: task10-temporal-relay
    spec:
      automountServiceAccountToken: false
      terminationGracePeriodSeconds: 1
      containers:
        - name: relay
          image: %s
          imagePullPolicy: Never
          env:
            - name: TASK10_RELAY_UPSTREAM
              value: temporal.temporal.svc.cluster.local:7233
          readinessProbe:
            tcpSocket:
              port: 7233
            periodSeconds: 1
`, image)
	// Create separately so a partial create records ownership of every successful
	// object before attempting the next resource.
	for _, part := range strings.Split(manifest, "---\n") {
		ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
		cmd := exec.CommandContext(ctx, "kubectl", h.argv("create", "-f", "-", "-o", "json")...)
		cmd.Stdin = strings.NewReader(part)
		b, err := cmd.CombinedOutput()
		cancel()
		h.g.Expect(err).NotTo(gomega.HaveOccurred(), string(b))
		var o struct {
			Kind     string
			Metadata struct{ Namespace, Name, UID string }
		}
		h.g.Expect(json.Unmarshal(b, &o)).To(gomega.Succeed())
		f, err := os.OpenFile(filepath.Join(os.Getenv("ACCEPTANCE_PROOF"), "created.tsv"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = fmt.Fprintf(f, "%s\t%s\t%s\t%s\n", o.Kind, o.Metadata.Namespace, o.Metadata.Name, o.Metadata.UID)
		closeErr := f.Close()
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		h.g.Expect(closeErr).NotTo(gomega.HaveOccurred())
	}
	h.k("-n", "temporal-demo-orchestrator", "rollout", "status", "deployment/task10-temporal-relay", "--timeout=90s")
}

func TestRecoveryTemporalConnectivity(t *testing.T) {
	h := newAcceptanceHarness(t)
	h.preflight()
	h.waitPlan(h.expected(false))
	h.waitZero("worker-a", 3*time.Minute)
	h.snapshotResource("temporal-demo-orchestrator", "deployment", "orchestrator")
	h.snapshotResource("temporal-demo-worker-a", "deployment", "worker-a")
	h.createRelay()
	uid := strings.TrimSpace(string(h.k("-n", "temporal-demo-orchestrator", "get", "deployment", "task10-temporal-relay", "-o", "jsonpath={.metadata.uid}")))
	// Register unconditional bounded outage rollback BEFORE disconnecting.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		patch := fmt.Sprintf(`[{"op":"test","path":"/metadata/uid","value":%s},{"op":"replace","path":"/spec/replicas","value":1}]`, strconv.Quote(uid))
		b, err := exec.CommandContext(ctx, "kubectl", h.argv("-n", "temporal-demo-orchestrator", "patch", "deployment", "task10-temporal-relay", "--type=json", "-p", patch)...).CombinedOutput()
		if err != nil {
			t.Errorf("relay outage rollback: %v: %s", err, b)
		}
	}()
	h.k("-n", "temporal-demo-orchestrator", "set", "env", "deployment/orchestrator", "TEMPORAL_ADDRESS=task10-temporal-relay.temporal-demo-orchestrator.svc.cluster.local:7233")
	h.restart()
	relayAddress := h.forward("temporal-demo-orchestrator", "task10-temporal-relay", 7233)
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	relayClient, err := client.DialContext(ctx, client.Options{HostPort: relayAddress, Namespace: "default"})
	cancel()
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	relayClient.Close()
	h.k("-n", "temporal-demo-worker-a", "set", "env", "deployment/worker-a", "ACTIVITY_DURATION=30s")
	h.k("-n", "temporal-demo-worker-a", "rollout", "status", "deployment/worker-a", "--timeout=180s")
	key := fmt.Sprintf("task10-connectivity-%d", time.Now().UnixNano())
	original := h.start(key)
	h.runningActivity(original.WorkflowID)
	outage := time.Now()
	h.k("-n", "temporal-demo-orchestrator", "scale", "deployment/task10-temporal-relay", "--replicas=0")
	h.k("-n", "temporal-demo-orchestrator", "wait", "--for=delete", "pod", "-l", "app=task10-temporal-relay", "--timeout=10s")
	// Observe a real failed Temporal gRPC connection, not just the replica value.
	ctx, cancel = context.WithTimeout(h.ctx, 3*time.Second)
	failedClient, connectionErr := client.DialContext(ctx, client.Options{HostPort: relayAddress, Namespace: "default"})
	cancel()
	if failedClient != nil {
		failedClient.Close()
	}
	h.g.Expect(connectionErr).To(gomega.HaveOccurred())
	rejected := key + "-rejected"
	before := time.Now()
	code, b, err := h.request("POST", "/runs", rejected, nil)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(code).To(gomega.Equal(503), string(b))
	h.g.Expect(time.Since(before)).To(gomega.BeNumerically("<", 4*time.Second), "existing 2s server budget plus transport")
	h.missing(rejected)
	h.g.Expect(time.Since(outage)).To(gomega.BeNumerically("<", 30*time.Second))
	h.k("-n", "temporal-demo-orchestrator", "scale", "deployment/task10-temporal-relay", "--replicas=1")
	h.k("-n", "temporal-demo-orchestrator", "rollout", "status", "deployment/task10-temporal-relay", "--timeout=90s")
	// Recovery through the original API process/SDK connection, not a restart.
	h.g.Eventually(func() error {
		c, _, err := h.request("POST", "/runs", key, nil)
		if err != nil {
			return err
		}
		if c != 202 {
			return fmt.Errorf("replay status %d", c)
		}
		return nil
	}, 30*time.Second, time.Second).Should(gomega.Succeed())
	original = h.completed(original)
	h.replay(key, original)
	h.assertHistory(original, h.expected(false))
	h.missing(rejected)
	t.Logf("orchestrator-only outage-start through healthy completion=%s (not outage duration); cut interval asserted <30s; 503 and direct NotFound before/after recovery; existing run/replay recovered", time.Since(outage).Round(time.Millisecond))
}

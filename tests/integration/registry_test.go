//go:build integration && registry_integration

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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/registry"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/runs"
	"github.com/onsi/gomega"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

const registrySA = "system:serviceaccount:temporal-demo-orchestrator:orchestrator"
const registryRole = "temporal-demo-registry"

type registryHarness struct {
	*harness
	endpoint string
}

func newRegistryHarness(t *testing.T) *registryHarness {
	t.Helper()
	g := gomega.NewWithT(t)
	root := os.Getenv("REGISTRY_ROOT")
	g.Expect(root).NotTo(gomega.BeEmpty(), "use mise run test:registry or test:rbac")
	h := &harness{t: t, g: g, root: root}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var err error
	h.c, err = client.DialContext(ctx, client.Options{HostPort: h.forward("temporal", "temporal", 7233), Namespace: "default"})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(h.c.Close)
	return &registryHarness{harness: h, endpoint: "http://" + h.forward("temporal-demo-orchestrator", "orchestrator", 8080)}
}
func (h *registryHarness) command(input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", h.argv(args...)...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.CombinedOutput()
}
func (h *registryHarness) apply(obj any) {
	h.t.Helper()
	b, err := json.Marshal(obj)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	out, err := h.command(b, "apply", "-f", "-")
	h.g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", out)
}
func (h *registryHarness) request(method, path, key string) (int, []byte, error) {
	req, err := http.NewRequest(method, h.endpoint+path, nil)
	if err != nil {
		return 0, nil, err
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	c := &http.Client{Timeout: 10 * time.Second}
	res, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return res.StatusCode, b, err
}
func (h *registryHarness) snapshot() (registry.Snapshot, error) {
	status, b, err := h.request("GET", "/workers", "")
	if err != nil {
		return registry.Snapshot{}, err
	}
	if status != 200 {
		return registry.Snapshot{}, fmt.Errorf("workers %d: %s", status, b)
	}
	var s registry.Snapshot
	err = json.Unmarshal(b, &s)
	return s, err
}
func (h *registryHarness) waitGraph(names []string, hash string) {
	h.t.Helper()
	sort.Strings(names)
	h.g.Eventually(func() error {
		s, err := h.snapshot()
		if err != nil {
			return err
		}
		got := []string{}
		for _, n := range s.Plan.Nodes {
			got = append(got, n.Name)
		}
		sort.Strings(got)
		if !s.Synced || len(s.Problems) != 0 || strings.Join(got, ",") != strings.Join(names, ",") || hash != "" && s.Plan.Hash != hash {
			return fmt.Errorf("unexpected registry: %+v", s)
		}
		return nil
	}, 90*time.Second, time.Second).Should(gomega.Succeed())
	s, err := h.snapshot()
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.t.Logf("observed names=%v hash=%s", names, s.Plan.Hash)
}
func (h *registryHarness) waitInvalid(problem string, synced bool) {
	h.t.Helper()
	h.g.Eventually(func() error {
		s, err := h.snapshot()
		if err != nil {
			return err
		}
		if s.Synced != synced || !strings.Contains(strings.Join(s.Problems, ";"), problem) || s.Plan.Hash != "" {
			return fmt.Errorf("expected invalid %q synced=%v, got %+v", problem, synced, s)
		}
		return nil
	}, 90*time.Second, time.Second).Should(gomega.Succeed())
	status, _, err := h.request("GET", "/healthz", "")
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(status).To(gomega.Equal(200))
	// The tunnel targets a Pod directly, so unsynchronized readiness remains observable.
	status, _, err = h.request("GET", "/readyz", "")
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	want := 503
	if synced {
		want = 200
	}
	h.g.Expect(status).To(gomega.Equal(want))
	h.noStart(problem)
}
func (h *registryHarness) noStart(reason string) {
	h.t.Helper()
	key := fmt.Sprintf("task9-invalid-%d", time.Now().UnixNano())
	id := fmt.Sprintf("run-%x", sha256.Sum256([]byte(key)))
	code, b, err := h.request("POST", "/runs", key)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(code).To(gomega.Equal(503), "%s: %s", reason, b)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = h.c.DescribeWorkflowExecution(ctx, id, "")
	var missing *serviceerror.NotFound
	h.g.Expect(errors.As(err, &missing)).To(gomega.BeTrue(), "invalid registry created actual execution %s: %v", id, err)
	h.t.Logf("503 + DescribeWorkflowExecution NotFound: %s (%s)", id, reason)
}
func (h *registryHarness) grant(ns string) {
	h.apply(map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": map[string]any{"name": registryRole, "namespace": ns}, "rules": []any{map[string]any{"apiGroups": []string{""}, "resources": []string{"configmaps"}, "verbs": []string{"get", "list", "watch"}}}})
	h.apply(map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": map[string]any{"name": registryRole, "namespace": ns}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "orchestrator", "namespace": "temporal-demo-orchestrator"}}, "roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": registryRole}})
}
func (h *registryHarness) service(ns, name, deps string) {
	h.apply(map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": name, "namespace": ns, "annotations": map[string]string{"temporal-worker": "true", "temporal-worker-run-after": deps}}, "spec": map[string]any{"ports": []any{map[string]any{"port": 8080}}}})
}
func (h *registryHarness) annotate(name, deps string) {
	h.k("-n", "temporal-demo-"+name, "annotate", "service", name, "temporal-worker-run-after="+deps, "--overwrite")
}

func TestLiveRegistry(t *testing.T) {
	h := newRegistryHarness(t)
	h.preflight()
	original, err := registry.LoadStatic(filepath.Join(h.root, "deploy/config/workers.json"))
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	hash := original.Snapshot().Plan.Hash
	partialBytes, err := os.ReadFile(filepath.Join(h.root, "deploy/config/workers.json"))
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	var partial []plan.Registration
	h.g.Expect(json.Unmarshal(partialBytes, &partial)).To(gomega.Succeed())
	for i := range partial {
		if partial[i].WorkerName == "worker-b" {
			partial[i].RunAfter = "worker-c"
		}
	}
	partialPlan, err := plan.Build(partial)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	names := []string{"worker-a", "worker-b", "worker-c", "worker-d"}
	h.waitGraph(names, hash)
	// Snapshot only fields/resources this test is authorized to mutate.
	identities := map[string]string{}
	for _, name := range names {
		identities[name] = string(h.k("-n", "temporal-demo-"+name, "get", "service", name, "-o", "jsonpath={.metadata.uid}"))
	}
	identities["PVC"] = string(h.k("-n", "temporal", "get", "pvc", "data-postgres-0", "-o", "jsonpath={.metadata.uid}:{.spec.volumeName}:{.status.phase}"))
	identities["Secret"] = string(h.k("-n", "temporal", "get", "secret", "postgres-credentials", "-o", "jsonpath={.metadata.uid}:{.metadata.resourceVersion}:{.immutable}"))
	originals := map[string][]byte{}
	for _, name := range names {
		originals[name] = h.k("-n", "temporal-demo-"+name, "get", "service", name, "-o", "json")
	}
	binding := h.k("-n", "temporal-demo-worker-d", "get", "rolebinding", registryRole, "-o", "json")
	var bindingObject map[string]any
	h.g.Expect(json.Unmarshal(binding, &bindingObject)).To(gomega.Succeed())
	for _, key := range []string{"uid", "resourceVersion", "generation", "creationTimestamp", "managedFields"} {
		delete(bindingObject["metadata"].(map[string]any), key)
	}
	binding, err = json.Marshal(bindingObject)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	var temp []string
	t.Cleanup(func() {
		// A restart's tunnel cleanup runs before this older callback (LIFO). Open
		// a fresh owned tunnel here, whose cleanup is registered after this callback.
		h.endpoint = "http://" + h.forward("temporal-demo-orchestrator", "orchestrator", 8080)
		// Restore dependency annotation exactly, including absence, rather than apply whole Services.
		for name, b := range originals {
			var svc struct {
				Metadata struct{ Annotations map[string]string }
			}
			if err := json.Unmarshal(b, &svc); err != nil {
				t.Error(err)
				continue
			}
			value, ok := svc.Metadata.Annotations["temporal-worker-run-after"]
			arg := "temporal-worker-run-after-"
			if ok {
				arg = "temporal-worker-run-after=" + value
			}
			out, err := h.command(nil, "-n", "temporal-demo-"+name, "annotate", "service", name, arg, "--overwrite")
			if err != nil {
				t.Errorf("restore annotation: %v: %s", err, out)
			}
		}
		out, err := h.command(binding, "apply", "-f", "-")
		if err != nil {
			t.Errorf("restore binding: %v: %s", err, out)
		}
		for _, ns := range temp {
			out, err := h.command(nil, "delete", "namespace", ns, "--ignore-not-found", "--wait=true", "--timeout=90s")
			if err != nil {
				t.Errorf("delete fixture namespace: %v: %s", err, out)
			}
		}
		h.waitGraph(names, hash)
		for _, name := range names {
			h.g.Expect(string(h.k("-n", "temporal-demo-"+name, "get", "service", name, "-o", "jsonpath={.metadata.uid}"))).To(gomega.Equal(identities[name]))
		}
		h.g.Expect(string(h.k("-n", "temporal", "get", "pvc", "data-postgres-0", "-o", "jsonpath={.metadata.uid}:{.spec.volumeName}:{.status.phase}"))).To(gomega.Equal(identities["PVC"]))
		h.g.Expect(string(h.k("-n", "temporal", "get", "secret", "postgres-credentials", "-o", "jsonpath={.metadata.uid}:{.metadata.resourceVersion}:{.immutable}"))).To(gomega.Equal(identities["Secret"]))
		h.retainedHistory()
		t.Logf("preserved identities: %v", identities)
	})
	// The only valid execution uses the unchanged diamond and actual fixed workers.
	key := fmt.Sprintf("task9-valid-%d", time.Now().UnixNano())
	status, b, err := h.request("POST", "/runs", key)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(status).To(gomega.Equal(202), "%s", b)
	var run runs.Run
	h.g.Expect(json.Unmarshal(b, &run)).To(gomega.Succeed())
	h.own(h.c.GetWorkflow(context.Background(), run.WorkflowID, run.RunID))
	h.g.Expect(run.PlanHash).To(gomega.Equal(hash))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var result contracts.ParentResult
	h.g.Expect(h.c.GetWorkflow(ctx, run.WorkflowID, run.RunID).Get(ctx, &result)).To(gomega.Succeed())
	h.g.Expect(result.Results).To(gomega.Equal([]contracts.ChildResult{{WorkerName: "worker-a"}, {WorkerName: "worker-b"}, {WorkerName: "worker-c"}, {WorkerName: "worker-d"}}))
	t.Logf("completed live zero-body diamond %s/%s hash=%s result=%+v", run.WorkflowID, run.RunID, hash, result)
	// Partial reconciliation can't be inferred from intent; unknown refs and a
	// cycle introduced through separate locally admitted Service updates must fail.
	h.annotate("worker-a", "not-yet-deployed")
	h.waitInvalid("unknown dependency", true)
	status, b, err = h.request("POST", "/runs", key)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(status).To(gomega.Equal(202))
	var replay runs.Run
	h.g.Expect(json.Unmarshal(b, &replay)).To(gomega.Succeed())
	h.g.Expect(replay.WorkflowID).To(gomega.Equal(run.WorkflowID))
	h.g.Expect(replay.RunID).To(gomega.Equal(run.RunID))
	h.g.Expect(replay.PlanHash).To(gomega.Equal(hash))
	h.g.Expect(replay.Result).To(gomega.Equal(&result))
	h.annotate("worker-a", "")
	h.waitGraph(names, hash)
	h.annotate("worker-b", "worker-c")
	h.waitGraph(names, partialPlan.Hash) // explicit expected intermediate, not rollout intent
	h.annotate("worker-c", "worker-b")
	h.waitInvalid("cycle", true)
	h.annotate("worker-b", "worker-a")
	h.annotate("worker-c", "worker-a")
	h.waitGraph(names, hash)
	// Revoke an actual demo-SA binding. Bounded watch reconnection must detect it.
	h.k("-n", "temporal-demo-worker-d", "delete", "rolebinding", registryRole)
	h.waitInvalid("forbidden", false)
	out, err := h.command(binding, "apply", "-f", "-")
	h.g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", out)
	h.waitGraph(names, hash)
	suffix := fmt.Sprint(time.Now().UnixNano())
	ns := "registry-" + suffix
	if file := os.Getenv("REGISTRY_NAMESPACES_FILE"); file != "" {
		f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = fmt.Fprintln(f, ns)
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		h.g.Expect(f.Close()).To(gomega.Succeed())
	}
	h.apply(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ns, "labels": map[string]string{"temporal-demo.io/worker-namespace": "true"}}})
	temp = append(temp, ns)
	h.waitInvalid("forbidden", false)
	h.grant(ns)
	h.waitGraph(names, hash)
	h.service(ns, "worker-extra", "")
	h.waitGraph(append(append([]string{}, names...), "worker-extra"), "")
	h.k("label", "namespace", ns, "temporal-demo.io/worker-namespace-")
	h.waitGraph(names, hash)
	h.k("label", "namespace", ns, "temporal-demo.io/worker-namespace=true")
	h.waitGraph(append(append([]string{}, names...), "worker-extra"), "")
	// Actual Kyverno source cleanup, not manually deleting a generated ConfigMap.
	h.k("-n", ns, "annotate", "service", "worker-extra", "temporal-worker=false", "--overwrite")
	h.waitGraph(names, hash)
	h.service(ns, "worker-a", "")
	h.waitInvalid("duplicate worker", true)
	h.k("-n", ns, "delete", "service", "worker-a")
	h.waitGraph(names, hash)
	// Labeled malformed fixture CMs are trusted input tests, not source provenance claims.
	data := map[string]string{"schemaVersion": "1", "workerName": "worker-extra", "taskQueue": "worker-extra.v1.temporal", "workflowType": "ExampleChildWorkflow", "runAfter": "", "sourceServiceName": "worker-extra", "sourceServiceNamespace": ns, "sourceServiceUID": "fixture-source-uid"}
	for _, tc := range []struct{ key, value, problem string }{{"schemaVersion", "2", "unsupported schema"}, {"sourceServiceNamespace", "other", "source namespace"}, {"taskQueue", "wrong", "invalid task queue"}} {
		data[tc.key] = tc.value
		h.apply(map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "temporal-worker-worker-extra", "namespace": ns, "labels": map[string]string{"temporal-demo.io/registration": "true"}}, "data": data})
		h.waitInvalid(tc.problem, true)
		h.k("-n", ns, "delete", "configmap", "temporal-worker-worker-extra")
		h.waitGraph(names, hash)
		switch tc.key {
		case "schemaVersion":
			data[tc.key] = "1"
		case "sourceServiceNamespace":
			data[tc.key] = ns
		case "taskQueue":
			data[tc.key] = "worker-extra.v1.temporal"
		}
	}
	h.k("delete", "namespace", ns, "--wait=true", "--timeout=90s")
	h.waitGraph(names, hash)
	// Restart forces initial list/cache synchronization against the actual API.
	h.k("-n", "temporal-demo-orchestrator", "rollout", "restart", "deployment/orchestrator")
	h.k("-n", "temporal-demo-orchestrator", "rollout", "status", "deployment/orchestrator", "--timeout=180s")
	h.endpoint = "http://" + h.forward("temporal-demo-orchestrator", "orchestrator", 8080)
	h.waitGraph(names, hash)
}

// Impersonation asks the live authorizer, not a text-only Role inspection.
func TestRegistryRBAC(t *testing.T) {
	h := newRegistryHarness(t)
	check := func(want string, args ...string) {
		t.Helper()
		args = append([]string{"auth", "can-i"}, args...)
		args = append(args, "--as="+registrySA)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "kubectl", h.argv(args...)...)
		var diagnostic bytes.Buffer
		cmd.Stderr = &diagnostic
		out, err := cmd.Output()
		if want == "yes" {
			h.g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", diagnostic.Bytes())
		} else {
			var denied *exec.ExitError
			h.g.Expect(errors.As(err, &denied)).To(gomega.BeTrue(), "expected authorization denial, got %v: %s", err, diagnostic.Bytes())
			h.g.Expect(denied.ExitCode()).To(gomega.Equal(1))
		}
		h.g.Expect(strings.TrimSpace(string(out))).To(gomega.Equal(want), "%v: %s", args, diagnostic.Bytes())
		t.Logf("impersonation %v -> %s (diagnostic: %s)", args, strings.TrimSpace(string(out)), strings.TrimSpace(diagnostic.String()))
	}
	for _, verb := range []string{"get", "list", "watch"} {
		check("yes", verb, "namespaces", "--all-namespaces")
		check("no", verb, "configmaps", "--all-namespaces")
		for _, ns := range []string{"temporal-demo-worker-a", "temporal-demo-worker-b", "temporal-demo-worker-c", "temporal-demo-worker-d"} {
			check("yes", verb, "configmaps", "-n", ns)
			check("no", verb, "secrets", "-n", ns)
			check("no", verb, "services", "-n", ns)
		}
		for _, ns := range []string{"default", "temporal", "temporal-demo-orchestrator"} {
			check("no", verb, "configmaps", "-n", ns)
			check("no", verb, "secrets", "-n", ns)
		}
		check("no", verb, "secrets", "--all-namespaces")
	}
	for _, verb := range []string{"create", "update", "patch", "delete", "deletecollection"} {
		check("no", verb, "namespaces", "--all-namespaces")
		for _, resource := range []string{"configmaps", "secrets", "services"} {
			for _, ns := range []string{"temporal-demo-worker-a", "temporal-demo-worker-b", "temporal-demo-worker-c", "temporal-demo-worker-d", "default", "temporal-demo-orchestrator"} {
				check("no", verb, resource, "-n", ns)
			}
		}
	}
	h.g.Expect(string(h.k("-n", "temporal-demo-orchestrator", "get", "deployment", "orchestrator", "-o", "jsonpath={.spec.replicas}:{.spec.template.spec.serviceAccountName}:{.spec.template.spec.automountServiceAccountToken}"))).To(gomega.Equal("1:orchestrator:true"))
	h.g.Expect(string(h.k("-n", "temporal-demo-orchestrator", "get", "serviceaccount", "orchestrator", "-o", "jsonpath={.metadata.namespace}"))).To(gomega.Equal("temporal-demo-orchestrator"))
	t.Log("PASS actual allow/deny for every get/list/watch scope, namespace-only cluster grant, no Secret/Service/all-namespace ConfigMap access, all writes denied; orchestrator replica1 in its own namespace")
}

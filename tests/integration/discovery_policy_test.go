//go:build integration && policies_integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/onsi/gomega"
	"go.temporal.io/sdk/client"
)

type policyHarness struct {
	t    *testing.T
	g    *gomega.WithT
	root string
}

func (h *policyHarness) command(input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args = append([]string{"--kubeconfig", filepath.Join(h.root, ".local/kubeconfig"), "--context", "kind-temporal-demo", "--request-timeout=15s"}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.CombinedOutput()
}
func (h *policyHarness) k(args ...string) []byte {
	h.t.Helper()
	b, err := h.command(nil, args...)
	h.g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", b)
	return b
}
func (h *policyHarness) apply(obj any) []byte {
	h.t.Helper()
	b, err := json.Marshal(obj)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	out, err := h.command(b, "apply", "-f", "-")
	h.g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", out)
	return out
}
func (h *policyHarness) service(ns, name string, annotations map[string]string) string {
	h.t.Helper()
	h.apply(map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": name, "namespace": ns, "annotations": annotations}, "spec": map[string]any{"ports": []any{map[string]any{"port": 8080}}}})
	return string(h.k("-n", ns, "get", "service", name, "-o", "jsonpath={.metadata.uid}"))
}
func (h *policyHarness) registration(ns, name, uid, deps string) plan.Registration {
	h.t.Helper()
	expected := map[string]string{"schemaVersion": "1", "workerName": name, "taskQueue": name + ".v1.temporal", "workflowType": "ExampleChildWorkflow", "runAfter": deps, "sourceServiceName": name, "sourceServiceNamespace": ns, "sourceServiceUID": uid}
	var raw []byte
	var data map[string]string
	h.g.Eventually(func() error {
		b, err := h.command(nil, "-n", ns, "get", "configmap", "temporal-worker-"+name, "-o", "json")
		if err != nil {
			return fmt.Errorf("%s: %w", b, err)
		}
		var cm struct {
			Data     map[string]string
			Metadata struct{ Labels map[string]string }
		}
		if err = json.Unmarshal(b, &cm); err != nil {
			return err
		}
		if cm.Metadata.Labels["temporal-demo.io/registration"] != "true" {
			return fmt.Errorf("missing registration label: %s", b)
		}
		actual, _ := json.Marshal(cm.Data)
		want, _ := json.Marshal(expected)
		if !bytes.Equal(actual, want) {
			return fmt.Errorf("wrong registration: %s; want %s", actual, want)
		}
		raw, data = b, cm.Data
		return nil
	}, 90*time.Second, time.Second).Should(gomega.Succeed())
	h.t.Logf("registration %s/%s: %s", ns, name, raw)
	return plan.Registration{SchemaVersion: data["schemaVersion"], WorkerName: data["workerName"], TaskQueue: data["taskQueue"], WorkflowType: data["workflowType"], RunAfter: data["runAfter"], SourceServiceName: data["sourceServiceName"], SourceServiceNamespace: data["sourceServiceNamespace"], SourceServiceUID: data["sourceServiceUID"]}
}
func (h *policyHarness) absent(ns, name string) {
	h.t.Helper()
	check := func() error {
		b, err := h.command(nil, "-n", ns, "get", "configmap", "temporal-worker-"+name, "--ignore-not-found", "-o", "name")
		if err != nil {
			return fmt.Errorf("%s: %w", b, err)
		}
		if len(bytes.TrimSpace(b)) != 0 {
			return fmt.Errorf("still registered: %s", b)
		}
		return nil
	}
	h.g.Eventually(check, 90*time.Second, time.Second).Should(gomega.Succeed())
	// Stability, not an immediate absence before the controllers can process events.
	h.g.Consistently(check, 5*time.Second, time.Second).Should(gomega.Succeed())
}
func (h *policyHarness) preservedIdentity() map[string]string {
	h.t.Helper()
	identity := map[string]string{}
	for _, name := range []string{"worker-a", "worker-b", "worker-c", "worker-d"} {
		identity[name] = string(h.k("-n", "temporal-demo-"+name, "get", "service", name, "-o", "jsonpath={.metadata.uid}"))
	}
	identity["PVC"] = string(h.k("-n", "temporal", "get", "pvc", "data-postgres-0", "-o", "jsonpath={.metadata.uid}:{.spec.volumeName}:{.status.phase}"))
	identity["Secret"] = string(h.k("-n", "temporal", "get", "secret", "postgres-credentials", "-o", "jsonpath={.metadata.uid}:{.metadata.resourceVersion}:{.immutable}"))
	identity["registry"] = string(h.k("-n", "temporal-demo-orchestrator", "get", "deployment", "orchestrator", "-o", `jsonpath={.spec.template.spec.containers[0].env[?(@.name=="REGISTRY_MODE")].value}`))
	h.g.Expect(identity["registry"]).To(gomega.Equal("static"))
	for key, value := range identity {
		h.g.Expect(value).NotTo(gomega.BeEmpty(), key)
	}
	return identity
}

func (h *policyHarness) install() {
	h.t.Helper()
	out, err := exec.Command("bash", filepath.Join(h.root, "scripts/policies-install.sh")).CombinedOutput()
	h.g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", out)
}

// Missing admission, bad generated data, stale registrations, lost source UID,
// and absent background processing each fail at the actual Kubernetes boundary.
func TestDiscoveryPolicy(t *testing.T) {
	h := &policyHarness{t: t, g: gomega.NewWithT(t), root: os.Getenv("POLICIES_ROOT")}
	h.g.Expect(h.root).NotTo(gomega.BeEmpty(), "use mise run test:policies-integration")
	h.install() // checks owned policy/RBAC names before any global policy mutation
	before := h.preservedIdentity()
	t.Logf("preserved identity before: %v", before)
	// Reuse the existing deterministic completed-history witness and owned tunnel.
	history := &harness{t: t, g: h.g, root: h.root}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var err error
	history.c, err = client.DialContext(ctx, client.Options{HostPort: history.forward("temporal", "temporal", 7233), Namespace: "default"})
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(history.c.Close)
	t.Setenv("SCALING_HISTORY_FILE", filepath.Join(t.TempDir(), "policy-history.json"))
	history.retainedHistory()
	suffix := fmt.Sprint(time.Now().UnixNano())
	ns, other, ordinary := "policy-workers-"+suffix, "policy-other-"+suffix, "policy-ordinary-"+suffix
	var ownedNamespaces []string
	// Register cleanup before mutation; do not mask deletion or restoration errors.
	t.Cleanup(func() {
		out, err := exec.Command("bash", filepath.Join(h.root, "scripts/policies-install.sh")).CombinedOutput()
		if err != nil {
			t.Errorf("restore owned policies: %v: %s", err, out)
		}
		for _, n := range ownedNamespaces {
			b, err := h.command(nil, "delete", "namespace", n, "--ignore-not-found", "--wait=true", "--timeout=90s")
			if err != nil {
				t.Errorf("cleanup %s: %v: %s", n, err, b)
			}
		}
		for _, name := range []string{"worker-a", "worker-b", "worker-c", "worker-d"} {
			deps := string(h.k("-n", "temporal-demo-"+name, "get", "service", name, "-o", "jsonpath={.metadata.annotations.temporal-worker-run-after}"))
			h.registration("temporal-demo-"+name, name, before[name], deps)
		}
		after := h.preservedIdentity()
		h.g.Expect(after).To(gomega.Equal(before), "original app Services/storage/credentials/static registry changed")
		history.retainedHistory()
		t.Logf("preserved identity after: %v", after)
	})
	for _, n := range []string{ns, other, ordinary} {
		labels := map[string]string{"temporal-demo.io/policy-test": suffix}
		if n != ordinary {
			labels["temporal-demo.io/worker-namespace"] = "true"
		}
		input, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": n, "labels": labels}})
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		out, err := h.command(input, "create", "-f", "-")
		h.g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", out)
		ownedNamespaces = append(ownedNamespaces, n)
	}
	// Wait for actual admission, not only policy readiness / webhook configuration.
	h.g.Eventually(func() bool {
		b, err := h.command([]byte(fmt.Sprintf(`{"apiVersion":"v1","kind":"Service","metadata":{"name":"probe","namespace":%q,"annotations":{"temporal-worker":"invalid"}},"spec":{"ports":[{"port":8080}]}}`, ns)), "create", "--dry-run=server", "-f", "-")
		return err != nil && bytes.Contains(b, []byte("temporal-validate-workers")) && bytes.Contains(b, []byte("worker-flag"))
	}, 60*time.Second, time.Second).Should(gomega.BeTrue())
	t.Run("admission", func(t *testing.T) {
		h := &policyHarness{t: t, g: gomega.NewWithT(t), root: h.root}
		var cases []struct {
			Raw string
			Bad bool
		}
		b, err := os.ReadFile(filepath.Join(h.root, "internal/plan/testdata/dependencies.json"))
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		h.g.Expect(json.Unmarshal(b, &cases)).To(gomega.Succeed())
		for i, tc := range cases {
			name := fmt.Sprintf("case-%d", i)
			for _, op := range []string{"CREATE", "UPDATE"} {
				if op == "UPDATE" {
					h.service(ns, name, map[string]string{"temporal-worker": "false"})
				}
				obj := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": name, "namespace": ns, "annotations": map[string]string{"temporal-worker": "true", "temporal-worker-run-after": tc.Raw}}, "spec": map[string]any{"ports": []any{map[string]any{"port": 8080}}}}
				input, _ := json.Marshal(obj)
				verb := "create"
				if op == "UPDATE" {
					verb = "apply"
				}
				out, err := h.command(input, verb, "--dry-run=server", "-f", "-")
				if tc.Bad {
					h.g.Expect(err).To(gomega.HaveOccurred(), "%s %q: %s", op, tc.Raw, out)
					h.g.Expect(string(out)).To(gomega.ContainSubstring("temporal-validate-workers"))
					h.g.Expect(string(out)).To(gomega.ContainSubstring("worker-dependencies"))
				} else {
					h.g.Expect(err).NotTo(gomega.HaveOccurred(), "%s %q: %s", op, tc.Raw, out)
				}
			}
		}
		// Explicit CREATE and UPDATE rule denials, including whitespace self-reference.
		for _, op := range []string{"create", "apply"} {
			if op == "apply" {
				h.service(ns, "self", map[string]string{"temporal-worker": "false"})
				h.service(ns, "flag", nil)
			}
			for name, annotations := range map[string]map[string]string{"self": {"temporal-worker": "true", "temporal-worker-run-after": " \u00a0self\t "}, "flag": {"temporal-worker": "TRUE"}} {
				input, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": name, "namespace": ns, "annotations": annotations}, "spec": map[string]any{"ports": []any{map[string]any{"port": 8080}}}})
				out, err := h.command(input, op, "--dry-run=server", "-f", "-")
				h.g.Expect(err).To(gomega.HaveOccurred())
				h.g.Expect(string(out)).To(gomega.ContainSubstring("temporal-validate-workers"))
				rule := "worker-dependencies"
				if name == "flag" {
					rule = "worker-flag"
				}
				h.g.Expect(string(out)).To(gomega.ContainSubstring(rule))
				t.Logf("%s denied by %s: %s", op, rule, out)
			}
		}
	})
	if t.Failed() {
		return
	}
	annotations := map[string]string{"temporal-worker": "true"}
	uid := h.service(ns, "worker-x", annotations)
	h.registration(ns, "worker-x", uid, "")
	annotations["temporal-worker-run-after"] = " worker-missing,worker-missing "
	h.service(ns, "worker-x", annotations)
	h.registration(ns, "worker-x", uid, annotations["temporal-worker-run-after"])
	annotations["temporal-worker"] = "false"
	annotations["temporal-worker-run-after"] = "," // stale text must not prevent deregistration
	h.service(ns, "worker-x", annotations)
	h.absent(ns, "worker-x")
	t.Log("PASS disable cleans registration")
	annotations = map[string]string{"temporal-worker": "true"}
	h.service(ns, "worker-x", annotations)
	h.registration(ns, "worker-x", uid, "")
	h.service(ns, "worker-x", map[string]string{"temporal-worker-run-after": ","})
	h.absent(ns, "worker-x")
	t.Log("PASS annotation removal cleans registration")
	h.service(ns, "worker-x", annotations)
	h.registration(ns, "worker-x", uid, "")
	h.k("-n", ns, "delete", "service", "worker-x", "--wait=true")
	h.absent(ns, "worker-x")
	t.Log("PASS source deletion cleans registration")
	newUID := h.service(ns, "worker-x", annotations)
	h.g.Expect(newUID).NotTo(gomega.Equal(uid))
	first := h.registration(ns, "worker-x", newUID, "")
	otherUID := h.service(other, "worker-x", annotations)
	second := h.registration(other, "worker-x", otherUID, "")
	_, err = plan.Build([]plan.Registration{first, second})
	h.g.Expect(err).To(gomega.HaveOccurred())
	h.g.Expect(err.Error()).To(gomega.ContainSubstring("duplicate"))
	t.Log("PASS identical names in two namespaces; combined graph rejects duplicate identity")
	h.service(ordinary, "unselected", map[string]string{"temporal-worker": "invalid"})
	// True registration plus malformed dependencies isolates namespace exclusion
	// for both generation and dependency admission, on actual CREATE and UPDATE.
	h.service(ordinary, "unselected-worker", map[string]string{"temporal-worker": "true", "temporal-worker-run-after": ","})
	h.service(ordinary, "unselected-worker", map[string]string{"temporal-worker": "true", "temporal-worker-run-after": "worker-a,"})
	h.service(ns, "unregistered", nil)
	unrelatedAnnotations := map[string]string{"temporal-worker": "true"}
	// Both kinds reside in a selected namespace and satisfy the true-registration
	// precondition; only kind scope permits their malformed dependencies.
	for _, deps := range []string{",", "worker-a,"} {
		unrelatedAnnotations["temporal-worker-run-after"] = deps
		h.apply(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "ordinary", "namespace": ns, "annotations": unrelatedAnnotations}, "spec": map[string]any{"containers": []any{map[string]any{"name": "pause", "image": "registry.k8s.io/pause:3.10"}}}})
		h.apply(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "ordinary-deploy", "namespace": ns, "annotations": unrelatedAnnotations}, "spec": map[string]any{"replicas": 0, "selector": map[string]any{"matchLabels": map[string]string{"app": "ordinary"}}, "template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app": "ordinary"}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "pause", "image": "registry.k8s.io/pause:3.10"}}}}}})
	}
	t.Log("PASS CREATE/UPDATE accepts true registrations with malformed dependencies in unselected namespace and unrelated kinds")
	for _, pair := range [][2]string{{ordinary, "unselected"}, {ordinary, "unselected-worker"}, {ns, "unregistered"}, {ns, "ordinary"}, {ns, "ordinary-deploy"}} {
		h.absent(pair[0], pair[1])
	}
	// Delete only our two policies. Cleanup always reinstalls their checked-in baseline.
	h.k("delete", "clusterpolicy", "temporal-generate-registrations", "temporal-validate-workers", "--wait=true")
	// Policy deletion and webhook cache removal are different asynchronous events.
	h.g.Eventually(func() error {
		input := []byte(fmt.Sprintf(`{"apiVersion":"v1","kind":"Service","metadata":{"name":"probe","namespace":%q,"annotations":{"temporal-worker":"invalid"}},"spec":{"ports":[{"port":8080}]}}`, ns))
		out, err := h.command(input, "create", "--dry-run=server", "-f", "-")
		if err != nil {
			return fmt.Errorf("policy removal not converged: %s: %w", out, err)
		}
		return nil
	}, 60*time.Second, time.Second).Should(gomega.Succeed())
	lateUID := h.service(ns, "late", annotations)
	h.absent(ns, "late") // source exists without a registration BEFORE reinstall
	badUID := h.service(ns, "preexisting-invalid", map[string]string{"temporal-worker": "invalid"})
	h.install()
	h.registration(ns, "late", lateUID, "")
	h.g.Eventually(func() error {
		b, err := h.command(nil, "-n", ns, "get", "policyreports", "-o", "json")
		if err != nil {
			return fmt.Errorf("%s: %w", b, err)
		}
		var reports struct {
			Items []struct {
				Scope   struct{ UID string }
				Results []struct{ Policy, Rule, Result string }
			}
		}
		if err = json.Unmarshal(b, &reports); err != nil {
			return err
		}
		for _, report := range reports.Items {
			if report.Scope.UID != badUID {
				continue
			}
			for _, result := range report.Results {
				if result.Policy == "temporal-validate-workers" && result.Rule == "worker-flag" && result.Result == "fail" {
					t.Logf("preexisting violation for UID %s: %+v", badUID, report)
					return nil
				}
			}
		}
		return fmt.Errorf("no background violation for UID %s: %s", badUID, strings.TrimSpace(string(b)))
	}, 120*time.Second, time.Second).Should(gomega.Succeed())
	h.absent(ns, "preexisting-invalid")
	h.k("-n", ns, "delete", "service", "preexisting-invalid", "--wait=true")
	t.Log("PASS preexisting generation and validation reporting; invalid source deletion allowed")
}

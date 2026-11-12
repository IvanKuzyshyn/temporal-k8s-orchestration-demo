//go:build demo_integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// Proves real admission and asynchronous generation, beyond CLI evaluation.
// Fixtures have no Deployments and are removed before the demo graph is checked.
func demoCheckPolicyLifecycle(t *testing.T, root string) {
	t.Helper()
	run := func(input []byte, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", filepath.Join(root, ".local/kubeconfig"), "--context", "kind-temporal-demo", "--request-timeout=15s"}, args...)...)
		cmd.Stdin = bytes.NewReader(input)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return append(out, stderr.Bytes()...), err
		}
		// Reading a deprecated CRD endpoint itself emits an API warning even
		// for a missing instance. Do not confuse stderr with resource data.
		return out, nil
	}
	must := func(args ...string) []byte {
		t.Helper()
		out, err := run(nil, args...)
		if err != nil {
			t.Fatalf("kubectl %v: %v\n%s", args, err, out)
		}
		return out
	}
	apply := func(obj any, deny bool) {
		t.Helper()
		b, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		out, err := run(b, "apply", "-f", "-")
		if deny {
			if err == nil || !strings.Contains(string(out), "temporal-validate-workers") {
				t.Fatalf("expected worker policy denial: %v %s", err, out)
			}
		} else if err != nil {
			t.Fatalf("apply: %v %s", err, out)
		}
	}
	eventually := func(label string, check func() bool) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("timed out: %s", label)
	}
	for _, policy := range []string{"validatingpolicy.policies.kyverno.io/temporal-validate-workers", "generatingpolicy.policies.kyverno.io/temporal-generate-registrations"} {
		must("wait", "--for=jsonpath={.status.conditionStatus.ready}=true", policy, "--timeout=90s")
	}
	for _, name := range []string{"temporal-validate-workers", "temporal-generate-registrations"} {
		if out := must("get", "clusterpolicy/"+name, "--ignore-not-found", "-o", "name"); len(bytes.TrimSpace(out)) != 0 {
			t.Fatalf("legacy instance remains: %s", out)
		}
	}
	log, err := os.ReadFile(filepath.Join(root, ".local/lifecycle/up.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "ClusterPolicy is deprecated") {
		t.Fatal("legacy API warning in fresh startup")
	}
	const selected = "cel-policy-selected"
	const ordinary = "cel-policy-ordinary"
	const existing = "cel-policy-existing"
	const copied = "cel-policy-existing-registrations"
	t.Cleanup(func() {
		out, err := run(nil, "delete", "generatingpolicy.policies.kyverno.io/"+copied, "--ignore-not-found", "--wait=true", "--timeout=90s")
		if err != nil {
			t.Errorf("cleanup policy: %v %s", err, out)
		}
		out, err = run(nil, "delete", "namespace", selected, ordinary, existing, "--ignore-not-found", "--wait=true", "--timeout=90s")
		if err != nil {
			t.Errorf("cleanup namespaces: %v %s", err, out)
		}
	})
	for _, ns := range []string{selected, ordinary, existing} {
		// Create, never adopt an existing namespace under a deterministic fixture name.
		must("create", "namespace", ns)
	}
	must("label", "namespace", selected, "temporal-demo.io/worker-namespace=true")
	service := func(ns, name, flag, deps string) map[string]any {
		annotations := map[string]string{"temporal-worker": flag, "temporal-worker-run-after": deps}
		return map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": name, "namespace": ns, "annotations": annotations}, "spec": map[string]any{"ports": []any{map[string]any{"port": 80}}}}
	}
	for _, tc := range []struct{ name, flag, deps string }{{"bad-flag", "TRUE", ""}, {"bad-deps", "true", ","}, {"self", "true", " \u00a0self\t "}} {
		apply(service(selected, tc.name, tc.flag, tc.deps), true)
		apply(service(ordinary, tc.name, tc.flag, tc.deps), false)
	}
	// Admission of unchanged invalid old/new data must not be grandfathered.
	must("label", "namespace", ordinary, "temporal-demo.io/worker-namespace=true")
	invalid := service(ordinary, "bad-flag", "TRUE", "")
	invalid["metadata"].(map[string]any)["labels"] = map[string]string{"update": "true"}
	apply(invalid, true)
	must("label", "namespace", ordinary, "temporal-demo.io/worker-namespace-")
	const name = "probe"
	cm := func(ns string) (map[string]string, string, bool) {
		out, err := run(nil, "-n", ns, "get", "configmap", "temporal-worker-"+name, "--ignore-not-found", "-o", "json")
		if err != nil || len(bytes.TrimSpace(out)) == 0 {
			return nil, "", false
		}
		var obj struct {
			Metadata struct{ UID string }
			Data     map[string]string
		}
		if json.Unmarshal(out, &obj) != nil {
			return nil, "", false
		}
		return obj.Data, obj.Metadata.UID, true
	}
	uid := func(ns string) string {
		return strings.TrimSpace(string(must("-n", ns, "get", "service", name, "-o", "jsonpath={.metadata.uid}")))
	}
	expected := func(ns, sourceUID, deps string) map[string]string {
		return map[string]string{
			"schemaVersion": "1", "workerName": name, "taskQueue": name + ".v1.temporal", "workflowType": "ExampleChildWorkflow",
			"runAfter": deps, "sourceServiceName": name, "sourceServiceNamespace": ns, "sourceServiceUID": sourceUID,
		}
	}
	awaitData := func(ns, sourceUID, deps string) {
		t.Helper()
		eventually("registration data in "+ns, func() bool {
			data, _, ok := cm(ns)
			return ok && reflect.DeepEqual(data, expected(ns, sourceUID, deps))
		})
	}
	awaitAbsent := func(ns string) {
		t.Helper()
		eventually("registration deleted in "+ns, func() bool {
			out, err := run(nil, "-n", ns, "get", "configmap", "temporal-worker-"+name, "--ignore-not-found", "-o", "name")
			return err == nil && len(bytes.TrimSpace(out)) == 0
		})
	}
	apply(service(selected, name, "true", ""), false)
	sourceUID := uid(selected)
	awaitData(selected, sourceUID, "")
	apply(service(selected, name, "TRUE", ""), true)
	apply(service(selected, name, "true", ","), true)
	raw := " \u00a0worker-z,worker-y,worker-z\t "
	apply(service(selected, name, "true", raw), false)
	awaitData(selected, sourceUID, raw)
	must("-n", selected, "patch", "configmap", "temporal-worker-"+name, "--type=merge", "-p", `{"data":{"workerName":"tampered"}}`)
	awaitData(selected, sourceUID, raw)
	_, oldCMUID, _ := cm(selected)
	must("-n", selected, "delete", "configmap", "temporal-worker-"+name)
	awaitData(selected, sourceUID, raw)
	_, newCMUID, _ := cm(selected)
	if oldCMUID == newCMUID {
		t.Fatal("deleted ConfigMap was not recreated")
	}
	must("-n", selected, "annotate", "service", name, "temporal-worker-")
	awaitAbsent(selected)
	apply(service(selected, name, "true", raw), false)
	awaitData(selected, sourceUID, raw)
	apply(service(selected, name, "false", raw), false)
	awaitAbsent(selected)
	apply(service(selected, name, "true", raw), false)
	awaitData(selected, sourceUID, raw)
	must("-n", selected, "delete", "service", name)
	awaitAbsent(selected)
	apply(service(selected, name, "true", ""), false)
	newUID := uid(selected)
	if newUID == sourceUID {
		t.Fatal("source Service UID did not change")
	}
	awaitData(selected, newUID, "")

	// Isolate a copied policy by a label absent from all demo namespaces. A
	// pre-existing source must be generated without sharing downstream ownership.
	must("label", "namespace", existing, "cel-test-existing=true")
	apply(service(existing, name, "true", raw), false)
	policyBytes, err := os.ReadFile(filepath.Join(root, "k8s/policies/generate-registrations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	if err = yaml.Unmarshal(policyBytes, &policy); err != nil {
		t.Fatal(err)
	}
	policy["metadata"] = map[string]any{"name": copied}
	spec := policy["spec"].(map[string]any)
	spec["matchConstraints"].(map[string]any)["namespaceSelector"] = map[string]any{"matchLabels": map[string]any{"cel-test-existing": "true"}}
	apply(policy, false)
	must("wait", "--for=jsonpath={.status.conditionStatus.ready}=true", "generatingpolicy.policies.kyverno.io/"+copied, "--timeout=90s")
	awaitData(existing, uid(existing), raw)
	must("delete", "generatingpolicy.policies.kyverno.io/"+copied)
	awaitAbsent(existing)
}

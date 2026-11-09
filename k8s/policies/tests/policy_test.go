package policies_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/gomega"
)

// Removing either validation rule, its namespace filter, or Unicode trimming
// must break these actual CLI CREATE/UPDATE evaluations of shared parser cases.
func TestAdmission(t *testing.T) {
	g := gomega.NewWithT(t)
	var cases []struct {
		Raw string
		Bad bool
	}
	data, err := os.ReadFile("../../../internal/plan/testdata/dependencies.json")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(json.Unmarshal(data, &cases)).To(gomega.Succeed())
	policy, err := filepath.Abs("../validate-workers.yaml")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, operation := range []string{"CREATE", "UPDATE", "DELETE"} {
		t.Run(operation, func(t *testing.T) {
			g := gomega.NewWithT(t)
			dir := t.TempDir()
			var resources, results []any
			add := func(name, kind, ns string, annotations map[string]string, flag, deps string) {
				if operation == "DELETE" {
					flag, deps = "skip", "skip"
				}
				version := "v1"
				if kind == "Deployment" {
					version = "apps/v1"
				}
				resources = append(resources, map[string]any{"apiVersion": version, "kind": kind, "metadata": map[string]any{"name": name, "namespace": ns, "annotations": annotations}})
				for rule, result := range map[string]string{"worker-flag": flag, "worker-dependencies": deps} {
					results = append(results, map[string]any{"kind": kind, "policy": "temporal-validate-workers", "rule": rule, "resources": []string{ns + "/" + name}, "result": result})
				}
			}
			for i, tc := range cases {
				result := "pass"
				if tc.Bad {
					result = "fail"
				}
				add(fmt.Sprintf("case-%d", i), "Service", "workers", map[string]string{"temporal-worker": "true", "temporal-worker-run-after": tc.Raw}, "pass", result)
			}
			add("self", "Service", "workers", map[string]string{"temporal-worker": "true", "temporal-worker-run-after": " \u00a0self\t "}, "pass", "fail")
			add("missing-deps", "Service", "workers", map[string]string{"temporal-worker": "true"}, "pass", "pass")
			add("false", "Service", "workers", map[string]string{"temporal-worker": "false", "temporal-worker-run-after": ","}, "pass", "skip")
			add("removed", "Service", "workers", map[string]string{"temporal-worker-run-after": ","}, "pass", "skip")
			for i, value := range []string{"", "TRUE", "yes"} {
				add(fmt.Sprintf("invalid-%d", i), "Service", "workers", map[string]string{"temporal-worker": value}, "fail", "skip")
			}
			add("unselected", "Service", "ordinary", map[string]string{"temporal-worker": "invalid"}, "skip", "skip")
			// True registration and malformed dependencies isolate namespace scope.
			add("unselected-dependencies", "Service", "ordinary", map[string]string{"temporal-worker": "true", "temporal-worker-run-after": ","}, "skip", "skip")
			for _, kind := range []string{"Pod", "Deployment"} {
				add("ordinary-"+strings.ToLower(kind), kind, "workers", map[string]string{"temporal-worker": "invalid"}, "skip", "skip")
				add("dependencies-"+strings.ToLower(kind), kind, "workers", map[string]string{"temporal-worker": "true", "temporal-worker-run-after": ","}, "skip", "skip")
			}
			writeJSON := func(name string, value any) {
				b, err := json.MarshalIndent(value, "", "  ")
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(os.WriteFile(filepath.Join(dir, name), b, 0600)).To(gomega.Succeed())
			}
			// JSON documents are YAML; the CLI expects individual resources, not a List.
			var documents []byte
			for _, resource := range resources {
				b, err := json.Marshal(resource)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				documents = append(documents, append(b, []byte("\n---\n")...)...)
			}
			g.Expect(os.WriteFile(filepath.Join(dir, "resources.yaml"), documents, 0600)).To(gomega.Succeed())
			writeJSON("values.yaml", map[string]any{"apiVersion": "cli.kyverno.io/v1alpha1", "kind": "Values", "globalValues": map[string]string{"request.operation": operation}, "namespaceSelector": []any{map[string]any{"name": "workers", "labels": map[string]string{"temporal-demo.io/worker-namespace": "true"}}}})
			writeJSON("kyverno-test.yaml", map[string]any{"apiVersion": "cli.kyverno.io/v1alpha1", "kind": "Test", "metadata": map[string]string{"name": "worker-admission-" + operation}, "policies": []string{policy}, "resources": []string{"resources.yaml"}, "variables": "values.yaml", "results": results})
			cmd := exec.Command("kyverno", "test", dir, "--remove-color")
			output, err := cmd.CombinedOutput()
			t.Logf("%s", output)
			g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", output)
		})
	}
}

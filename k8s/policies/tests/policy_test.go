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
	"go.yaml.in/yaml/v3"
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
	manifest, err := os.ReadFile(policy)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	var identity struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string
		Spec       struct {
			ValidationActions []string `yaml:"validationActions"`
		}
	}
	g.Expect(yaml.Unmarshal(manifest, &identity)).To(gomega.Succeed())
	g.Expect(identity.APIVersion).To(gomega.Equal("policies.kyverno.io/v1"))
	g.Expect(identity.Kind).To(gomega.Equal("ValidatingPolicy"))
	g.Expect(identity.Spec.ValidationActions).To(gomega.Equal([]string{"Deny"}))
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
				metadata := map[string]any{"name": name, "namespace": ns}
				if annotations != nil {
					metadata["annotations"] = annotations
				}
				resources = append(resources, map[string]any{"apiVersion": version, "kind": kind, "metadata": metadata})
				result := "pass"
				if flag == "skip" && deps == "skip" {
					result = "skip"
				}
				if flag == "fail" || deps == "fail" {
					result = "fail"
				}
				results = append(results, map[string]any{"kind": kind, "policy": "temporal-validate-workers", "isValidatingPolicy": true, "operation": operation, "resources": []string{ns + "/" + name}, "result": result})
			}
			for i, tc := range cases {
				result := "pass"
				if tc.Bad {
					result = "fail"
				}
				add(fmt.Sprintf("case-%d", i), "Service", "workers", map[string]string{"temporal-worker": "true", "temporal-worker-run-after": tc.Raw}, "pass", result)
			}
			add("self", "Service", "workers", map[string]string{"temporal-worker": "true", "temporal-worker-run-after": " \u00a0self\t "}, "pass", "fail")
			add("absent-annotations", "Service", "workers", nil, "pass", "skip")
			add("empty-annotations", "Service", "workers", map[string]string{}, "pass", "skip")
			for i, space := range " \t\n\v\f\r\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000" {
				add(fmt.Sprintf("space-%d", i), "Service", "workers", map[string]string{"temporal-worker": "true", "temporal-worker-run-after": string(space) + "worker-z" + string(space)}, "pass", "pass")
			}
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
			cmd := exec.Command("kyverno", "test", dir, "--remove-color", "--warnings-as-errors")
			output, err := cmd.CombinedOutput()
			t.Logf("%s", output)
			g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", output)
		})
	}
}

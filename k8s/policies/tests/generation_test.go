package policies_test

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
)

// Catches wrong schema fields, hard-coded namespace/UID, and generation scope.
func TestGeneration(t *testing.T) {
	g := gomega.NewWithT(t)
	dir := t.TempDir()
	policy, err := filepath.Abs("../generate-registrations.yaml")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	manifest, err := os.ReadFile(policy)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	var identity struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string
	}
	g.Expect(yaml.Unmarshal(manifest, &identity)).To(gomega.Succeed())
	g.Expect(identity.APIVersion).To(gomega.Equal("policies.kyverno.io/v1"))
	g.Expect(identity.Kind).To(gomega.Equal("GeneratingPolicy"))
	write := func(name, text string) {
		g.Expect(os.WriteFile(filepath.Join(dir, name), []byte(text), 0600)).To(gomega.Succeed())
	}
	write("service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: worker-x
  namespace: workers
  uid: source-uid
  annotations:
    temporal-worker: "true"
    temporal-worker-run-after: " worker-z,worker-y,worker-z "
`)
	// This Service satisfies the annotation match; only namespace scope excludes it.
	write("unselected.yaml", `apiVersion: v1
kind: Service
metadata:
  name: unselected-worker
  namespace: ordinary
  uid: unselected-source-uid
  annotations:
    temporal-worker: "true"
`)
	write("expected.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: temporal-worker-worker-x
  namespace: workers
  labels:
    temporal-demo.io/registration: "true"
data:
  schemaVersion: "1"
  workerName: worker-x
  taskQueue: worker-x.v1.temporal
  workflowType: ExampleChildWorkflow
  runAfter: " worker-z,worker-y,worker-z "
  sourceServiceName: worker-x
  sourceServiceNamespace: workers
  sourceServiceUID: source-uid
`)
	write("values.yaml", `apiVersion: cli.kyverno.io/v1alpha1
kind: Values
namespaceSelector:
  - name: workers
    labels:
      temporal-demo.io/worker-namespace: "true"
  - name: ordinary
    labels:
      temporal-demo.io/worker-namespace: "false"
`)
	write("kyverno-test.yaml", `apiVersion: cli.kyverno.io/v1alpha1
kind: Test
metadata:
  name: registration-schema
policies:
  - `+policy+`
resources:
  - service.yaml
variables: values.yaml
results:
  - policy: temporal-generate-registrations
    isGeneratingPolicy: true
    kind: Service
    resources:
      - workers/worker-x
    result: pass
    generatedResource: expected.yaml
`)
	output, err := exec.Command("kyverno", "test", dir, "--remove-color", "--warnings-as-errors").CombinedOutput()
	t.Logf("%s", output)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", output)

	// Inspect actual generated resources as well as CLI result status: an
	// excluded trigger must never produce a downstream ConfigMap.
	generated := filepath.Join(dir, "generated.yaml")
	output, err = exec.Command("kyverno", "apply", policy,
		"--resource", filepath.Join(dir, "service.yaml"),
		"--resource", filepath.Join(dir, "unselected.yaml"),
		"--values-file", filepath.Join(dir, "values.yaml"), "--output", generated, "--warnings-as-errors").CombinedOutput()
	t.Logf("%s", output)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", output)
	data, err := os.ReadFile(generated)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	var identities []string
	for {
		var resource struct {
			Kind     string
			Metadata struct{ Name, Namespace string }
		}
		err := decoder.Decode(&resource)
		if err == io.EOF {
			break
		}
		g.Expect(err).NotTo(gomega.HaveOccurred())
		if resource.Kind != "" {
			identities = append(identities, resource.Kind+"/"+resource.Metadata.Namespace+"/"+resource.Metadata.Name)
		}
	}
	// Positive and negative differ only in namespace selection, not registration.
	g.Expect(identities).To(gomega.Equal([]string{"ConfigMap/workers/temporal-worker-worker-x"}))
}

// Removing the worker match condition must generate unwanted registrations;
// dropping optional annotation guards must break the missing-map cases.
func TestGenerationSelection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{"no-annotations", nil, false},
		{"empty-annotations", map[string]string{}, false},
		{"false", map[string]string{"temporal-worker": "false"}, false},
		{"empty", map[string]string{"temporal-worker": ""}, false},
		{"missing-deps", map[string]string{"temporal-worker": "true"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			dir := t.TempDir()
			metadata := map[string]any{"name": "worker-x", "namespace": "workers", "uid": "source-uid"}
			if tc.annotations != nil {
				metadata["annotations"] = tc.annotations
			}
			b, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": metadata})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			resource := filepath.Join(dir, "resource.yaml")
			g.Expect(os.WriteFile(resource, b, 0600)).To(gomega.Succeed())
			values := filepath.Join(dir, "values.yaml")
			g.Expect(os.WriteFile(values, []byte(`apiVersion: cli.kyverno.io/v1alpha1
kind: Values
namespaceSelector:
- name: workers
  labels:
    temporal-demo.io/worker-namespace: "true"
`), 0600)).To(gomega.Succeed())
			generated := filepath.Join(dir, "generated.yaml")
			out, err := exec.Command("kyverno", "apply", "../generate-registrations.yaml", "--resource", resource, "--values-file", values, "--output", generated, "--warnings-as-errors").CombinedOutput()
			g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", out)
			b, err = os.ReadFile(generated)
			if !tc.want && os.IsNotExist(err) {
				return
			}
			g.Expect(err).NotTo(gomega.HaveOccurred())
			var cm struct {
				Kind string
				Data map[string]string
			}
			g.Expect(yaml.Unmarshal(b, &cm)).To(gomega.Succeed())
			if !tc.want {
				g.Expect(cm.Kind).To(gomega.BeEmpty())
				return
			}
			g.Expect(cm.Kind).To(gomega.Equal("ConfigMap"))
			g.Expect(cm.Data).To(gomega.Equal(map[string]string{
				"schemaVersion": "1", "workerName": "worker-x", "taskQueue": "worker-x.v1.temporal",
				"workflowType": "ExampleChildWorkflow", "runAfter": "", "sourceServiceName": "worker-x",
				"sourceServiceNamespace": "workers", "sourceServiceUID": "source-uid",
			}))
		})
	}
}

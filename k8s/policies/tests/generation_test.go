package policies_test

import (
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
    rule: register-worker
    kind: Service
    resources:
      - workers/worker-x
    result: pass
    generatedResource: expected.yaml
`)
	output, err := exec.Command("kyverno", "test", dir, "--remove-color").CombinedOutput()
	t.Logf("%s", output)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", output)

	// Inspect actual generated resources: excluded generate triggers have no CLI
	// test response in this release, so a result: skip expectation is not usable.
	generated := filepath.Join(dir, "generated.yaml")
	output, err = exec.Command("kyverno", "apply", policy,
		"--resource", filepath.Join(dir, "service.yaml"),
		"--resource", filepath.Join(dir, "unselected.yaml"),
		"--values-file", filepath.Join(dir, "values.yaml"), "--output", generated).CombinedOutput()
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

package policies_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/onsi/gomega"
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
}

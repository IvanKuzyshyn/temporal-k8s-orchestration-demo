package chart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/onsi/gomega"
)

// Each known GVK must first validate with the correct field type, then fail
// when ONLY that field's type changes. An unrelated fixture error cannot keep
// the negative test green if the intended schema validation regresses.
func TestControllerManifestsValidate(t *testing.T) {
	g := gomega.NewWithT(t)
	path := filepath.Join(t.TempDir(), "kyverno.yaml")
	render := exec.Command("bash", "scripts/render-kyverno.sh")
	render.Dir = "../.."
	out, err := render.Output()
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(os.WriteFile(path, out, 0600)).To(gomega.Succeed())
	cmd := exec.Command("bash", "../../scripts/validate-manifests.sh", "k8s/infrastructure/keda.yaml", path)
	b, err := cmd.CombinedOutput()
	g.Expect(err).NotTo(gomega.HaveOccurred(), string(b))
	g.Expect(string(b)).To(gomega.ContainSubstring("Invalid: 0, Errors: 0, Skipped: 0"))
}

func TestKnownCRDsRejectInvalidFields(t *testing.T) {
	for _, tc := range []struct{ name, input, valid, invalid, field string }{
		{"CustomResourceDefinition", "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata: {name: tests.example.invalid}\nspec:\n  group: example.invalid\n  names: {kind: Test, plural: tests}\n  scope: %s\n  versions:\n    - name: v1\n      served: true\n      storage: true\n      schema:\n        openAPIV3Schema: {type: object}\n", "Namespaced", "123", "scope"},
		{"ScaledObject", "apiVersion: keda.sh/v1alpha1\nkind: ScaledObject\nmetadata: {name: schema-test, namespace: temporal-demo-worker-a}\nspec:\n  scaleTargetRef: {name: worker-a}\n  maxReplicaCount: %s\n  triggers:\n    - type: temporal\n      metadata:\n        endpoint: temporal.temporal.svc.cluster.local:7233\n        namespace: default\n        taskQueue: worker-a.v1.temporal\n        queueTypes: workflow,activity\n        targetQueueSize: '1'\n        activationTargetQueueSize: '0'\n", "1", "wrong", "maxReplicaCount"},
		{"ClusterPolicy", "apiVersion: kyverno.io/v1\nkind: ClusterPolicy\nmetadata: {name: schema-test}\nspec:\n  rules:\n    - name: %s\n      match: {any: [{resources: {kinds: [Pod]}}]}\n      validate: {message: schema-test, pattern: {metadata: {name: x}}}\n", "valid-rule", "123", "/spec/rules/0/name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, invalid := range []bool{false, true} {
				t.Run(map[bool]string{false: "valid field", true: "wrong field type"}[invalid], func(t *testing.T) {
					g := gomega.NewWithT(t)
					value := tc.valid
					if invalid {
						value = tc.invalid
					}
					path := filepath.Join(t.TempDir(), "resource.yaml")
					g.Expect(os.WriteFile(path, []byte(fmt.Sprintf(tc.input, value)), 0600)).To(gomega.Succeed())
					cmd := exec.Command("bash", "../../scripts/validate-manifests.sh", path)
					b, err := cmd.CombinedOutput()
					if invalid {
						g.Expect(err).To(gomega.HaveOccurred(), string(b))
						g.Expect(string(b)).To(gomega.ContainSubstring("Invalid: 1"))
						g.Expect(string(b)).To(gomega.ContainSubstring(tc.field))
					} else {
						g.Expect(err).NotTo(gomega.HaveOccurred(), string(b))
						g.Expect(string(b)).To(gomega.ContainSubstring("Valid: 1, Invalid: 0"))
					}
					g.Expect(string(b)).To(gomega.ContainSubstring("Errors: 0"))
					g.Expect(string(b)).To(gomega.ContainSubstring("Skipped: 0"))
				})
			}
		})
	}
}

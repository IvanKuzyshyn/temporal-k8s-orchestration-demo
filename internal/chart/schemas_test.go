package chart

import (
	"github.com/onsi/gomega"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A known GVK with an invalid field must fail schema validation, not merely
// exist in a catalog. These exercise each release-extracted custom schema.
func TestKnownCRDsRejectInvalidFields(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"ScaledObject", "apiVersion: keda.sh/v1alpha1\nkind: ScaledObject\nmetadata: {name: bad, namespace: test}\nspec:\n  scaleTargetRef: {name: worker}\n  maxReplicaCount: wrong\n  triggers: [{type: temporal}]\n"},
		{"ClusterPolicy", "apiVersion: kyverno.io/v1\nkind: ClusterPolicy\nmetadata: {name: bad}\nspec:\n  rules:\n    - name: 123\n      match: {any: [{resources: {kinds: [Pod]}}]}\n      validate: {message: bad, pattern: {metadata: {name: x}}}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			path := filepath.Join(t.TempDir(), "bad.yaml")
			g.Expect(os.WriteFile(path, []byte(tc.input), 0600)).To(gomega.Succeed())
			cmd := exec.Command("bash", "../../scripts/validate-manifests.sh", path)
			b, err := cmd.CombinedOutput()
			g.Expect(err).To(gomega.HaveOccurred())
			g.Expect(string(b)).To(gomega.ContainSubstring("Invalid: 1"))
			g.Expect(string(b)).To(gomega.ContainSubstring("Errors: 0"))
		})
	}
}

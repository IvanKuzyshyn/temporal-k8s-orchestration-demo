package chart

import (
	"github.com/onsi/gomega"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A user kubeconfig must never be used even for destructive commands. This
// executes the real guard; no Kubernetes/Docker mutation is needed or allowed.
func TestForeignKubeconfigFailsClosed(t *testing.T) {
	for _, script := range []string{"cluster-delete.sh", "apps-deploy.sh", "infrastructure-install.sh", "images-load.sh"} {
		t.Run(script, func(t *testing.T) {
			g := gomega.NewWithT(t)
			foreign := filepath.Join(t.TempDir(), "config")
			g.Expect(os.WriteFile(foreign, []byte("do-not-touch\n"), 0600)).To(gomega.Succeed())
			cmd := exec.Command("bash", "../../scripts/"+script)
			cmd.Env = append(os.Environ(), "KUBECONFIG="+foreign)
			b, err := cmd.CombinedOutput()
			g.Expect(err).To(gomega.HaveOccurred())
			g.Expect(string(b)).To(gomega.ContainSubstring("refusing foreign KUBECONFIG"))
			original, err := os.ReadFile(foreign)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(string(original)).To(gomega.Equal("do-not-touch\n"))
		})
	}
}

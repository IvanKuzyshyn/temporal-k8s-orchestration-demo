package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/onsi/gomega"
)

// Execute the real shell guard with native-command test doubles. This proves
// fail-closed behavior only, not scaler compatibility (the integration tests do).
func TestScalingRefusesForeignOrPartialBeforeFixtureMutation(t *testing.T) {
	for _, mode := range []string{"foreign", "partial"} {
		t.Run(mode, func(t *testing.T) {
			g := gomega.NewWithT(t)
			root := t.TempDir()
			for _, dir := range []string{"scripts", ".local", "bin", "tmp"} {
				g.Expect(os.Mkdir(filepath.Join(root, dir), 0700)).To(gomega.Succeed())
			}
			for _, name := range []string{"scaling.sh", "kind-common.sh"} {
				b, err := os.ReadFile("../../scripts/" + name)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(os.WriteFile(filepath.Join(root, "scripts", name), b, 0700)).To(gomega.Succeed())
			}
			cfg := filepath.Join(root, ".local/kubeconfig")
			g.Expect(os.WriteFile(cfg, []byte("fixture"), 0600)).To(gomega.Succeed())
			g.Expect(os.WriteFile(filepath.Join(root, "bin/go"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0700)).To(gomega.Succeed())
			stub := `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$CALLS"
case "$*" in
  *'config current-context'*) echo kind-temporal-demo ;;
  *'get scaledobject worker-a '*)
    if [[ $FIXTURE_MODE == foreign ]]; then
      echo '{"metadata":{"labels":{}}}'
    else
      echo '{"metadata":{"labels":{"temporal-demo.io/scaling":"true"}}}'
    fi
    ;;
  *'get scaledobject '*) ;;
  *) echo '{"apiVersion":"v1","kind":"List","items":[]}' ;;
esac
`
			g.Expect(os.WriteFile(filepath.Join(root, "bin/kubectl"), []byte(stub), 0700)).To(gomega.Succeed())
			calls := filepath.Join(root, "calls")
			cmd := exec.Command("bash", "scripts/scaling.sh", "test")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "KUBECONFIG=", "TMPDIR="+filepath.Join(root, "tmp"), "CALLS="+calls, "FIXTURE_MODE="+mode, "SCALING_HISTORY_FILE=")
			b, err := cmd.CombinedOutput()
			g.Expect(err).To(gomega.HaveOccurred(), string(b))
			if mode == "foreign" {
				g.Expect(string(b)).To(gomega.ContainSubstring("Refusing foreign ScaledObject"))
			} else {
				g.Expect(string(b)).To(gomega.ContainSubstring("Partial scaler installation"))
			}
			b, err = os.ReadFile(calls)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			for _, verb := range []string{"apply", "delete", "rollout", "patch", "scale", "annotate"} {
				g.Expect(string(b)).NotTo(gomega.ContainSubstring(" "+verb+" "), "must fail BEFORE fixture changes/restore")
			}
		})
	}
}

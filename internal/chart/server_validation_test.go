package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/onsi/gomega"
)

// Missing API/Namespace/CRD errors must propagate, with no bootstrap or fallback.
func TestStandaloneServerValidation(t *testing.T) {
	for _, scenario := range []string{"ready", "not ready"} {
		t.Run(scenario, func(t *testing.T) {
			g := gomega.NewWithT(t)
			root := t.TempDir()
			for _, dir := range []string{"scripts", ".local", "tools"} {
				g.Expect(os.Mkdir(filepath.Join(root, dir), 0700)).To(gomega.Succeed())
			}
			for _, path := range []string{"scripts/validate-k8s.sh", "scripts/kind-common.sh"} {
				b, err := os.ReadFile("../../" + path)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(os.WriteFile(filepath.Join(root, path), b, 0600)).To(gomega.Succeed())
			}
			g.Expect(os.WriteFile(filepath.Join(root, ".local/kubeconfig"), []byte("fixture"), 0600)).To(gomega.Succeed())
			g.Expect(os.WriteFile(filepath.Join(root, "desired.yaml"), []byte("desired bytes"), 0600)).To(gomega.Succeed())
			body := `#!/usr/bin/env bash
set -euo pipefail
[[ $1 == --kubeconfig && $2 == "$STATE/.local/kubeconfig" ]] || exit 90
shift 2
if [[ $* == 'config current-context' ]]; then echo kind-temporal-demo; exit; fi
[[ $* == '--context kind-temporal-demo --request-timeout=15s apply --dry-run=server --validate=strict -f desired.yaml' ]] || exit 91
[[ $(<desired.yaml) == 'desired bytes' ]] || exit 92
if [[ $SCENARIO == 'not ready' ]]; then echo 'Namespace or CRD not ready' >&2; exit 42; fi
echo 'validated desired bytes (server dry run)'
`
			g.Expect(os.WriteFile(filepath.Join(root, "tools/kubectl"), []byte(body), 0755)).To(gomega.Succeed())
			cmd := exec.Command("bash", filepath.Join(root, "scripts/validate-k8s.sh"), "desired.yaml")
			cmd.Env = append(os.Environ(), "KUBECONFIG=", "STATE="+root, "SCENARIO="+scenario, "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if scenario == "ready" {
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
			} else {
				g.Expect(err).To(gomega.HaveOccurred())
				g.Expect(cmd.ProcessState.ExitCode()).To(gomega.Equal(42))
				g.Expect(string(out)).To(gomega.ContainSubstring("Namespace or CRD not ready"))
			}
		})
	}
}

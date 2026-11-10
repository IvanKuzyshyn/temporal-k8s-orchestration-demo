package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
)

// Failure in the additional phase cannot skip rollback, hide cleanup failure or
// erase cumulative static/policy/scaling/reset phases in the real CI workflow.
func TestDiscoveryWorkflowCleanup(t *testing.T) {
	g := gomega.NewWithT(t)
	b, err := os.ReadFile("../../.github/workflows/kind-integration.yaml")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	var w struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run, Shell string }
		}
	}
	g.Expect(yaml.Unmarshal(b, &w)).To(gomega.Succeed())
	var run string
	for _, s := range w.Jobs["baseline-persistence"].Steps {
		if s.Name == "Live registry lifecycle, pre-start protection and least-privilege RBAC" {
			g.Expect(s.Shell).To(gomega.Equal("bash"))
			run = s.Run
		}
	}
	g.Expect(run).NotTo(gomega.BeEmpty())
	for _, tc := range []struct {
		name, fail string
		exit       int
		want       string
	}{{"success", "", 0, "run apps:deploy-discovery\nrun test:registry\nrun test:rbac\nexec -- bash scripts/discovery.sh restore\n"}, {"registry failure", "test:registry", 42, "run apps:deploy-discovery\nrun test:registry\nexec -- bash scripts/discovery.sh restore\n"}, {"cleanup failure", "restore", 1, "run apps:deploy-discovery\nrun test:registry\nrun test:rbac\nexec -- bash scripts/discovery.sh restore\n"}} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			root := t.TempDir()
			g.Expect(os.Mkdir(filepath.Join(root, "bin"), 0700)).To(gomega.Succeed())
			const fake = "#!/bin/bash\necho \"$*\" >> calls\nif [[ $* == 'run apps:deploy-discovery' ]]; then mkdir -p .local/task9/static-baseline; touch .local/task9/static-baseline/identity; fi\nif [[ -n $FAIL && $* == *\"$FAIL\"* ]]; then exit 42; fi\n"
			g.Expect(os.WriteFile(filepath.Join(root, "bin/mise"), []byte(fake), 0755)).To(gomega.Succeed())
			cmd := exec.Command("bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", run)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"), "FAIL="+tc.fail)
			out, err := cmd.CombinedOutput()
			if tc.exit == 0 {
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
			} else {
				g.Expect(err).To(gomega.HaveOccurred(), string(out))
				g.Expect(cmd.ProcessState.ExitCode()).To(gomega.Equal(tc.exit))
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(strings.TrimSpace(string(calls))).To(gomega.Equal(strings.TrimSpace(tc.want)))
		})
	}
}

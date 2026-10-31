package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
)

// Execute the real baseline step with GitHub's documented Linux Bash defaults:
// unspecified shell uses -e, explicit bash also enables pipefail. A producer
// failing after printing history must not become a successful CI step via tee.
func TestKindWorkflowBaselinePropagatesFailure(t *testing.T) {
	g := gomega.NewWithT(t)
	b, err := os.ReadFile("../../.github/workflows/kind-integration.yaml")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run, Shell string }
		}
	}
	g.Expect(yaml.Unmarshal(b, &workflow)).To(gomega.Succeed())
	var run, shell string
	for _, step := range workflow.Jobs["baseline-persistence"].Steps {
		if step.Name == "Baseline and retained-history identity" {
			run, shell = step.Run, step.Shell
		}
	}
	g.Expect(run).NotTo(gomega.BeEmpty())
	for _, exit := range []int{0, 42} {
		t.Run("producer_exit_"+strconv.Itoa(exit), func(t *testing.T) {
			g := gomega.NewWithT(t)
			root := t.TempDir()
			for _, dir := range []string{"tools", ".local"} {
				g.Expect(os.Mkdir(filepath.Join(root, dir), 0700)).To(gomega.Succeed())
			}
			const history = `{"workflowId":"fixture-history","runId":"fixture-run","status":"completed"}`
			producer := "#!/bin/sh\n[ \"$*\" = 'run test:kind-baseline' ] || exit 99\nprintf '%s\\n' '" + history + "'\nexit \"$PRODUCER_EXIT\"\n"
			g.Expect(os.WriteFile(filepath.Join(root, "tools/mise"), []byte(producer), 0755)).To(gomega.Succeed())
			step := filepath.Join(root, "step.sh")
			g.Expect(os.WriteFile(step, []byte(run), 0600)).To(gomega.Succeed())
			args := []string{"-e", step}
			switch shell {
			case "":
			case "bash":
				args = []string{"--noprofile", "--norc", "-e", "-o", "pipefail", step}
			default:
				t.Fatalf("unsupported runner shell %q; model its execution explicitly", shell)
			}
			cmd := exec.Command("bash", args...)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"), "PRODUCER_EXIT="+strconv.Itoa(exit))
			out, err := cmd.CombinedOutput()
			if exit == 0 {
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
				saved, err := os.ReadFile(filepath.Join(root, ".local/history-before-reset.json"))
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(string(saved)).To(gomega.Equal(history + "\n"))
			} else {
				g.Expect(err).To(gomega.HaveOccurred(), "baseline failure after workflow JSON must survive tee: "+string(out))
				g.Expect(cmd.ProcessState.ExitCode()).To(gomega.Equal(exit))
			}
		})
	}
}

package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/gomega"
)

func TestAcceptanceWorkflow(t *testing.T) {
	g := gomega.NewWithT(t)
	b, err := os.ReadFile("../../.github/workflows/kind-integration.yaml")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	text := string(b)
	first := strings.Index(text, "mise run smoke")
	reset := strings.Index(text, "Destructive reset ONLY")
	last := strings.LastIndex(text, "mise run smoke")
	g.Expect(first).To(gomega.BeNumerically(">=", 0))
	g.Expect(first).To(gomega.BeNumerically("<", reset))
	g.Expect(last).To(gomega.BeNumerically(">", reset))
	g.Expect(text).To(gomega.ContainSubstring("mise run test:resilience"))
	g.Expect(text).To(gomega.ContainSubstring("if: github.event_name == 'workflow_dispatch'"))
	for _, old := range []string{"test:kind-validation", "test:kind-persistence", "test:policies-integration", "test:scaling", "test:registry", "test:rbac"} {
		g.Expect(text).To(gomega.ContainSubstring("mise run " + old))
	}
}

func TestAcceptanceKubectlStream(t *testing.T) {
	g := gomega.NewWithT(t)
	b, err := os.ReadFile("../../scripts/smoke.sh")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	var pipeline string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, `k create --dry-run=client -f "$proof/apps.yaml"`) {
			pipeline = line
		}
	}
	g.Expect(pipeline).NotTo(gomega.BeEmpty())
	dir := t.TempDir()
	// kubectl emits one JSON object per YAML document, NOT a List. Exercise
	// the real production pipeline against that native output boundary.
	script := `set -euo pipefail
k() { printf '%s\n' '{"kind":"Deployment","metadata":{"name":"worker-a"}}' '{"kind":"Service","metadata":{"name":"worker-a"}}'; }
proof=$1
` + pipeline + "\njq -e '.kind==\"List\" and (.items|length)==1 and .items[0].kind==\"Deployment\"' \"$proof/owned.json\"\n"
	cmd := exec.Command("bash", "-c", script, "test", dir)
	out, err := cmd.CombinedOutput()
	g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
}

func TestAcceptanceRestoreAccessOrder(t *testing.T) {
	g := gomega.NewWithT(t)
	b, err := os.ReadFile("../../scripts/smoke.sh")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	text := string(b)
	function := func(name string) string {
		start := strings.Index(text, name+"() {")
		g.Expect(start).To(gomega.BeNumerically(">=", 0))
		end := strings.Index(text[start:], "\n}\n")
		return text[start : start+end+3]
	}
	dir := t.TempDir()
	obj := `{"metadata":{"uid":"uid","annotations":{"deployment.kubernetes.io/revision":"1","kubectl.kubernetes.io/last-applied-configuration":"original"}},"spec":{"replicas":1}}`
	for _, name := range []string{"orchestrator", "worker-a", "worker-b", "worker-c", "worker-d"} {
		g.Expect(os.WriteFile(filepath.Join(dir, "deployment-"+name+".json"), []byte(obj), 0600)).To(gomega.Succeed())
		g.Expect(os.WriteFile(filepath.Join(dir, "service-"+name+".json"), []byte(obj), 0600)).To(gomega.Succeed())
	}
	g.Expect(os.WriteFile(filepath.Join(dir, "created.tsv"), []byte("ServiceAccount temporal-demo-orchestrator orchestrator uid\nScaledObject temporal-demo-worker-a worker-a uid\n"), 0600)).To(gomega.Succeed())
	g.Expect(os.WriteFile(filepath.Join(dir, "identity-before"), []byte("identity\n"), 0600)).To(gomega.Succeed())
	script := `set -euo pipefail
proof=$1
k() {
  echo "$*" >> "$proof/calls"
  if [[ $* == *"jsonpath={.metadata.uid}"* ]]; then printf uid
  elif [[ $* == *"get deployment"* ]]; then printf '%s' '` + obj + `'
  fi
}
go() { return 0; }
identity() { echo identity; }
` + function("restore")
	if strings.Contains(text, "delete_created() {") {
		script += function("delete_created")
	}
	script += "\nrestore\n"
	cmd := exec.Command("bash", "-c", script, "test", dir)
	out, err := cmd.CombinedOutput()
	g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	g.Expect(err).NotTo(gomega.HaveOccurred())
	access := strings.Index(string(calls), "delete ServiceAccount")
	rolled := strings.LastIndex(string(calls), "rollout status")
	g.Expect(access).To(gomega.BeNumerically(">", rolled), string(calls))
}

func TestAcceptanceCleanupOwnershipAndErrors(t *testing.T) {
	b, err := os.ReadFile("../../scripts/smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	start := strings.Index(text, "delete_created() {")
	end := strings.Index(text[start:], "\n}\n")
	for _, tc := range []struct {
		name, uid, deleteExit string
		exit                  int
		deletes               bool
	}{
		{"owned", "uid", "0", 0, true},
		{"replacement UID", "foreign", "0", 1, false},
		{"delete failure", "uid", "42", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			dir := t.TempDir()
			g.Expect(os.WriteFile(filepath.Join(dir, "created.tsv"), []byte("ScaledObject temporal-demo-worker-a worker-a uid\n"), 0600)).To(gomega.Succeed())
			script := `set -euo pipefail
proof=$1
k() {
  if [[ $* == *"get ScaledObject"* ]]; then printf '%s' "$CURRENT_UID"
  elif [[ $* == *"delete ScaledObject"* ]]; then echo delete >> "$proof/deletes"; return "$DELETE_EXIT"
  else exit 99
  fi
}
` + text[start:start+end+3] + "\ndelete_created scalers\n"
			cmd := exec.Command("bash", "-c", script, "test", dir)
			cmd.Env = append(os.Environ(), "CURRENT_UID="+tc.uid, "DELETE_EXIT="+tc.deleteExit)
			out, err := cmd.CombinedOutput()
			if tc.exit == 0 {
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
			} else {
				g.Expect(err).To(gomega.HaveOccurred())
				g.Expect(cmd.ProcessState.ExitCode()).To(gomega.Equal(tc.exit))
			}
			_, err = os.Stat(filepath.Join(dir, "deletes"))
			g.Expect(err == nil).To(gomega.Equal(tc.deletes))
		})
	}
}

func TestAcceptanceEmptyForwardCleanup(t *testing.T) {
	g := gomega.NewWithT(t)
	b, err := os.ReadFile("../../scripts/smoke.sh")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	text := string(b)
	start := strings.Index(text, "stop_forwards() {")
	g.Expect(start).To(gomega.BeNumerically(">=", 0))
	end := strings.Index(text[start:], "\n}\n")
	g.Expect(end).To(gomega.BeNumerically(">=", 0))
	script := "set -euo pipefail\npids=()\nSCALING_FORWARD_DIR=$1\n" + text[start:start+end+3] + "\nstop_forwards\n"
	cmd := exec.Command("/bin/bash", "-c", script, "test", filepath.Join(t.TempDir(), "empty"))
	out, err := cmd.CombinedOutput()
	g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
}

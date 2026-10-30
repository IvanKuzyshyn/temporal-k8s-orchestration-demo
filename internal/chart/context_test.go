package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/gomega"
)

// Down must scope deletion to the named cluster/local file, refuse a misplaced
// local context, and do nothing when that cluster is absent.
func TestDownNamedAndContextScoped(t *testing.T) {
	for _, scenario := range []string{"owned", "wrong context", "absent", "enumeration failure"} {
		t.Run(scenario, func(t *testing.T) {
			g := gomega.NewWithT(t)
			root := t.TempDir()
			for _, dir := range []string{"scripts", ".local", "tools"} {
				g.Expect(os.MkdirAll(filepath.Join(root, dir), 0700)).To(gomega.Succeed())
			}
			for _, path := range []string{"scripts/k8s-down.sh", "scripts/kind-common.sh"} {
				b, err := os.ReadFile("../../" + path)
				g.Expect(err).NotTo(gomega.HaveOccurred(), "new scoped teardown entrypoint")
				g.Expect(os.WriteFile(filepath.Join(root, path), b, 0600)).To(gomega.Succeed())
			}
			config := "apiVersion: v1\nkind: Config\ncurrent-context: kind-temporal-demo\ncontexts: []\nclusters: []\nusers: []\n"
			if scenario == "wrong context" {
				config = strings.ReplaceAll(config, "kind-temporal-demo", "kind-unrelated")
			}
			g.Expect(os.WriteFile(filepath.Join(root, ".local/kubeconfig"), []byte(config), 0600)).To(gomega.Succeed())
			body := "#!/bin/sh\nif [ \"$*\" = 'get clusters' ]; then\n"
			if scenario == "enumeration failure" {
				body += "exit 99\n"
			} else if scenario != "absent" {
				body += "echo temporal-demo\n"
			}
			body += "exit 0\nfi\nprintf '%s\\n' \"$*\" >> \"$CALLS\"\n"
			g.Expect(os.WriteFile(filepath.Join(root, "tools/kind"), []byte(body), 0755)).To(gomega.Succeed())
			cmd := exec.Command("bash", filepath.Join(root, "scripts/k8s-down.sh"))
			cmd.Env = append(os.Environ(), "KUBECONFIG=", "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"), "CALLS="+filepath.Join(root, "calls"))
			out, err := cmd.CombinedOutput()
			calls, _ := os.ReadFile(filepath.Join(root, "calls"))
			if scenario == "wrong context" || scenario == "enumeration failure" {
				g.Expect(err).To(gomega.HaveOccurred(), string(out))
				g.Expect(string(calls)).To(gomega.BeEmpty())
			} else {
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
				if scenario == "owned" {
					g.Expect(string(calls)).To(gomega.Equal("delete cluster --name temporal-demo --kubeconfig " + filepath.Join(root, ".local/kubeconfig") + "\n"))
				} else {
					g.Expect(string(calls)).To(gomega.BeEmpty())
				}
			}
		})
	}
}

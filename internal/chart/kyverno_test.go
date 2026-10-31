package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/onsi/gomega"
)

// Corrupt cached bytes and failed external commands must never produce a usable
// manifest. Real Helm is exercised by TestInfrastructureAndBlockYAML.
func TestKyvernoRenderFailures(t *testing.T) {
	for _, scenario := range []string{"tampered cache", "download failure", "template failure"} {
		t.Run(scenario, func(t *testing.T) {
			g := gomega.NewWithT(t)
			root := t.TempDir()
			repo, err := filepath.Abs("../..")
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(os.Symlink(filepath.Join(repo, "k8s"), filepath.Join(root, "k8s"))).To(gomega.Succeed())
			cache := filepath.Join(root, ".local/charts")
			g.Expect(os.MkdirAll(cache, 0700)).To(gomega.Succeed())
			tools := filepath.Join(root, "tools")
			g.Expect(os.Mkdir(tools, 0700)).To(gomega.Succeed())
			switch scenario {
			case "tampered cache":
				g.Expect(os.WriteFile(filepath.Join(cache, "kyverno-3.9.2.tgz"), []byte("tampered"), 0600)).To(gomega.Succeed())
			case "download failure":
				g.Expect(os.WriteFile(filepath.Join(tools, "curl"), []byte("#!/bin/sh\nexit 42\n"), 0755)).To(gomega.Succeed())
			case "template failure":
				b, err := os.ReadFile(filepath.Join(repo, ".local/charts/kyverno-3.9.2.tgz"))
				// Populate the real verified cache if this is a fresh checkout.
				if os.IsNotExist(err) {
					cmd := exec.Command("bash", "scripts/render-kyverno.sh")
					cmd.Dir = repo
					out, runErr := cmd.CombinedOutput()
					g.Expect(runErr).NotTo(gomega.HaveOccurred(), string(out))
					b, err = os.ReadFile(filepath.Join(repo, ".local/charts/kyverno-3.9.2.tgz"))
				}
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(os.WriteFile(filepath.Join(cache, "kyverno-3.9.2.tgz"), b, 0600)).To(gomega.Succeed())
				g.Expect(os.WriteFile(filepath.Join(tools, "helm"), []byte("#!/bin/sh\nexit 43\n"), 0755)).To(gomega.Succeed())
			}
			cmd := exec.Command("bash", filepath.Join(repo, "scripts/render-kyverno.sh"))
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.Output()
			g.Expect(err).To(gomega.HaveOccurred())
			g.Expect(out).To(gomega.BeEmpty(), "failed rendering must emit no deployable data")
		})
	}
}

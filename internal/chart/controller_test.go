package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/onsi/gomega"
)

// Corrupt fresh/cached bytes and failed external commands must never produce a
// usable manifest. Real Helm is exercised by TestInfrastructureAndBlockYAML.
func TestControllerRenderFailures(t *testing.T) {
	for _, controller := range []struct{ name, version, url string }{
		{"keda", "2.21.0", "https://kedacore.github.io/charts/keda-2.21.0.tgz"},
		{"kyverno", "3.9.2", "https://kyverno.github.io/kyverno/kyverno-3.9.2.tgz"},
	} {
		t.Run(controller.name, func(t *testing.T) {
			for _, scenario := range []string{"tampered cache", "tampered download", "download failure", "template failure", "fresh download"} {
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
					archive := controller.name + "-" + controller.version + ".tgz"
					source := filepath.Join(repo, ".local/charts", archive)
					if _, err := os.Stat(source); os.IsNotExist(err) {
						cmd := exec.Command("bash", "scripts/render-controller.sh", controller.name)
						cmd.Dir = repo
						out, err := cmd.CombinedOutput()
						g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
					}
					// A cached mismatch must fail without attempting a download.
					curl := "exit 42\n"
					switch scenario {
					case "tampered cache":
						g.Expect(os.WriteFile(filepath.Join(cache, archive), []byte("tampered"), 0600)).To(gomega.Succeed())
						curl = "touch download-attempted; exit 42\n"
					case "tampered download", "fresh download":
						// Check exact upstream URL and finite bounds at the network boundary.
						curl = `[[ $* == *'--connect-timeout 15 --max-time 120 --retry 2 --retry-max-time 240 ` + controller.url + ` -o '* ]] || exit 91
while [[ $1 != -o ]]; do shift; done
`
						if scenario == "tampered download" {
							curl += `printf tampered > "$2"`
						} else {
							curl += `cp "` + source + `" "$2"`
						}
					case "template failure":
						b, err := os.ReadFile(source)
						g.Expect(err).NotTo(gomega.HaveOccurred())
						g.Expect(os.WriteFile(filepath.Join(cache, archive), b, 0600)).To(gomega.Succeed())
						g.Expect(os.WriteFile(filepath.Join(tools, "helm"), []byte("#!/bin/sh\nprintf 'apiVersion: v1\\nkind: ConfigMap\\nmetadata:\\n  name: partial-render\\n'\nexit 43\n"), 0755)).To(gomega.Succeed())
					}
					g.Expect(os.WriteFile(filepath.Join(tools, "curl"), []byte("#!/usr/bin/env bash\nset -euo pipefail\n"+curl+"\n"), 0755)).To(gomega.Succeed())
					cmd := exec.Command("bash", filepath.Join(repo, "scripts/render-controller.sh"), controller.name)
					cmd.Dir = root
					cmd.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"))
					out, err := cmd.Output()
					if scenario == "fresh download" {
						g.Expect(err).NotTo(gomega.HaveOccurred())
						g.Expect(string(out)).To(gomega.ContainSubstring("kind: CustomResourceDefinition"))
					} else {
						g.Expect(err).To(gomega.HaveOccurred())
						g.Expect(out).To(gomega.BeEmpty(), "failed rendering must emit no deployable data")
					}
					_, err = os.Stat(filepath.Join(root, "download-attempted"))
					g.Expect(os.IsNotExist(err)).To(gomega.BeTrue(), "corrupt cache must not be silently replaced")
					if scenario == "tampered download" || scenario == "download failure" {
						_, err = os.Stat(filepath.Join(cache, archive))
						g.Expect(os.IsNotExist(err)).To(gomega.BeTrue(), "failed downloads must not enter cache")
					}
				})
			}
		})
	}
}

func TestControllerRenderRejectsSelector(t *testing.T) {
	for _, args := range [][]string{nil, {"other"}, {"keda", "extra"}} {
		t.Run("selector="+filepath.Join(args...), func(t *testing.T) {
			g := gomega.NewWithT(t)
			repo, err := filepath.Abs("../..")
			g.Expect(err).NotTo(gomega.HaveOccurred())
			// No k8s directory or tools: argument validation must precede all IO.
			cmd := exec.Command("bash", append([]string{filepath.Join(repo, "scripts/render-controller.sh")}, args...)...)
			cmd.Dir = t.TempDir()
			out, err := cmd.Output()
			g.Expect(err).To(gomega.HaveOccurred())
			g.Expect(out).To(gomega.BeEmpty())
			g.Expect(string(err.(*exec.ExitError).Stderr)).To(gomega.ContainSubstring("Usage:"))
		})
	}
}

package chart

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/manifest"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/onsi/gomega"
)

// Real script + real Helm render, with only external runtime/build boundaries
// replaced. Reordering waits, rotating an existing Secret, applying a different
// render, or continuing after failed schemas must be observable failures.
func TestUpRerunAndBootstrapOrdering(t *testing.T) {
	// Populate/verify the real archive even when this test runs alone on CI.
	render := exec.Command("bash", "scripts/render-kyverno.sh")
	render.Dir = "../.."
	out, err := render.CombinedOutput()
	gomega.NewWithT(t).Expect(err).NotTo(gomega.HaveOccurred(), string(out))
	for _, scenario := range []string{"rerun", "unpaired storage", "failed schema", "template failure", "download failure", "tampered cache", "invalid Kyverno schema"} {
		t.Run(scenario, func(t *testing.T) {
			g := gomega.NewWithT(t)
			root := t.TempDir()
			for _, dir := range []string{"scripts", "tools", ".local"} {
				g.Expect(os.MkdirAll(filepath.Join(root, dir), 0700)).To(gomega.Succeed())
			}
			for _, path := range []string{"scripts/k8s-up.sh", "scripts/render-kyverno.sh", "scripts/kind-common.sh", "scripts/validate-manifests.sh", "scripts/kind-diagnostics.sh", "scripts/webhooks-ready.jq"} {
				b, err := os.ReadFile("../../" + path)
				g.Expect(err).NotTo(gomega.HaveOccurred(), "direct rerunnable up entrypoint")
				g.Expect(os.WriteFile(filepath.Join(root, path), b, 0600)).To(gomega.Succeed())
			}
			repo, err := filepath.Abs("../..")
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(os.Symlink(filepath.Join(repo, "k8s"), filepath.Join(root, "k8s"))).To(gomega.Succeed())
			g.Expect(os.WriteFile(filepath.Join(root, ".local/kubeconfig"), []byte("fixture"), 0600)).To(gomega.Succeed())
			cache := filepath.Join(root, ".local/charts")
			g.Expect(os.MkdirAll(cache, 0700)).To(gomega.Succeed())
			if scenario != "download failure" {
				b, err := os.ReadFile(filepath.Join(repo, ".local/charts/kyverno-3.9.2.tgz"))
				g.Expect(err).NotTo(gomega.HaveOccurred())
				if scenario == "tampered cache" {
					b = []byte("tampered")
				}
				g.Expect(os.WriteFile(filepath.Join(cache, "kyverno-3.9.2.tgz"), b, 0600)).To(gomega.Succeed())
			}
			realHelm, err := exec.LookPath("helm")
			g.Expect(err).NotTo(gomega.HaveOccurred())
			tools := map[string]string{
				"curl": `exit 42`,
				"helm": `if [[ $SCENARIO == 'template failure' && $* == *'template kyverno'* ]]; then exit 43; fi
exec "` + realHelm + `" "$@"`,
				"git":    `case "$1" in rev-parse) echo abc123456789;; status) :;; *) exit 91;; esac`,
				"docker": `[[ $1 == build ]] || exit 92; echo build >> "$STATE/calls"`,
				"kind":   `case "$1 $2" in 'get clusters') echo temporal-demo;; 'load docker-image') echo load >> "$STATE/calls";; *) exit 93;; esac`,
				"go":     `cat > "$STATE/graph.yaml"`,
				"kubeconform": `for arg in "$@"; do
if [[ $arg == */kyverno.yaml ]]; then
 [[ $SCENARIO != 'invalid Kyverno schema' ]] || exit 44
 cp "$arg" "$STATE/validated-kyverno.yaml"; echo validate-kyverno >> "$STATE/calls"; fi
if [[ $arg == */apps.yaml ]]; then cmp "$arg" "$STATE/graph.yaml"; cp "$arg" "$STATE/validated.yaml"; echo validate-apps >> "$STATE/calls"; fi; done`,
				"kubectl": `while [[ $1 == --* ]]; do case "$1" in --*=*) shift;; *) shift 2;; esac; done
if [[ $1 == config ]]; then echo kind-temporal-demo; exit; fi
printf '%s\n' "$*" >> "$STATE/calls"
if [[ $1 == get && $* == *validatingwebhookconfigurations* ]]; then
 printf '{"items":['
 sep=''
 for name in keda-admission kyverno-policy-validating-webhook-cfg kyverno-policy-mutating-webhook-cfg kyverno-verify-mutating-webhook-cfg kyverno-cleanup-validating-webhook-cfg; do printf '%s{"metadata":{"name":"%s"},"webhooks":[{"clientConfig":{"caBundle":"cert"}}]}' "$sep" "$name"; sep=,; done
 printf ']}'; exit
fi
if [[ $* == *'get secret postgres-credentials'* ]]; then
 if [[ $* == *'-o json' ]]; then echo '{"immutable":true,"data":{"POSTGRES_PASSWORD":"fixture"}}'; else [[ $SCENARIO == 'unpaired storage' ]] || echo secret/postgres-credentials; fi; exit 0
fi
if [[ $* == *'get pvc data-postgres-0'* ]]; then echo persistentvolumeclaim/data-postgres-0; exit; fi
if [[ $SCENARIO == 'failed schema' && $* == *'wait --for=condition=complete job/temporal-schema'* ]]; then exit 42; fi
if [[ $1 == apply ]]; then
 for arg in "$@"; do if [[ $arg == */kyverno.yaml ]]; then
  cmp "$arg" "$STATE/validated-kyverno.yaml"
  [[ $* != *--force-conflicts* ]] || exit 94
  case "$*" in
   *' -l temporal-demo.io/phase=namespace') echo apply-kyverno-namespace >> "$STATE/calls";;
   *'--server-side --field-manager=temporal-demo'*' -l temporal-demo.io/phase=crds') echo apply-kyverno-crds >> "$STATE/calls";;
   *' -l temporal-demo.io/phase notin (crds,namespace)') [[ $* != *--server-side* ]] || exit 95; echo apply-kyverno-controllers >> "$STATE/calls";;
   *) exit 96;;
  esac
 fi; done
 for arg in "$@"; do if [[ $arg == */apps.yaml ]]; then cmp "$arg" "$STATE/validated.yaml"; cp "$arg" "$STATE/applied.yaml"; echo apply-apps >> "$STATE/calls"; fi; done
fi`,
			}
			for name, body := range tools {
				g.Expect(os.WriteFile(filepath.Join(root, "tools", name), []byte("#!/usr/bin/env bash\nset -euo pipefail\n"+body+"\n"), 0755)).To(gomega.Succeed())
			}
			run := func() ([]byte, error) {
				cmd := exec.Command("bash", filepath.Join(root, "scripts/k8s-up.sh"))
				cmd.Env = append(os.Environ(), "KUBECONFIG=", "STATE="+root, "SCENARIO="+scenario, "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
				return cmd.CombinedOutput()
			}
			out, err := run()
			calls, _ := os.ReadFile(filepath.Join(root, "calls"))
			text := string(calls)
			if scenario == "template failure" || scenario == "download failure" || scenario == "tampered cache" || scenario == "invalid Kyverno schema" {
				g.Expect(err).To(gomega.HaveOccurred(), string(out))
				g.Expect(text).NotTo(gomega.ContainSubstring("apply "), "render failure must stop deployment")
				return
			}
			if scenario == "unpaired storage" {
				g.Expect(err).To(gomega.HaveOccurred(), string(out))
				g.Expect(string(out)).To(gomega.ContainSubstring("PVC has no paired Secret"))
				g.Expect(text).NotTo(gomega.ContainSubstring("create secret"))
				g.Expect(text).NotTo(gomega.ContainSubstring("phase=postgres"))
				return
			}
			if scenario == "failed schema" {
				g.Expect(err).To(gomega.HaveOccurred(), string(out))
				g.Expect(text).NotTo(gomega.ContainSubstring("phase=server"))
				g.Expect(text).NotTo(gomega.ContainSubstring("build\n"))
				return
			}
			g.Expect(err).NotTo(gomega.HaveOccurred(), string(out)+"\n"+text)
			out, err = run()
			g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
			calls, _ = os.ReadFile(filepath.Join(root, "calls"))
			text = string(calls)
			g.Expect(text).NotTo(gomega.ContainSubstring("create secret"))
			g.Expect(text).NotTo(gomega.ContainSubstring("delete cluster"))
			stages := []string{"validate-kyverno", "apply-kyverno-namespace", "phase=postgres", "rollout status statefulset/postgres", "phase=schema", "wait --for=condition=complete job/temporal-schema", "phase=server", "rollout status deployment/temporal ", "phase=namespace-job", "wait --for=condition=complete job/temporal-namespace", "phase=ui", "apply-kyverno-crds", "wait --for=condition=Established", "phase=controller", "apply-kyverno-controllers", "build\n", "load\n", "validate-apps", "apply-apps", "rollout status deployment/orchestrator"}
			pos := 0
			for _, stage := range stages {
				idx := strings.Index(text[pos:], stage)
				g.Expect(idx).To(gomega.BeNumerically(">=", 0), "missing/out of order: "+stage+"\n"+text)
				pos += idx + len(stage)
			}
			applied, err := os.ReadFile(filepath.Join(root, "applied.yaml"))
			g.Expect(err).NotTo(gomega.HaveOccurred())
			regs, err := manifest.Registrations(bytes.NewReader(applied))
			g.Expect(err).NotTo(gomega.HaveOccurred())
			p, err := plan.Build(regs)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(p.Hash).To(gomega.Equal("5fefcdcc52af25c9c21a523f1f3c1f7d928018051ffe89ac8b5a0e6ccf8c3b62"))
		})
	}
}

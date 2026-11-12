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
// render, or continuing after failed server validation must be observable failures.
func TestUpRerunAndBootstrapOrdering(t *testing.T) {
	// Populate/verify the real archive even when this test runs alone on CI.
	for _, controller := range []string{"keda", "kyverno"} {
		render := exec.Command("bash", "scripts/render-controller.sh", controller)
		render.Dir = "../.."
		out, err := render.CombinedOutput()
		gomega.NewWithT(t).Expect(err).NotTo(gomega.HaveOccurred(), string(out))
	}
	for _, controller := range []string{"keda", "kyverno"} {
		for _, scenario := range []string{"rerun", "full-retry", "unpaired storage", "failed schema", "template failure", "download failure", "tampered cache", "failed validation"} {
			t.Run(controller+"/"+scenario, func(t *testing.T) {
				g := gomega.NewWithT(t)
				root := t.TempDir()
				for _, dir := range []string{"scripts", "tools", ".local"} {
					g.Expect(os.MkdirAll(filepath.Join(root, dir), 0700)).To(gomega.Succeed())
				}
				for _, path := range []string{"scripts/k8s-up.sh", "scripts/local-output.sh", "scripts/render-controller.sh", "scripts/kind-common.sh", "scripts/validate-k8s.sh", "scripts/kind-diagnostics.sh", "scripts/webhooks-ready.jq", "scripts/policies-install.sh", "scripts/expected-workers.jq"} {
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
				for name, archive := range map[string]string{"keda": "keda-2.21.0.tgz", "kyverno": "kyverno-3.9.2.tgz"} {
					if name == controller && scenario == "download failure" {
						continue
					}
					b, err := os.ReadFile(filepath.Join(repo, ".local/charts", archive))
					g.Expect(err).NotTo(gomega.HaveOccurred())
					if name == controller && scenario == "tampered cache" {
						b = []byte("tampered")
					}
					g.Expect(os.WriteFile(filepath.Join(cache, archive), b, 0600)).To(gomega.Succeed())
				}
				realHelm, err := exec.LookPath("helm")
				g.Expect(err).NotTo(gomega.HaveOccurred())
				tools := map[string]string{
					"curl": `exit 42`,
					"helm": `if [[ $* == *'template temporal-demo '* ]]; then echo render-apps >> "$STATE/calls"; fi
if [[ $SCENARIO == 'template failure' && $* == *"template $CONTROLLER"* ]]; then exit 43; fi
exec "` + realHelm + `" "$@"`,
					"git":         `case "$1" in rev-parse) echo abc123456789;; status) :;; *) exit 91;; esac`,
					"docker":      `[[ $1 == build ]] || exit 92; echo build >> "$STATE/calls"`,
					"kind":        `case "$1 $2" in 'get clusters') echo temporal-demo;; 'load docker-image') echo load >> "$STATE/calls";; *) exit 93;; esac`,
					"go":          `echo 'runtime graph CLI is forbidden' >&2; exit 99`,
					"kubeconform": `echo 'offline validator must not run' >&2; exit 99`,
					"kubectl": `[[ $* == *'--kubeconfig '* && $* == *'--context kind-temporal-demo'* || $* == *'config current-context'* ]] || exit 97
while [[ $1 == --* ]]; do case "$1" in --*=*) shift;; *) shift 2;; esac; done
if [[ $1 == config ]]; then echo kind-temporal-demo; exit; fi
if [[ $1 == auth ]]; then echo yes; exit; fi
if [[ $1 == create && $* == *'--dry-run=client'* ]]; then
 for w in worker-a worker-b worker-c worker-d; do
  printf '{"kind":"Namespace","metadata":{"name":"temporal-demo-%s","labels":{"temporal-demo.io/worker-namespace":"true"}}}\n' "$w"
  printf '{"kind":"Service","metadata":{"name":"%s","namespace":"temporal-demo-%s","annotations":{"temporal-worker":"true"}}}\n' "$w" "$w"
 done
 exit
fi
if [[ $1 == get && $2 == deployments ]]; then
 if [[ $SCENARIO == full-retry ]]; then echo '{"items":[{"metadata":{"name":"worker-a","namespace":"temporal-demo-worker-a"}}]}'; else echo '{"items":[]}'; fi; exit
fi
if [[ $SCENARIO == full-retry && $* == *'get deployment worker-'* ]]; then
 echo '{"metadata":{"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{\"spec\":{\"replicas\":0}}"}}}'; exit
fi
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
 for name in keda kyverno; do
 for arg in "$@"; do if [[ $arg == */$name.yaml ]]; then
  [[ $* != *--force-conflicts* ]] || exit 94
  case "$*" in
   *' -l temporal-demo.io/phase=namespace') echo apply-$name-namespace >> "$STATE/calls";;
   *'--server-side --field-manager=temporal-demo'*' -l temporal-demo.io/phase=crds') echo apply-$name-crds >> "$STATE/calls";;
   *' -l app.kubernetes.io/part-of=keda-operator,!app.kubernetes.io/instance') [[ $* == *'--server-side --field-manager=temporal-demo'* ]] || exit 95; echo apply-keda-crds >> "$STATE/calls";;
   *' -l temporal-demo.io/phase=controller'|*' -l temporal-demo.io/phase notin (crds,namespace)') [[ $* != *--server-side* ]] || exit 95; echo apply-$name-controllers >> "$STATE/calls";;
   *) exit 96;;
  esac
 fi; done
 done
 for arg in "$@"; do if [[ $arg == */apps.yaml ]]; then
  if [[ $* == *'-l temporal-demo.io/phase=namespace'* ]]; then
   echo bootstrap-apps >> "$STATE/calls"
  elif [[ $* == *'--dry-run=server'* ]]; then
   [[ $* == *'--validate=strict'* ]] || exit 98
   [[ $SCENARIO != 'failed validation' ]] || exit 44
   cp "$arg" "$STATE/validated.yaml"; echo validate-apps >> "$STATE/calls"
  else
   cmp "$arg" "$STATE/validated.yaml"; cp "$arg" "$STATE/applied.yaml"; echo apply-apps >> "$STATE/calls"
  fi
 fi; done
fi`,
				}
				for name, body := range tools {
					g.Expect(os.WriteFile(filepath.Join(root, "tools", name), []byte("#!/usr/bin/env bash\nset -euo pipefail\n"+body+"\n"), 0755)).To(gomega.Succeed())
				}
				run := func() ([]byte, error) {
					args := []string{filepath.Join(root, "scripts/k8s-up.sh")}
					cmd := exec.Command("bash", args...)
					cmd.Env = append(os.Environ(), "KUBECONFIG=", "STATE="+root, "SCENARIO="+scenario, "CONTROLLER="+controller, "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
					return cmd.CombinedOutput()
				}
				out, err := run()
				calls, _ := os.ReadFile(filepath.Join(root, "calls"))
				text := string(calls)
				if scenario == "template failure" || scenario == "download failure" || scenario == "tampered cache" {
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
				if scenario == "failed validation" {
					g.Expect(err).To(gomega.HaveOccurred(), string(out))
					g.Expect(text).NotTo(gomega.ContainSubstring("apply-apps"))
					return
				}
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(out)+"\n"+text)
				out, err = run()
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
				calls, _ = os.ReadFile(filepath.Join(root, "calls"))
				text = string(calls)
				if scenario == "full-retry" {
					annotation := strings.Index(text, "annotate deployment worker-a")
					g.Expect(annotation).To(gomega.BeNumerically(">=", 0))
					g.Expect(annotation).To(gomega.BeNumerically("<", strings.Index(text, "apply-apps")))
				}
				g.Expect(text).To(gomega.ContainSubstring("apply set-last-applied"))
				g.Expect(text).To(gomega.ContainSubstring("wait --for=condition=Ready scaledobject/worker-a"))
				g.Expect(text).NotTo(gomega.ContainSubstring("rollout status deployment/worker-a"))
				g.Expect(strings.Count(text, "render-apps\n")).To(gomega.Equal(4))
				g.Expect(text).NotTo(gomega.ContainSubstring("create secret"))
				g.Expect(text).NotTo(gomega.ContainSubstring("delete cluster"))
				stages := []string{"apply-keda-namespace", "apply-kyverno-namespace", "phase=postgres", "rollout status statefulset/postgres", "phase=schema", "wait --for=condition=complete job/temporal-schema", "phase=server", "rollout status deployment/temporal ", "phase=namespace-job", "wait --for=condition=complete job/temporal-namespace", "phase=ui", "apply-keda-crds", "apply-kyverno-crds", "wait --for=condition=Established crd -l app.kubernetes.io/part-of=keda-operator,!app.kubernetes.io/instance", "wait --for=condition=Established crd -l temporal-demo.io/phase=crds", "apply-keda-controllers", "apply-kyverno-controllers", "rollout status deployment/keda-admission-webhooks", "rollout status deployment/kyverno-reports-controller", "get validatingwebhookconfigurations,mutatingwebhookconfigurations", "build\n", "load\n", "bootstrap-apps", "validate-apps", "apply-apps", "rollout status deployment/orchestrator"}
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
}

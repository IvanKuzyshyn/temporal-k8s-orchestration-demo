package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const validatingWorkerPolicy = "validatingpolicy.policies.kyverno.io/temporal-validate-workers"
const generatingWorkerPolicy = "generatingpolicy.policies.kyverno.io/temporal-generate-registrations"

// Exercise the installer, not its source text: a wrong kind must miss the
// collision, and an apply before preflight must appear in the mutation log.
func runPolicyInstall(t *testing.T, foreign string) (string, string, error) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"scripts", "tools", ".local"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"policies-install.sh", "kind-common.sh"} {
		b, err := os.ReadFile("../../scripts/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, "scripts", name), b, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".local/kubeconfig"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	stub := `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$STATE/calls"
if [[ $* == *'config current-context'* ]]; then echo kind-temporal-demo; exit; fi
shift 5
case "$1" in
 get)
  if [[ $2 == "$FOREIGN" ]]; then echo '{"metadata":{"labels":{"app.kubernetes.io/part-of":"someone-else"}}}';
  else echo '{"metadata":{"labels":{"app.kubernetes.io/part-of":"temporal-worker-discovery"}}}'; fi;;
 auth) echo yes;;
 wait) [[ $* == *'--for=jsonpath={.status.conditionStatus.ready}=true'* ]];;
 apply|rollout|-n) :;;
 *) exit 91;;
esac
`
	if err := os.WriteFile(filepath.Join(root, "tools/kubectl"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "scripts/policies-install.sh"))
	cmd.Env = append(os.Environ(), "KUBECONFIG=", "STATE="+root, "FOREIGN="+foreign, "PATH="+filepath.Join(root, "tools")+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	calls, readErr := os.ReadFile(filepath.Join(root, "calls"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(out), string(calls), err
}

func TestPoliciesInstallCELResources(t *testing.T) {
	out, calls, err := runPolicyInstall(t, "")
	if err != nil {
		t.Fatalf("install: %v\n%s\n%s", err, out, calls)
	}
	for _, policy := range []string{validatingWorkerPolicy, generatingWorkerPolicy} {
		if !strings.Contains(calls, "get "+policy+" --ignore-not-found -o json") || !strings.Contains(calls, "wait --for=jsonpath={.status.conditionStatus.ready}=true "+validatingWorkerPolicy+" "+generatingWorkerPolicy+" --timeout=90s") {
			t.Fatalf("missing collision/readiness: %s", calls)
		}
	}
	for _, name := range []string{"rbac", "validate-workers", "generate-registrations"} {
		dry := strings.Index(calls, "apply --dry-run=server --validate=strict -f k8s/policies/"+name+".yaml")
		apply := strings.Index(calls, "apply -f k8s/policies/"+name+".yaml")
		if dry < 0 || apply <= dry {
			t.Fatalf("missing ordered dry-run/apply for %s: %s", name, calls)
		}
	}
}

func TestPoliciesInstallRefusesForeignResources(t *testing.T) {
	for _, resource := range []string{validatingWorkerPolicy, generatingWorkerPolicy, "clusterrole/temporal-worker-service-reader"} {
		t.Run(resource, func(t *testing.T) {
			out, calls, err := runPolicyInstall(t, resource)
			if err == nil || !strings.Contains(out, "Refusing foreign "+resource) || strings.Contains(calls, " apply ") {
				t.Fatalf("unsafe collision handling: %v\n%s\n%s", err, out, calls)
			}
		})
	}
}

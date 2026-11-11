package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Catch recording nohup's transient command rather than the final tunnel
// identity, resetting workloads on resume, and claiming ready on bind failure.
func TestLocalResumeTunnels(t *testing.T) {
	for _, scenario := range []string{"ready", "occupied"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"scripts", "tools", ".local/lifecycle", "k8s/infrastructure"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"local.sh", "kind-common.sh", "wait-graph.sh"} {
				b, err := os.ReadFile("../../scripts/" + name)
				if err != nil {
					t.Fatal(err)
				}
				write("scripts/"+name, string(b))
			}
			for name, body := range map[string]string{"node-id": "owned-id\n", "initialized": "", "expected-workers.json": `[{"name":"worker-a","taskQueue":"worker-a.v1.temporal","workflowType":"ExampleChildWorkflow","dependencies":[]}]`} {
				write(".local/lifecycle/"+name, body)
			}
			write(".local/kubeconfig", "fixture")
			write("k8s/infrastructure/versions.env", "KIND_NODE_IMAGE=fixture\n")
			write("tools/docker", `#!/usr/bin/env bash
set -eu
echo "docker $*" >> "$STATE/calls"
case "$1" in
ps) echo temporal-demo-control-plane;;
inspect) echo '[{"Id":"owned-id","Config":{"Labels":{"io.x-k8s.kind.cluster":"temporal-demo"}},"State":{"Running":false}}]';;
start|stop) [[ $2 == owned-id ]];;
*) exit 91;;
esac
`)
			write("tools/nohup", `#!/usr/bin/env bash
sleep 0.3
exec "$@"
`)
			write("tools/kubectl", `#!/usr/bin/env bash
set -eu
echo "kubectl $*" >> "$STATE/calls"
if [[ $* == *port-forward* ]]; then
 [[ $SCENARIO != occupied ]] || exit 1
 echo "Forwarding from 127.0.0.1:18080 -> 8080"
 trap 'exit 0' TERM
 while true; do sleep 0.1; done
elif [[ $* == *'config current-context'* ]]; then echo kind-temporal-demo
elif [[ $* == *apply* ]]; then exit 92
fi
`)
			write("tools/curl", `#!/usr/bin/env bash
if [[ $* == */graph ]]; then
 while (($#)); do case "$1" in -o) file=$2; shift 2;; *) shift;; esac; done
 jq -n --slurpfile nodes "$STATE/.local/lifecycle/expected-workers.json" '{synced:true,valid:true,plan:{nodes:$nodes[0]},problems:[]}' > "$file"
 printf 200
else echo ok; fi
`)
			run := func(mode string) (string, error) {
				cmd := exec.Command("bash", filepath.Join(root, "scripts/local.sh"), mode)
				cmd.Env = append(os.Environ(), "KUBECONFIG=", "STATE="+root, "SCENARIO="+scenario, "PATH="+filepath.Join(root, "tools")+":"+os.Getenv("PATH"))
				out, err := cmd.CombinedOutput()
				return string(out), err
			}
			t.Cleanup(func() {
				for _, name := range []string{"api", "ui"} {
					b, _ := os.ReadFile(filepath.Join(root, ".local/lifecycle", name+".pid"))
					if len(b) > 0 {
						_ = exec.Command("kill", strings.TrimSpace(string(b))).Run()
					}
				}
			})
			out, err := run("up")
			if scenario == "occupied" {
				if err == nil || strings.Contains(out, "Temporal UI:") {
					t.Fatalf("false success: %v %s", err, out)
				}
				return
			}
			if err != nil || !strings.Contains(out, "Temporal UI: http://127.0.0.1:18088") {
				t.Fatalf("up: %v %s", err, out)
			}
			// Repeat up must stop exactly its previous tunnels, not fail on a nohup PID.
			out, err = run("up")
			if err != nil {
				t.Fatalf("repeat up: %v %s", err, out)
			}
			out, err = run("down")
			if err != nil {
				t.Fatalf("down: %v %s", err, out)
			}
			calls, _ := os.ReadFile(filepath.Join(root, "calls"))
			if !strings.Contains(string(calls), "docker start owned-id") {
				t.Fatal("did not resume stopped node")
			}
			for _, name := range []string{"api", "ui"} {
				if _, err := os.Stat(filepath.Join(root, ".local/lifecycle", name+".pid")); !os.IsNotExist(err) {
					t.Fatal("retained tunnel record")
				}
			}
		})
	}
}

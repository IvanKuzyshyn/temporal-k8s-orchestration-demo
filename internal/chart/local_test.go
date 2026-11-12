package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// External Docker/kind boundaries are simulated; the real lifecycle script must
// choose the safe operation and preserve local data on down.
func TestLocalLifecycleOwnership(t *testing.T) {
	for _, scenario := range []string{"down", "destroy", "foreign", "replaced", "absent", "unowned", "locked", "reused-pid", "create-failure"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"scripts", "tools", ".local/lifecycle", "k8s/infrastructure"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, file := range []string{"local.sh", "local-output.sh", "kind-common.sh"} {
				b, err := os.ReadFile("../../scripts/" + file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "scripts", file), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write(".local/kubeconfig", "fixture")
			write("k8s/infrastructure/versions.env", "KIND_NODE_IMAGE=fixture\n")
			if scenario != "unowned" && scenario != "absent" && scenario != "create-failure" {
				write(".local/lifecycle/node-id", "owned-id\n")
			}
			if scenario == "reused-pid" {
				write(".local/lifecycle/api.pid", strconv.Itoa(os.Getpid())+"\n")
				write(".local/lifecycle/api.identity", "not this process\n")
			}
			if scenario == "locked" {
				if err := os.Mkdir(filepath.Join(root, ".local/lifecycle.lock"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write("tools/docker", `#!/usr/bin/env bash
set -eu
printf '%s\n' "docker $*" >> "$STATE/calls"
case "$1" in
 ps) [[ $SCENARIO == absent || $SCENARIO == create-failure ]] || echo temporal-demo-control-plane;;
 inspect)
  id=owned-id; label=temporal-demo
  [[ $SCENARIO != replaced ]] || id=replaced-id
  [[ $SCENARIO != foreign ]] || label=foreign
  printf '[{"Id":"%s","Config":{"Labels":{"io.x-k8s.kind.cluster":"%s"}},"State":{"Running":true}}]\n' "$id" "$label";;
 stop) [[ $2 == owned-id ]];;
 *) exit 90;;
esac
`)
			write("tools/kind", "#!/usr/bin/env bash\nprintf '%s\\n' \"kind $*\" >> \"$STATE/calls\"\n[[ $SCENARIO != create-failure ]]\n")
			write("tools/kubectl", "#!/usr/bin/env bash\necho kind-temporal-demo\n")
			mode := "down"
			if scenario == "create-failure" {
				mode = "up"
			}
			if scenario == "destroy" {
				mode = "destroy"
			}
			cmd := exec.Command("bash", filepath.Join(root, "scripts/local.sh"), mode)
			cmd.Env = append(os.Environ(), "KUBECONFIG=", "STATE="+root, "SCENARIO="+scenario, "PATH="+filepath.Join(root, "tools")+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			reject := scenario == "foreign" || scenario == "replaced" || scenario == "unowned" || scenario == "locked" || scenario == "reused-pid" || scenario == "create-failure"
			if (err != nil) != reject {
				t.Fatalf("unexpected result %v: %s", err, out)
			}
			b, _ := os.ReadFile(filepath.Join(root, "calls"))
			calls := string(b)
			if reject && (strings.Contains(calls, "docker stop") || strings.Contains(calls, "kind delete")) {
				t.Fatal("mutated unverified cluster")
			}
			if scenario == "create-failure" {
				if _, err := os.Stat(filepath.Join(root, ".local/lifecycle/node-id")); !os.IsNotExist(err) {
					t.Fatal("claimed ownership after failed create")
				}
			}
			if scenario == "down" {
				if !strings.Contains(calls, "docker stop owned-id") || strings.Contains(calls, "delete") {
					t.Fatalf("unsafe down: %s", calls)
				}
				if _, err := os.Stat(filepath.Join(root, ".local/kubeconfig")); err != nil {
					t.Fatal("down removed kubeconfig")
				}
			}
			if scenario == "destroy" {
				if !strings.Contains(calls, "kind delete cluster --name temporal-demo --kubeconfig ") {
					t.Fatalf("missing scoped delete: %s", calls)
				}
				if _, err := os.Stat(filepath.Join(root, ".local/lifecycle/node-id")); !os.IsNotExist(err) {
					t.Fatal("destroy retained ownership")
				}
			}
		})
	}
}

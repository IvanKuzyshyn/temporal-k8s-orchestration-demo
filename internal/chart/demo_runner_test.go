package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoRunnerOwnership(t *testing.T) {
	for _, scenario := range []string{"existing", "docker-error", "stale", "success", "test-failure", "create-failure"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			for _, d := range []string{"scripts", "tools", ".local"} {
				if e := os.Mkdir(filepath.Join(root, d), 0700); e != nil {
					t.Fatal(e)
				}
			}
			write := func(p, s string) {
				t.Helper()
				if e := os.WriteFile(filepath.Join(root, p), []byte(s), 0700); e != nil {
					t.Fatal(e)
				}
			}
			for _, s := range []string{"test-demo.sh", "kind-common.sh"} {
				b, e := os.ReadFile("../../scripts/" + s)
				if e != nil {
					t.Fatal(e)
				}
				write("scripts/"+s, string(b))
			}
			if scenario == "stale" {
				write(".local/kubeconfig", "owned elsewhere")
			}
			write("tools/docker", `#!/bin/bash
[[ $SCENARIO != docker-error ]] || exit 1
[[ $SCENARIO != existing ]] || echo temporal-demo-control-plane
`)
			write("tools/mise", `#!/bin/bash
set -eu
echo "$*" >> "$STATE/calls"
if [[ $* == 'run up' ]]; then
 [[ $SCENARIO != create-failure ]] || exit 1
 mkdir -p "$STATE/.local/lifecycle"; echo owned > "$STATE/.local/lifecycle/node-id"
fi
`)
			write("tools/go", `#!/bin/bash
echo tests >> "$STATE/calls"
[[ $SCENARIO != test-failure ]]
`)
			write("scripts/kind-diagnostics.sh", "echo diagnostics >> \"$STATE/calls\"\n")
			cmd := exec.Command("bash", filepath.Join(root, "scripts/test-demo.sh"))
			cmd.Env = append(os.Environ(), "KUBECONFIG=", "STATE="+root, "SCENARIO="+scenario, "PATH="+filepath.Join(root, "tools")+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if (err == nil) != (scenario == "success") {
				t.Fatalf("%v %s", err, out)
			}
			b, _ := os.ReadFile(filepath.Join(root, "calls"))
			s := string(b)
			if scenario == "existing" || scenario == "docker-error" || scenario == "stale" {
				if s != "" {
					t.Fatalf("preflight mutated: %s", s)
				}
			}
			if scenario == "create-failure" && strings.Contains(s, "destroy") {
				t.Fatal("destroyed after failed unowned creation")
			}
			if scenario == "success" || scenario == "test-failure" {
				if !strings.Contains(s, "run destroy") {
					t.Fatal("no scoped cleanup")
				}
			}
			if scenario == "test-failure" && !strings.Contains(s, "diagnostics") {
				t.Fatal("no failure diagnostics")
			}
		})
	}
}

package chart

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/manifest"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"go.yaml.in/yaml/v3"
	"io"
)

func TestDemoGraphProjection(t *testing.T) {
	b, err := render(t)
	if err != nil {
		t.Fatal(err, string(b))
	}
	regs, err := manifest.Registrations(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Build(regs)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Nodes) != 4 || p.Nodes[3].Name != "worker-d" || !reflect.DeepEqual(p.Nodes[3].Dependencies, []string{"worker-b", "worker-c"}) {
		t.Fatal("not canonical diamond")
	}
	var stream bytes.Buffer
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var obj map[string]any
		err := dec.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if obj != nil {
			if obj["kind"] == "Service" {
				meta := obj["metadata"].(map[string]any)
				if meta["name"] == "worker-d" {
					meta["annotations"].(map[string]any)["temporal-worker-run-after"] = "worker-c,\u0085 worker-b , worker-b"
				}
			}
			if err := json.NewEncoder(&stream).Encode(obj); err != nil {
				t.Fatal(err)
			}
		}
	}
	stream.WriteString(`{"kind":"Service","metadata":{"name":"ignored","namespace":"unselected","annotations":{"temporal-worker":"true"}}}`)
	cmd := exec.Command("jq", "-se", "-f", "../../scripts/expected-workers.jq")
	cmd.Stdin = &stream
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("projection %v: %s", err, out)
	}
	var nodes []plan.Node
	if err := json.Unmarshal(out, &nodes); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(nodes, p.Nodes) {
		t.Fatalf("projection mismatch: %s", out)
	}
}

func TestGraphWaitDeadline(t *testing.T) {
	for _, scenario := range []string{"ready", "transient", "partial", "wrong", "timeout", "slow", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			tools := filepath.Join(root, "tools")
			if err := os.Mkdir(tools, 0700); err != nil {
				t.Fatal(err)
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			expected := `[{"name":"worker-a","taskQueue":"worker-a.v1.temporal","workflowType":"ExampleChildWorkflow","dependencies":[]}]`
			if scenario != "missing" {
				write("expected.json", expected)
			}
			write("clock", "0\n")
			write("count", "0\n")
			write("tools/date", "#!/bin/bash\nhead -1 \"$STATE/clock\"\n")
			write("tools/sleep", `#!/bin/bash
set -eu
v=$(head -1 "$STATE/clock"); echo $((v+$1)) > "$STATE/clock"
echo "sleep $1" >> "$STATE/calls"
`)
			write("tools/curl", `#!/bin/bash
set -eu
file=''; limit=''
while (($#)); do case "$1" in -o) file=$2;shift 2;; --max-time) limit=$2;shift 2;; *) shift;; esac; done
now=$(head -1 "$STATE/clock"); n=$(head -1 "$STATE/count"); echo $((n+1)) > "$STATE/count"
echo "request $now $limit" >> "$STATE/calls"
((now < 120 && limit <= 2 && limit <= 120-now)) || exit 99
if [[ $SCENARIO == slow ]]; then echo $((now+limit)) > "$STATE/clock"; echo 'connection failed' >&2; exit 28; fi
if [[ $SCENARIO == timeout || ($SCENARIO == transient && $n == 0) ]]; then echo '{"synced":false,"valid":false,"problems":["registration unavailable"],"plan":{"nodes":[]}}' > "$file"; printf 503; exit; fi
if [[ $SCENARIO == partial && $n == 0 ]]; then echo '{"synced":true,"valid":true,"problems":[],"plan":{"nodes":[]}}' > "$file"; printf 200; exit; fi
if [[ $SCENARIO == wrong && $n == 0 ]]; then echo '{"synced":true,"valid":true,"problems":[],"plan":{"nodes":[{"name":"worker-a","dependencies":["other"]}]}}' > "$file"; printf 200; exit; fi
jq -n --slurpfile nodes "$STATE/expected.json" '{synced:true,valid:true,problems:[],plan:{nodes:$nodes[0]}}' > "$file"; printf 200
`)
			cmd := exec.Command("bash", "../../scripts/wait-graph.sh", "http://fixture", filepath.Join(root, "expected.json"))
			cmd.Env = append(os.Environ(), "STATE="+root, "SCENARIO="+scenario, "PATH="+tools+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			failure := scenario == "timeout" || scenario == "slow" || scenario == "missing"
			if (err != nil) != failure {
				t.Fatalf("%v: %s", err, out)
			}
			calls, _ := os.ReadFile(filepath.Join(root, "calls"))
			clock, _ := os.ReadFile(filepath.Join(root, "clock"))
			if scenario == "ready" && string(calls) != "request 0 2\n" {
				t.Fatalf("not immediate: %s", calls)
			}
			if scenario == "partial" || scenario == "wrong" || scenario == "transient" {
				if !strings.Contains(string(calls), "sleep 1\nrequest 1 2") {
					t.Fatalf("did not retry: %s", calls)
				}
			}
			if scenario == "timeout" || scenario == "slow" {
				if string(clock) != "120\n" {
					t.Fatalf("unbounded elapsed clock: %s", clock)
				}
				if !strings.Contains(string(out), "worker-a") {
					t.Fatalf("missing diagnostics: %s", out)
				}
			}
		})
	}
}

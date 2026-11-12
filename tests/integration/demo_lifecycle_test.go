//go:build demo_integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/runs"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
)

func TestDemoLifecycle(t *testing.T) {
	root, api := os.Getenv("DEMO_ROOT"), os.Getenv("DEMO_API_URL")
	if root == "" || api == "" {
		t.Fatal("use mise run test:integration on an unused demo cluster")
	}
	command := func(name string, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = root
		b, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, e, b)
		}
		return b
	}
	k := func(args ...string) []byte {
		t.Helper()
		return command("kubectl", append([]string{"--kubeconfig", filepath.Join(root, ".local/kubeconfig"), "--context", "kind-temporal-demo", "--request-timeout=15s"}, args...)...)
	}
	request := func(method, path, key string, body []byte) (int, []byte, error) {
		req, e := http.NewRequest(method, api+path, bytes.NewReader(body))
		if e != nil {
			return 0, nil, e
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		r, e := (&http.Client{Timeout: 3 * time.Second}).Do(req)
		if e != nil {
			return 0, nil, e
		}
		defer r.Body.Close()
		b, e := io.ReadAll(r.Body)
		return r.StatusCode, b, e
	}
	eventually := func(label string, bound time.Duration, f func() bool) {
		t.Helper()
		deadline := time.Now().Add(bound)
		for time.Now().Before(deadline) {
			if f() {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("timed out: %s", label)
	}
	if !t.Run("CELPolicyLifecycle", func(t *testing.T) { demoCheckPolicyLifecycle(t, root) }) {
		t.Fatal("CEL policy lifecycle failed")
	}
	// Fixture cleanup is asynchronous from discovery's perspective.
	command("bash", "scripts/wait-graph.sh", api, filepath.Join(root, ".local/lifecycle/expected-workers.json"))
	p := demoPlan(t)
	status, b, e := request("GET", "/graph", "", nil)
	if e != nil || status != 200 {
		t.Fatalf("graph: %d %s %v", status, b, e)
	}
	var graph struct {
		Synced, Valid bool
		Plan          plan.Plan
	}
	if e = json.Unmarshal(b, &graph); e != nil || !graph.Synced || !graph.Valid || !reflect.DeepEqual(graph.Plan, p) {
		t.Fatalf("graph mismatch: %s", b)
	}
	for _, n := range p.Nodes {
		ns := "temporal-demo-" + n.Name
		uid := string(k("-n", ns, "get", "service", n.Name, "-o", "jsonpath={.metadata.uid}"))
		var cm struct{ Data map[string]string }
		if e := json.Unmarshal(k("-n", ns, "get", "configmap", "temporal-worker-"+n.Name, "-o", "json"), &cm); e != nil {
			t.Fatal(e)
		}
		if cm.Data["sourceServiceUID"] != uid || cm.Data["workerName"] != n.Name || cm.Data["taskQueue"] != n.TaskQueue {
			t.Fatalf("bad generated registration: %+v", cm.Data)
		}
		sa := "system:serviceaccount:temporal-demo-orchestrator:orchestrator"
		if strings.TrimSpace(string(k("auth", "can-i", "list", "configmaps", "-n", ns, "--as="+sa))) != "yes" {
			t.Fatal("missing discovery permission")
		}
	}
	// can-i exits nonzero on deny; inspect its actual exit/output instead of the success helper.
	for _, args := range [][]string{{"get", "secrets", "-n", "temporal"}, {"create", "configmaps", "-n", "temporal-demo-worker-a"}} {
		argv := append([]string{"--kubeconfig", filepath.Join(root, ".local/kubeconfig"), "--context", "kind-temporal-demo", "auth", "can-i"}, args...)
		argv = append(argv, "--as=system:serviceaccount:temporal-demo-orchestrator:orchestrator")
		out, err := exec.Command("kubectl", argv...).CombinedOutput()
		if err == nil || strings.TrimSpace(string(out)) != "no" {
			t.Fatalf("expected deny: %v %s", err, out)
		}
	}
	zero := func() bool {
		for _, n := range p.Nodes {
			ns := "temporal-demo-" + n.Name
			var d struct{ Spec struct{ Replicas int } }
			if e := json.Unmarshal(k("-n", ns, "get", "deployment", n.Name, "-o", "json"), &d); e != nil {
				t.Fatal(e)
			}
			var pods struct{ Items []json.RawMessage }
			if e := json.Unmarshal(k("-n", ns, "get", "pods", "-l", "app="+n.Name, "-o", "json"), &pods); e != nil {
				t.Fatal(e)
			}
			if d.Spec.Replicas != 0 || len(pods.Items) != 0 {
				return false
			}
			k("-n", ns, "wait", "--for=condition=Ready", "scaledobject/"+n.Name, "--timeout=15s")
		}
		return true
	}
	start := func(key string) runs.Run {
		t.Helper()
		code, b, e := request("POST", "/runs", key, nil)
		if e != nil || code != 202 {
			t.Fatalf("start: %d %s %v", code, b, e)
		}
		var r runs.Run
		if e = json.Unmarshal(b, &r); e != nil || r.WorkflowID == "" || r.RunID == "" {
			t.Fatalf("start response: %s", b)
		}
		return r
	}
	complete := func(r runs.Run) runs.Run {
		t.Helper()
		var done runs.Run
		eventually("workflow completion", 3*time.Minute, func() bool {
			code, b, e := request("GET", "/runs/"+r.WorkflowID, "", nil)
			if e != nil || code != 200 {
				return false
			}
			if e = json.Unmarshal(b, &done); e != nil {
				t.Fatal(e)
			}
			if done.Status != "running" && done.Status != "completed" {
				t.Fatalf("workflow failed: %s", b)
			}
			return done.Status == "completed"
		})
		if done.Result == nil || !reflect.DeepEqual(*done.Result, demoIdentityResult()) {
			t.Fatalf("wrong result: %+v", done)
		}
		return done
	}
	eventually("initial scale to zero", 3*time.Minute, zero)
	first := start("")
	eventually("worker-a activates", 90*time.Second, func() bool {
		return strings.TrimSpace(string(k("-n", "temporal-demo-worker-a", "get", "deployment", "worker-a", "-o", "jsonpath={.spec.replicas}"))) == "1"
	})
	first = complete(first)
	// Read real Temporal histories through an exact test-owned child process.
	address, stop := demoTemporalForward(t, root)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tc, err := client.DialContext(ctx, client.Options{HostPort: address, Namespace: "default"})
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	history := func(id, rid string) []*historypb.HistoryEvent {
		t.Helper()
		it := tc.GetWorkflowHistory(ctx, id, rid, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		var events []*historypb.HistoryEvent
		for it.HasNext() {
			ev, e := it.Next()
			if e != nil {
				t.Fatal(e)
			}
			events = append(events, ev)
		}
		return events
	}
	children, err := demoCheckParent(history(first.WorkflowID, first.RunID), first.WorkflowID, p)
	if err != nil {
		t.Fatal(err)
	}
	for name, rid := range children {
		if e := demoCheckChild(history(first.WorkflowID+"/"+name, rid), name); e != nil {
			t.Fatal(e)
		}
	}
	tc.Close()
	stop()
	eventually("return to zero", 3*time.Minute, zero)
	key := fmt.Sprintf("demo-lifecycle-%d", time.Now().UnixNano())
	keyed := complete(start(key))
	if replay := start(key); !reflect.DeepEqual(replay, keyed) {
		t.Fatal("keyed replay changed execution")
	}
	if code, _, e := request("POST", "/runs", "", []byte(" ")); e != nil || code != 400 {
		t.Fatal("body accepted")
	}
	eventually("idle before repeated up", 3*time.Minute, zero)
	identity := func() string {
		return string(k("-n", "temporal", "get", "pvc", "data-postgres-0", "-o", "jsonpath={.metadata.uid}:{.spec.volumeName}")) + string(k("-n", "temporal", "get", "secret", "postgres-credentials", "-o", "jsonpath={.metadata.uid}:{.metadata.resourceVersion}"))
	}
	before := identity()
	command("mise", "run", "up")
	if !zero() {
		t.Fatal("repeated up changed replicas")
	}
	command("mise", "run", "down")
	command("mise", "run", "down")
	if strings.TrimSpace(string(command("docker", "inspect", "temporal-demo-control-plane", "--format", "{{.State.Running}}"))) != "false" {
		t.Fatal("down did not stop node")
	}
	if _, _, e := request("GET", "/readyz", "", nil); e == nil {
		t.Fatal("down retained API tunnel")
	}
	command("mise", "run", "up")
	if identity() != before {
		t.Fatal("resume changed PVC or credential identity")
	}
	if replay := start(key); !reflect.DeepEqual(replay, keyed) {
		t.Fatal("resume lost durable result")
	}
	next := complete(start(""))
	if next.WorkflowID == first.WorkflowID {
		t.Fatal("unkeyed POST reused ID")
	}
	t.Log("PASS: full discovery/scaling, histories, idempotency, stop/resume and new run")
}

func demoTemporalForward(t *testing.T, root string) (string, func()) {
	t.Helper()
	log, err := os.CreateTemp(t.TempDir(), "forward")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("kubectl", "--kubeconfig", filepath.Join(root, ".local/kubeconfig"), "--context", "kind-temporal-demo", "-n", "temporal", "port-forward", "--address", "127.0.0.1", "service/temporal", ":7233")
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			_ = log.Close()
		}
	}
	t.Cleanup(stop)
	for i := 0; i < 100; i++ {
		b, _ := os.ReadFile(log.Name())
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "Forwarding from 127.0.0.1:") {
				return strings.Fields(line)[2], stop
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Temporal tunnel timeout")
	return "", stop
}

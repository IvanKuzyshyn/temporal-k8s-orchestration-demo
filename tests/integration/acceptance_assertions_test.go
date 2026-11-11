//go:build integration && (acceptance_integration || resilience_integration)

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/registry"
	"github.com/onsi/gomega"
	commonpb "go.temporal.io/api/common/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestHistoryAssertionsSensitivity(t *testing.T) {
	g := gomega.NewWithT(t)
	dc := converter.GetDefaultDataConverter()
	payload := func(v any) *commonpb.Payloads {
		p, err := dc.ToPayloads(v)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		return p
	}
	r, err := registry.LoadStatic("../../deploy/config/workers.json")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	p := r.Snapshot().Plan
	hash, err := dc.ToPayload(p.Hash)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	events := []*historypb.HistoryEvent{{EventId: 1, Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{Input: payload(contracts.ParentInput{Plan: p}), Memo: &commonpb.Memo{Fields: map[string]*commonpb.Payload{"planHash": hash}}}}}}
	// A completion precedes B/C commands, which both precede either B/C
	// completion. D joins both. IDs, not timestamps, encode the proof.
	initiate := func(name string) int64 {
		id := int64(len(events) + 1)
		events = append(events, &historypb.HistoryEvent{EventId: id, Attributes: &historypb.HistoryEvent_StartChildWorkflowExecutionInitiatedEventAttributes{StartChildWorkflowExecutionInitiatedEventAttributes: &historypb.StartChildWorkflowExecutionInitiatedEventAttributes{WorkflowId: "parent/" + name, WorkflowType: &commonpb.WorkflowType{Name: contracts.ChildWorkflowName}, TaskQueue: &taskqueuepb.TaskQueue{Name: name + ".v1.temporal"}, Input: payload(contracts.ChildInput{WorkerName: name})}}})
		events = append(events, &historypb.HistoryEvent{EventId: id + 1, Attributes: &historypb.HistoryEvent_ChildWorkflowExecutionStartedEventAttributes{ChildWorkflowExecutionStartedEventAttributes: &historypb.ChildWorkflowExecutionStartedEventAttributes{InitiatedEventId: id, WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: "parent/" + name, RunId: name}}}})
		return id
	}
	complete := func(id int64, name string) {
		events = append(events, &historypb.HistoryEvent{EventId: int64(len(events) + 1), Attributes: &historypb.HistoryEvent_ChildWorkflowExecutionCompletedEventAttributes{ChildWorkflowExecutionCompletedEventAttributes: &historypb.ChildWorkflowExecutionCompletedEventAttributes{InitiatedEventId: id, Result: payload(contracts.ChildResult{WorkerName: name})}}})
	}
	a := initiate("worker-a")
	complete(a, "worker-a")
	b := initiate("worker-b")
	c := initiate("worker-c")
	complete(b, "worker-b")
	complete(c, "worker-c")
	d := initiate("worker-d")
	complete(d, "worker-d")
	events = append(events, &historypb.HistoryEvent{EventId: int64(len(events) + 1), Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{Result: payload(identityResult())}}})
	_, err = checkParent(events, "parent", p)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	for _, tc := range []struct {
		name   string
		mutate func([]*historypb.HistoryEvent) []*historypb.HistoryEvent
	}{
		{"business field", func(e []*historypb.HistoryEvent) []*historypb.HistoryEvent {
			e[1].GetStartChildWorkflowExecutionInitiatedEventAttributes().Input = payload(map[string]string{"workerName": "worker-a", "businessData": "secret"})
			return e
		}},
		{"wrong queue", func(e []*historypb.HistoryEvent) []*historypb.HistoryEvent {
			e[1].GetStartChildWorkflowExecutionInitiatedEventAttributes().TaskQueue.Name = "other"
			return e
		}},
		{"B before A completion", func(e []*historypb.HistoryEvent) []*historypb.HistoryEvent { e[3], e[4] = e[4], e[3]; return e }},
		{"D before C completion", func(e []*historypb.HistoryEvent) []*historypb.HistoryEvent { e[9], e[10] = e[10], e[9]; return e }},
		{"duplicate child", func(e []*historypb.HistoryEvent) []*historypb.HistoryEvent { return append(e, e[1]) }},
		{"missing result", func(e []*historypb.HistoryEvent) []*historypb.HistoryEvent { return e[:len(e)-1] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			copy := proto.Clone(&historypb.History{Events: events}).(*historypb.History).Events
			_, err := checkParent(tc.mutate(copy), "parent", p)
			g.Expect(err).To(gomega.HaveOccurred())
		})
	}
	child := []*historypb.HistoryEvent{
		{Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{TaskQueue: &taskqueuepb.TaskQueue{Name: "worker-a.v1.temporal"}, Input: payload(contracts.ChildInput{WorkerName: "worker-a"})}}},
		{Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{TaskQueue: &taskqueuepb.TaskQueue{Name: "worker-a.v1.temporal"}, ActivityType: &commonpb.ActivityType{Name: contracts.ExampleActivityName}, Input: payload(contracts.ChildInput{WorkerName: "worker-a"})}}},
		{Attributes: &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{ActivityTaskCompletedEventAttributes: &historypb.ActivityTaskCompletedEventAttributes{Result: payload(contracts.ChildResult{WorkerName: "worker-a"})}}},
		{Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{Result: payload(contracts.ChildResult{WorkerName: "worker-a"})}}},
	}
	g.Expect(checkChild(child, "worker-a")).To(gomega.Succeed())
	child[1].GetActivityTaskScheduledEventAttributes().Input = payload(map[string]any{"workerName": "worker-a", "business": true})
	g.Expect(checkChild(child, "worker-a")).NotTo(gomega.Succeed())
	g.Expect(exactPayload(payload(map[string]any{"results": identityResult().Results, "business": true}), identityResult())).NotTo(gomega.Succeed())
}

func TestSnapshotCleanupFailurePaths(t *testing.T) {
	// A subprocess exercises actual testing.T cleanup/FailNow semantics while
	// allowing the parent to verify truthful failures and subsequent actions.
	if os.Getenv("TASK10_SNAPSHOT_CLEANUP_CHILD") != "" {
		t.Setenv("ACCEPTANCE_PROOF", os.Getenv("CLEANUP_DIR"))
		root, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h := &acceptanceHarness{harness: &harness{t: t, g: gomega.NewWithT(t), root: root}, ctx: ctx, endpoint: os.Getenv("CLEANUP_URL")}
		h.registerSnapshotIsolationCleanup("scaler-uid", map[string]string{"owned": "before"})
		return
	}
	for _, scenario := range []string{"success", "service_failure", "scaler_failure", "both_failures", "registry_failure"} {
		t.Run(scenario, func(t *testing.T) {
			g := gomega.NewWithT(t)
			dir := t.TempDir()
			service := `{"metadata":{"uid":"service-uid","annotations":{"temporal-worker-run-after":"worker-a"}}}`
			g.Expect(os.WriteFile(filepath.Join(dir, "service-worker-b.json"), []byte(service), 0600)).To(gomega.Succeed())
			var items []any
			for _, name := range diamondNames {
				items = append(items, map[string]any{"metadata": map[string]string{"name": "temporal-worker-" + name, "namespace": "temporal-demo-" + name}, "data": map[string]string{"workerName": name}})
			}
			b, err := json.Marshal(map[string]any{"items": items})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(os.WriteFile(filepath.Join(dir, "registrations.json"), b, 0600)).To(gomega.Succeed())
			const kubectl = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$CLEANUP_DIR/calls"
if [[ $* == *' get service worker-b '* ]]; then
  printf service-uid
elif [[ $* == *' annotate service worker-b '* || $* == *' patch service worker-b '* ]]; then
  if [[ $TASK10_SNAPSHOT_CLEANUP_CHILD == service_failure || $TASK10_SNAPSHOT_CLEANUP_CHILD == both_failures ]]; then
    echo service-restore-denied >&2; exit 42
  fi
elif [[ $* == *' patch scaledobject worker-a '* ]]; then
  while (($#)); do
    if [[ $1 == -p ]]; then printf '%s' "$2" > "$CLEANUP_DIR/scaler-patch.json"; break; fi
    shift
  done
  if [[ $TASK10_SNAPSHOT_CLEANUP_CHILD == scaler_failure || $TASK10_SNAPSHOT_CLEANUP_CHILD == both_failures ]]; then
    echo scaler-restore-denied >&2; exit 43
  fi
elif [[ $* == *' get configmaps '* ]]; then
  if [[ $TASK10_SNAPSHOT_CLEANUP_CHILD == registry_failure ]]; then echo registry-list-denied >&2; exit 44; fi
  cat "$CLEANUP_DIR/registrations.json"
else echo "unexpected command: $*" >&2; exit 99
fi
`
			g.Expect(os.WriteFile(filepath.Join(dir, "kubectl"), []byte(kubectl), 0755)).To(gomega.Succeed())
			reg, err := registry.LoadStatic("../../deploy/config/workers.json")
			g.Expect(err).NotTo(gomega.HaveOccurred())
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_ = json.NewEncoder(w).Encode(reg.Snapshot())
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSnapshotCleanupFailurePaths$", "-test.v")
			cmd.Env = append(os.Environ(), "TASK10_SNAPSHOT_CLEANUP_CHILD="+scenario, "CLEANUP_DIR="+dir, "CLEANUP_URL="+server.URL, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			t.Logf("cleanup subprocess %s (failure exits are expected only for injected boundary errors):\n%s", scenario, out)
			if scenario == "success" {
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
			} else {
				g.Expect(err).To(gomega.HaveOccurred())
				g.Expect(cmd.ProcessState.ExitCode()).To(gomega.Equal(1), string(out))
			}
			calls, readErr := os.ReadFile(filepath.Join(dir, "calls"))
			g.Expect(readErr).NotTo(gomega.HaveOccurred())
			g.Expect(string(calls)).To(gomega.ContainSubstring("service worker-b"), "service rollback must always be attempted")
			g.Expect(string(calls)).To(gomega.ContainSubstring("patch scaledobject worker-a"), "independent scaler rollback was skipped")
			patchBytes, err := os.ReadFile(filepath.Join(dir, "scaler-patch.json"))
			g.Expect(err).NotTo(gomega.HaveOccurred())
			var patch []struct {
				Op, Path string
				Value    any
			}
			g.Expect(json.Unmarshal(patchBytes, &patch)).To(gomega.Succeed())
			g.Expect(patch).To(gomega.HaveLen(2))
			g.Expect(patch[0].Op).To(gomega.Equal("test"))
			g.Expect(patch[0].Path).To(gomega.Equal("/metadata/uid"))
			g.Expect(patch[0].Value).To(gomega.Equal("scaler-uid"))
			g.Expect(patch[1].Value).To(gomega.Equal(map[string]any{"owned": "before"}))
			if scenario == "service_failure" || scenario == "both_failures" {
				g.Expect(string(out)).To(gomega.ContainSubstring("service-restore-denied"))
			}
			if scenario == "scaler_failure" || scenario == "both_failures" {
				g.Expect(string(out)).To(gomega.ContainSubstring("scaler-restore-denied"))
			}
			if scenario == "registry_failure" {
				g.Expect(string(out)).To(gomega.ContainSubstring("registry-list-denied"))
			}
			if !strings.Contains(scenario, "service") && scenario != "both_failures" {
				g.Expect(requests.Load()).To(gomega.BeNumerically(">", 0), "registry verification must use a fresh context, not expired experiment context")
			}
		})
	}
}

func TestAcceptanceDiagnostics(t *testing.T) {
	path := os.Getenv("SCALING_RUNS_FILE")
	dir := os.Getenv("ACCEPTANCE_PROOF")
	if path == "" || dir == "" {
		t.Fatal("diagnostics require execution ledger and proof directory")
	}
	h := newHarness(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for {
		var ref executionRef
		err := dec.Decode(&ref)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		a := &acceptanceHarness{harness: h, ctx: ctx}
		events := a.history(ref.ID, ref.RunID)
		for _, e := range events {
			if child := e.GetChildWorkflowExecutionStartedEventAttributes(); child != nil {
				a.history(child.WorkflowExecution.WorkflowId, child.WorkflowExecution.RunId)
			}
		}
		b, err := protojson.Marshal(&historypb.History{Events: events})
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		h.g.Expect(os.WriteFile(filepath.Join(dir, fmt.Sprintf("diagnostic-parent-%s.json", ref.RunID)), b, 0600)).To(gomega.Succeed())
		cancel()
	}
}

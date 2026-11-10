//go:build integration && (acceptance_integration || resilience_integration)

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

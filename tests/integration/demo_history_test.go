package integration

import (
	"encoding/json"
	"fmt"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/onsi/gomega"
	commonpb "go.temporal.io/api/common/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
	"reflect"
	"strings"
	"testing"
)

func demoPlan(t *testing.T) plan.Plan {
	t.Helper()
	regs := []plan.Registration{}
	for i, name := range []string{"worker-a", "worker-b", "worker-c", "worker-d"} {
		regs = append(regs, plan.Registration{SchemaVersion: "1", WorkerName: name, TaskQueue: name + ".v1.temporal", WorkflowType: contracts.ChildWorkflowName, SourceServiceName: name, SourceServiceNamespace: "temporal-demo-" + name, RunAfter: []string{"", "worker-a", "worker-a", "worker-b,worker-c"}[i]})
	}
	p, err := plan.Build(regs)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func demoIdentityResult() contracts.ParentResult {
	r := contracts.ParentResult{}
	for _, name := range []string{"worker-a", "worker-b", "worker-c", "worker-d"} {
		r.Results = append(r.Results, contracts.ChildResult{WorkerName: name})
	}
	return r
}
func demoExactPayload(p *commonpb.Payloads, expected any) error {
	if len(p.GetPayloads()) != 1 {
		return fmt.Errorf("expected one payload: %v", p)
	}
	var got any
	if err := converter.GetDefaultDataConverter().FromPayload(p.Payloads[0], &got); err != nil {
		return err
	}
	b, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	var want any
	if err := json.Unmarshal(b, &want); err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("payload got %v want %v", got, want)
	}
	return nil
}

// Validate commands and order from parent event IDs, never log timestamps.
func demoCheckParent(events []*historypb.HistoryEvent, id string, p plan.Plan) (map[string]string, error) {
	starts, finishes := map[string]int64{}, map[string]int64{}
	children := map[string]string{}
	initiated := map[int64]string{}
	seenInput, seenResult := false, false
	for _, e := range events {
		if a := e.GetWorkflowExecutionStartedEventAttributes(); a != nil {
			if seenInput {
				return nil, fmt.Errorf("duplicate parent input")
			}
			seenInput = true
			if err := demoExactPayload(a.Input, contracts.ParentInput{Plan: p}); err != nil {
				return nil, err
			}
			if len(a.Memo.GetFields()) != 1 {
				return nil, fmt.Errorf("unexpected parent memo")
			}
			var hash string
			if err := converter.GetDefaultDataConverter().FromPayload(a.Memo.Fields["planHash"], &hash); err != nil || hash != p.Hash {
				return nil, fmt.Errorf("memo hash mismatch")
			}
		}
		if a := e.GetStartChildWorkflowExecutionInitiatedEventAttributes(); a != nil {
			name := strings.TrimPrefix(a.WorkflowId, id+"/")
			var node *plan.Node
			for i := range p.Nodes {
				if p.Nodes[i].Name == name {
					node = &p.Nodes[i]
				}
			}
			if node == nil || starts[name] != 0 || a.WorkflowId != id+"/"+name || a.TaskQueue.GetName() != node.TaskQueue || a.WorkflowType.GetName() != contracts.ChildWorkflowName {
				return nil, fmt.Errorf("unexpected/duplicate child command %s", a.WorkflowId)
			}
			if len(a.Header.GetFields()) != 0 || len(a.Memo.GetFields()) != 0 || len(a.SearchAttributes.GetIndexedFields()) != 0 {
				return nil, fmt.Errorf("extra child command data")
			}
			if err := demoExactPayload(a.Input, contracts.ChildInput{WorkerName: name}); err != nil {
				return nil, err
			}
			starts[name] = e.EventId
			initiated[e.EventId] = name
			for _, dep := range node.Dependencies {
				if finishes[dep] == 0 || finishes[dep] >= e.EventId {
					return nil, fmt.Errorf("%s started before %s completion", name, dep)
				}
			}
		}
		if a := e.GetChildWorkflowExecutionStartedEventAttributes(); a != nil {
			name := initiated[a.InitiatedEventId]
			if name == "" || children[name] != "" || a.WorkflowExecution.GetWorkflowId() != id+"/"+name {
				return nil, fmt.Errorf("unexpected child start")
			}
			children[name] = a.WorkflowExecution.GetRunId()
		}
		if a := e.GetChildWorkflowExecutionCompletedEventAttributes(); a != nil {
			name := initiated[a.InitiatedEventId]
			if name == "" || finishes[name] != 0 {
				return nil, fmt.Errorf("unexpected child completion")
			}
			if err := demoExactPayload(a.Result, contracts.ChildResult{WorkerName: name}); err != nil {
				return nil, err
			}
			finishes[name] = e.EventId
		}
		if a := e.GetWorkflowExecutionCompletedEventAttributes(); a != nil {
			seenResult = true
			if err := demoExactPayload(a.Result, demoIdentityResult()); err != nil {
				return nil, err
			}
		}
		if e.GetActivityTaskScheduledEventAttributes() != nil {
			return nil, fmt.Errorf("parent scheduled activity")
		}
	}
	if !seenInput || !seenResult || len(starts) != 4 || len(children) != 4 || len(finishes) != 4 {
		return nil, fmt.Errorf("incomplete diamond: starts=%v children=%v finishes=%v", starts, children, finishes)
	}
	// In the diamond, B/C are both eligible immediately after A: neither
	// command may be held behind the other's completion (a global barrier).
	if reflect.DeepEqual(p.Nodes[1].Dependencies, p.Nodes[2].Dependencies) &&
		(starts["worker-b"] >= finishes["worker-c"] || starts["worker-c"] >= finishes["worker-b"]) {
		return nil, fmt.Errorf("eligible B/C were serialized")
	}
	return children, nil
}
func demoCheckChild(events []*historypb.HistoryEvent, name string) error {
	input, scheduled, finished, result := 0, 0, 0, 0
	for _, e := range events {
		if a := e.GetWorkflowExecutionStartedEventAttributes(); a != nil {
			input++
			if a.TaskQueue.GetName() != name+".v1.temporal" {
				return fmt.Errorf("child queue mismatch")
			}
			if err := demoExactPayload(a.Input, contracts.ChildInput{WorkerName: name}); err != nil {
				return err
			}
		}
		if a := e.GetActivityTaskScheduledEventAttributes(); a != nil {
			scheduled++
			if a.TaskQueue.GetName() != name+".v1.temporal" || a.ActivityType.GetName() != contracts.ExampleActivityName || len(a.Header.GetFields()) != 0 {
				return fmt.Errorf("unexpected activity command")
			}
			if err := demoExactPayload(a.Input, contracts.ChildInput{WorkerName: name}); err != nil {
				return err
			}
		}
		if a := e.GetActivityTaskCompletedEventAttributes(); a != nil {
			finished++
			if err := demoExactPayload(a.Result, contracts.ChildResult{WorkerName: name}); err != nil {
				return err
			}
		}
		if a := e.GetWorkflowExecutionCompletedEventAttributes(); a != nil {
			result++
			if err := demoExactPayload(a.Result, contracts.ChildResult{WorkerName: name}); err != nil {
				return err
			}
		}
		if e.GetStartChildWorkflowExecutionInitiatedEventAttributes() != nil {
			return fmt.Errorf("child started nested workflow")
		}
	}
	if input != 1 || scheduled != 1 || finished != 1 || result != 1 {
		return fmt.Errorf("child lifecycle counts %d/%d/%d/%d", input, scheduled, finished, result)
	}
	return nil
}
func TestDemoHistoryAssertionsSensitivity(t *testing.T) {
	g := gomega.NewWithT(t)
	dc := converter.GetDefaultDataConverter()
	payload := func(v any) *commonpb.Payloads {
		p, err := dc.ToPayloads(v)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		return p
	}
	p := demoPlan(t)
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
	events = append(events, &historypb.HistoryEvent{EventId: int64(len(events) + 1), Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{Result: payload(demoIdentityResult())}}})
	_, err = demoCheckParent(events, "parent", p)
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
			_, err := demoCheckParent(tc.mutate(copy), "parent", p)
			g.Expect(err).To(gomega.HaveOccurred())
		})
	}
	child := []*historypb.HistoryEvent{
		{Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{TaskQueue: &taskqueuepb.TaskQueue{Name: "worker-a.v1.temporal"}, Input: payload(contracts.ChildInput{WorkerName: "worker-a"})}}},
		{Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{TaskQueue: &taskqueuepb.TaskQueue{Name: "worker-a.v1.temporal"}, ActivityType: &commonpb.ActivityType{Name: contracts.ExampleActivityName}, Input: payload(contracts.ChildInput{WorkerName: "worker-a"})}}},
		{Attributes: &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{ActivityTaskCompletedEventAttributes: &historypb.ActivityTaskCompletedEventAttributes{Result: payload(contracts.ChildResult{WorkerName: "worker-a"})}}},
		{Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{Result: payload(contracts.ChildResult{WorkerName: "worker-a"})}}},
	}
	g.Expect(demoCheckChild(child, "worker-a")).To(gomega.Succeed())
	child[1].GetActivityTaskScheduledEventAttributes().Input = payload(map[string]any{"workerName": "worker-a", "business": true})
	g.Expect(demoCheckChild(child, "worker-a")).NotTo(gomega.Succeed())
	g.Expect(demoExactPayload(payload(map[string]any{"results": demoIdentityResult().Results, "business": true}), demoIdentityResult())).NotTo(gomega.Succeed())
}

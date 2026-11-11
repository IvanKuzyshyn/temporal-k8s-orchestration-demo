package registry

import (
	"errors"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"testing"
)

func TestValidateSnapshotCopiesAndRejectsNoncanonical(t *testing.T) {
	p, err := plan.Build([]plan.Registration{
		{SchemaVersion: "1", WorkerName: "worker-a", TaskQueue: "worker-a.v1.temporal", WorkflowType: "ExampleChildWorkflow", SourceServiceName: "worker-a", SourceServiceNamespace: "ns-a"},
		{SchemaVersion: "1", WorkerName: "worker-b", TaskQueue: "worker-b.v1.temporal", WorkflowType: "ExampleChildWorkflow", RunAfter: "worker-a", SourceServiceName: "worker-b", SourceServiceNamespace: "ns-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{Synced: true, Plan: p}
	copy, err := ValidateSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	copy.Nodes[1].Dependencies[0] = "changed"
	if s.Plan.Nodes[1].Dependencies[0] != "worker-a" {
		t.Fatal("shared dependencies")
	}
	s.Plan.Nodes[0], s.Plan.Nodes[1] = s.Plan.Nodes[1], s.Plan.Nodes[0]
	if _, err := ValidateSnapshot(s); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("accepted noncanonical: %v", err)
	}
}

package registry

import (
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"reflect"
	"sync"
	"testing"
)

func TestSnapshotDeepCopy(t *testing.T) {
	p := &Kubernetes{snapshot: Snapshot{Synced: true, Problems: []string{"diagnostic"}, Plan: plan.Plan{SchemaVersion: "1", Hash: "hash", Nodes: []plan.Node{{Name: "worker-a"}, {Name: "worker-b", Dependencies: []string{"worker-a"}}}}}}
	initial := p.Snapshot()
	s := p.Snapshot()
	s.Plan.Nodes[0].Name = "changed"
	s.Plan.Nodes[1].Dependencies[0] = "changed"
	s.Plan.Nodes = append(s.Plan.Nodes, plan.Node{Name: "extra"})
	s.Problems[0] = "changed"
	s.Synced = false
	s.Plan.Hash = "changed"
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { s := p.Snapshot(); s.Plan.Nodes[1].Dependencies[0] = "concurrent" })
	}
	wg.Wait()
	if !reflect.DeepEqual(initial, p.Snapshot()) {
		t.Fatal("snapshot callers mutated registry")
	}
}

package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
)

func TestSnapshotDeepCopy(t *testing.T) {
	p, err := LoadStatic("../../deploy/config/workers.json")
	if err != nil {
		t.Fatal(err)
	}
	initial := p.Snapshot()
	if !initial.Synced || len(initial.Problems) != 0 || len(initial.Plan.Nodes) != 4 {
		t.Fatalf("snapshot: %+v", initial)
	}
	s := p.Snapshot()
	s.Plan.Nodes[0].Name = "corrupted"
	s.Plan.Nodes[3].Dependencies[0] = "corrupted"
	s.Plan.Nodes = append(s.Plan.Nodes, plan.Node{Name: "extra"})
	s.Problems = append(s.Problems, "corrupted")
	s.Synced = false
	s.Plan.Hash = "corrupted"
	if !reflect.DeepEqual(initial, p.Snapshot()) {
		t.Fatal("caller mutated stored snapshot")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { s := p.Snapshot(); s.Plan.Nodes[3].Dependencies[0] = "changed" })
	}
	wg.Wait()
	if !reflect.DeepEqual(initial, p.Snapshot()) {
		t.Fatal("concurrent caller mutated stored snapshot")
	}
}

// Strict decoding prevents typos and appended JSON from silently changing the registry.
func TestLoadStatic(t *testing.T) {
	if _, err := LoadStatic(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
	for _, raw := range []string{"{", "[]", "null", "{}", "[] {}", `[{"unknown":true}]`, `[{"schemaVersion":"2"}]`} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workers.json")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadStatic(path); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		})
	}
	data, err := os.ReadFile("../../deploy/config/workers.json")
	if err != nil {
		t.Fatal(err)
	}
	var regs []plan.Registration
	if err = json.Unmarshal(data, &regs); err != nil {
		t.Fatal(err)
	}
	if len(regs) != 4 {
		t.Fatalf("fixture size %d", len(regs))
	}
	for _, mutate := range []func(){
		func() { regs[1].SourceServiceNamespace = regs[0].SourceServiceNamespace },
		func() {
			regs[1].SourceServiceNamespace = "different"
			regs[1].WorkerName = regs[0].WorkerName
			regs[1].SourceServiceName = regs[0].WorkerName
			regs[1].TaskQueue = regs[0].TaskQueue
		},
	} {
		mutate()
		b, err := json.Marshal(regs)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "bad.json")
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = LoadStatic(path); err == nil {
			t.Fatal("accepted ambiguous registry")
		}
	}
}

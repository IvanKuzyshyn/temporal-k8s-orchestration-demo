package runs_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/registry"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/runs"
	. "github.com/onsi/gomega"
)

type provider struct{ snapshot registry.Snapshot }

func (p *provider) Snapshot() registry.Snapshot { return p.snapshot }
func snapshot(t *testing.T) registry.Snapshot {
	t.Helper()
	p, err := plan.Build([]plan.Registration{{SchemaVersion: "1", WorkerName: "worker-a", TaskQueue: "worker-a.v1.temporal", WorkflowType: "ExampleChildWorkflow", SourceServiceName: "worker-a", SourceServiceNamespace: "ns-a"}})
	NewWithT(t).Expect(err).NotTo(HaveOccurred())
	return registry.Snapshot{Plan: p, Synced: true}
}

// This fake models only the external durable store; service logic remains real.
type gateway struct {
	mu                  sync.Mutex
	stored              map[string]runs.Execution
	starts              int
	input               contracts.ParentInput
	lookupErr, startErr error
	duplicateLookupErr  error
	ambiguous           bool
	forceMiss           bool
}

func (f *gateway) Get(_ context.Context, id string) (runs.Execution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lookupErr != nil {
		return runs.Execution{}, f.lookupErr
	}
	if f.forceMiss {
		f.forceMiss = false
		return runs.Execution{}, runs.ErrNotFound
	}
	if e, ok := f.stored[id]; ok {
		return e, nil
	}
	return runs.Execution{}, runs.ErrNotFound
}
func (f *gateway) Start(_ context.Context, id string, input contracts.ParentInput) (runs.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if f.startErr != nil {
		return runs.Run{}, f.startErr
	}
	if _, ok := f.stored[id]; ok {
		f.lookupErr = f.duplicateLookupErr
		return runs.Run{}, runs.ErrAlreadyExists
	}
	r := runs.Run{WorkflowID: id, RunID: "original-run", PlanHash: input.Plan.Hash, Status: "running"}
	if f.stored == nil {
		f.stored = map[string]runs.Execution{}
	}
	f.stored[id] = runs.Execution{Run: r}
	f.input = input
	if f.ambiguous {
		f.ambiguous = false
		return runs.Run{}, runs.ErrUnavailable
	}
	return r, nil
}
func TestDurableRetriesSurviveRegistryChangesAndRestart(t *testing.T) {
	g := NewWithT(t)
	p := &provider{snapshot(t)}
	f := &gateway{}
	s := runs.New(p, f)
	first, err := s.Start(context.Background(), "key")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(first.WorkflowID).To(Equal("run-2c70e12b7a0646f92279f427c7b38e7334d8e5389cff167a1dc30e73f826b683"))
	p.snapshot = registry.Snapshot{Synced: false, Problems: []string{"invalid now"}}
	f.mu.Lock()
	e := f.stored[first.WorkflowID]
	e.Run.Status = "completed"
	e.Run.Result = &contracts.ParentResult{Results: []contracts.ChildResult{{WorkerName: "worker-a"}}}
	f.stored[first.WorkflowID] = e
	f.mu.Unlock()
	repeated, err := runs.New(p, f).Start(context.Background(), "key")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(repeated.RunID).To(Equal(first.RunID))
	g.Expect(repeated.PlanHash).To(Equal(first.PlanHash))
	g.Expect(repeated.Status).To(Equal("completed"))
	g.Expect(f.starts).To(Equal(1))
	g.Expect(repeated.Result).To(Equal(e.Run.Result))
	_, err = s.Start(context.Background(), "new-key")
	g.Expect(errors.Is(err, runs.ErrInvalidRegistry)).To(BeTrue())
	g.Expect(f.starts).To(Equal(1))
}
func TestConcurrentSubmissionsAndAlreadyExistsRace(t *testing.T) {
	g := NewWithT(t)
	p := &provider{snapshot(t)}
	f := &gateway{}
	s := runs.New(p, f)
	var wg sync.WaitGroup
	results := make(chan runs.Run, 24)
	errs := make(chan error, 24)
	for range 24 {
		wg.Go(func() {
			r, e := s.Start(context.Background(), "race")
			results <- r
			errs <- e
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		g.Expect(e).NotTo(HaveOccurred())
	}
	var original runs.Run
	for r := range results {
		if original.RunID == "" {
			original = r
		}
		g.Expect(r).To(Equal(original))
	}
	f.forceMiss = true
	r, e := s.Start(context.Background(), "race")
	g.Expect(e).NotTo(HaveOccurred())
	g.Expect(r).To(Equal(original))
	g.Expect(f.starts).To(BeNumerically(">=", 2))
	// Simulate a start race against a completed original run and a changed graph.
	execution := f.stored[original.WorkflowID]
	execution.Run.Status = "completed"
	execution.Run.Result = &contracts.ParentResult{Results: []contracts.ChildResult{{WorkerName: "worker-a"}}}
	f.stored[original.WorkflowID] = execution
	changed := snapshot(t)
	changed.Plan, e = plan.Build([]plan.Registration{{SchemaVersion: "1", WorkerName: "worker-b", TaskQueue: "worker-b.v1.temporal", WorkflowType: contracts.ChildWorkflowName, SourceServiceName: "worker-b", SourceServiceNamespace: "ns-b"}})
	g.Expect(e).NotTo(HaveOccurred())
	p.snapshot = changed
	f.forceMiss = true
	r, e = s.Start(context.Background(), "race")
	g.Expect(e).NotTo(HaveOccurred())
	g.Expect(r).To(Equal(execution.Run))
	g.Expect(r.PlanHash).NotTo(Equal(changed.Plan.Hash))
}
func TestAlreadyExistsLookupFailureNeverInventsAcceptance(t *testing.T) {
	g := NewWithT(t)
	f := &gateway{}
	s := runs.New(&provider{snapshot(t)}, f)
	_, err := s.Start(context.Background(), "key")
	g.Expect(err).NotTo(HaveOccurred())
	f.forceMiss = true
	f.duplicateLookupErr = runs.ErrUnavailable
	result, err := s.Start(context.Background(), "key")
	g.Expect(errors.Is(err, runs.ErrUnavailable)).To(BeTrue())
	g.Expect(result).To(BeZero())
}

func TestAmbiguousStartIsUnavailableUntilDurableRetry(t *testing.T) {
	g := NewWithT(t)
	f := &gateway{ambiguous: true}
	p := &provider{snapshot(t)}
	s := runs.New(p, f)
	_, e := s.Start(context.Background(), "key")
	g.Expect(errors.Is(e, runs.ErrUnavailable)).To(BeTrue())
	r, e := runs.New(p, f).Start(context.Background(), "key")
	g.Expect(e).NotTo(HaveOccurred())
	g.Expect(r.RunID).To(Equal("original-run"))
	g.Expect(f.starts).To(Equal(1))
}
func TestNoStartOnLookupFailure(t *testing.T) {
	g := NewWithT(t)
	f := &gateway{lookupErr: runs.ErrUnavailable}
	s := runs.New(&provider{snapshot(t)}, f)
	_, e := s.Start(context.Background(), "key")
	g.Expect(errors.Is(e, runs.ErrUnavailable)).To(BeTrue())
	g.Expect(f.starts).To(BeZero())
}
func TestNewStartsRevalidateCopiedPlans(t *testing.T) {
	mutations := map[string]func(*registry.Snapshot){
		"unsynced": func(s *registry.Snapshot) { s.Synced = false }, "problems": func(s *registry.Snapshot) { s.Problems = []string{"bad"} }, "empty": func(s *registry.Snapshot) { s.Plan.Nodes = nil },
		"schema": func(s *registry.Snapshot) { s.Plan.SchemaVersion = "2" }, "hash": func(s *registry.Snapshot) { s.Plan.Hash = "bad" }, "identity": func(s *registry.Snapshot) { s.Plan.Nodes[0].Name = "BAD" }, "queue": func(s *registry.Snapshot) { s.Plan.Nodes[0].TaskQueue = "wrong" }, "workflow": func(s *registry.Snapshot) { s.Plan.Nodes[0].WorkflowType = "wrong" },
		"unknown": func(s *registry.Snapshot) { s.Plan.Nodes[0].Dependencies = []string{"missing"} }, "self": func(s *registry.Snapshot) {
			s.Plan.Nodes[0].Dependencies = []string{"worker-a"}
			// Hand-derived SHA-256 of the invalid canonical envelope: a correct
			// content hash is not proof that the graph is runnable.
			s.Plan.Hash = "36b9be5c2ead78f3aeef16504bceb86563bbbee6dedc8463ba70a7befafc1bb9"
		}, "duplicate": func(s *registry.Snapshot) { s.Plan.Nodes = append(s.Plan.Nodes, s.Plan.Nodes[0]) },
		"cycle": func(s *registry.Snapshot) {
			n := s.Plan.Nodes[0]
			n.Name = "worker-b"
			n.TaskQueue = "worker-b.v1.temporal"
			n.Dependencies = []string{"worker-a"}
			s.Plan.Nodes[0].Dependencies = []string{"worker-b"}
			s.Plan.Nodes = append(s.Plan.Nodes, n)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			snap := snapshot(t)
			mutate(&snap)
			f := &gateway{}
			_, e := runs.New(&provider{snap}, f).Start(context.Background(), "")
			g.Expect(errors.Is(e, runs.ErrInvalidRegistry)).To(BeTrue())
			g.Expect(f.starts).To(BeZero())
		})
	}
}
func TestStartsHaveIndependentInputsAndUniqueUnkeyedIDs(t *testing.T) {
	g := NewWithT(t)
	p := &provider{snapshot(t)}
	f := &gateway{}
	s := runs.New(p, f)
	a, e := s.Start(context.Background(), "")
	g.Expect(e).NotTo(HaveOccurred())
	originalHash := f.input.Plan.Hash
	p.snapshot.Plan.Nodes[0].Dependencies = append(p.snapshot.Plan.Nodes[0].Dependencies, "changed")
	g.Expect(f.input.Plan.Nodes[0].Dependencies).To(BeEmpty())
	p.snapshot = snapshot(t)
	b, e := s.Start(context.Background(), "")
	g.Expect(e).NotTo(HaveOccurred())
	g.Expect(a.WorkflowID).NotTo(Equal(b.WorkflowID))
	g.Expect(f.input.Plan.Hash).To(Equal(originalHash))
}
func TestRetryKeepsOriginalValidPlanAndNewKeyUsesChangedPlan(t *testing.T) {
	g := NewWithT(t)
	p := &provider{snapshot(t)}
	f := &gateway{}
	s := runs.New(p, f)
	first, err := s.Start(context.Background(), "old")
	g.Expect(err).NotTo(HaveOccurred())
	changed, err := plan.Build([]plan.Registration{
		{SchemaVersion: "1", WorkerName: "worker-a", TaskQueue: "worker-a.v1.temporal", WorkflowType: "ExampleChildWorkflow", SourceServiceName: "worker-a", SourceServiceNamespace: "ns-a"},
		{SchemaVersion: "1", WorkerName: "worker-b", TaskQueue: "worker-b.v1.temporal", WorkflowType: "ExampleChildWorkflow", RunAfter: "worker-a", SourceServiceName: "worker-b", SourceServiceNamespace: "ns-b"},
	})
	g.Expect(err).NotTo(HaveOccurred())
	p.snapshot.Plan = changed
	repeated, err := s.Start(context.Background(), "old")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(repeated).To(Equal(first))
	next, err := s.Start(context.Background(), "new")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(next.PlanHash).To(Equal(changed.Hash))
	g.Expect(next.PlanHash).NotTo(Equal(first.PlanHash))
	p.snapshot.Plan.Nodes[1].Dependencies[0] = "mutated"
	g.Expect(f.input.Plan.Nodes[1].Dependencies).To(Equal([]string{"worker-a"}))
}

func TestServiceGetAndMalformedKeys(t *testing.T) {
	g := NewWithT(t)
	f := &gateway{}
	s := runs.New(&provider{snapshot(t)}, f)
	_, e := s.Get(context.Background(), "unknown")
	g.Expect(errors.Is(e, runs.ErrNotFound)).To(BeTrue())
	for _, key := range []string{"bad\n", "é", string(make([]byte, 129))} {
		_, e = s.Start(context.Background(), key)
		g.Expect(errors.Is(e, runs.ErrBadRequest)).To(BeTrue())
	}
	g.Expect(f.starts).To(BeZero())
}

package api_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/registry"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/api"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/runs"
)

type noGraphGateway struct{ t *testing.T }

func (g noGraphGateway) Start(context.Context, string, contracts.ParentInput) (runs.Run, error) {
	g.t.Fatal("graph called Temporal Start")
	return runs.Run{}, nil
}
func (g noGraphGateway) Get(context.Context, string) (runs.Execution, error) {
	g.t.Fatal("graph called Temporal Get")
	return runs.Execution{}, nil
}

func TestGraphValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		mutate func(*registry.Snapshot)
	}{
		{"valid", 200, func(*registry.Snapshot) {}},
		{"unsynced", 503, func(s *registry.Snapshot) { s.Synced = false }},
		{"empty", 422, func(s *registry.Snapshot) { s.Plan.Nodes = nil }},
		{"reported problem", 422, func(s *registry.Snapshot) { s.Problems = []string{"invalid registration"} }},
		{"unknown dependency", 422, func(s *registry.Snapshot) { s.Plan.Nodes[0].Dependencies = []string{"missing"} }},
		{"self cycle", 422, func(s *registry.Snapshot) { s.Plan.Nodes[0].Dependencies = []string{"worker-a"} }},
		{"identity", 422, func(s *registry.Snapshot) { s.Plan.Nodes[0].Name = "INVALID" }},
		{"stale hash", 422, func(s *registry.Snapshot) { s.Plan.Hash = "bad" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _ := fixture(t)
			tc.mutate(&p.s)
			before, _ := json.Marshal(p.s)
			h := api.New(p, runs.New(p, noGraphGateway{t}), func(context.Context) error { return nil })
			w := request(h, "GET", "/graph", "")
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			var body struct {
				Synced, Valid bool
				Plan          json.RawMessage
				Problems      []string
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Valid != (tc.status == 200) || body.Synced != p.s.Synced || body.Problems == nil {
				t.Fatalf("wrong validation: %s", w.Body.String())
			}
			if tc.status != 200 && len(body.Problems) == 0 {
				t.Fatal("missing diagnostic")
			}
			after, _ := json.Marshal(p.s)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("mutated provider")
			}
			if p.s.Synced && request(h, "GET", "/readyz", "").Code != 200 {
				t.Fatal("graph validity changed readiness")
			}
		})
	}
}

func TestGraphInvalidityDoesNotBlockDurableReplay(t *testing.T) {
	for _, synced := range []bool{true, false} {
		p, f, h := fixture(t)
		first := request(h, "POST", "/runs", "", "existing")
		if first.Code != 202 {
			t.Fatal(first.Body.String())
		}
		p.s = registry.Snapshot{Synced: synced, Problems: []string{"missing registrations"}}
		want := 422
		if !synced {
			want = 503
		}
		if got := request(h, "GET", "/graph", "").Code; got != want {
			t.Fatalf("graph=%d", got)
		}
		replay := request(h, "POST", "/runs", "", "existing")
		if replay.Code != 202 || replay.Body.String() != first.Body.String() {
			t.Fatalf("replay: %s", replay.Body.String())
		}
		if request(h, "POST", "/runs", "", "new").Code != 503 || f.starts != 1 {
			t.Fatal("invalid graph admitted new start")
		}
	}
}

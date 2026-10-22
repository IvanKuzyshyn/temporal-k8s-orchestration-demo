// Package registry publishes immutable, validated worker graph snapshots.
package registry

import "github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"

type Snapshot struct {
	Plan     plan.Plan `json:"plan"`
	Synced   bool      `json:"synced"`
	Problems []string  `json:"problems"`
}

// Provider returns a deep copy owned by the caller on every access.
// A future live provider must set Synced false on incomplete discovery and expose
// invalid state through Problems rather than serving a stale-valid plan.
type Provider interface{ Snapshot() Snapshot }

func cloneSnapshot(s Snapshot) Snapshot {
	s.Problems = append([]string(nil), s.Problems...)
	nodes := s.Plan.Nodes
	s.Plan.Nodes = make([]plan.Node, len(nodes))
	for i, n := range nodes {
		n.Dependencies = append([]string(nil), n.Dependencies...)
		s.Plan.Nodes[i] = n
	}
	return s
}

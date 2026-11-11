package registry

import (
	"errors"
	"reflect"
	"strings"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
)

var ErrInvalidSnapshot = errors.New("registry snapshot is not synchronized, valid and canonical")

// ValidateSnapshot rebuilds a private copy through the shared graph authority.
// A cached hash or Problems flag alone is not evidence of a runnable graph.
func ValidateSnapshot(snapshot Snapshot) (plan.Plan, error) {
	if !snapshot.Synced || len(snapshot.Problems) > 0 {
		return plan.Plan{}, ErrInvalidSnapshot
	}
	p := cloneSnapshot(snapshot).Plan
	registrations := make([]plan.Registration, 0, len(p.Nodes))
	for _, n := range p.Nodes {
		registrations = append(registrations, plan.Registration{SchemaVersion: p.SchemaVersion, WorkerName: n.Name, TaskQueue: n.TaskQueue, WorkflowType: n.WorkflowType, RunAfter: strings.Join(n.Dependencies, ","), SourceServiceName: n.Name, SourceServiceNamespace: n.Name})
	}
	canonical, err := plan.Build(registrations)
	if err != nil || !reflect.DeepEqual(canonical, p) {
		return plan.Plan{}, ErrInvalidSnapshot
	}
	return p, nil
}

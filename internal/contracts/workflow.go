// Package contracts defines serialized workflow inputs/results without SDK dependencies.
package contracts

import "github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"

const (
	ParentWorkflowName  = "ParentWorkflow"
	ChildWorkflowName   = "ExampleChildWorkflow"
	ExampleActivityName = "ExampleActivity"
)

type ParentInput struct {
	Plan plan.Plan `json:"plan"`
}
type ChildInput struct {
	WorkerName string `json:"workerName"`
}
type ChildResult struct {
	WorkerName string `json:"workerName"`
}
type ParentResult struct {
	Results []ChildResult `json:"results"`
}

// Package contracts defines serialized workflow inputs/results without SDK dependencies.
package contracts

import "github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"

const (
	ParentWorkflowName  = "ParentWorkflow"
	ChildWorkflowName   = "ExampleChildWorkflow"
	ExampleActivityName = "ExampleActivity"
)

type Payload struct {
	Message string `json:"message"`
}
type ParentInput struct {
	Plan        plan.Plan `json:"plan"`
	Payload     Payload   `json:"payload"`
	RequestHash string    `json:"requestHash"`
}
type ChildInput struct {
	WorkerName string  `json:"workerName"`
	Payload    Payload `json:"payload"`
}
type ChildResult struct {
	WorkerName string `json:"workerName"`
	Message    string `json:"message"`
}
type ParentResult struct {
	Results []ChildResult `json:"results"`
}

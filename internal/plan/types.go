// Package plan defines and validates the execution graph shared by registry adapters.
package plan

// Registration is schema-1 source metadata, shared by static and Kubernetes adapters.
// Kubernetes adapters must additionally verify the actual ConfigMap namespace and UID.
type Registration struct {
	SchemaVersion          string `json:"schemaVersion"`
	WorkerName             string `json:"workerName"`
	TaskQueue              string `json:"taskQueue"`
	WorkflowType           string `json:"workflowType"`
	RunAfter               string `json:"runAfter"`
	SourceServiceName      string `json:"sourceServiceName"`
	SourceServiceNamespace string `json:"sourceServiceNamespace"`
	SourceServiceUID       string `json:"sourceServiceUID"`
}

// Node contains only execution-relevant worker data.
type Node struct {
	Name         string   `json:"name"`
	TaskQueue    string   `json:"taskQueue"`
	WorkflowType string   `json:"workflowType"`
	Dependencies []string `json:"dependencies"`
}

// Plan is canonical: nodes and dependencies are sorted by worker identity.
// Treat it as a value owned by its recipient; providers must deep-copy its slices.
type Plan struct {
	SchemaVersion string `json:"schemaVersion"`
	Nodes         []Node `json:"nodes"`
	Hash          string `json:"hash"`
}

package plan

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Use the default Kubernetes Service RFC 1035 rule, not the optional relaxed
// Service-name feature gate. Namespace names use the RFC 1123 label rule.
var serviceName = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)
var namespaceName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func validServiceName(name string) bool { return len(name) <= 63 && serviceName.MatchString(name) }

// Build rejects invalid or ambiguous registries without publishing partial plans.
// Source provenance is validated but intentionally excluded from the execution hash.
func Build(registrations []Registration) (Plan, error) {
	if len(registrations) == 0 {
		return Plan{}, fmt.Errorf("empty worker registry")
	}
	nodes := make(map[string]Node, len(registrations))
	namespaces := make(map[string]string, len(registrations))
	for _, r := range registrations {
		if r.SchemaVersion != "1" {
			return Plan{}, fmt.Errorf("worker %q: unsupported schema %q", r.WorkerName, r.SchemaVersion)
		}
		if !validServiceName(r.WorkerName) {
			return Plan{}, fmt.Errorf("invalid worker Service name %q", r.WorkerName)
		}
		if _, exists := nodes[r.WorkerName]; exists {
			return Plan{}, fmt.Errorf("duplicate worker identity %q across registrations", r.WorkerName)
		}
		if r.SourceServiceName != r.WorkerName {
			return Plan{}, fmt.Errorf("worker %q: source Service name %q must match identity", r.WorkerName, r.SourceServiceName)
		}
		ns := r.SourceServiceNamespace
		if len(ns) > 63 || !namespaceName.MatchString(ns) {
			return Plan{}, fmt.Errorf("worker %q: invalid source namespace %q", r.WorkerName, ns)
		}
		if other, exists := namespaces[ns]; exists {
			return Plan{}, fmt.Errorf("namespace %q has multiple workers %q and %q", ns, other, r.WorkerName)
		}
		namespaces[ns] = r.WorkerName
		if r.TaskQueue != r.WorkerName+".v1.temporal" {
			return Plan{}, fmt.Errorf("worker %q: invalid task queue %q", r.WorkerName, r.TaskQueue)
		}
		if r.WorkflowType != "ExampleChildWorkflow" {
			return Plan{}, fmt.Errorf("worker %q: invalid workflow type %q", r.WorkerName, r.WorkflowType)
		}
		dependencies, err := ParseDependencies(r.RunAfter)
		if err != nil {
			return Plan{}, fmt.Errorf("worker %q: %w", r.WorkerName, err)
		}
		nodes[r.WorkerName] = Node{Name: r.WorkerName, TaskQueue: r.TaskQueue, WorkflowType: r.WorkflowType, Dependencies: dependencies}
	}
	p := Plan{SchemaVersion: "1", Nodes: make([]Node, 0, len(nodes))}
	for _, node := range nodes {
		p.Nodes = append(p.Nodes, node)
	}
	sort.Slice(p.Nodes, func(i, j int) bool { return p.Nodes[i].Name < p.Nodes[j].Name })
	for _, node := range p.Nodes {
		for _, dep := range node.Dependencies {
			if dep == node.Name {
				return Plan{}, fmt.Errorf("worker %q depends on itself", node.Name)
			}
			if _, exists := nodes[dep]; !exists {
				return Plan{}, fmt.Errorf("worker %q: unknown dependency %q", node.Name, dep)
			}
		}
	}
	// Three-color DFS catches arbitrary cycles, with a deterministic path for diagnostics.
	colors := make(map[string]uint8, len(nodes))
	var path []string
	var visit func(string) error
	visit = func(name string) error {
		if colors[name] == 2 {
			return nil
		}
		if colors[name] == 1 {
			return fmt.Errorf("dependency cycle: %s", strings.Join(append(path, name), " -> "))
		}
		colors[name] = 1
		path = append(path, name)
		for _, dep := range nodes[name].Dependencies {
			if err := visit(dep); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		colors[name] = 2
		return nil
	}
	for _, node := range p.Nodes {
		if err := visit(node.Name); err != nil {
			return Plan{}, err
		}
	}
	p.Hash = hashCanonical(p)
	return p, nil
}

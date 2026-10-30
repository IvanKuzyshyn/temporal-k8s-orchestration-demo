// Package manifest extracts worker registrations from complete rendered manifests.
package manifest

import (
	"fmt"
	"io"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"go.yaml.in/yaml/v3"
)

const workerNamespaceLabel = "temporal-demo.io/worker-namespace"

type resource struct {
	Kind     string     `yaml:"kind"`
	Metadata yaml.Node  `yaml:"metadata"`
	Items    []resource `yaml:"items"`
}

type metadata struct {
	Name        string            `yaml:"name"`
	Namespace   string            `yaml:"namespace"`
	UID         string            `yaml:"uid"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

// Registrations reads the complete deployment set before selecting Services in
// labeled Namespaces. It rejects ambiguous resource definitions, but is not a
// Kubernetes schema validator. Call plan.Build on the result to validate the
// entire graph, canonicalize dependencies and calculate its execution hash.
func Registrations(r io.Reader) ([]plan.Registration, error) {
	namespaces := make(map[string]bool)
	serviceIDs := make(map[[2]string]bool)
	var services []metadata
	var collect func(resource) error
	collect = func(obj resource) error {
		if obj.Kind == "List" {
			for _, item := range obj.Items {
				if err := collect(item); err != nil {
					return err
				}
			}
			return nil
		}
		if obj.Kind != "Namespace" && obj.Kind != "Service" {
			return nil
		}
		var m metadata
		if err := obj.Metadata.Decode(&m); err != nil {
			return fmt.Errorf("decode %s metadata: %w", obj.Kind, err)
		}
		if obj.Kind == "Namespace" {
			if m.Name == "" {
				return fmt.Errorf("Namespace requires metadata.name")
			}
			if _, exists := namespaces[m.Name]; exists {
				return fmt.Errorf("duplicate Namespace %q", m.Name)
			}
			namespaces[m.Name] = m.Labels[workerNamespaceLabel] == "true"
		} else {
			id := [2]string{m.Namespace, m.Name}
			if serviceIDs[id] {
				return fmt.Errorf("duplicate Service %q in namespace %q", m.Name, m.Namespace)
			}
			serviceIDs[id] = true
			services = append(services, m)
		}
		return nil
	}

	decoder := yaml.NewDecoder(r)
	for document := 1; ; document++ {
		var obj resource
		if err := decoder.Decode(&obj); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode manifest document %d: %w", document, err)
		}
		if err := collect(obj); err != nil {
			return nil, fmt.Errorf("manifest document %d: %w", document, err)
		}
	}

	var registrations []plan.Registration
	for _, service := range services {
		if service.Annotations["temporal-worker"] != "true" {
			continue
		}
		if service.Namespace == "" {
			return nil, fmt.Errorf("worker Service %q requires an explicit namespace", service.Name)
		}
		selected, declared := namespaces[service.Namespace]
		if !declared {
			return nil, fmt.Errorf("worker Service %q: namespace %q is not declared", service.Name, service.Namespace)
		}
		if !selected {
			continue
		}
		registrations = append(registrations, plan.Registration{
			SchemaVersion: "1", WorkerName: service.Name,
			TaskQueue: service.Name + ".v1.temporal", WorkflowType: "ExampleChildWorkflow",
			RunAfter:          service.Annotations["temporal-worker-run-after"],
			SourceServiceName: service.Name, SourceServiceNamespace: service.Namespace,
			SourceServiceUID: service.UID,
		})
	}
	return registrations, nil
}

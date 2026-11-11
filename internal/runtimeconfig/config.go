// Package runtimeconfig validates process-only configuration, never workflow state.
package runtimeconfig

import (
	"fmt"
	"net"
	"strconv"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"k8s.io/apimachinery/pkg/labels"
)

type Config struct {
	TemporalAddress, TemporalNamespace string
	WorkerNamespaceSelector            string
	WorkerName, TaskQueue, HTTPAddr    string
}

func Load(role string, getenv func(string) string) (Config, error) {
	value := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	c := Config{TemporalAddress: value("TEMPORAL_ADDRESS", "temporal.temporal.svc.cluster.local:7233"), TemporalNamespace: value("TEMPORAL_NAMESPACE", "default"), WorkerNamespaceSelector: value("WORKER_NAMESPACE_SELECTOR", "temporal-demo.io/worker-namespace=true"), WorkerName: getenv("WORKER_NAME")}
	var err error
	if _, err := labels.Parse(c.WorkerNamespaceSelector); err != nil {
		return Config{}, fmt.Errorf("invalid WORKER_NAMESPACE_SELECTOR: %w", err)
	}
	switch role {
	case "worker":
		c.TaskQueue = value("TASK_QUEUE", c.WorkerName+".v1.temporal")
		c.HTTPAddr = value("HTTP_ADDR", ":8081")
		_, err = plan.Build([]plan.Registration{{SchemaVersion: "1", WorkerName: c.WorkerName, TaskQueue: c.TaskQueue, WorkflowType: "ExampleChildWorkflow", SourceServiceName: c.WorkerName, SourceServiceNamespace: "worker"}})
		if err != nil {
			return Config{}, fmt.Errorf("invalid worker identity/queue: %w", err)
		}
	case "orchestrator":
		c.TaskQueue = value("TASK_QUEUE", "orchestrator.v1.temporal")
		c.HTTPAddr = value("HTTP_ADDR", ":8080")
		if c.TaskQueue != "orchestrator.v1.temporal" {
			return Config{}, fmt.Errorf("TASK_QUEUE must be orchestrator.v1.temporal")
		}
	default:
		return Config{}, fmt.Errorf("unknown process role")
	}
	for _, addr := range []string{c.HTTPAddr, c.TemporalAddress} {
		_, port, e := net.SplitHostPort(addr)
		if e != nil {
			return Config{}, fmt.Errorf("invalid host:port configuration")
		}
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return Config{}, fmt.Errorf("invalid port configuration")
		}
	}
	return c, nil
}

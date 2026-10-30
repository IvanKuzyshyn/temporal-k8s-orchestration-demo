// Package runtimeconfig validates process-only configuration, never workflow state.
package runtimeconfig

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
)

type Config struct {
	TemporalAddress, TemporalNamespace                  string
	RegistryMode, RegistryFile, WorkerNamespaceSelector string
	WorkerName, TaskQueue, HTTPAddr                     string
	ActivityDuration                                    time.Duration
}

func Load(role string, getenv func(string) string) (Config, error) {
	value := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	c := Config{TemporalAddress: value("TEMPORAL_ADDRESS", "temporal:7233"), TemporalNamespace: value("TEMPORAL_NAMESPACE", "default"), RegistryMode: value("REGISTRY_MODE", "static"), RegistryFile: value("REGISTRY_FILE", "deploy/config/workers.json"), WorkerNamespaceSelector: getenv("WORKER_NAMESPACE_SELECTOR"), WorkerName: getenv("WORKER_NAME")}
	var err error
	c.ActivityDuration, err = time.ParseDuration(value("ACTIVITY_DURATION", "5s"))
	if err != nil || c.ActivityDuration <= 0 || c.ActivityDuration >= 60*time.Second {
		return Config{}, fmt.Errorf("ACTIVITY_DURATION must be positive and less than 60s")
	}
	switch c.RegistryMode {
	case "static":
	case "kubernetes":
		return Config{}, fmt.Errorf("REGISTRY_MODE=kubernetes is not wired until Task 9")
	default:
		return Config{}, fmt.Errorf("unknown REGISTRY_MODE")
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

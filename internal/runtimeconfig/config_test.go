package runtimeconfig

import (
	"testing"

	"github.com/onsi/gomega"
)

// These cases catch accepting deployments that cannot poll the intended queue,
// silently pretending Kubernetes discovery works, or losing bounded timing.
func TestConfig(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		env        map[string]string
		bad        bool
	}{
		{"worker defaults", "worker", map[string]string{"WORKER_NAME": "worker-a"}, false},
		{"missing worker", "worker", nil, true},
		{"invalid identity", "worker", map[string]string{"WORKER_NAME": "Bad_Name"}, true},
		{"mismatched queue", "worker", map[string]string{"WORKER_NAME": "worker-a", "TASK_QUEUE": "worker-b.v1.temporal"}, true},
		{"orchestrator defaults", "orchestrator", nil, false},
		{"bad parent queue", "orchestrator", map[string]string{"TASK_QUEUE": "other"}, true},
		{"kubernetes discovery", "orchestrator", map[string]string{"WORKER_NAMESPACE_SELECTOR": "temporal-demo.io/worker-namespace=true"}, false},
		{"invalid selector", "orchestrator", map[string]string{"WORKER_NAMESPACE_SELECTOR": "invalid in ("}, true},
		{"bad HTTP address", "worker", map[string]string{"WORKER_NAME": "worker-a", "HTTP_ADDR": "no-port"}, true},
		{"bad temporal address", "orchestrator", map[string]string{"TEMPORAL_ADDRESS": "no-port"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			c, err := Load(tc.role, func(k string) string { return tc.env[k] })
			if tc.bad {
				g.Expect(err).To(gomega.HaveOccurred())
				return
			}
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(c.TemporalAddress).To(gomega.Equal("temporal.temporal.svc.cluster.local:7233"))
			g.Expect(c.TemporalNamespace).To(gomega.Equal("default"))
			if tc.role == "worker" {
				g.Expect(c.TaskQueue).To(gomega.Equal("worker-a.v1.temporal"))
				g.Expect(c.HTTPAddr).To(gomega.Equal(":8081"))
			} else {
				g.Expect(c.TaskQueue).To(gomega.Equal("orchestrator.v1.temporal"))
				g.Expect(c.HTTPAddr).To(gomega.Equal(":8080"))
				g.Expect(c.WorkerNamespaceSelector).To(gomega.Equal("temporal-demo.io/worker-namespace=true"))
			}
		})
	}
}

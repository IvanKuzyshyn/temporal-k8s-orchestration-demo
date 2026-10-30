package runtimeconfig

import (
	"testing"
	"time"

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
		{"bad duration", "worker", map[string]string{"WORKER_NAME": "worker-a", "ACTIVITY_DURATION": "later"}, true},
		{"zero duration", "worker", map[string]string{"WORKER_NAME": "worker-a", "ACTIVITY_DURATION": "0s"}, true},
		{"negative duration", "worker", map[string]string{"WORKER_NAME": "worker-a", "ACTIVITY_DURATION": "-1s"}, true},
		{"exceeds activity timeout", "worker", map[string]string{"WORKER_NAME": "worker-a", "ACTIVITY_DURATION": "60s"}, true},
		{"orchestrator defaults", "orchestrator", nil, false},
		{"bad parent queue", "orchestrator", map[string]string{"TASK_QUEUE": "other"}, true},
		{"unknown mode", "orchestrator", map[string]string{"REGISTRY_MODE": "magic"}, true},
		{"kubernetes not wired", "orchestrator", map[string]string{"REGISTRY_MODE": "kubernetes", "WORKER_NAMESPACE_SELECTOR": "temporal-demo.io/worker-namespace=true"}, true},
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
			g.Expect(c.TemporalAddress).To(gomega.Equal("temporal:7233"))
			g.Expect(c.TemporalNamespace).To(gomega.Equal("default"))
			g.Expect(c.ActivityDuration).To(gomega.Equal(5 * time.Second))
			if tc.role == "worker" {
				g.Expect(c.TaskQueue).To(gomega.Equal("worker-a.v1.temporal"))
				g.Expect(c.HTTPAddr).To(gomega.Equal(":8081"))
			} else {
				g.Expect(c.TaskQueue).To(gomega.Equal("orchestrator.v1.temporal"))
				g.Expect(c.HTTPAddr).To(gomega.Equal(":8080"))
				g.Expect(c.RegistryFile).To(gomega.Equal("deploy/config/workers.json"))
			}
		})
	}
}

package chart

import (
	"bytes"
	"io"
	"testing"

	"github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
)

// Omitting scalers or changing queue/threshold/ownership settings must fail on
// real Helm output, not on a handwritten fixture.
func TestScalingRender(t *testing.T) {
	for _, bootstrap := range []string{"true", "false"} {
		t.Run(bootstrap, func(t *testing.T) {
			g := gomega.NewWithT(t)
			b, err := render(t, "--set", "scaling.bootstrapZero="+bootstrap)
			g.Expect(err).NotTo(gomega.HaveOccurred(), string(b))
			dec := yaml.NewDecoder(bytes.NewReader(b))
			scalers, workers := 0, 0
			for {
				var r map[string]any
				err := dec.Decode(&r)
				if err == io.EOF {
					break
				}
				g.Expect(err).NotTo(gomega.HaveOccurred())
				if r == nil {
					continue
				}
				kind := r["kind"]
				g.Expect(kind).NotTo(gomega.Equal("HorizontalPodAutoscaler"))
				meta := r["metadata"].(map[string]any)
				name := meta["name"].(string)
				spec, _ := r["spec"].(map[string]any)
				if kind == "Deployment" && name != "orchestrator" {
					workers++
					if bootstrap == "true" {
						g.Expect(spec["replicas"]).To(gomega.Equal(0))
					} else {
						g.Expect(spec).NotTo(gomega.HaveKey("replicas"))
					}
				}
				if kind != "ScaledObject" {
					continue
				}
				scalers++
				g.Expect(meta["namespace"]).To(gomega.Equal("temporal-demo-" + name))
				g.Expect(spec["scaleTargetRef"]).To(gomega.Equal(map[string]any{"name": name}))
				g.Expect(spec["minReplicaCount"]).To(gomega.Equal(0))
				g.Expect(spec["maxReplicaCount"]).To(gomega.Equal(1))
				g.Expect(spec["pollingInterval"]).To(gomega.Equal(5))
				g.Expect(spec["cooldownPeriod"]).To(gomega.Equal(120))
				triggers := spec["triggers"].([]any)
				g.Expect(triggers).To(gomega.HaveLen(1))
				trigger := triggers[0].(map[string]any)
				g.Expect(trigger["type"]).To(gomega.Equal("temporal"))
				g.Expect(trigger["metadata"]).To(gomega.Equal(map[string]any{
					"endpoint": "temporal.temporal.svc.cluster.local:7233", "namespace": "default", "taskQueue": name + ".v1.temporal", "queueTypes": "workflow,activity", "activationTargetQueueSize": "0", "targetQueueSize": "1", "enableTLS": "false", "includeRunningWorkflowCount": "false",
				}))
			}
			g.Expect(scalers).To(gomega.Equal(4))
			g.Expect(workers).To(gomega.Equal(scalers))
		})
	}
}

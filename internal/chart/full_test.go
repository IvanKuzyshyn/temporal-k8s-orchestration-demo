package chart

import (
	"bytes"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/manifest"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
	"io"
	"testing"
)

func TestFullDemoDefault(t *testing.T) {
	for _, bootstrap := range []string{"true", "false"} {
		t.Run(bootstrap, func(t *testing.T) {
			g := gomega.NewWithT(t)
			b, e := render(t, "--set", "scaling.bootstrapZero="+bootstrap)
			g.Expect(e).NotTo(gomega.HaveOccurred(), string(b))
			regs, e := manifest.Registrations(bytes.NewReader(b))
			g.Expect(e).NotTo(gomega.HaveOccurred())
			p, e := plan.Build(regs)
			g.Expect(e).NotTo(gomega.HaveOccurred())
			g.Expect(p.Hash).To(gomega.Equal("5fefcdcc52af25c9c21a523f1f3c1f7d928018051ffe89ac8b5a0e6ccf8c3b62"))
			d := yaml.NewDecoder(bytes.NewReader(b))
			counts := map[string]int{}
			for {
				var obj map[string]any
				e = d.Decode(&obj)
				if e == io.EOF {
					break
				}
				g.Expect(e).NotTo(gomega.HaveOccurred())
				if obj == nil {
					continue
				}
				kind := obj["kind"].(string)
				counts[kind]++
				meta := obj["metadata"].(map[string]any)
				name := meta["name"].(string)
				switch kind {
				case "Deployment":
					spec := obj["spec"].(map[string]any)
					pod := spec["template"].(map[string]any)["spec"].(map[string]any)
					g.Expect(pod["terminationGracePeriodSeconds"]).To(gomega.Equal(45))
					g.Expect(pod).To(gomega.HaveKey("securityContext"))
					c := pod["containers"].([]any)[0].(map[string]any)
					g.Expect(c).To(gomega.HaveKey("readinessProbe"))
					g.Expect(c).To(gomega.HaveKey("livenessProbe"))
					g.Expect(c).To(gomega.HaveKey("resources"))
					env := map[string]string{}
					for _, v := range c["env"].([]any) {
						m := v.(map[string]any)
						env[m["name"].(string)] = m["value"].(string)
					}
					g.Expect(env).NotTo(gomega.HaveKey("REGISTRY_MODE"))
					g.Expect(env).NotTo(gomega.HaveKey("REGISTRY_FILE"))
					g.Expect(env).NotTo(gomega.HaveKey("ACTIVITY_DURATION"))
					g.Expect(env["TEMPORAL_ADDRESS"]).To(gomega.Equal("temporal.temporal.svc.cluster.local:7233"))
					if name == "orchestrator" {
						g.Expect(spec["replicas"]).To(gomega.Equal(1))
						g.Expect(pod["serviceAccountName"]).To(gomega.Equal("orchestrator"))
						g.Expect(pod["automountServiceAccountToken"]).To(gomega.BeTrue())
						g.Expect(pod).NotTo(gomega.HaveKey("volumes"))
					} else {
						g.Expect(meta["namespace"]).To(gomega.Equal("temporal-demo-" + name))
						g.Expect(env["WORKER_NAME"]).To(gomega.Equal(name))
						g.Expect(env["TASK_QUEUE"]).To(gomega.Equal(name + ".v1.temporal"))
						g.Expect(pod["automountServiceAccountToken"]).To(gomega.BeFalse())
						if bootstrap == "true" {
							g.Expect(spec["replicas"]).To(gomega.Equal(0))
						} else {
							g.Expect(spec).NotTo(gomega.HaveKey("replicas"))
						}
					}
				case "ScaledObject":
					spec := obj["spec"].(map[string]any)
					g.Expect(meta["namespace"]).To(gomega.Equal("temporal-demo-" + name))
					g.Expect(spec["scaleTargetRef"]).To(gomega.Equal(map[string]any{"name": name}))
					g.Expect(spec["minReplicaCount"]).To(gomega.Equal(0))
					g.Expect(spec["maxReplicaCount"]).To(gomega.Equal(1))
					g.Expect(spec["pollingInterval"]).To(gomega.Equal(5))
					g.Expect(spec["cooldownPeriod"]).To(gomega.Equal(120))
					g.Expect(spec["triggers"]).To(gomega.Equal([]any{map[string]any{"type": "temporal", "metadata": map[string]any{
						"endpoint": "temporal.temporal.svc.cluster.local:7233", "namespace": "default", "taskQueue": name + ".v1.temporal",
						"queueTypes": "workflow,activity", "activationTargetQueueSize": "0", "targetQueueSize": "1", "enableTLS": "false", "includeRunningWorkflowCount": "false",
					}}}))
				case "Role", "ClusterRole":
					res := "configmaps"
					if kind == "ClusterRole" {
						res = "namespaces"
					}
					g.Expect(obj["rules"]).To(gomega.Equal([]any{map[string]any{"apiGroups": []any{""}, "resources": []any{res}, "verbs": []any{"get", "list", "watch"}}}))
				case "RoleBinding", "ClusterRoleBinding":
					g.Expect(obj["subjects"]).To(gomega.Equal([]any{map[string]any{"kind": "ServiceAccount", "name": "orchestrator", "namespace": "temporal-demo-orchestrator"}}))
				}
			}
			for kind, n := range map[string]int{"Deployment": 5, "Namespace": 5, "Service": 5, "ScaledObject": 4, "Role": 4, "RoleBinding": 4, "ClusterRole": 1, "ClusterRoleBinding": 1, "ServiceAccount": 1} {
				g.Expect(counts[kind]).To(gomega.Equal(n), kind)
			}
			g.Expect(counts["ConfigMap"]).To(gomega.BeZero())
			g.Expect(counts["HorizontalPodAutoscaler"]).To(gomega.BeZero())
		})
	}
}

func TestRemovedChartModesRejected(t *testing.T) {
	for _, setting := range []string{"registry.mode=static", "scaling.enabled=false", "replicas=1", "scaling.includeRunningWorkflowCount=true"} {
		t.Run(setting, func(t *testing.T) {
			b, e := render(t, "--set", setting)
			if e == nil {
				t.Fatalf("obsolete mode accepted: %s", b)
			}
		})
	}
}

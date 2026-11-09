package chart

import (
	"bytes"
	"io"
	"testing"

	"github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
)

// Real renders must constrain discovery credentials and grants to their scopes;
// static/scaling overlays must remain tokenless and need no discovery RBAC.
func TestDiscoveryRender(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "static", true: "kubernetes"}[live], func(t *testing.T) {
			g := gomega.NewWithT(t)
			args := []string{}
			if live {
				args = append(args, "-f", "../../k8s/worker-helm-chart/values-discovery.yaml")
			}
			b, err := render(t, args...)
			g.Expect(err).NotTo(gomega.HaveOccurred(), string(b))
			dec := yaml.NewDecoder(bytes.NewReader(b))
			counts := map[string]int{}
			for {
				var r struct {
					Kind     string
					Metadata struct{ Name, Namespace string }
					Rules    []map[string]any
					Subjects []map[string]any
					RoleRef  map[string]any `yaml:"roleRef"`
					Spec     struct {
						Replicas int
						Template struct {
							Spec struct {
								Automount  bool   `yaml:"automountServiceAccountToken"`
								SA         string `yaml:"serviceAccountName"`
								Volumes    []any
								Containers []struct {
									Env    []struct{ Name, Value string }
									Mounts []any `yaml:"volumeMounts"`
								}
							}
						}
					}
				}
				err := dec.Decode(&r)
				if err == io.EOF {
					break
				}
				g.Expect(err).NotTo(gomega.HaveOccurred())
				counts[r.Kind]++
				switch r.Kind {
				case "ServiceAccount":
					g.Expect(r.Metadata.Namespace).To(gomega.Equal("temporal-demo-orchestrator"))
					g.Expect(r.Metadata.Name).To(gomega.Equal("orchestrator"))
				case "ClusterRole", "Role":
					resource := "namespaces"
					if r.Kind == "Role" {
						resource = "configmaps"
						g.Expect(r.Metadata.Namespace).To(gomega.HavePrefix("temporal-demo-worker-"))
					}
					g.Expect(r.Rules).To(gomega.Equal([]map[string]any{{"apiGroups": []any{""}, "resources": []any{resource}, "verbs": []any{"get", "list", "watch"}}}))
				case "ClusterRoleBinding", "RoleBinding":
					g.Expect(r.Subjects).To(gomega.Equal([]map[string]any{{"kind": "ServiceAccount", "name": "orchestrator", "namespace": "temporal-demo-orchestrator"}}))
					g.Expect(r.RoleRef["name"]).To(gomega.Equal("temporal-demo-registry"))
				case "Deployment":
					s := r.Spec.Template.Spec
					if r.Metadata.Name == "orchestrator" {
						g.Expect(r.Spec.Replicas).To(gomega.Equal(1))
						g.Expect(s.Automount).To(gomega.Equal(live))
						env := map[string]string{}
						for _, e := range s.Containers[0].Env {
							env[e.Name] = e.Value
						}
						if live {
							g.Expect(s.SA).To(gomega.Equal("orchestrator"))
							g.Expect(env["REGISTRY_MODE"]).To(gomega.Equal("kubernetes"))
							g.Expect(env["WORKER_NAMESPACE_SELECTOR"]).To(gomega.Equal("temporal-demo.io/worker-namespace=true"))
							g.Expect(env).NotTo(gomega.HaveKey("REGISTRY_FILE"))
							g.Expect(s.Volumes).To(gomega.BeEmpty())
							g.Expect(s.Containers[0].Mounts).To(gomega.BeEmpty())
						} else {
							g.Expect(env["REGISTRY_MODE"]).To(gomega.Equal("static"))
							g.Expect(s.Volumes).To(gomega.HaveLen(1))
						}
					} else {
						g.Expect(s.Automount).To(gomega.BeFalse())
						g.Expect(r.Spec.Replicas).To(gomega.Equal(1))
					}
				}
			}
			if live {
				g.Expect(counts["ServiceAccount"]).To(gomega.Equal(1))
				g.Expect(counts["ClusterRole"]).To(gomega.Equal(1))
				g.Expect(counts["ClusterRoleBinding"]).To(gomega.Equal(1))
				g.Expect(counts["Role"]).To(gomega.Equal(4))
				g.Expect(counts["RoleBinding"]).To(gomega.Equal(4))
				g.Expect(counts["ConfigMap"]).To(gomega.BeZero())
			} else {
				for _, kind := range []string{"ServiceAccount", "ClusterRole", "ClusterRoleBinding", "Role", "RoleBinding"} {
					g.Expect(counts[kind]).To(gomega.BeZero())
				}
				g.Expect(counts["ConfigMap"]).To(gomega.Equal(1))
			}
			g.Expect(counts["ScaledObject"]).To(gomega.BeZero())
		})
	}
}

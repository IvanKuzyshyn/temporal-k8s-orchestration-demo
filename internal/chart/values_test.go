package chart

import (
	"bytes"
	"encoding/json"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/manifest"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
	"io"
	"testing"
)

// Renaming values must update Services, queue env, namespaces and static
// registrations together; a copied default identity in any template breaks this.
func TestValuesDriveAllIdentities(t *testing.T) {
	g := gomega.NewWithT(t)
	b, err := render(t, "--set-json", `workers=[{"name":"alpha","runAfter":""},{"name":"beta","runAfter":"alpha"}]`)
	g.Expect(err).NotTo(gomega.HaveOccurred(), string(b))
	regs, err := manifest.Registrations(bytes.NewReader(b))
	g.Expect(err).NotTo(gomega.HaveOccurred())
	p, err := plan.Build(regs)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(p.Nodes).To(gomega.HaveLen(2))
	g.Expect(p.Nodes[0].Name).To(gomega.Equal("alpha"))
	g.Expect(p.Nodes[1].Name).To(gomega.Equal("beta"))
	g.Expect(p.Nodes[1].Dependencies).To(gomega.Equal([]string{"alpha"}))
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	deployments := map[string]string{}
	var static []plan.Registration
	for {
		var r resource
		err := decoder.Decode(&r)
		if err == io.EOF {
			break
		}
		g.Expect(err).NotTo(gomega.HaveOccurred())
		if r.Kind == "ConfigMap" && r.Metadata.Name == "static-registry" {
			g.Expect(json.Unmarshal([]byte(r.Data["workers.json"]), &static)).To(gomega.Succeed())
		}
		if r.Kind != "Deployment" || r.Metadata.Name == "orchestrator" {
			continue
		}
		g.Expect(r.Metadata.Namespace).To(gomega.Equal("temporal-demo-" + r.Metadata.Name))
		for _, e := range r.Spec.Template.Spec.Containers[0].Env {
			if e.Name == "TASK_QUEUE" {
				deployments[r.Metadata.Name] = e.Value
			}
			if e.Name == "WORKER_NAME" {
				g.Expect(e.Value).To(gomega.Equal(r.Metadata.Name))
			}
		}
	}
	g.Expect(deployments).To(gomega.Equal(map[string]string{"alpha": "alpha.v1.temporal", "beta": "beta.v1.temporal"}))
	sp, err := plan.Build(static)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(sp).To(gomega.Equal(p))
}

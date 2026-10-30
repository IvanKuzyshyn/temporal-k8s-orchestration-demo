package chart

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
)

// Plain per-component files must be usable Kubernetes resources (kind input is
// separate), contain the controllers/CRDs and preserve Temporal's tested inputs.
func TestInfrastructureAndBlockYAML(t *testing.T) {
	g := gomega.NewWithT(t)
	for _, file := range []string{"temporal", "keda", "kyverno"} {
		b, err := os.ReadFile("../../k8s/infrastructure/" + file + ".yaml")
		g.Expect(err).NotTo(gomega.HaveOccurred(), "complete per-component manifest: "+file)
		docs := yaml.NewDecoder(bytes.NewReader(b))
		identities := map[string]bool{}
		configs := map[string]string{}
		images := map[string]bool{}
		for {
			var node yaml.Node
			err := docs.Decode(&node)
			if err == io.EOF {
				break
			}
			g.Expect(err).NotTo(gomega.HaveOccurred())
			assertBlock(t, &node)
			var r resource
			g.Expect(node.Decode(&r)).To(gomega.Succeed())
			identity := r.Kind + "/" + r.Metadata.Name
			identities[identity] = true
			phase := "controller"
			if r.Kind == "Namespace" {
				phase = "namespace"
			} else if r.Kind == "CustomResourceDefinition" {
				phase = "crds"
			} else if file == "temporal" {
				phase = map[string]string{"ConfigMap/postgres-init": "postgres", "ConfigMap/temporal-scripts": "postgres", "ConfigMap/temporal-config": "postgres", "Service/postgres": "postgres", "StatefulSet/postgres": "postgres", "Job/temporal-schema": "schema", "Service/temporal": "server", "Deployment/temporal": "server", "Job/temporal-namespace": "namespace-job", "Service/temporal-ui": "ui", "Deployment/temporal-ui": "ui"}[identity]
			}
			g.Expect(phase).NotTo(gomega.BeEmpty(), identity)
			g.Expect(r.Metadata.Labels["temporal-demo.io/phase"]).To(gomega.Equal(phase), identity)
			g.Expect(r.Metadata.Annotations["helm.sh/hook"]).To(gomega.BeEmpty(), "Helm lifecycle hooks must not execute during apply")
			if file != "temporal" && r.Kind == "Deployment" {
				g.Expect(r.Spec.Replicas).To(gomega.Equal(1), identity)
			}
			for _, c := range r.Spec.Template.Spec.Containers {
				images[c.Image] = true
			}
			for _, c := range r.Spec.Template.Spec.InitContainers {
				images[c.Image] = true
			}
			if r.Kind == "ConfigMap" {
				for key, value := range r.Data {
					configs[key] = value
				}
			}
			g.Expect(r.Kind).NotTo(gomega.Equal("Cluster"), "kind input must not be applied")
		}
		required := map[string][]string{
			"temporal": {"Namespace/temporal", "StatefulSet/postgres", "Job/temporal-schema", "Job/temporal-namespace", "Deployment/temporal", "Deployment/temporal-ui"},
			"keda":     {"Namespace/keda", "CustomResourceDefinition/scaledobjects.keda.sh", "Deployment/keda-operator", "Deployment/keda-operator-metrics-apiserver", "Deployment/keda-admission-webhooks"},
			"kyverno":  {"Namespace/kyverno", "CustomResourceDefinition/clusterpolicies.kyverno.io", "Deployment/kyverno-admission-controller", "Deployment/kyverno-background-controller", "Deployment/kyverno-cleanup-controller", "Deployment/kyverno-reports-controller"},
		}
		for _, id := range required[file] {
			g.Expect(identities[id]).To(gomega.BeTrue(), id)
		}
		if file == "temporal" {
			for key, path := range map[string]string{"10-visibility.sh": "init-db.sh", "schema.sh": "schema.sh", "namespace.sh": "namespace.sh", "config.yaml": "config.yaml"} {
				original, err := os.ReadFile("../../deploy/temporal/" + path)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(configs[key]).To(gomega.Equal(string(original)), "same tested Compose input: "+key)
			}
		}
		pins := map[string][]string{
			"temporal": {"postgres:16.15@sha256:9e2cf6287f25c3151f4c9421ccce2b17f3e60061c111a29165e0d2cdd9ff0a97", "temporalio/server:1.32.0@sha256:c3e752127759616bb1615e0f9ba0e21635aeb5fdeb922de4f371c350955f46ae", "temporalio/admin-tools:1.32.0@sha256:a9f84fb9a374b2374fe2e67c8efc0468ff3f1c66c8a0b14597ec86e349e62bca", "temporalio/ui:2.48.0@sha256:5bc8712de27029b64a9cad156221b082a43ae27bfb108fc1743076eaabf1437f"},
			"keda":     {"ghcr.io/kedacore/keda:2.21.0@sha256:81fe6547ce8d1cc29273f887b76507e9b853e5bc1f875ec23fe60061a38ed809", "ghcr.io/kedacore/keda-metrics-apiserver:2.21.0@sha256:255375037fe592732eeb554743aae360c2da88cf0b9b1258d0a987ef885f0881", "ghcr.io/kedacore/keda-admission-webhooks:2.21.0@sha256:e1969628cca6123e32eea13c47c75817091af2657fa68d9d0d58a8620d1b75b1"},
			"kyverno":  {"reg.kyverno.io/kyverno/kyvernopre:v1.19.2@sha256:27f3cc52e628559b56d08363b85316819802086c9808606914f7805a244148f4", "reg.kyverno.io/kyverno/kyverno:v1.19.2@sha256:c8c4dd09e720d2cd7fd24ca73ac28e46651001372f988e0cba3f2f612f04b955", "reg.kyverno.io/kyverno/background-controller:v1.19.2@sha256:c46e124f21ef8a6bb4b48de57106808b1007edcd5ddd4ff7df5d23b9efa60ce2", "reg.kyverno.io/kyverno/cleanup-controller:v1.19.2@sha256:d7340aa7c9315080c3dafa7b765d090ced37353e89e5cd7a3f9ac69e8fb532b2", "reg.kyverno.io/kyverno/reports-controller:v1.19.2@sha256:5fb6d3e738050dbce84f60ecabaede7665fbe0578d07c1e8c912443b035e79c3"},
		}
		g.Expect(images).To(gomega.HaveLen(len(pins[file])))
		for _, pin := range pins[file] {
			g.Expect(images).To(gomega.HaveKey(pin))
		}
	}
	b, err := render(t)
	g.Expect(err).NotTo(gomega.HaveOccurred(), string(b))
	d := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var n yaml.Node
		err := d.Decode(&n)
		if err == io.EOF {
			break
		}
		g.Expect(err).NotTo(gomega.HaveOccurred())
		assertBlock(t, &n)
	}
}

func assertBlock(t *testing.T, n *yaml.Node) {
	t.Helper()
	if (n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode) && len(n.Content) > 0 && n.Style&yaml.FlowStyle != 0 {
		t.Errorf("nonempty flow collection at line %d", n.Line)
	}
	for _, child := range n.Content {
		assertBlock(t, child)
	}
}

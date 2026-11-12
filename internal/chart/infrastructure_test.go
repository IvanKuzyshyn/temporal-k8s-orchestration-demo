package chart

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
)

// Committed inputs and rendered controllers must contain complete usable resources
// (kind input is separate), pinned images/CRDs and Temporal's tested inputs.
func TestInfrastructureAndBlockYAML(t *testing.T) {
	g := gomega.NewWithT(t)
	for _, file := range []string{"temporal", "keda", "kyverno"} {
		b, err := os.ReadFile("../../k8s/infrastructure/" + file + ".yaml")
		if file != "temporal" {
			g.Expect(os.IsNotExist(err)).To(gomega.BeTrue(), "generated bundle must not be committed")
			cmd := exec.Command("bash", "scripts/render-controller.sh", file)
			cmd.Dir = "../.."
			b, err = cmd.Output()
		}
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
			if file == "temporal" {
				assertBlock(t, &node)
			}
			var r resource
			g.Expect(node.Decode(&r)).To(gomega.Succeed())
			if r.Kind == "" { // Upstream Helm output can contain empty documents.
				continue
			}
			identity := r.Kind + "/" + r.Metadata.Name
			identities[identity] = true
			if r.Kind == "CustomResourceDefinition" && (r.Metadata.Name == "validatingpolicies.policies.kyverno.io" || r.Metadata.Name == "generatingpolicies.policies.kyverno.io") {
				var crd struct {
					Spec struct {
						Versions []struct {
							Name   string
							Served bool
						}
					}
				}
				g.Expect(node.Decode(&crd)).To(gomega.Succeed())
				served := false
				for _, version := range crd.Spec.Versions {
					served = served || (version.Name == "v1" && version.Served)
				}
				g.Expect(served).To(gomega.BeTrue(), identity+" must serve the CEL policy API")
			}
			phase := "controller"
			if r.Kind == "Namespace" {
				phase = "namespace"
			} else if r.Kind == "CustomResourceDefinition" {
				phase = "crds"
			} else if file == "temporal" {
				phase = map[string]string{"ConfigMap/postgres-init": "postgres", "ConfigMap/temporal-scripts": "postgres", "ConfigMap/temporal-config": "postgres", "Service/postgres": "postgres", "StatefulSet/postgres": "postgres", "Job/temporal-schema": "schema", "Service/temporal": "server", "Deployment/temporal": "server", "Job/temporal-namespace": "namespace-job", "Service/temporal-ui": "ui", "Deployment/temporal-ui": "ui"}[identity]
			}
			g.Expect(phase).NotTo(gomega.BeEmpty(), identity)
			if file == "keda" && r.Kind == "CustomResourceDefinition" {
				g.Expect(r.Metadata.Labels["app.kubernetes.io/part-of"]).To(gomega.Equal("keda-operator"))
				g.Expect(r.Metadata.Labels).NotTo(gomega.HaveKey("app.kubernetes.io/instance"), "native CRD selector must exclude controllers")
			} else if file == "kyverno" {
				g.Expect(r.Metadata.Labels["temporal-demo.io/phase"] == "crds").To(gomega.Equal(r.Kind == "CustomResourceDefinition"), identity)
				g.Expect(r.Metadata.Labels["temporal-demo.io/phase"] == "namespace").To(gomega.Equal(r.Kind == "Namespace"), identity)
			} else {
				g.Expect(r.Metadata.Labels["temporal-demo.io/phase"]).To(gomega.Equal(phase), identity)
			}
			if file == "keda" && r.Kind != "Namespace" && r.Kind != "CustomResourceDefinition" {
				g.Expect(r.Metadata.Labels["app.kubernetes.io/instance"]).To(gomega.Equal("keda"), "native CRD selector must exclude every controller")
			}
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
			"kyverno":  {"Namespace/kyverno", "CustomResourceDefinition/clusterpolicies.kyverno.io", "CustomResourceDefinition/validatingpolicies.policies.kyverno.io", "CustomResourceDefinition/generatingpolicies.policies.kyverno.io", "Deployment/kyverno-admission-controller", "Deployment/kyverno-background-controller", "Deployment/kyverno-cleanup-controller", "Deployment/kyverno-reports-controller"},
		}
		if file == "keda" {
			g.Expect(identities).To(gomega.HaveLen(30), "complete chart resources plus Namespace")
			crds := 0
			for id := range identities {
				if strings.HasPrefix(id, "CustomResourceDefinition/") {
					crds++
				}
			}
			g.Expect(crds).To(gomega.Equal(6))
		}
		if file == "kyverno" {
			g.Expect(identities).To(gomega.HaveLen(70), "include every upstream resource plus Namespace")
			crds := 0
			for id := range identities {
				if strings.HasPrefix(id, "CustomResourceDefinition/") {
					crds++
				}
			}
			g.Expect(crds).To(gomega.Equal(22), "both upstream CRD subcharts")
		}
		for _, id := range required[file] {
			g.Expect(identities[id]).To(gomega.BeTrue(), id)
		}
		if file == "temporal" {
			for _, key := range []string{"10-visibility.sh", "schema.sh", "namespace.sh", "config.yaml"} {
				g.Expect(configs[key]).NotTo(gomega.BeEmpty(), "required Kubernetes bootstrap input: "+key)
			}
			g.Expect(configs["schema.sh"]).To(gomega.ContainSubstring("temporal-sql-tool"))
			g.Expect(configs["namespace.sh"]).To(gomega.ContainSubstring("--retention 72h"))
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

// A brief PostgreSQL Service outage must permit bounded native Job retries,
// without restarting containers or extending the bootstrap's time budget.
func TestSchemaJobBoundedRetries(t *testing.T) {
	g := gomega.NewWithT(t)
	b, err := os.ReadFile("../../k8s/infrastructure/temporal.yaml")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	d := yaml.NewDecoder(bytes.NewReader(b))
	found := 0
	for {
		var job struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				BackoffLimit          int `yaml:"backoffLimit"`
				ActiveDeadlineSeconds int `yaml:"activeDeadlineSeconds"`
				Template              struct {
					Spec struct {
						RestartPolicy string `yaml:"restartPolicy"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		err := d.Decode(&job)
		if err == io.EOF {
			break
		}
		g.Expect(err).NotTo(gomega.HaveOccurred())
		if job.Kind != "Job" || job.Metadata.Name != "temporal-schema" {
			continue
		}
		found++
		g.Expect(job.Spec.BackoffLimit).To(gomega.Equal(2), "permit two native retries after initial connection failure")
		g.Expect(job.Spec.ActiveDeadlineSeconds).To(gomega.Equal(240), "keep the existing total Job deadline")
		g.Expect(job.Spec.Template.Spec.RestartPolicy).To(gomega.Equal("Never"))
	}
	g.Expect(found).To(gomega.Equal(1))
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

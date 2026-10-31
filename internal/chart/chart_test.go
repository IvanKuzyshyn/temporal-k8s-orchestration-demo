// Package chart tests real Helm output at the application boundary, without a cluster.
package chart

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/manifest"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/registry"
	"github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"
)

type resource struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Namespace   string            `yaml:"namespace"`
		Labels      map[string]string `yaml:"labels"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
	Spec struct {
		Replicas int `yaml:"replicas"`
		Template struct {
			Spec struct {
				TerminationGracePeriodSeconds int `yaml:"terminationGracePeriodSeconds"`
				InitContainers                []struct {
					Image string `yaml:"image"`
				} `yaml:"initContainers"`
				Containers []struct {
					Name  string `yaml:"name"`
					Image string `yaml:"image"`
					Env   []struct {
						Name  string `yaml:"name"`
						Value string `yaml:"value"`
					} `yaml:"env"`
					ReadinessProbe any `yaml:"readinessProbe"`
					LivenessProbe  any `yaml:"livenessProbe"`
					Resources      any `yaml:"resources"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func render(t *testing.T, extra ...string) ([]byte, error) {
	t.Helper()
	args := []string{"template", "temporal-demo", "../../k8s/worker-helm-chart", "-f", "../../k8s/worker-helm-chart/values-static.yaml", "--kube-version", "1.35.8", "--set", "images.tag=test-immutable"}
	return exec.Command("helm", append(args, extra...)...).CombinedOutput()
}

// Queue drift, missing Namespace labels, a worker omitted from the static
// registry, or reintroducing ScaledObjects must fail on the actual render.
func TestStaticRender(t *testing.T) {
	g := gomega.NewWithT(t)
	b, err := render(t)
	g.Expect(err).NotTo(gomega.HaveOccurred(), string(b))
	registrations, err := manifest.Registrations(bytes.NewReader(b))
	g.Expect(err).NotTo(gomega.HaveOccurred())
	p, err := plan.Build(registrations)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	original, err := registry.LoadStatic("../../deploy/config/workers.json")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(p).To(gomega.Equal(original.Snapshot().Plan))
	path := filepath.Join(t.TempDir(), "rendered.yaml")
	g.Expect(os.WriteFile(path, b, 0600)).To(gomega.Succeed())
	cmd := exec.Command("go", "run", "../../cmd/validate-graph")
	cmd.Stdin = bytes.NewReader(b)
	hash, err := cmd.CombinedOutput()
	g.Expect(err).NotTo(gomega.HaveOccurred(), string(hash))
	g.Expect(strings.TrimSpace(string(hash))).To(gomega.Equal(p.Hash))
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	namespaces := map[string]bool{}
	deployments := map[string]string{}
	services := map[string]string{}
	var staticJSON string
	for {
		var r resource
		err := decoder.Decode(&r)
		if err == io.EOF {
			break
		}
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(r.Kind).NotTo(gomega.Equal("ScaledObject"))
		if r.Kind == "Namespace" {
			namespaces[r.Metadata.Name] = r.Metadata.Labels["temporal-demo.io/worker-namespace"] == "true"
			continue
		}
		g.Expect(r.Metadata.Namespace).NotTo(gomega.BeEmpty(), r.Kind+"/"+r.Metadata.Name)
		switch r.Kind {
		case "Service":
			if r.Metadata.Annotations["temporal-worker"] == "true" {
				services[r.Metadata.Name] = r.Metadata.Namespace
				g.Expect(r.Metadata.Annotations["temporal-worker-run-after"]).To(gomega.Equal(map[string]string{"worker-a": "", "worker-b": "worker-a", "worker-c": "worker-a", "worker-d": "worker-b,worker-c"}[r.Metadata.Name]))
			}
		case "Deployment":
			g.Expect(r.Spec.Replicas).To(gomega.Equal(1))
			g.Expect(r.Spec.Template.Spec.TerminationGracePeriodSeconds).To(gomega.Equal(45))
			g.Expect(r.Spec.Template.Spec.Containers).To(gomega.HaveLen(1))
			c := r.Spec.Template.Spec.Containers[0]
			env := map[string]string{}
			for _, e := range c.Env {
				env[e.Name] = e.Value
			}
			g.Expect(env["TEMPORAL_ADDRESS"]).To(gomega.Equal("temporal.temporal.svc.cluster.local:7233"))
			g.Expect(c.ReadinessProbe).NotTo(gomega.BeNil())
			g.Expect(c.LivenessProbe).NotTo(gomega.BeNil())
			g.Expect(c.Resources).NotTo(gomega.BeNil())
			if r.Metadata.Name == "orchestrator" {
				g.Expect(r.Metadata.Namespace).To(gomega.Equal("temporal-demo-orchestrator"))
				g.Expect(env["REGISTRY_MODE"]).To(gomega.Equal("static"))
			} else {
				deployments[r.Metadata.Name] = r.Metadata.Namespace
				g.Expect(env["WORKER_NAME"]).To(gomega.Equal(r.Metadata.Name))
				g.Expect(env["TASK_QUEUE"]).To(gomega.Equal(r.Metadata.Name + ".v1.temporal"))
			}
		case "ConfigMap":
			if r.Metadata.Name == "static-registry" {
				staticJSON = r.Data["workers.json"]
			}
		}
	}
	g.Expect(namespaces).To(gomega.HaveLen(5))
	g.Expect(deployments).To(gomega.HaveLen(4))
	g.Expect(services).To(gomega.Equal(deployments))
	for name, ns := range deployments {
		g.Expect(ns).To(gomega.Equal("temporal-demo-" + name))
		g.Expect(namespaces[ns]).To(gomega.BeTrue())
	}
	var regs []plan.Registration
	g.Expect(json.Unmarshal([]byte(staticJSON), &regs)).To(gomega.Succeed())
	sp, err := plan.Build(regs)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(sp).To(gomega.Equal(p))
}

// Invalid values must fail instead of quietly dropping a worker, duplicating a
// namespace, accepting mutable images, or allowing an independently maintained queue.
func TestInvalidValuesFail(t *testing.T) {
	for _, arg := range []string{"workers=[]", "workers[0].name=Bad_Name", "images.tag=latest", "workers[0].taskQueue=drift", "replicas=0"} {
		t.Run(arg, func(t *testing.T) {
			g := gomega.NewWithT(t)
			b, err := render(t, "--set", arg)
			g.Expect(err).To(gomega.HaveOccurred(), string(b))
		})
	}
	t.Run("empty worker array", func(t *testing.T) {
		g := gomega.NewWithT(t)
		b, err := render(t, "--set-json", "workers=[]")
		g.Expect(err).To(gomega.HaveOccurred(), string(b))
	})
	t.Run("reserved orchestrator namespace", func(t *testing.T) {
		g := gomega.NewWithT(t)
		b, err := render(t, "--set-json", `workers=[{"name":"orchestrator","runAfter":""}]`)
		g.Expect(err).To(gomega.HaveOccurred(), string(b))
	})
	t.Run("complete duplicate identities", func(t *testing.T) {
		g := gomega.NewWithT(t)
		b, err := render(t, "--set-json", `workers=[{"name":"worker-a","runAfter":""},{"name":"worker-a","runAfter":""}]`)
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(string(b)).To(gomega.ContainSubstring("duplicate worker identity"))
	})
}

// Strict validation must reject unknown CRDs, not skip missing schemas.
func TestUnknownKindFails(t *testing.T) {
	g := gomega.NewWithT(t)
	path := filepath.Join(t.TempDir(), "unknown.yaml")
	g.Expect(os.WriteFile(path, []byte("apiVersion: imaginary.invalid/v1\nkind: Unknown\nmetadata:\n  name: must-fail\n  namespace: default\n"), 0600)).To(gomega.Succeed())
	cmd := exec.Command("bash", "../../scripts/validate-manifests.sh", path)
	b, err := cmd.CombinedOutput()
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(string(b)).To(gomega.ContainSubstring("could not find schema"))
}

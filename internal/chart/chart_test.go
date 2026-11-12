// Package chart tests rendered deployment resources without a cluster.
package chart

import (
	"github.com/onsi/gomega"
	"os/exec"
	"testing"
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
	args := []string{"template", "temporal-demo", "../../k8s/worker-helm-chart", "--kube-version", "1.35.8", "--set", "images.tag=test-immutable"}
	return exec.Command("helm", append(args, extra...)...).CombinedOutput()
}

func TestInvalidValuesFail(t *testing.T) {
	for _, arg := range []string{"workers=[]", "workers[0].name=Bad_Name", "images.tag=latest", "workers[0].taskQueue=drift", "replicas=0"} {
		t.Run(arg, func(t *testing.T) {
			b, e := render(t, "--set", arg)
			gomega.NewWithT(t).Expect(e).To(gomega.HaveOccurred(), string(b))
		})
	}
	for _, workers := range []string{`[]`, `[{"name":"orchestrator","runAfter":""}]`} {
		t.Run(workers, func(t *testing.T) {
			b, e := render(t, "--set-json", "workers="+workers)
			gomega.NewWithT(t).Expect(e).To(gomega.HaveOccurred(), string(b))
		})
	}
	t.Run("duplicate identities", func(t *testing.T) {
		b, e := render(t, "--set-json", `workers=[{"name":"worker-a","runAfter":""},{"name":"worker-a","runAfter":""}]`)
		g := gomega.NewWithT(t)
		g.Expect(e).To(gomega.HaveOccurred())
		g.Expect(string(b)).To(gomega.ContainSubstring("duplicate worker identity"))
	})
}

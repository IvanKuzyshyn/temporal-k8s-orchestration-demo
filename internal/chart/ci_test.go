package chart

import (
	"os"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestWorkflowTriggers(t *testing.T) {
	for _, name := range []string{"validate.yaml", "kind-integration.yaml"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("../../.github/workflows/" + name)
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				On map[string]struct {
					Branches []string `yaml:"branches"`
				} `yaml:"on"`
			}
			if err := yaml.Unmarshal(data, &workflow); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(workflow.On["push"].Branches, []string{"main"}) {
				t.Fatal("push trigger must target only main")
			}
			for _, event := range []string{"pull_request", "workflow_dispatch"} {
				if _, ok := workflow.On[event]; !ok {
					t.Fatalf("missing %s trigger", event)
				}
			}
		})
	}
}

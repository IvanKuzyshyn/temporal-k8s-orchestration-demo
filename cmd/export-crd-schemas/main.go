// export-crd-schemas extracts an exact structural schema from a pinned chart's
// complete render. It is a maintenance tool, not a cluster API client.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"go.yaml.in/yaml/v3"
)

func extract(input io.Reader, output io.Writer, group, kind, version string) error {
	decoder := yaml.NewDecoder(input)
	for {
		var r struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Group string `yaml:"group"`
				Names struct {
					Kind string `yaml:"kind"`
				} `yaml:"names"`
				Versions []struct {
					Name   string `yaml:"name"`
					Schema struct {
						OpenAPI map[string]any `yaml:"openAPIV3Schema"`
					} `yaml:"schema"`
				} `yaml:"versions"`
			} `yaml:"spec"`
		}
		err := decoder.Decode(&r)
		if err == io.EOF {
			return fmt.Errorf("schema not found: %s/%s/%s", group, kind, version)
		}
		if err != nil {
			return err
		}
		if r.Kind != "CustomResourceDefinition" || r.Spec.Group != group || r.Spec.Names.Kind != kind {
			continue
		}
		for _, v := range r.Spec.Versions {
			if v.Name == version && v.Schema.OpenAPI != nil {
				return json.NewEncoder(output).Encode(v.Schema.OpenAPI)
			}
		}
	}
}
func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: export-crd-schemas GROUP KIND VERSION < chart-render.yaml")
		os.Exit(2)
	}
	if err := extract(os.Stdin, os.Stdout, os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

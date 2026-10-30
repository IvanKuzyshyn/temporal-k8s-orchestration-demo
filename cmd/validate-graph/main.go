// validate-graph checks a complete rendered deployment set from stdin.
package main

import (
	"fmt"
	"os"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/manifest"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
)

func main() {
	registrations, err := manifest.Registrations(os.Stdin)
	if err == nil {
		var p plan.Plan
		p, err = plan.Build(registrations)
		if err == nil {
			fmt.Println(p.Hash)
			return
		}
	}
	fmt.Fprintln(os.Stderr, "validate-graph:", err)
	os.Exit(1)
}

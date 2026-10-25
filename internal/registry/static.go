package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
)

type static struct{ snapshot Snapshot }

// LoadStatic loads one JSON array of schema-1 registrations. It fails closed on
// malformed input or invalid graphs; it does not watch subsequent file changes.
func LoadStatic(path string) (Provider, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open static registry: %w", err)
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var registrations []plan.Registration
	if err := decoder.Decode(&registrations); err != nil {
		return nil, fmt.Errorf("decode static registry: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return nil, fmt.Errorf("trailing static registry data: %w", err)
	}
	p, err := plan.Build(registrations)
	if err != nil {
		return nil, fmt.Errorf("validate static registry: %w", err)
	}
	return &static{snapshot: Snapshot{Plan: p, Synced: true}}, nil
}

func (s *static) Snapshot() Snapshot { return cloneSnapshot(s.snapshot) }

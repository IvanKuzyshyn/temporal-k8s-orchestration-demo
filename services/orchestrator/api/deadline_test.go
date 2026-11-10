package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/api"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/runs"
	"github.com/onsi/gomega"
)

type unavailableGateway struct{ t *testing.T }

func (f unavailableGateway) unavailable(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		f.t.Error("Temporal API request has no server-side deadline")
		return runs.ErrUnavailable
	}
	if remaining := time.Until(deadline); remaining > 2*time.Second {
		f.t.Errorf("unbounded Temporal API deadline: %s", remaining)
	}
	<-ctx.Done()
	return runs.ErrUnavailable
}
func (f unavailableGateway) Start(ctx context.Context, _ string, _ contracts.ParentInput) (runs.Run, error) {
	return runs.Run{}, f.unavailable(ctx)
}
func (f unavailableGateway) Get(ctx context.Context, _ string) (runs.Execution, error) {
	return runs.Execution{}, f.unavailable(ctx)
}

func TestTemporalUnavailableRequestDeadline(t *testing.T) {
	for _, tc := range []struct{ name, method, path, key string }{
		{"unkeyed start", "POST", "/runs", ""},
		{"durable lookup before keyed start", "POST", "/runs", "key"},
		{"status lookup", "GET", "/runs/existing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			p, _, _ := fixture(t)
			h := api.New(p, runs.New(p, unavailableGateway{t}), nil)
			before := time.Now()
			keys := []string(nil)
			if tc.key != "" {
				keys = []string{tc.key}
			}
			response := request(h, tc.method, tc.path, "", keys...)
			g.Expect(response.Code).To(gomega.Equal(http.StatusServiceUnavailable))
			g.Expect(time.Since(before)).To(gomega.BeNumerically("<", 3*time.Second))
		})
	}
}

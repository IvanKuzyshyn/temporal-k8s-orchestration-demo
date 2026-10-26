// Package process owns HTTP, SDK worker and connection lifecycles for both binaries.
package process

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/registry"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/runtimeconfig"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/api"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/runs"
	parent "github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/workflows"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/worker/activities"
	child "github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/worker/workflows"
	"github.com/go-chi/chi/v5"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	sdklog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

type health struct {
	polling atomic.Bool
	check   func(context.Context) error
}

func (h *health) ready(ctx context.Context) error {
	if !h.polling.Load() {
		return errors.New("not polling")
	}
	return h.check(ctx)
}

// guard rejects new runs before polling starts and as soon as shutdown begins.
func (h *health) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/runs" && !h.polling.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *health) router() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.ready(ctx); err != nil || ctx.Err() != nil {
			http.Error(w, "not ready", 503)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return r
}
func retry(ctx context.Context, delay time.Duration, attempt func(context.Context) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := attempt(ctx); err == nil {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func Run(ctx context.Context, role string, c runtimeconfig.Config, logger *slog.Logger) error {
	logger = logger.With("role", role, "worker", c.WorkerName, "taskQueue", c.TaskQueue, "namespace", c.TemporalNamespace)
	var provider registry.Provider
	var err error
	if role == "orchestrator" {
		provider, err = registry.LoadStatic(c.RegistryFile)
		if err != nil {
			return err
		}
	}
	h := &health{}
	// Serve liveness throughout bounded startup; readiness cannot touch the client
	// until polling.Store(true) publishes the fully initialized connection.
	srv := &http.Server{Addr: c.HTTPAddr, Handler: h.router(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	// Publish the API handler exactly once after the client gateway exists.
	// New starts get503 until polling begins; liveness is available throughout.
	type route struct{ handler http.Handler }
	var handler atomic.Value
	handler.Store(route{h.router()})
	srv.Handler = h.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.Load().(route).handler.ServeHTTP(w, r) }))
	httpErr := make(chan error, 1)
	go func() { httpErr <- srv.ListenAndServe() }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	startup, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var temporal client.Client
	err = retry(startup, time.Second, func(ctx context.Context) error {
		attempt, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		var e error
		temporal, e = client.DialContext(attempt, client.Options{HostPort: c.TemporalAddress, Namespace: c.TemporalNamespace, Logger: sdklog.NewStructuredLogger(logger)})
		if e != nil {
			logger.Warn("Temporal connection unavailable; retrying")
			return e
		}
		return nil
	})
	if err != nil {
		return err
	}
	defer temporal.Close()
	h.check = func(ctx context.Context) error {
		_, err := temporal.CheckHealth(ctx, &client.CheckHealthRequest{})
		return err
	}
	poller := worker.New(temporal, c.TaskQueue, worker.Options{WorkerStopTimeout: 30 * time.Second})
	if role == "orchestrator" {
		poller.RegisterWorkflowWithOptions(parent.Parent, workflow.RegisterOptions{Name: contracts.ParentWorkflowName})
		handler.Store(route{api.New(provider, runs.New(provider, runs.NewTemporal(temporal)), h.ready)})
	} else {
		poller.RegisterWorkflowWithOptions(child.Child, workflow.RegisterOptions{Name: contracts.ChildWorkflowName})
		poller.RegisterActivityWithOptions(activities.New(c.WorkerName, c.ActivityDuration).Example, activity.RegisterOptions{Name: contracts.ExampleActivityName})
	}
	if err := poller.Start(); err != nil {
		return err
	}
	h.polling.Store(true)
	defer poller.Stop()
	logger.Info("polling started", "httpAddress", c.HTTPAddr)
	select {
	case <-ctx.Done():
		h.polling.Store(false)
		logger.Info("shutdown requested")
		return nil
	case err := <-httpErr:
		h.polling.Store(false)
		return err
	}
}

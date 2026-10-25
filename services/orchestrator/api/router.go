// Package api exposes the orchestrator's HTTP contract without process wiring.
package api

import (
	"context"
	"net/http"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/registry"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/runs"
	"github.com/go-chi/chi/v5"
)

type handlers struct {
	provider registry.Provider
	service  *runs.Service
	ready    func(context.Context) error
}

func New(provider registry.Provider, service *runs.Service, ready func(context.Context) error) http.Handler {
	h := handlers{provider, service, ready}
	r := chi.NewRouter()
	r.Post("/runs", h.start)
	r.Get("/runs/{workflowID}", h.get)
	r.Get("/workers", func(w http.ResponseWriter, r *http.Request) { respond(w, http.StatusOK, provider.Snapshot()) })
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", h.readiness)
	return r
}

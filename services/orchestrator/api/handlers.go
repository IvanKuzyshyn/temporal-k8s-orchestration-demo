package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/runs"
	"github.com/go-chi/chi/v5"
)

func (h handlers) start(w http.ResponseWriter, r *http.Request) {
	// Probe at most one byte, including for unknown-length/chunked requests.
	// Do not trust ContentLength or buffer unexpected input.
	if r.Body != nil {
		var probe [1]byte
		n, err := io.ReadFull(r.Body, probe[:])
		if n != 0 || err != io.EOF {
			fail(w, http.StatusBadRequest, "malformed request")
			return
		}
	}
	key := ""
	if keys, present := r.Header[http.CanonicalHeaderKey("Idempotency-Key")]; present {
		if len(keys) != 1 || !runs.ValidKey(keys[0]) {
			fail(w, http.StatusBadRequest, "malformed request")
			return
		}
		key = keys[0]
	}
	run, err := h.service.Start(r.Context(), key)
	if err != nil {
		respondError(w, err)
		return
	}
	respond(w, http.StatusAccepted, run)
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	run, err := h.service.Get(r.Context(), chi.URLParam(r, "workflowID"))
	if err != nil {
		respondError(w, err)
		return
	}
	respond(w, http.StatusOK, run)
}
func (h handlers) readiness(w http.ResponseWriter, r *http.Request) {
	if !h.provider.Snapshot().Synced || h.ready == nil {
		fail(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.ready(ctx); err != nil || ctx.Err() != nil {
		fail(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	respond(w, http.StatusOK, map[string]string{"status": "ready"})
}
func respondError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runs.ErrBadRequest):
		fail(w, 400, "malformed request")
	case errors.Is(err, runs.ErrInvalidRegistry), errors.Is(err, runs.ErrUnavailable):
		fail(w, 503, "service unavailable")
	case errors.Is(err, runs.ErrNotFound):
		fail(w, 404, "run not found")
	default:
		fail(w, 500, "internal error")
	}
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func respond(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

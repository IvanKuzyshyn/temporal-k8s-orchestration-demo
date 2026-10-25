package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/runs"
	"github.com/go-chi/chi/v5"
)

const maxBody = 16 * 1024

func (h handlers) start(w http.ResponseWriter, r *http.Request) {
	// Read through the limit, including whitespace after a valid JSON prefix.
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if len(data) > maxBody {
		fail(w, http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "malformed request")
		return
	}
	payload, err := decodePayload(data)
	if err != nil {
		fail(w, http.StatusBadRequest, "malformed request")
		return
	}
	key := ""
	if keys, present := r.Header[http.CanonicalHeaderKey("Idempotency-Key")]; present {
		if len(keys) != 1 || !runs.ValidKey(keys[0]) {
			fail(w, http.StatusBadRequest, "malformed request")
			return
		}
		key = keys[0]
	}
	run, err := h.service.Start(r.Context(), key, payload)
	if err != nil {
		respondError(w, err)
		return
	}
	respond(w, http.StatusAccepted, run)
}

// Token-level shape checking avoids encoding/json's case-insensitive field
// matching, duplicate-key acceptance, and null-to-string zero-value behavior.
func decodePayload(data []byte) (contracts.Payload, error) {
	bad := runs.ErrBadRequest
	if !utf8.Valid(data) {
		return contracts.Payload{}, bad
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return contracts.Payload{}, bad
	}
	token, err = d.Token()
	if err != nil || token != "message" {
		return contracts.Payload{}, bad
	}
	token, err = d.Token()
	message, ok := token.(string)
	if err != nil || !ok {
		return contracts.Payload{}, bad
	}
	token, err = d.Token()
	if err != nil || token != json.Delim('}') {
		return contracts.Payload{}, bad
	}
	if _, err = d.Token(); err != io.EOF {
		return contracts.Payload{}, bad
	}
	return contracts.Payload{Message: message}, nil
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
	case errors.Is(err, runs.ErrConflict):
		fail(w, 409, "idempotency conflict")
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

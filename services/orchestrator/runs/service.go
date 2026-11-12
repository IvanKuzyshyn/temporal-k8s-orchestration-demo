// Package runs binds validated immutable registry plans to durable Temporal runs.
package runs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/registry"
)

var (
	ErrBadRequest      = errors.New("malformed request")
	ErrInvalidRegistry = errors.New("registry unavailable")
	ErrUnavailable     = errors.New("Temporal unavailable")
	ErrNotFound        = errors.New("run not found")
	ErrAlreadyExists   = errors.New("run already exists")
)

type Run struct {
	WorkflowID string                  `json:"workflowId"`
	RunID      string                  `json:"runId"`
	PlanHash   string                  `json:"planHash"`
	Status     string                  `json:"status"`
	Result     *contracts.ParentResult `json:"result,omitempty"`
	Failure    string                  `json:"failure,omitempty"`
}

// Execution carries metadata read from durable storage, never process-local dedupe.
type Execution struct {
	Run Run
}
type Gateway interface {
	Start(context.Context, string, contracts.ParentInput) (Run, error)
	Get(context.Context, string) (Execution, error)
}
type Service struct {
	provider registry.Provider
	gateway  Gateway
}

func New(provider registry.Provider, gateway Gateway) *Service {
	return &Service{provider: provider, gateway: gateway}
}

// ValidKey preserves all printable ASCII, including leading/trailing spaces.
func ValidKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for i := range len(key) {
		if key[i] < 0x20 || key[i] > 0x7e {
			return false
		}
	}
	return true
}
func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (s *Service) Start(ctx context.Context, key string) (Run, error) {
	if key != "" && !ValidKey(key) {
		return Run{}, ErrBadRequest
	}
	var id string
	if key != "" {
		id = "run-" + hash([]byte(key))
		existing, err := s.gateway.Get(ctx, id)
		if err == nil {
			return repeat(existing)
		}
		if !errors.Is(err, ErrNotFound) {
			return Run{}, err
		}
	} else {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return Run{}, err
		}
		id = "run-" + hex.EncodeToString(random[:])
	}
	inputPlan, err := registry.ValidateSnapshot(s.provider.Snapshot())
	if err != nil {
		return Run{}, ErrInvalidRegistry
	}
	run, err := s.gateway.Start(ctx, id, contracts.ParentInput{Plan: inputPlan})
	if errors.Is(err, ErrAlreadyExists) && key != "" {
		existing, lookupErr := s.gateway.Get(ctx, id)
		if lookupErr != nil {
			return Run{}, ErrUnavailable
		}
		return repeat(existing)
	}
	return run, err
}
func repeat(existing Execution) (Run, error) {
	if !validHash(existing.Run.PlanHash) || existing.Run.WorkflowID == "" || existing.Run.RunID == "" {
		return Run{}, errors.New("invalid durable execution metadata")
	}
	return existing.Run, nil
}
func (s *Service) Get(ctx context.Context, id string) (Run, error) {
	e, err := s.gateway.Get(ctx, id)
	return e.Run, err
}

func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for i := range len(h) {
		if !(h[i] >= '0' && h[i] <= '9' || h[i] >= 'a' && h[i] <= 'f') {
			return false
		}
	}
	return true
}

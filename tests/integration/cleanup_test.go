//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/onsi/gomega"
	"go.temporal.io/api/serviceerror"
)

// Pending is an exact keyed-ID intent, not permission to terminate a current
// execution with an empty run ID. Reconciliation must first persist its pin.
type executionRef struct {
	ID, RunID string
	Pending   bool `json:",omitempty"`
}

func recordExecution(path string, ref executionRef) error {
	if path == "" {
		return fmt.Errorf("SCALING_RUNS_FILE is mandatory for start intent")
	}
	b, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	// Start a new JSONL frame even if a killed process left a partial final row.
	// Sync and close before POST: an unwritten intent cannot authorize a start.
	_, err = file.Write(append(append([]byte{'\n'}, b...), '\n'))
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func TestScalingCleanup(t *testing.T) {
	path := os.Getenv("SCALING_RUNS_FILE")
	if path == "" {
		return
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return
	}
	g := gomega.NewWithT(t)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	defer file.Close()
	newHarness(t).cleanupLedger(file)
}

func (h *harness) cleanupLedger(file *os.File) {
	for _, ref := range h.executionRefs(file) {
		h.cleanupExecution(ref, file.Name())
	}
}

// Read all rows before selecting pins, so old pending rows never cause an
// ID-only lookup when this ledger already contains a known exact execution.
func (h *harness) executionRefs(file *os.File) []executionRef {
	var refs []executionRef
	pinned := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var ref executionRef
		if err := json.Unmarshal(scanner.Bytes(), &ref); err != nil {
			// A partial tail cannot suppress cleanup of valid, already durable intents.
			h.t.Errorf("owned execution ledger decode: %v", err)
			continue
		}
		refs = append(refs, ref)
		if !ref.Pending && ref.ID != "" && ref.RunID != "" {
			pinned[ref.ID] = true
		}
	}
	if err := scanner.Err(); err != nil {
		h.t.Errorf("owned execution ledger read: %v", err)
	}
	seen := map[executionRef]bool{}
	var selected []executionRef
	for _, ref := range refs {
		if seen[ref] || (ref.Pending && pinned[ref.ID]) {
			continue
		}
		seen[ref] = true
		selected = append(selected, ref)
	}
	return selected
}

func (h *harness) cleanupExecution(ref executionRef, path string) {
	ref, found, err := h.resolveOwnedExecution(ref, path)
	if err != nil {
		h.t.Errorf("owned execution reconciliation: %v", err)
	}
	if !found {
		return
	}
	h.t.Logf("owned execution cleanup %s/%s", ref.ID, ref.RunID)
	h.finish(h.c.GetWorkflow(context.Background(), ref.ID, ref.RunID))
}

// Shared by Go cleanup, the shell fallback and diagnostics. NotFound retains
// pending intent; unavailable is an error, never evidence of absence.
func (h *harness) resolveOwnedExecution(ref executionRef, path string) (executionRef, bool, error) {
	if ref.Pending {
		if len(ref.ID) != 68 || !strings.HasPrefix(ref.ID, "run-") || strings.ToLower(ref.ID) != ref.ID || ref.RunID != "" {
			return ref, false, fmt.Errorf("invalid keyed cleanup intent identity: %q/%q", ref.ID, ref.RunID)
		}
		if _, err := hex.DecodeString(ref.ID[4:]); err != nil {
			return ref, false, fmt.Errorf("invalid keyed cleanup intent identity: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		d, err := h.c.DescribeWorkflowExecution(ctx, ref.ID, "")
		if err != nil {
			var notFound *serviceerror.NotFound
			if errors.As(err, &notFound) {
				h.t.Logf("pending owned execution %s not found at reconciliation", ref.ID)
				return ref, false, nil
			}
			return ref, false, fmt.Errorf("reconcile owned execution %s: %w (intent retained)", ref.ID, err)
		}
		execution := d.GetWorkflowExecutionInfo().GetExecution()
		if execution.GetWorkflowId() != ref.ID || execution.GetRunId() == "" {
			return ref, false, fmt.Errorf("reconcile owned execution identity mismatch for %s", ref.ID)
		}
		ref.RunID = execution.GetRunId()
		ref.Pending = false
		if err := recordExecution(path, ref); err != nil {
			// Still use the exact in-memory pin; retain the failure and old intent.
			return ref, true, fmt.Errorf("persist reconciled execution pin: %w", err)
		}
	}
	if ref.ID == "" || ref.RunID == "" {
		return ref, false, fmt.Errorf("owned execution requires exact nonempty identity")
	}
	return ref, true, nil
}

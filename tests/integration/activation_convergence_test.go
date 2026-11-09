//go:build integration

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onsi/gomega"
)

// Only kubectl reads are doubled here; these tests exercise the actual waitZero
// polling path without a cluster. Runtime acceptance remains test:scaling.
func convergenceHarness(t *testing.T, mode string) (*harness, string) {
	t.Helper()
	g := gomega.NewWithT(t)
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	g.Expect(os.Mkdir(bin, 0700)).To(gomega.Succeed())
	attempts := filepath.Join(root, "attempts")
	g.Expect(os.WriteFile(attempts, []byte("0\n"), 0600)).To(gomega.Succeed())
	stub := `#!/usr/bin/env bash
set -eu
n=$(<"$CONVERGENCE_ATTEMPTS")
case "$*" in
  *'get deployment '*)
    n=$((n + 1))
    printf '%s\n' "$n" >"$CONVERGENCE_ATTEMPTS"
    replicas=0
    if [[ $CONVERGENCE_MODE == static || ( $CONVERGENCE_MODE == delayed && $n -eq 1 ) ]]; then
      replicas=1
    fi
    if [[ $* == *jsonpath* ]]; then
      printf '%s' "$replicas"
    else
      printf '{"spec":{"replicas":%s}}' "$replicas"
    fi
    ;;
  *'get pods '*)
    if [[ $CONVERGENCE_MODE == terminating || ( $CONVERGENCE_MODE == delayed && $n -lt 3 ) ]]; then
      echo '{"items":[{"metadata":{"deletionTimestamp":"2026-11-08T00:00:00Z"}}]}'
    else
      echo '{"items":[]}'
    fi
    ;;
  *'get scaledobject '*)
    case "$CONVERGENCE_MODE" in
      missing) exit 0 ;;
      rpc) echo 'Temporal probe unavailable' >&2; exit 1 ;;
      error) echo '{"status":{"conditions":[{"type":"Ready","status":"False","reason":"ScaledObjectCheckFailed","message":"connection refused"}]}}'; exit 0 ;;
      pending) echo '{"status":{"conditions":[{"type":"Ready","status":"Unknown"}]}}'; exit 0 ;;
    esac
    if [[ $CONVERGENCE_MODE == delayed && $n -lt 5 ]]; then
      echo '{"status":{"conditions":[{"type":"Ready","status":"False","reason":"Initializing"}]}}'
    else
      echo '{"status":{"conditions":[{"type":"Ready","status":"True"},{"type":"Active","status":"False","reason":"ScalerNotActive"}]}}'
    fi
    ;;
  *) echo 'unexpected kubectl read' >&2; exit 2 ;;
esac
`
	g.Expect(os.WriteFile(filepath.Join(bin, "kubectl"), []byte(stub), 0700)).To(gomega.Succeed())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CONVERGENCE_MODE", mode)
	t.Setenv("CONVERGENCE_ATTEMPTS", attempts)
	return &harness{t: t, g: g, root: root}, attempts
}

func TestInitialScalingConvergence(t *testing.T) {
	h, attempts := convergenceHarness(t, "delayed")
	h.waitZero("worker-a", 12*time.Second)
	b, err := os.ReadFile(attempts)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	h.g.Expect(strings.TrimSpace(string(b))).To(gomega.Equal("5"), "must wait through replicas1, terminating Pods and pending Ready before activation")
}

// Capture only this assertion instance's expected terminal failure, never
// install a global Gomega handler. A fatal assertion inside a pending probe
// would also be recorded and fail the count/timing checks below.
type convergenceFailure struct{ messages []string }

func (*convergenceFailure) Helper() {}
func (f *convergenceFailure) Fatalf(format string, args ...any) {
	f.messages = append(f.messages, fmt.Sprintf(format, args...))
}

func TestInitialScalingConvergenceTimeout(t *testing.T) {
	for mode, diagnostic := range map[string]string{
		"static": "replicas=1", "terminating": "pods=1", "missing": "missing ScaledObject",
		"pending": "Unknown", "error": "connection refused", "rpc": "Temporal probe unavailable",
	} {
		t.Run(mode, func(t *testing.T) {
			h, _ := convergenceHarness(t, mode)
			g := gomega.NewWithT(t)
			failure := &convergenceFailure{}
			h.g = gomega.NewWithT(failure)
			bound := 200 * time.Millisecond
			start := time.Now()
			h.waitZero("worker-a", bound)
			elapsed := time.Since(start)
			g.Expect(elapsed).To(gomega.BeNumerically(">=", bound))
			g.Expect(elapsed).To(gomega.BeNumerically("<", 2*time.Second))
			g.Expect(failure.messages).To(gomega.HaveLen(1), "pending samples must not assert fatally")
			g.Expect(failure.messages[0]).To(gomega.ContainSubstring(diagnostic))
		})
	}
}

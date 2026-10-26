package composetest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/onsi/gomega"
)

func TestSmokeWaitSecondsBoundsSlowHTTP(t *testing.T) {
	for _, phase := range []string{"readiness", "completion"} {
		t.Run(phase, func(t *testing.T) {
			g := gomega.NewWithT(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				slow := phase == "readiness" || r.Method == http.MethodGet && r.URL.Path != "/readyz"
				if slow {
					select {
					case <-time.After(3 * time.Second):
					case <-r.Context().Done():
						return
					}
				}
				switch {
				case r.URL.Path == "/readyz":
					if phase == "readiness" {
						w.WriteHeader(503)
					} else {
						w.WriteHeader(200)
					}
				case r.Method == http.MethodPost:
					w.WriteHeader(202)
					fmt.Fprintln(w, completed)
				default:
					fmt.Fprintln(w, `{"status":"running"}`)
				}
			}))
			defer server.Close()
			// Diagnostics are read-only, but keep this focused deadline test Docker-free.
			dir := t.TempDir()
			g.Expect(os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0755)).To(gomega.Succeed())
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "../../scripts/compose-smoke.sh")
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "COMPOSE_API_URL="+server.URL, "SMOKE_RESTART=0", "SMOKE_WAIT_SECONDS=1", "COMPOSE_PROJECT_NAME=temporal-script-deadline-test")
			start := time.Now()
			output, err := cmd.CombinedOutput()
			elapsed := time.Since(start)
			g.Expect(err).To(gomega.HaveOccurred(), string(output))
			g.Expect(elapsed).To(gomega.BeNumerically("<", 2500*time.Millisecond), "one-second %s budget, elapsed=%s\n%s", phase, elapsed, output)
			g.Expect(string(output)).NotTo(gomega.ContainSubstring("PASS:"))
		})
	}
}

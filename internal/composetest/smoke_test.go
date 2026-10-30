// Package composetest exercises the real smoke script against controlled HTTP
// responses. It guards against false-success shell assertions (notably Bash3.2).
package composetest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/onsi/gomega"
)

const completed = `{"workflowId":"run-test","runId":"run-id","planHash":"5fefcdcc52af25c9c21a523f1f3c1f7d928018051ffe89ac8b5a0e6ccf8c3b62","status":"completed","result":{"results":[{"workerName":"worker-a"},{"workerName":"worker-b"},{"workerName":"worker-c"},{"workerName":"worker-d"}]}}`

func TestSmokeRejectsWrongHTTPContracts(t *testing.T) {
	for _, tc := range []struct {
		name         string
		replay, body int
		wantError    bool
	}{
		{"valid", 202, 400, false},
		{"replay rejected", 500, 400, true},
		{"body unexpectedly accepted", 202, 202, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			starts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/readyz":
					w.WriteHeader(200)
				case r.URL.Path == "/workers":
					fmt.Fprint(w, `{"plan":{"hash":"5fefcdcc52af25c9c21a523f1f3c1f7d928018051ffe89ac8b5a0e6ccf8c3b62"}}`)
				case r.Method == http.MethodGet:
					fmt.Fprintln(w, completed)
				case r.ContentLength > 0:
					w.WriteHeader(tc.body)
				default:
					starts++
					if starts > 1 {
						w.WriteHeader(tc.replay)
					} else {
						w.WriteHeader(202)
					}
					fmt.Fprintln(w, completed)
				}
			}))
			defer server.Close()
			cmd := exec.Command("bash", "../../scripts/compose-smoke.sh")
			cmd.Env = append(os.Environ(), "COMPOSE_API_URL="+server.URL, "SMOKE_RESTART=0", "SMOKE_WAIT_SECONDS=1", fmt.Sprintf("COMPOSE_PROJECT_NAME=temporal-script-test-%d", os.Getpid()))
			output, err := cmd.CombinedOutput()
			if tc.wantError {
				g.Expect(err).To(gomega.HaveOccurred(), string(output))
				g.Expect(strings.Contains(string(output), "PASS:")).To(gomega.BeFalse())
			} else {
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(output))
				g.Expect(string(output)).To(gomega.ContainSubstring("PASS:"))
			}
		})
	}
}

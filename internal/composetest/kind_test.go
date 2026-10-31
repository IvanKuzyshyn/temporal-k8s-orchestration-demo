package composetest

import (
	"github.com/onsi/gomega"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A failed kind smoke must never diagnose/restart the user's Compose runtime.
func TestKindSmokeNeverInvokesCompose(t *testing.T) {
	g := gomega.NewWithT(t)
	dir := t.TempDir()
	calls := filepath.Join(dir, "docker-calls")
	g.Expect(os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DOCKER_CALLS\"\nexit 77\n"), 0755)).To(gomega.Succeed())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	cmd := exec.Command("bash", "../../scripts/compose-smoke.sh")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "DOCKER_CALLS="+calls, "COMPOSE_API_URL="+server.URL, "SMOKE_RESTART=0", "SMOKE_RUNTIME=kind", "KUBECONFIG="+filepath.Join(dir, "foreign"))
	output, err := cmd.CombinedOutput()
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(string(output)).To(gomega.ContainSubstring("start returned 500"))
	_, err = os.Stat(calls)
	g.Expect(os.IsNotExist(err)).To(gomega.BeTrue(), string(output))
}

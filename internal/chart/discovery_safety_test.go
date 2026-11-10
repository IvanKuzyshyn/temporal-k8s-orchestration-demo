package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/onsi/gomega"
)

// Failed ownership/preflight reads must stop before snapshots, builds or apply;
// command substitution inside [[ -z ... ]] must not disguise a failed API read.
func TestDiscoveryPreflightReadFailsClosed(t *testing.T) {
	g := gomega.NewWithT(t)
	root := t.TempDir()
	for _, dir := range []string{"scripts", "bin", ".local"} {
		g.Expect(os.Mkdir(filepath.Join(root, dir), 0700)).To(gomega.Succeed())
	}
	for _, file := range []string{"discovery.sh", "kind-common.sh"} {
		b, err := os.ReadFile("../../scripts/" + file)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(os.WriteFile(filepath.Join(root, "scripts", file), b, 0600)).To(gomega.Succeed())
	}
	g.Expect(os.WriteFile(filepath.Join(root, ".local/kubeconfig"), nil, 0600)).To(gomega.Succeed())
	tools := map[string]string{
		"go":      "#!/bin/sh\nexit 0\n",
		"kubectl": "#!/bin/sh\ncase \"$*\" in\n *'config current-context'*) echo kind-temporal-demo ;;\n *'get scaledobjects'*) exit 42 ;;\n *'get deployment'*) echo static ;;\n *'get service '*) echo service-uid ;;\n *'get pvc '*) echo pvc-uid ;;\n *'get secret '*) echo secret-uid ;;\n *) exit 0 ;;\nesac\n",
		"docker":  "#!/bin/sh\ntouch mutation\nexit 0\n",
		"kind":    "#!/bin/sh\nexit 0\n",
		"helm":    "#!/bin/sh\nexit 0\n",
		"git":     "#!/bin/sh\necho test-sha\n",
	}
	for name, body := range tools {
		g.Expect(os.WriteFile(filepath.Join(root, "bin", name), []byte(body), 0755)).To(gomega.Succeed())
	}
	cmd := exec.Command("bash", "scripts/discovery.sh", "deploy")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"), "KUBECONFIG=")
	out, err := cmd.CombinedOutput()
	g.Expect(err).To(gomega.HaveOccurred(), string(out))
	g.Expect(cmd.ProcessState.ExitCode()).To(gomega.Equal(42), string(out))
	_, err = os.Stat(filepath.Join(root, "mutation"))
	g.Expect(os.IsNotExist(err)).To(gomega.BeTrue(), "must not build/apply or attempt broad restore on failed preflight")
	_, err = os.Stat(filepath.Join(root, ".local/task9/static-baseline"))
	g.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
}

package chart

import (
	"github.com/onsi/gomega"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The destructive entrypoint must reject a locally misplaced kubeconfig before
// looking up any Docker node or issuing any mutation, even with the right name.
func TestWrongLocalContextFailsBeforeMutation(t *testing.T) {
	g := gomega.NewWithT(t)
	root := t.TempDir()
	for _, dir := range []string{"scripts", "k8s/infrastructure", ".local", "tools"} {
		g.Expect(os.MkdirAll(filepath.Join(root, dir), 0700)).To(gomega.Succeed())
	}
	for _, path := range []string{"scripts/cluster-delete.sh", "scripts/kind-common.sh", "k8s/infrastructure/versions.env"} {
		b, err := os.ReadFile("../../" + path)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(os.WriteFile(filepath.Join(root, path), b, 0600)).To(gomega.Succeed())
	}
	config := `apiVersion: v1
kind: Config
current-context: kind-unrelated
contexts:
- name: kind-unrelated
  context: {cluster: kind-unrelated, user: nobody}
clusters:
- name: kind-unrelated
  cluster: {server: 'https://127.0.0.1:1234'}
users: []
`
	path := filepath.Join(root, ".local/kubeconfig")
	g.Expect(os.WriteFile(path, []byte(config), 0600)).To(gomega.Succeed())
	g.Expect(os.WriteFile(filepath.Join(root, ".local/kind-cluster-uid"), []byte("owned-uid"), 0600)).To(gomega.Succeed())
	// Only cluster enumeration is allowed. Any deletion or Docker inspection is a failure.
	calls := filepath.Join(root, "calls")
	for tool, body := range map[string]string{
		"kind":   "if [ \"$*\" = 'get clusters' ]; then echo temporal-demo; exit 0; fi\necho mutation >> \"$CALLS\"; exit 99\n",
		"docker": "echo inspected >> \"$CALLS\"; exit 99\n",
	} {
		g.Expect(os.WriteFile(filepath.Join(root, "tools", tool), []byte("#!/bin/sh\n"+body), 0755)).To(gomega.Succeed())
	}
	cmd := exec.Command("bash", filepath.Join(root, "scripts/cluster-delete.sh"))
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "KUBECONFIG=") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"), "CALLS="+calls)
	b, err := cmd.CombinedOutput()
	g.Expect(err).To(gomega.HaveOccurred())
	g.Expect(string(b)).To(gomega.ContainSubstring("Wrong kind context/cluster"))
	_, err = os.Stat(calls)
	g.Expect(os.IsNotExist(err)).To(gomega.BeTrue(), string(b))
	unchanged, err := os.ReadFile(path)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(string(unchanged)).To(gomega.Equal(config))
}

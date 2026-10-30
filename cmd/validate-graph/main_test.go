package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/gomega"
)

// Exercise the built CLI, not an in-process helper: invalid inputs must really
// exit nonzero with stderr only, while valid inputs print exactly one hash.
// One build serves every fixture and the public mise test:manifests entrypoint.
func TestCLI(t *testing.T) {
	g := gomega.NewWithT(t)
	binary := filepath.Join(t.TempDir(), "validate-graph")
	output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput()
	g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", output)
	// Independently SHA-256 checked canonical schema-1 JSON, including null root dependencies.
	wantHashes := map[string]string{
		"diamond.yaml":    "5fefcdcc52af25c9c21a523f1f3c1f7d928018051ffe89ac8b5a0e6ccf8c3b62",
		"roots.yaml":      "fe5cffc2b268e7c998ccec1af725149f25592112476ac28cb258f6fa5ff1bd18",
		"list.yaml":       "2ac94d88fdffcc26d23e6ed1d7711ff8ecc1bfc4c21bf1130b1ead00bcf534b1",
		"filtering.yaml":  "2ac94d88fdffcc26d23e6ed1d7711ff8ecc1bfc4c21bf1130b1ead00bcf534b1",
		"empty-docs.yaml": "2ac94d88fdffcc26d23e6ed1d7711ff8ecc1bfc4c21bf1130b1ead00bcf534b1",
	}
	for _, group := range []string{"valid", "invalid"} {
		paths, err := filepath.Glob("../../internal/manifest/testdata/" + group + "/*.yaml")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(paths).NotTo(gomega.BeEmpty())
		for _, path := range paths {
			t.Run(group+"/"+filepath.Base(path), func(t *testing.T) {
				g := gomega.NewWithT(t)
				f, err := os.Open(path)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				defer f.Close()
				cmd := exec.Command(binary)
				cmd.Stdin = f
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err = cmd.Run()
				if group == "valid" {
					g.Expect(err).NotTo(gomega.HaveOccurred(), "%s", stderr.String())
					g.Expect(stderr.String()).To(gomega.BeEmpty())
					g.Expect(wantHashes).To(gomega.HaveKey(filepath.Base(path)))
					g.Expect(stdout.String()).To(gomega.Equal(wantHashes[filepath.Base(path)] + "\n"))
				} else {
					var exit *exec.ExitError
					g.Expect(err).To(gomega.BeAssignableToTypeOf(exit))
					g.Expect(err.(*exec.ExitError).ExitCode()).To(gomega.Equal(1))
					g.Expect(stdout.String()).To(gomega.BeEmpty(), "invalid graph printed a hash")
					g.Expect(strings.TrimSpace(stderr.String())).NotTo(gomega.BeEmpty())
				}
			})
		}
	}
	t.Run("empty stdin", func(t *testing.T) {
		g := gomega.NewWithT(t)
		cmd := exec.Command(binary)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		g.Expect(err).To(gomega.HaveOccurred())
		g.Expect(stdout.String()).To(gomega.BeEmpty())
		g.Expect(stderr.String()).To(gomega.ContainSubstring("empty"))
	})
}

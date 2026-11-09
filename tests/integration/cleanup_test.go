//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/onsi/gomega"
)

type executionRef struct{ ID, RunID string }

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
	h := newHarness(t)
	dec := json.NewDecoder(file)
	for {
		var ref executionRef
		err := dec.Decode(&ref)
		if err == io.EOF {
			break
		}
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(ref.ID).NotTo(gomega.BeEmpty())
		g.Expect(ref.RunID).NotTo(gomega.BeEmpty())
		h.finish(h.c.GetWorkflow(context.Background(), ref.ID, ref.RunID))
	}
}

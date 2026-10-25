package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/onsi/gomega"
)

func TestSnapshotDeepCopy(t *testing.T) {
	g := gomega.NewWithT(t)
	p, err := LoadStatic("../../deploy/config/workers.json")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	initial := p.Snapshot()
	g.Expect(initial.Synced).To(gomega.BeTrue())
	g.Expect(initial.Problems).To(gomega.HaveLen(0))
	g.Expect(initial.Plan.Nodes).To(gomega.HaveLen(4))
	s := p.Snapshot()
	s.Plan.Nodes[0].Name = "corrupted"
	s.Plan.Nodes[3].Dependencies[0] = "corrupted"
	s.Plan.Nodes = append(s.Plan.Nodes, plan.Node{Name: "extra"})
	s.Problems = append(s.Problems, "corrupted")
	s.Synced = false
	s.Plan.Hash = "corrupted"
	g.Expect(p.Snapshot()).To(gomega.Equal(initial), "caller mutated stored snapshot")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { s := p.Snapshot(); s.Plan.Nodes[3].Dependencies[0] = "changed" })
	}
	wg.Wait()
	g.Expect(p.Snapshot()).To(gomega.Equal(initial), "concurrent caller mutated stored snapshot")
}

// Strict decoding prevents typos and appended JSON from silently changing the registry.
func TestLoadStatic(t *testing.T) {
	g := gomega.NewWithT(t)
	_, err := LoadStatic(filepath.Join(t.TempDir(), "missing"))
	g.Expect(err).To(gomega.HaveOccurred(), "missing file accepted")
	for _, raw := range []string{"{", "[]", "null", "{}", "[] {}", `[{"unknown":true}]`, `[{"schemaVersion":"2"}]`} {
		t.Run(raw, func(t *testing.T) {
			g := gomega.NewWithT(t)
			path := filepath.Join(t.TempDir(), "workers.json")
			g.Expect(os.WriteFile(path, []byte(raw), 0600)).To(gomega.Succeed())
			_, err := LoadStatic(path)
			g.Expect(err).To(gomega.HaveOccurred(), "accepted %s", raw)
		})
	}
	data, err := os.ReadFile("../../deploy/config/workers.json")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	var regs []plan.Registration
	err = json.Unmarshal(data, &regs)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(regs).To(gomega.HaveLen(4))
	for _, mutate := range []func(){
		func() { regs[1].SourceServiceNamespace = regs[0].SourceServiceNamespace },
		func() {
			regs[1].SourceServiceNamespace = "different"
			regs[1].WorkerName = regs[0].WorkerName
			regs[1].SourceServiceName = regs[0].WorkerName
			regs[1].TaskQueue = regs[0].TaskQueue
		},
	} {
		mutate()
		b, err := json.Marshal(regs)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		path := filepath.Join(t.TempDir(), "bad.json")
		g.Expect(os.WriteFile(path, b, 0600)).To(gomega.Succeed())
		_, err = LoadStatic(path)
		g.Expect(err).To(gomega.HaveOccurred(), "accepted ambiguous registry")
	}
}

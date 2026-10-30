package manifest

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/registry"
	"github.com/onsi/gomega"
)

func fixture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + path)
	gomega.NewWithT(t).Expect(err).NotTo(gomega.HaveOccurred())
	return string(b)
}

// Missing namespace resolution, wrong source metadata, queue derivation, or
// dependency normalization must break parity with the independent static source.
func TestDiamondMatchesStatic(t *testing.T) {
	g := gomega.NewWithT(t)
	r, err := Registrations(strings.NewReader(fixture(t, "valid/diamond.yaml")))
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(r).To(gomega.HaveLen(4))
	g.Expect(r[0]).To(gomega.Equal(plan.Registration{
		SchemaVersion: "1", WorkerName: "worker-d", TaskQueue: "worker-d.v1.temporal",
		WorkflowType: "ExampleChildWorkflow", RunAfter: " worker-c, worker-b,worker-c ",
		SourceServiceName: "worker-d", SourceServiceNamespace: "temporal-demo-worker-d", SourceServiceUID: "service-d-uid",
	}))
	p, err := plan.Build(r)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	static, err := registry.LoadStatic("../../deploy/config/workers.json")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(p).To(gomega.Equal(static.Snapshot().Plan))
	g.Expect(p.Nodes[3].Dependencies).To(gomega.Equal([]string{"worker-b", "worker-c"}))
}

// Ignoring List items, empty docs, namespace labels or Service annotations
// changes these exact node sets (and must not silently add unrelated resources).
func TestValidFixtures(t *testing.T) {
	for _, tc := range []struct {
		file  string
		names []string
	}{
		{"roots.yaml", []string{"worker-a", "worker-b"}},
		{"list.yaml", []string{"worker-a"}},
		{"empty-docs.yaml", []string{"worker-a"}},
		{"filtering.yaml", []string{"worker-a"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			g := gomega.NewWithT(t)
			r, err := Registrations(strings.NewReader(fixture(t, "valid/"+tc.file)))
			g.Expect(err).NotTo(gomega.HaveOccurred())
			p, err := plan.Build(r)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			var names []string
			for _, n := range p.Nodes {
				names = append(names, n.Name)
				g.Expect(n.Dependencies).To(gomega.BeEmpty())
			}
			g.Expect(names).To(gomega.Equal(tc.names))
		})
	}
}

// These complete sets catch skipping later documents or bypassing plan.Build.
func TestInvalidFixtures(t *testing.T) {
	for _, tc := range []struct{ file, problem string }{
		{"unknown.yaml", "unknown dependency"}, {"self.yaml", "itself"},
		{"two-node-cycle.yaml", "cycle"}, {"long-cycle.yaml", "cycle"},
		{"duplicate-service.yaml", "duplicate Service"}, {"duplicate-worker.yaml", "duplicate worker"},
		{"duplicate-namespace.yaml", "duplicate Namespace"}, {"multiple-workers.yaml", "multiple workers"},
		{"missing-namespace.yaml", "namespace"}, {"undeclared-namespace.yaml", "namespace"},
		{"malformed.yaml", "decode"}, {"empty-entry.yaml", "dependency entry"},
		{"missing-service-name.yaml", "invalid worker Service name"}, {"malformed-metadata.yaml", "decode"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			g := gomega.NewWithT(t)
			r, err := Registrations(strings.NewReader(fixture(t, "invalid/"+tc.file)))
			if err == nil {
				_, err = plan.Build(r)
			} else {
				g.Expect(r).To(gomega.BeNil(), "must not expose partial registrations")
			}
			g.Expect(err).To(gomega.HaveOccurred())
			g.Expect(err.Error()).To(gomega.ContainSubstring(tc.problem))
		})
	}
}

// Passing the annotation unchanged to the shared parser preserves Task 1's
// whitespace/dedup/root contract and rejects every kind of empty list entry.
func TestDependencyAnnotations(t *testing.T) {
	base := fixture(t, "valid/diamond.yaml")
	for _, tc := range []struct {
		raw  string
		want []string
		bad  bool
	}{
		{"", nil, false}, {" \\t\\n", nil, false},
		{" worker-b,worker-a,worker-b ", []string{"worker-a", "worker-b"}, false},
		{"worker-a,", nil, true}, {",worker-a", nil, true}, {"worker-a,,worker-b", nil, true},
		{"worker-a, \\t ,worker-b", nil, true}, {"Worker-A", nil, true}, {"worker_a", nil, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			g := gomega.NewWithT(t)
			raw := strings.Replace(base, " worker-c, worker-b,worker-c ", tc.raw, 1)
			r, err := Registrations(strings.NewReader(raw))
			g.Expect(err).NotTo(gomega.HaveOccurred())
			p, err := plan.Build(r)
			if tc.bad {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(p.Nodes[3].Dependencies).To(gomega.Equal(tc.want))
			}
		})
	}
}

// Selected malformed Services must fail instead of disappearing from a valid
// partial graph. This is graph/source validation, not general Kubernetes schema validation.
func TestMalformedSelectedMetadata(t *testing.T) {
	base := fixture(t, "valid/diamond.yaml")
	for _, tc := range []struct{ name, old, new string }{
		{"missing name", "  name: worker-d\n", ""},
		{"null name", "name: worker-d", "name: null"},
		{"invalid name", "name: worker-d", "name: Worker_D"},
		{"structured name", "name: worker-d", "name: [worker-d]"},
		{"missing namespace", "  namespace: temporal-demo-worker-d\n", ""},
		{"null namespace", "namespace: temporal-demo-worker-d", "namespace: null"},
		{"invalid namespace", "namespace: temporal-demo-worker-d", "namespace: Invalid_Namespace"},
		{"structured dependencies", "\" worker-c, worker-b,worker-c \"", "[worker-b, worker-c]"},
		{"duplicate annotation key", "    temporal-worker: \"true\"", "    temporal-worker: \"false\"\n    temporal-worker: \"true\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			r, err := Registrations(strings.NewReader(strings.Replace(base, tc.old, tc.new, 1)))
			if err == nil {
				_, err = plan.Build(r)
			}
			g.Expect(err).To(gomega.HaveOccurred())
		})
	}
}

// Duplicate definitions are ambiguous even when identical or one disables scope;
// neither document order nor a last-write-wins map may choose a lucky winner.
func TestDuplicatesInEitherOrder(t *testing.T) {
	for _, kind := range []string{"Namespace", "Service"} {
		for _, labels := range [][2]string{{"true", "false"}, {"false", "true"}, {"true", "true"}} {
			t.Run(kind+labels[0]+labels[1], func(t *testing.T) {
				g := gomega.NewWithT(t)
				base := fixture(t, "valid/list.yaml")
				var extra string
				if kind == "Namespace" {
					extra = "kind: Namespace\nmetadata: {name: temporal-demo-worker-a, labels: {temporal-demo.io/worker-namespace: \"" + labels[1] + "\"}}\n"
					base = strings.Replace(base, "worker-namespace: \"true\"", "worker-namespace: \""+labels[0]+"\"", 1)
				} else {
					extra = "kind: Service\nmetadata: {name: worker-a, namespace: temporal-demo-worker-a, annotations: {temporal-worker: \"" + labels[1] + "\"}}\n"
					base = strings.Replace(base, "temporal-worker: \"true\"", "temporal-worker: \""+labels[0]+"\"", 1)
				}
				r, err := Registrations(strings.NewReader(base + "---\n" + extra))
				g.Expect(err).To(gomega.HaveOccurred())
				g.Expect(err.Error()).To(gomega.ContainSubstring("duplicate " + kind))
				g.Expect(r).To(gomega.BeNil())
			})
		}
	}
}

func TestEmptyRegistryIsNotRunnable(t *testing.T) {
	for _, raw := range []string{"", "---\n---\n", "kind: List\nitems: []\n", "kind: ConfigMap\nmetadata: {name: unrelated}\n"} {
		t.Run(raw, func(t *testing.T) {
			g := gomega.NewWithT(t)
			r, err := Registrations(strings.NewReader(raw))
			g.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = plan.Build(r)
			g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("empty")))
		})
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("fixture read failure") }

func TestReadFailure(t *testing.T) {
	g := gomega.NewWithT(t)
	r, err := Registrations(io.MultiReader(strings.NewReader(fixture(t, "valid/diamond.yaml")), brokenReader{}))
	g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("fixture read failure")))
	g.Expect(r).To(gomega.BeNil())
}

package plan

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// These cases catch accepting malformed lists or losing canonical order/deduplication.
func TestParseDependencies(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
		bad  bool
	}{
		{"", nil, false}, {" \t\n", nil, false},
		{" worker-b,worker-a,worker-b ", []string{"worker-a", "worker-b"}, false},
		{"worker-a,", nil, true}, {",worker-a", nil, true}, {"worker-a,,worker-b", nil, true},
		{"worker-a, \t ,worker-b", nil, true}, {"Worker-A", nil, true}, {"worker_a", nil, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ParseDependencies(tc.raw)
			if (err != nil) != tc.bad {
				t.Fatalf("error = %v", err)
			}
			if !tc.bad && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func diamond() []Registration {
	var regs []Registration
	for i, name := range []string{"worker-a", "worker-b", "worker-c", "worker-d"} {
		regs = append(regs, Registration{SchemaVersion: "1", WorkerName: name, TaskQueue: name + ".v1.temporal", WorkflowType: "ExampleChildWorkflow", RunAfter: []string{"", "worker-a", "worker-a", "worker-c,worker-b"}[i], SourceServiceName: name, SourceServiceNamespace: "temporal-demo-" + name})
	}
	return regs
}

// A missing validation branch must allow at least one of these malformed graphs.
func TestBuild(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func([]Registration) []Registration
		problem string
	}{
		{"empty", func(r []Registration) []Registration { return nil }, "empty"},
		{"unknown", func(r []Registration) []Registration { r[1].RunAfter = "worker-missing"; return r }, "unknown"},
		{"self", func(r []Registration) []Registration { r[0].RunAfter = " worker-a "; return r }, "itself"},
		{"duplicate across namespaces", func(r []Registration) []Registration {
			v := r[0]
			v.SourceServiceNamespace = "another-namespace"
			return append(r, v)
		}, "duplicate"},
		{"multiple workers in namespace", func(r []Registration) []Registration {
			r[1].SourceServiceNamespace = r[0].SourceServiceNamespace
			return r
		}, "namespace"},
		{"two node cycle", func(r []Registration) []Registration { r[0].RunAfter = "worker-b"; return r }, "cycle"},
		{"long cycle", func(r []Registration) []Registration { r[0].RunAfter = "worker-d"; return r }, "cycle"},
		{"schema", func(r []Registration) []Registration { r[0].SchemaVersion = "2"; return r }, "schema"},
		{"workflow", func(r []Registration) []Registration { r[0].WorkflowType = "other"; return r }, "workflow"},
		{"queue", func(r []Registration) []Registration { r[0].TaskQueue = "arbitrary"; return r }, "queue"},
		{"source mismatch", func(r []Registration) []Registration { r[0].SourceServiceName = "worker-other"; return r }, "source"},
		{"source missing", func(r []Registration) []Registration { r[0].SourceServiceName = ""; return r }, "source"},
		{"namespace missing", func(r []Registration) []Registration { r[0].SourceServiceNamespace = ""; return r }, "namespace"},
		{"namespace invalid", func(r []Registration) []Registration { r[0].SourceServiceNamespace = "bad.name"; return r }, "namespace"},
		{"dependency syntax", func(r []Registration) []Registration { r[1].RunAfter = "worker-a,"; return r }, "dependency"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Build(tc.mutate(diamond()))
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.problem) {
				t.Fatalf("plan=%+v error=%v; want %q", p, err, tc.problem)
			}
			if !reflect.DeepEqual(p, Plan{}) {
				t.Fatal("invalid graph exposed partial plan")
			}
		})
	}
	for _, name := range []string{"", "Worker-a", "worker_a", "worker.a", "-worker", "worker-", "1worker", strings.Repeat("a", 64)} {
		t.Run("invalid service "+name, func(t *testing.T) {
			r := diamond()
			r[0].WorkerName = name
			r[0].SourceServiceName = name
			r[0].TaskQueue = name + ".v1.temporal"
			if _, err := Build(r); err == nil {
				t.Fatalf("accepted %q", name)
			}
		})
	}
	r := diamond()
	p, err := Build(r)
	if err != nil {
		t.Fatal(err)
	}
	want := []Node{
		{Name: "worker-a", TaskQueue: "worker-a.v1.temporal", WorkflowType: "ExampleChildWorkflow"},
		{Name: "worker-b", TaskQueue: "worker-b.v1.temporal", WorkflowType: "ExampleChildWorkflow", Dependencies: []string{"worker-a"}},
		{Name: "worker-c", TaskQueue: "worker-c.v1.temporal", WorkflowType: "ExampleChildWorkflow", Dependencies: []string{"worker-a"}},
		{Name: "worker-d", TaskQueue: "worker-d.v1.temporal", WorkflowType: "ExampleChildWorkflow", Dependencies: []string{"worker-b", "worker-c"}},
	}
	if p.SchemaVersion != "1" || !reflect.DeepEqual(p.Nodes, want) {
		t.Fatalf("unexpected plan: %+v", p)
	}
	if b, e := hex.DecodeString(p.Hash); e != nil || len(b) != 32 {
		t.Fatalf("not SHA-256: %q", p.Hash)
	}
	// Empty source UID is valid for static registrations; max length names remain valid.
	name := strings.Repeat("a", 63)
	_, err = Build([]Registration{{SchemaVersion: "1", WorkerName: name, TaskQueue: name + ".v1.temporal", WorkflowType: "ExampleChildWorkflow", SourceServiceName: name, SourceServiceNamespace: strings.Repeat("n", 63)}})
	if err != nil {
		t.Fatal(err)
	}
}

// Hashes must describe execution, not discovery order or provenance.
func TestHashCanonical(t *testing.T) {
	base, err := Build(diamond())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func([]Registration)
		same   bool
	}{
		{"input order", func(r []Registration) { r[0], r[3] = r[3], r[0] }, true},
		{"dependency order and duplicates", func(r []Registration) { r[3].RunAfter = " worker-b, worker-c,worker-b " }, true},
		{"source UID", func(r []Registration) { r[0].SourceServiceUID = "changed" }, true},
		{"source namespace", func(r []Registration) { r[0].SourceServiceNamespace = "another-namespace" }, true},
		{"dependency change", func(r []Registration) { r[3].RunAfter = "worker-b" }, false},
		{"identity change", func(r []Registration) {
			r[3].WorkerName = "worker-e"
			r[3].SourceServiceName = "worker-e"
			r[3].TaskQueue = "worker-e.v1.temporal"
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := diamond()
			tc.change(r)
			p, err := Build(r)
			if err != nil {
				t.Fatal(err)
			}
			if (p.Hash == base.Hash) != tc.same {
				t.Fatalf("hash equality = %v, want %v", p.Hash == base.Hash, tc.same)
			}
		})
	}
	r := diamond()
	before := append([]Registration(nil), r...)
	p, err := Build(r)
	if err != nil {
		t.Fatal(err)
	}
	p.Nodes[1].Dependencies[0] = "changed"
	if !reflect.DeepEqual(r, before) {
		t.Fatal("Build mutated registrations")
	}
}

func TestPlanJSON(t *testing.T) {
	p, err := Build(diamond())
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schemaVersion", "nodes", "hash"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing %s in %s", key, b)
		}
	}
	var nodes []map[string]json.RawMessage
	if err = json.Unmarshal(fields["nodes"], &nodes); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"name", "taskQueue", "workflowType", "dependencies"} {
		if _, ok := nodes[0][key]; !ok {
			t.Fatalf("missing %s", key)
		}
	}
}

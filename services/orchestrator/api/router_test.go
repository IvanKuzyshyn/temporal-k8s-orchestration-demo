package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/registry"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/api"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/services/orchestrator/runs"
	. "github.com/onsi/gomega"
)

type provider struct{ s registry.Snapshot }

func (p *provider) Snapshot() registry.Snapshot { return p.s }

type gateway struct {
	mu                  sync.Mutex
	starts              int
	startErr, lookupErr error
	executions          map[string]runs.Execution
	ambiguous           bool
}

func (f *gateway) Start(_ context.Context, id string, in contracts.ParentInput) (runs.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if f.startErr != nil {
		return runs.Run{}, f.startErr
	}
	if _, exists := f.executions[id]; exists {
		return runs.Run{}, runs.ErrAlreadyExists
	}
	r := runs.Run{WorkflowID: id, RunID: "original-run", PlanHash: in.Plan.Hash, Status: "running"}
	if f.executions == nil {
		f.executions = make(map[string]runs.Execution)
	}
	f.executions[id] = runs.Execution{Run: r, RequestHash: in.RequestHash}
	if f.ambiguous {
		f.ambiguous = false
		return runs.Run{}, runs.ErrUnavailable
	}
	return r, nil
}
func (f *gateway) Get(_ context.Context, id string) (runs.Execution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lookupErr != nil {
		return runs.Execution{}, f.lookupErr
	}
	e, exists := f.executions[id]
	if !exists {
		return runs.Execution{}, runs.ErrNotFound
	}
	return e, nil
}
func fixture(t *testing.T) (*provider, *gateway, http.Handler) {
	t.Helper()
	g := NewWithT(t)
	p, e := plan.Build([]plan.Registration{{SchemaVersion: "1", WorkerName: "worker-a", TaskQueue: "worker-a.v1.temporal", WorkflowType: "ExampleChildWorkflow", SourceServiceName: "worker-a", SourceServiceNamespace: "ns-a"}})
	g.Expect(e).NotTo(HaveOccurred())
	reg := &provider{registry.Snapshot{Synced: true, Plan: p}}
	f := &gateway{}
	return reg, f, api.New(reg, runs.New(reg, f), func(context.Context) error { return nil })
}
func request(h http.Handler, method, path, body string, keys ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if keys != nil {
		r.Header["Idempotency-Key"] = keys
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestStartRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		keys       []string
		status     int
	}{
		{"empty body", ``, nil, 400}, {"boolean", `{"message":true}`, nil, 400}, {"invalid UTF-8", "{\"message\":\"\xff\"}", nil, 400}, {"valid", `{"message":"hello"}`, nil, 202}, {"empty string", `{"message":""}`, nil, 202}, {"missing", `{}`, nil, 400}, {"null field", `{"message":null}`, nil, 400}, {"null object", `null`, nil, 400}, {"number", `{"message":1}`, nil, 400}, {"array", `[]`, nil, 400}, {"unknown", `{"message":"x","secret":1}`, nil, 400}, {"case", `{"Message":"x"}`, nil, 202}, {"duplicate field", `{"message":"x","message":"y"}`, nil, 202}, {"duplicate final null", `{"message":"x","message":null}`, nil, 400}, {"duplicate wrong type first", `{"message":1,"message":"x"}`, nil, 400}, {"duplicate wrong type last", `{"message":"x","message":1}`, nil, 400}, {"unknown before message", `{"secret":1,"message":"x"}`, nil, 400}, {"object field", `{"message":{}}`, nil, 400}, {"array field", `{"message":[]}`, nil, 400}, {"root string", `"hello"`, nil, 400}, {"trailing", `{"message":"x"} {}`, nil, 400}, {"trailing malformed", `{"message":"x"} !`, nil, 400}, {"broken", `{"message":`, nil, 400},
		{"oversize", `{"message":"` + strings.Repeat("x", 16384) + `"}`, nil, 413}, {"oversize trailing whitespace", `{"message":"x"}` + strings.Repeat(" ", 16384), nil, 413}, {"exact limit", `{"message":"x"}` + strings.Repeat(" ", 16384-len(`{"message":"x"}`)), nil, 202},
		{"empty key", `{"message":"x"}`, []string{""}, 400}, {"long key", `{"message":"x"}`, []string{strings.Repeat("a", 129)}, 400}, {"control key", `{"message":"x"}`, []string{"bad\t"}, 400}, {"unicode key", `{"message":"x"}`, []string{"é"}, 400}, {"multiple keys", `{"message":"x"}`, []string{"a", "b"}, 400}, {"printable including spaces", `{"message":"x"}`, []string{" !~ "}, 202}, {"128 key", `{"message":"x"}`, []string{strings.Repeat("a", 128)}, 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			_, f, h := fixture(t)
			w := request(h, "POST", "/runs", tc.body, tc.keys...)
			g.Expect(w.Code).To(Equal(tc.status))
			g.Expect(w.Header().Get("Content-Type")).To(ContainSubstring("application/json"))
			if tc.status != 202 {
				g.Expect(f.starts).To(BeZero())
			}
		})
	}
}
func TestStartRequestSelectedMessage(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"case insensitive", `{"Message":"hello"}`},
		{"uppercase", `{"MESSAGE":"hello"}`},
		{"duplicate last wins", `{"message":"discarded","message":"hello"}`},
		{"mixed casing last wins", `{"message":"discarded","Message":"hello"}`},
		{"null before string", `{"message":null,"message":"hello"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			_, f, h := fixture(t)
			response := request(h, "POST", "/runs", tc.body, "selected-message")
			g.Expect(response.Code).To(Equal(202))
			var run runs.Run
			g.Expect(json.Unmarshal(response.Body.Bytes(), &run)).To(Succeed())
			g.Expect(f.executions[run.WorkflowID].RequestHash).To(Equal("9b2d43affbf49a367028df2e1414f84c0e099ac98c3d54a8a80157fd7771af25"))
			g.Expect(request(h, "POST", "/runs", `{"message":"hello"}`, "selected-message").Code).To(Equal(202))
			g.Expect(request(h, "POST", "/runs", `{"message":"discarded"}`, "selected-message").Code).To(Equal(409))
			g.Expect(f.starts).To(Equal(1))
		})
	}
}

func TestHTTPDurableRepeatConflictAndRegistryChange(t *testing.T) {
	g := NewWithT(t)
	p, f, h := fixture(t)
	w := request(h, "POST", "/runs", `{"message":"hello"}`, "key")
	g.Expect(w.Code).To(Equal(202))
	var first map[string]interface{}
	g.Expect(json.Unmarshal(w.Body.Bytes(), &first)).To(Succeed())
	g.Expect(first).To(HaveKey("workflowId"))
	g.Expect(first).To(HaveKey("runId"))
	g.Expect(first).To(HaveKeyWithValue("planHash", p.s.Plan.Hash))
	g.Expect(first).NotTo(HaveKey("WorkflowID"))
	p.s = registry.Snapshot{Problems: []string{"secret invalid"}}
	w = request(h, "POST", "/runs", `{"message":"hello"}`, "key")
	g.Expect(w.Code).To(Equal(202))
	g.Expect(f.starts).To(Equal(1))
	w = request(h, "POST", "/runs", `{"message":"other"}`, "key")
	g.Expect(w.Code).To(Equal(409))
	g.Expect(w.Body.String()).NotTo(ContainSubstring("key"))
	w = request(h, "GET", "/runs/"+first["workflowId"].(string), "")
	g.Expect(w.Code).To(Equal(200))
}
func TestHTTPConcurrentDuplicatesAndCompletedRetryAfterRestart(t *testing.T) {
	g := NewWithT(t)
	p, f, h := fixture(t)
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 20)
	for range 20 {
		wg.Go(func() { responses <- request(h, "POST", "/runs", `{ "message" : "hello" }`, "durable") })
	}
	wg.Wait()
	close(responses)
	var original runs.Run
	for response := range responses {
		g.Expect(response.Code).To(Equal(202))
		var run runs.Run
		g.Expect(json.Unmarshal(response.Body.Bytes(), &run)).To(Succeed())
		if original.RunID == "" {
			original = run
		}
		g.Expect(run).To(Equal(original))
	}
	g.Expect(f.executions).To(HaveLen(1))
	execution := f.executions[original.WorkflowID]
	execution.Run.Status = "completed"
	execution.Run.Result = &contracts.ParentResult{Results: []contracts.ChildResult{{WorkerName: "worker-a", Message: "hello"}}}
	f.executions[original.WorkflowID] = execution
	p.s = registry.Snapshot{Problems: []string{"invalid after restart"}}
	restarted := api.New(p, runs.New(p, f), func(context.Context) error { return nil })
	response := request(restarted, "POST", "/runs", `{"message":"hello"}`, "durable")
	g.Expect(response.Code).To(Equal(202))
	var repeated runs.Run
	g.Expect(json.Unmarshal(response.Body.Bytes(), &repeated)).To(Succeed())
	g.Expect(repeated).To(Equal(execution.Run))
	g.Expect(request(restarted, "POST", "/runs", `{"message":"different"}`, "durable").Code).To(Equal(409))
}

func TestHTTPAmbiguousStartAndUnavailableLookup(t *testing.T) {
	g := NewWithT(t)
	_, f, h := fixture(t)
	f.ambiguous = true
	response := request(h, "POST", "/runs", `{"message":"hello"}`, "ambiguous")
	g.Expect(response.Code).To(Equal(503))
	g.Expect(response.Body.String()).NotTo(ContainSubstring("original-run"))
	f.lookupErr = runs.ErrUnavailable
	g.Expect(request(h, "POST", "/runs", `{"message":"hello"}`, "ambiguous").Code).To(Equal(503))
	g.Expect(f.starts).To(Equal(1))
	f.lookupErr = nil
	g.Expect(request(h, "POST", "/runs", `{"message":"hello"}`, "ambiguous").Code).To(Equal(202))
	g.Expect(f.starts).To(Equal(1))
}

func TestHTTPErrorStatusesAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{{"unavailable", runs.ErrUnavailable, 503}, {"unexpected", errors.New("sensitive database token"), 500}} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			_, f, h := fixture(t)
			f.startErr = tc.err
			w := request(h, "POST", "/runs", `{"message":"hello"}`)
			g.Expect(w.Code).To(Equal(tc.status))
			g.Expect(w.Body.String()).NotTo(ContainSubstring("sensitive"))
			f.lookupErr = tc.err
			w = request(h, "GET", "/runs/unknown", "")
			g.Expect(w.Code).To(Equal(tc.status))
		})
	}
	g := NewWithT(t)
	p, f, h := fixture(t)
	g.Expect(request(h, "GET", "/runs/unknown", "").Code).To(Equal(404))
	p.s.Synced = false
	g.Expect(request(h, "POST", "/runs", `{"message":"hello"}`).Code).To(Equal(503))
	g.Expect(f.starts).To(BeZero())
}
func TestHealthReadinessAndInvalidWorkersDiagnostics(t *testing.T) {
	g := NewWithT(t)
	p, f, _ := fixture(t)
	p.s.Problems = []string{"cycle"}
	h := api.New(p, runs.New(p, f), func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		g.Expect(ok).To(BeTrue())
		g.Expect(time.Until(deadline)).To(BeNumerically("<=", 2*time.Second))
		return nil
	})
	g.Expect(request(h, "GET", "/readyz", "").Code).To(Equal(200))
	g.Expect(request(h, "GET", "/healthz", "").Code).To(Equal(200))
	w := request(h, "GET", "/workers", "")
	g.Expect(w.Code).To(Equal(200))
	g.Expect(w.Body.String()).To(ContainSubstring("cycle"))
	g.Expect(w.Body.String()).To(ContainSubstring(p.s.Plan.Hash))
	p.s.Synced = false
	g.Expect(request(h, "GET", "/readyz", "").Code).To(Equal(503))
	g.Expect(request(h, "GET", "/workers", "").Code).To(Equal(200))
	g.Expect(request(h, "GET", "/healthz", "").Code).To(Equal(200))
	p.s.Synced = true
	h = api.New(p, runs.New(p, f), func(context.Context) error { return errors.New("secret") })
	w = request(h, "GET", "/readyz", "")
	g.Expect(w.Code).To(Equal(503))
	g.Expect(w.Body.String()).NotTo(ContainSubstring("secret"))
	g.Expect(request(h, "GET", "/healthz", "").Code).To(Equal(200))
}

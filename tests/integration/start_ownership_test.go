//go:build integration && (acceptance_integration || resilience_integration)

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onsi/gomega"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

// Literal SHA256 of the literal key below, independently computed. Only this
// exact parent and history-proven child may be cleaned, never the foreign run.
const ownershipID = "run-5378a11e3f10877761a3b3314653a83700380fb0f806b91d26542d61cd2ef6f9"
const ownershipKey = "task10-owned-intent-regression"

type intentRecord struct {
	ID, RunID string
	Pending   bool
}
type ownershipCall struct {
	Op, ID, RunID string
	BoundedFresh  bool
}
type ownershipState struct {
	Requests               int
	IntentBeforePOST       bool
	UnpinnedUse            bool
	Parent, Child, Foreign bool
	Calls                  []ownershipCall
}

// Only Temporal's external boundary is replaced. Actual start assertions,
// durable ledger, reconciliation, cleanup callbacks and finish/history logic run.
type ownershipClient struct {
	client.Client
	mu           sync.Mutex
	dir, failure string
}

func (c *ownershipClient) state() ownershipState {
	b, err := os.ReadFile(filepath.Join(c.dir, "state.json"))
	if err != nil {
		panic(err)
	}
	var s ownershipState
	if err = json.Unmarshal(b, &s); err != nil {
		panic(err)
	}
	return s
}
func (c *ownershipClient) save(s ownershipState) {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile(filepath.Join(c.dir, "state.json"), b, 0600); err != nil {
		panic(err)
	}
}
func (c *ownershipClient) trace(ctx context.Context, op, id, rid string) {
	deadline, ok := ctx.Deadline()
	s := c.state()
	if id == ownershipID && rid != "" {
		b, _ := os.ReadFile(filepath.Join(c.dir, "runs.jsonl"))
		pinned := false
		for _, line := range strings.Split(string(b), "\n") {
			var ref intentRecord
			if json.Unmarshal([]byte(line), &ref) == nil && ref.ID == ownershipID && ref.RunID == "parent-run" && !ref.Pending {
				pinned = true
			}
		}
		if !pinned {
			s.UnpinnedUse = true
		}
	}
	s.Calls = append(s.Calls, ownershipCall{op, id, rid, ok && ctx.Err() == nil && time.Until(deadline) <= 16*time.Second})
	c.save(s)
}

type ownershipRun struct {
	client.WorkflowRun
	id, rid string
}

func (r ownershipRun) GetID() string    { return r.id }
func (r ownershipRun) GetRunID() string { return r.rid }
func (c *ownershipClient) GetWorkflow(_ context.Context, id, rid string) client.WorkflowRun {
	return ownershipRun{id: id, rid: rid}
}
func (c *ownershipClient) DescribeWorkflowExecution(ctx context.Context, id, rid string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace(ctx, "describe", id, rid)
	if rid == "" && c.failure == "unavailable" {
		return nil, serviceerror.NewUnavailable("intent-lookup-unavailable")
	}
	if rid == "" && c.failure == "not_found" {
		return nil, serviceerror.NewNotFound("not accepted")
	}
	if rid == "" && c.failure == "bad_lookup" {
		return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: "foreign", RunId: "foreign-run"}}}, nil
	}
	s := c.state()
	running := false
	actual := ""
	switch id {
	case ownershipID:
		actual = "parent-run"
		running = s.Parent
	case ownershipID + "/worker-a":
		actual = "child-run"
		running = s.Child
	case "foreign":
		actual = "foreign-run"
		running = s.Foreign
	default:
		return nil, fmt.Errorf("unrelated lookup: %s/%s", id, rid)
	}
	if rid != "" && rid != actual {
		return nil, fmt.Errorf("wrong pinned run: %s/%s", id, rid)
	}
	status := enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED
	if running {
		status = enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
	}
	return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: id, RunId: actual}, Status: status}}, nil
}
func (c *ownershipClient) TerminateWorkflow(ctx context.Context, id, rid, reason string, details ...any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace(ctx, "terminate", id, rid)
	// An empty run ID is not permitted to target an arbitrary current execution.
	if rid == "" {
		return fmt.Errorf("unsafe empty-run termination")
	}
	s := c.state()
	if id == ownershipID && c.failure == "terminate_failure" {
		return serviceerror.NewUnavailable("owned-terminate-unavailable")
	}
	switch id {
	case ownershipID:
		s.Parent = false
	case ownershipID + "/worker-a":
		s.Child = false
	case "foreign":
		s.Foreign = false
	default:
		return fmt.Errorf("foreign termination: %s", id)
	}
	c.save(s)
	return nil
}

type ownershipHistory struct{ events []*historypb.HistoryEvent }

func (i *ownershipHistory) HasNext() bool { return len(i.events) > 0 }
func (i *ownershipHistory) Next() (*historypb.HistoryEvent, error) {
	e := i.events[0]
	i.events = i.events[1:]
	return e, nil
}
func (c *ownershipClient) GetWorkflowHistory(ctx context.Context, id, rid string, _ bool, _ enumspb.HistoryEventFilterType) client.HistoryEventIterator {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trace(ctx, "history", id, rid)
	if id != ownershipID || rid != "parent-run" {
		return &ownershipHistory{}
	}
	return &ownershipHistory{events: []*historypb.HistoryEvent{{Attributes: &historypb.HistoryEvent_ChildWorkflowExecutionStartedEventAttributes{ChildWorkflowExecutionStartedEventAttributes: &historypb.ChildWorkflowExecutionStartedEventAttributes{WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: ownershipID + "/worker-a", RunId: "child-run"}}}}}}
}

func TestStartIntentOwnership(t *testing.T) {
	if mode := os.Getenv("TASK10_OWNERSHIP_CHILD"); mode != "" {
		dir := os.Getenv("OWNERSHIP_DIR")
		c := &ownershipClient{dir: dir, failure: mode}
		h := &acceptanceHarness{harness: &harness{t: t, g: gomega.NewWithT(t), c: c}, ctx: context.Background()}
		if mode == "fallback" || mode == "diagnostics" {
			file, err := os.Open(os.Getenv("SCALING_RUNS_FILE"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if mode == "diagnostics" {
				t.Setenv("ACCEPTANCE_PROOF", dir)
				h.executionDiagnostics(file, dir)
			} else {
				h.cleanupLedger(file)
			}
			return
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.Path != "/runs" || r.Header.Get("Idempotency-Key") != ownershipKey {
				http.Error(w, "wrong keyed request", 400)
				return
			}
			c.mu.Lock()
			s := c.state()
			s.Requests++
			b, _ := os.ReadFile(os.Getenv("SCALING_RUNS_FILE"))
			var ref intentRecord
			if json.Unmarshal(b, &ref) == nil {
				s.IntentBeforePOST = ref.ID == ownershipID && ref.RunID == "" && ref.Pending
			}
			if mode != "not_found" {
				s.Parent = true
				s.Child = true
			}
			c.save(s)
			c.mu.Unlock()
			if mode == "process_timeout" {
				_ = os.WriteFile(filepath.Join(dir, "request-reached"), []byte("ready"), 0600)
				<-r.Context().Done()
				return
			}
			if mode == "lost_response" {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					panic(err)
				}
				_ = conn.Close()
				return
			}
			if mode == "success" || mode == "wrong_identity" || mode == "empty_run" {
				id := ownershipID
				rid := "parent-run"
				if mode == "wrong_identity" {
					id = "foreign"
					rid = "foreign-run"
				}
				if mode == "empty_run" {
					rid = ""
				}
				w.WriteHeader(202)
				_ = json.NewEncoder(w).Encode(map[string]string{"workflowId": id, "runId": rid})
				return
			}
			if mode == "malformed_response" {
				w.WriteHeader(202)
				_, _ = w.Write([]byte("{invalid"))
				return
			}
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"service unavailable"}`))
		}))
		defer server.Close()
		h.endpoint = server.URL
		key := ownershipKey
		if mode == "invalid_key" {
			key = ""
		}
		h.start(key)
		return
	}
	for _, mode := range []string{"accepted503", "lost_response", "success", "malformed_response", "wrong_identity", "not_found", "unavailable", "bad_lookup", "persistence_failure", "process_timeout", "pending_fallback", "pending_diagnostics", "legacy_fallback", "truncated_fallback", "invalid_fallback", "terminate_failure", "empty_run", "invalid_key"} {
		t.Run(mode, func(t *testing.T) {
			g := gomega.NewWithT(t)
			dir := t.TempDir()
			c := &ownershipClient{dir: dir}
			c.save(ownershipState{Foreign: true})
			path := filepath.Join(dir, "runs.jsonl")
			if mode == "persistence_failure" {
				path = dir
			}
			runChild := func(which string, killAfterRequest bool) ([]byte, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStartIntentOwnership$", "-test.v")
				cmd.Env = append(os.Environ(), "TASK10_OWNERSHIP_CHILD="+which, "OWNERSHIP_DIR="+dir, "SCALING_RUNS_FILE="+path)
				if !killAfterRequest {
					return cmd.CombinedOutput()
				}
				// Kill only the child after the server has received POST, before any reply
				// or Go cleanup. The durable pending row must drive the real shell fallback.
				log, err := os.Create(filepath.Join(dir, "child.log"))
				g.Expect(err).NotTo(gomega.HaveOccurred())
				defer log.Close()
				cmd.Stdout = log
				cmd.Stderr = log
				g.Expect(cmd.Start()).To(gomega.Succeed())
				defer func() {
					if cmd.ProcessState == nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				}()
				g.Eventually(func() bool { _, err := os.Stat(filepath.Join(dir, "request-reached")); return err == nil }, 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
				g.Expect(cmd.Process.Kill()).To(gomega.Succeed())
				err = cmd.Wait()
				b, _ := os.ReadFile(log.Name())
				return b, err
			}
			if mode == "pending_diagnostics" {
				g.Expect(os.WriteFile(path, []byte(`{"ID":"`+ownershipID+`","RunID":"","Pending":true}`+"\n"), 0600)).To(gomega.Succeed())
				c.save(ownershipState{Parent: true, Child: true, Foreign: true})
				out, err := runChild("diagnostics", false)
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
				_, err = os.Stat(filepath.Join(dir, "diagnostic-parent-parent-run.json"))
				g.Expect(err).NotTo(gomega.HaveOccurred(), "diagnostics must persist and use recovered parent pin")
				histories := map[string]string{}
				for _, call := range c.state().Calls {
					if call.Op == "history" {
						histories[call.ID] = call.RunID
					}
				}
				g.Expect(histories).To(gomega.Equal(map[string]string{ownershipID: "parent-run", ownershipID + "/worker-a": "child-run"}))
				files, err := filepath.Glob(filepath.Join(dir, "history-*.json"))
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(files).To(gomega.HaveLen(2))
				g.Expect(c.state().Parent && c.state().Child && c.state().Foreign).To(gomega.BeTrue(), "diagnostics must remain read-only")
				g.Expect(c.state().UnpinnedUse).To(gomega.BeFalse(), "diagnostics must persist pin before exact history use")
				return
			}
			if mode == "pending_fallback" || mode == "legacy_fallback" || mode == "truncated_fallback" || mode == "invalid_fallback" {
				row := `{"ID":"` + ownershipID + `","RunID":"","Pending":true}` + "\n"
				if mode == "legacy_fallback" {
					row = `{"ID":"` + ownershipID + `","RunID":"parent-run"}` + "\n"
				}
				if mode == "truncated_fallback" {
					row += `{"ID":`
				}
				if mode == "invalid_fallback" {
					row = `{"ID":"` + ownershipID + `","RunID":""}` + "\n"
				}
				g.Expect(os.WriteFile(path, []byte(row), 0600)).To(gomega.Succeed())
				c.save(ownershipState{Parent: true, Child: true, Foreign: true})
				out, err := runChild("fallback", false)
				if mode == "invalid_fallback" {
					g.Expect(err).To(gomega.HaveOccurred())
					g.Expect(c.state().Calls).To(gomega.BeEmpty())
					g.Expect(c.state().Foreign).To(gomega.BeTrue())
					return
				}
				if mode == "truncated_fallback" {
					g.Expect(err).To(gomega.HaveOccurred())
					g.Expect(string(out)).To(gomega.ContainSubstring("ledger decode"))
				} else {
					g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
				}
				s := c.state()
				g.Expect(s.Parent || s.Child).To(gomega.BeFalse())
				g.Expect(s.Foreign).To(gomega.BeTrue())
				g.Expect(s.UnpinnedUse).To(gomega.BeFalse(), "fallback must persist recovered pin before finish")
				before := len(s.Calls)
				out, err = runChild("fallback", false)
				if mode == "truncated_fallback" {
					g.Expect(err).To(gomega.HaveOccurred())
					g.Expect(string(out)).To(gomega.ContainSubstring("ledger decode"))
				} else {
					g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
				}
				for _, call := range c.state().Calls[before:] {
					g.Expect(call.RunID).NotTo(gomega.BeEmpty(), "valid persisted pin after even a partial tail must supersede pending")
				}
				return
			}
			out, err := runChild(mode, mode == "process_timeout")
			t.Logf("%s child (nonzero expected except success):\n%s", mode, out)
			if mode == "success" {
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
			} else {
				g.Expect(err).To(gomega.HaveOccurred())
			}
			s := c.state()
			g.Expect(s.Foreign).To(gomega.BeTrue(), "unrelated execution must never be adopted")
			if mode == "persistence_failure" || mode == "invalid_key" {
				g.Expect(s.Requests).To(gomega.BeZero(), "unpersistable or invalid intent must prevent POST")
				return
			}
			g.Expect(s.Requests).To(gomega.Equal(1), "ambiguous start must not be retried")
			g.Expect(s.IntentBeforePOST).To(gomega.BeTrue(), "exact pending ownership must be persisted BEFORE POST")
			if mode == "accepted503" || mode == "unavailable" || mode == "not_found" || mode == "bad_lookup" || mode == "terminate_failure" {
				g.Expect(string(out)).To(gomega.ContainSubstring("503"), "original failed start assertion must remain")
			}
			if mode == "lost_response" {
				g.Expect(string(out)).To(gomega.ContainSubstring("EOF"), "original lost-response failure must remain")
			}
			b, err := os.ReadFile(path)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(string(b)).To(gomega.ContainSubstring(ownershipID))
			if mode == "process_timeout" || mode == "unavailable" || mode == "bad_lookup" {
				g.Expect(s.Parent && s.Child).To(gomega.BeTrue(), "unresolved ownership must stay pending, not imply absence")
				if mode == "unavailable" {
					g.Expect(string(out)).To(gomega.ContainSubstring("intent-lookup-unavailable"))
				}
				if mode == "bad_lookup" {
					g.Expect(string(out)).To(gomega.ContainSubstring("identity"))
				}
				fallbackOut, fallbackErr := runChild("fallback", false)
				g.Expect(fallbackErr).NotTo(gomega.HaveOccurred(), string(fallbackOut))
				s = c.state()
			}
			if mode == "terminate_failure" {
				g.Expect(s.Parent).To(gomega.BeTrue())
				g.Expect(s.Child).To(gomega.BeFalse())
				g.Expect(string(out)).To(gomega.ContainSubstring("owned-terminate-unavailable"))
				fallbackOut, fallbackErr := runChild("fallback", false)
				g.Expect(fallbackErr).NotTo(gomega.HaveOccurred(), string(fallbackOut))
				s = c.state()
			}
			g.Expect(s.Parent || s.Child).To(gomega.BeFalse(), "parent AND history-proven child must be finished")
			g.Expect(s.UnpinnedUse).To(gomega.BeFalse(), "exact pin must be durable before any finish/history operation")
			if mode == "not_found" {
				g.Expect(s.Calls).To(gomega.HaveLen(1))
				g.Expect(s.Calls[0].Op).To(gomega.Equal("describe"))
				g.Expect(string(out)).To(gomega.ContainSubstring("not found at reconciliation"))
				// NotFound is only a point-in-time safe outcome. Retain pending intent so
				// a later shell fallback can resolve acceptance that became visible later.
				s.Parent = true
				s.Child = true
				c.save(s)
				fallbackOut, fallbackErr := runChild("fallback", false)
				g.Expect(fallbackErr).NotTo(gomega.HaveOccurred(), string(fallbackOut))
				s = c.state()
				g.Expect(s.Parent || s.Child).To(gomega.BeFalse())
			}
			for _, call := range s.Calls {
				g.Expect(call.BoundedFresh).To(gomega.BeTrue(), "cleanup must use fresh bounded contexts: %+v", call)
				if call.Op == "terminate" || call.Op == "history" {
					g.Expect(call.RunID).NotTo(gomega.BeEmpty(), "never use an empty run ID to mutate/read an arbitrary current run")
				}
			}
			// A fallback must prefer a persisted pin, not resolve ID-only again and
			// accidentally target another current run. Also exercises repeat cleanup.
			before := len(s.Calls)
			fallbackOut, fallbackErr := runChild("fallback", false)
			g.Expect(fallbackErr).NotTo(gomega.HaveOccurred(), string(fallbackOut))
			for _, call := range c.state().Calls[before:] {
				g.Expect(call.RunID).NotTo(gomega.BeEmpty(), "persisted run-ID pin must supersede pending intent")
			}
		})
	}
}

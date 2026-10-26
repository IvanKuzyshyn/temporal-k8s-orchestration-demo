package process

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/runtimeconfig"
	"github.com/onsi/gomega"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// The double models the documented SDK boundary: a fatal hook runs off-poller,
// returns before SDK Stop, and concurrent Stop calls join the same cleanup.
// No test directly edits process readiness or admission state.
type lifecycleWorker struct {
	worker.Worker
	start       func() error
	stopStarted chan struct{}
	allowStop   chan struct{}
	once        sync.Once
}

func (w *lifecycleWorker) RegisterWorkflowWithOptions(interface{}, workflow.RegisterOptions) {}
func (w *lifecycleWorker) Start() error                                                      { return w.start() }
func (w *lifecycleWorker) Stop()                                                             { w.once.Do(func() { close(w.stopStarted) }); <-w.allowStop }

type lifecycleClient struct {
	client.Client
	closed chan struct{}
}

func (c *lifecycleClient) CheckHealth(context.Context, *client.CheckHealthRequest) (*client.CheckHealthResponse, error) {
	return &client.CheckHealthResponse{}, nil
}
func (c *lifecycleClient) Close() { close(c.closed) }

func TestFatalWorkerStopsRunAndAdmission(t *testing.T) {
	for _, duringStart := range []bool{false, true} {
		name := "after polling"
		if duringStart {
			name = "racing Start"
		}
		t.Run(name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			g.Expect(err).NotTo(gomega.HaveOccurred())
			addr := listener.Addr().String()
			g.Expect(listener.Close()).To(gomega.Succeed())
			c, err := runtimeconfig.Load("orchestrator", func(key string) string {
				switch key {
				case "HTTP_ADDR":
					return addr
				case "REGISTRY_FILE":
					return "../../deploy/config/workers.json"
				}
				return ""
			})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			startContinue := make(chan struct{})
			w := &lifecycleWorker{stopStarted: make(chan struct{}), allowStop: make(chan struct{}), start: func() error { <-startContinue; return nil }}
			tc := &lifecycleClient{closed: make(chan struct{})}
			opts := make(chan worker.Options, 1)
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			var startOnce, stopOnce sync.Once
			releaseStart := func() { startOnce.Do(func() { close(startContinue) }) }
			releaseStop := func() { stopOnce.Do(func() { close(w.allowStop) }) }
			defer func() { cancel(); releaseStart(); releaseStop() }()
			go func() {
				result <- run(ctx, "orchestrator", c, slog.New(slog.NewTextHandler(io.Discard, nil)),
					func(context.Context, client.Options) (client.Client, error) { return tc, nil },
					func(_ client.Client, _ string, o worker.Options) worker.Worker { opts <- o; return w },
				)
			}()
			var options worker.Options
			g.Eventually(opts, 2*time.Second).Should(gomega.Receive(&options))
			httpClient := &http.Client{Timeout: 100 * time.Millisecond}
			status := func(method, path string) int {
				req, e := http.NewRequest(method, "http://"+addr+path, nil)
				if e != nil {
					return 0
				}
				res, e := httpClient.Do(req)
				if e != nil {
					return 0
				}
				defer res.Body.Close()
				_, _ = io.Copy(io.Discard, res.Body)
				return res.StatusCode
			}
			if !duringStart {
				releaseStart()
				g.Eventually(func() int { return status("GET", "/readyz") }, 2*time.Second).Should(gomega.Equal(200))
			}
			cause := errors.New("fatal polling failure")
			notified := make(chan struct{})
			go func() {
				if options.OnFatalError != nil {
					options.OnFatalError(cause)
				}
				close(notified)
				w.Stop()
			}()
			// Notification must not synchronously wait for Stop (or the callback cannot
			// finish and the SDK cannot proceed to its own cleanup).
			g.Eventually(notified, time.Second).Should(gomega.BeClosed())
			g.Eventually(w.stopStarted, time.Second).Should(gomega.BeClosed())
			releaseStart()
			g.Eventually(func() int { return status("GET", "/healthz") }, time.Second).Should(gomega.Equal(200))
			g.Eventually(func() int { return status("GET", "/readyz") }, time.Second).Should(gomega.Equal(503))
			g.Expect(status("POST", "/runs")).To(gomega.Equal(503))
			g.Consistently(result, 50*time.Millisecond).ShouldNot(gomega.Receive())
			releaseStop()
			var fatal error
			g.Eventually(result, time.Second).Should(gomega.Receive(&fatal))
			g.Expect(fatal).To(gomega.MatchError(cause))
			g.Expect(tc.closed).To(gomega.BeClosed())
		})
	}
}

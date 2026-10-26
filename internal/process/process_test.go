package process

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onsi/gomega"
)

func TestRetryHonorsCancellationAndSuccess(t *testing.T) {
	g := gomega.NewWithT(t)
	n := 0
	g.Expect(retry(context.Background(), time.Millisecond, func(context.Context) error {
		n++
		if n < 3 {
			return errors.New("not yet")
		}
		return nil
	})).To(gomega.Succeed())
	g.Expect(n).To(gomega.Equal(3))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.Expect(retry(ctx, time.Hour, func(context.Context) error { return errors.New("offline") })).To(gomega.MatchError(context.Canceled))
}

func TestNoNewStartsBeforePollingOrDuringShutdown(t *testing.T) {
	g := gomega.NewWithT(t)
	h := &health{}
	forwarded := 0
	handler := h.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded++; w.WriteHeader(202) }))
	start := func() int {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/runs", nil))
		return w.Code
	}
	g.Expect(start()).To(gomega.Equal(503))
	g.Expect(forwarded).To(gomega.Equal(0))
	h.state.Store(workerPolling)
	g.Expect(start()).To(gomega.Equal(202))
	h.state.Store(workerStopped)
	g.Expect(start()).To(gomega.Equal(503))
	g.Expect(forwarded).To(gomega.Equal(1))
}

func TestHealthSeparatesPollingAndConnectivityFromLiveness(t *testing.T) {
	g := gomega.NewWithT(t)
	connected := false
	h := &health{check: func(ctx context.Context) error {
		_, ok := ctx.Deadline()
		g.Expect(ok).To(gomega.BeTrue())
		if !connected {
			return errors.New("offline")
		}
		return nil
	}}
	request := func(path string) int {
		r := httptest.NewRecorder()
		h.router().ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		return r.Code
	}
	g.Expect(request("/healthz")).To(gomega.Equal(200))
	g.Expect(request("/readyz")).To(gomega.Equal(503))
	connected = true
	g.Expect(request("/readyz")).To(gomega.Equal(503))
	h.state.Store(workerPolling)
	g.Expect(request("/readyz")).To(gomega.Equal(200))
	connected = false
	g.Expect(request("/readyz")).To(gomega.Equal(503))
	g.Expect(request("/healthz")).To(gomega.Equal(200))
	h.state.Store(workerStopped)
	connected = true
	g.Expect(request("/readyz")).To(gomega.Equal(503))
}

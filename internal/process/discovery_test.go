package process

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/runtimeconfig"
	"github.com/onsi/gomega"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// Kubernetes mode must use the live adapter, not read RegistryFile or kill HTTP
// when the graph is invalid. Readiness follows synchronization, not validity.
func TestProcessKubernetesDiscovery(t *testing.T) {
	g := gomega.NewWithT(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	addr := l.Addr().String()
	g.Expect(l.Close()).To(gomega.Succeed())
	c, err := runtimeconfig.Load("orchestrator", func(key string) string {
		switch key {
		case "HTTP_ADDR":
			return addr
		case "REGISTRY_MODE":
			return "kubernetes"
		case "REGISTRY_FILE":
			return "/must/not/be/read"
		}
		return ""
	})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	kc := fake.NewClientset(&core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "a", Labels: map[string]string{"temporal-demo.io/worker-namespace": "true"}}})
	w := &lifecycleWorker{stopStarted: make(chan struct{}), allowStop: make(chan struct{}), start: func() error { return nil }}
	close(w.allowStop)
	tc := &lifecycleClient{closed: make(chan struct{})}
	hc := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), Timeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, "orchestrator", c, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context, client.Options) (client.Client, error) { return tc, nil }, func(client.Client, string, worker.Options) worker.Worker { return w }, func() (kubernetes.Interface, error) { return kc, nil })
	}()
	t.Cleanup(func() {
		// A timed-out request may leave a background dial in the pool without
		// sending headers. Release our connections before HTTP Shutdown joins.
		hc.CloseIdleConnections()
		cancel()
		g.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.BeNil()))
	})
	status := func(path string) int {
		res, err := hc.Get("http://" + addr + path)
		if err != nil {
			return 0
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	g.Eventually(func() int { return status("/readyz") }, 5*time.Second).Should(gomega.Equal(200))
	res, err := hc.Get("http://" + addr + "/workers")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(string(b)).To(gomega.ContainSubstring("empty worker registry"))
	g.Expect(status("/healthz")).To(gomega.Equal(200))
}

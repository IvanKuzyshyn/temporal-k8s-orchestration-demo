package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// A server-side timeout cannot bound a black-holed TCP stream. Exercise the real
// REST client: even with no events or server EOF, the client must reconnect,
// while an idle healthy cache must remain synchronized across that reconnect.
func TestKubernetesClientBoundsIdleConnection(t *testing.T) {
	g := gomega.NewWithT(t)
	var watches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces" {
			t.Errorf("unexpected resource %s", r.URL)
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("labelSelector") != "temporal-demo.io/worker-namespace=true" {
			t.Errorf("missing selector: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("watch") == "true" {
			watches.Add(1)
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{"apiVersion":"v1","kind":"NamespaceList","metadata":{"resourceVersion":"1"},"items":[]}`)
	}))
	defer server.Close()
	c, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	k := NewKubernetes(c, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx) }()
	defer func() { cancel(); g.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.BeNil())) }()
	g.Eventually(func() bool { return k.Snapshot().Synced }, 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
	g.Eventually(func() int32 { return watches.Load() }, 17*time.Second, 10*time.Millisecond).Should(gomega.BeNumerically(">=", 2))
	g.Expect(k.Snapshot().Synced).To(gomega.BeTrue())
}

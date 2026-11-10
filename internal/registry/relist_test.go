package registry

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

// HasSynced alone stays true during relist: no old/partial cache may be accepted
// while a fresh RV list is in flight or has not drained into the informer store.
func TestKubernetesRelistBarrier(t *testing.T) {
	for _, resource := range []string{"namespaces", "configmaps"} {
		t.Run(resource, func(t *testing.T) {
			g := gomega.NewWithT(t)
			c := fake.NewClientset(namespace("a"), registration("a", "worker-a", ""))
			var relist atomic.Bool
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			defer close(release)
			c.PrependReactor("list", resource, func(a kt.Action) (bool, runtime.Object, error) {
				if relist.Load() {
					entered <- struct{}{}
					<-release
				}
				return false, nil, nil
			})
			watches := make(chan *watch.RaceFreeFakeWatcher, 10)
			c.PrependWatchReactor(resource, func(a kt.Action) (bool, watch.Interface, error) {
				w := watch.NewRaceFreeFake()
				watches <- w
				return true, w, nil
			})
			k := startRegistry(t, c)
			g.Eventually(validSnapshot(k, 1), 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
			relist.Store(true)
			w := <-watches
			w.Error(&metav1.Status{Status: "Failure", Code: 410, Reason: metav1.StatusReasonExpired})
			g.Eventually(entered, 5*time.Second).Should(gomega.Receive())
			g.Eventually(func() bool { return k.Snapshot().Synced }, time.Second, 10*time.Millisecond).Should(gomega.BeFalse())
		})
	}
}

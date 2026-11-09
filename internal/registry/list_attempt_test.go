package registry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onsi/gomega"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

// The production coordinator calls ready; controlling that call here lets list A
// reach the native informer store without acknowledging it. A failed WATCH must
// cause native Reflector LIST B, and A must not acknowledge B while B is blocked.
func TestKubernetesListAttemptBarrier(t *testing.T) {
	for _, resource := range []string{"namespaces", "configmaps"} {
		t.Run(resource, func(t *testing.T) {
			g := gomega.NewWithT(t)
			c := fake.NewClientset(namespace("a"), registration("a", "worker-a", ""))
			failWatchA, releaseListB := make(chan struct{}), make(chan struct{})
			listBEntered, watchBEntered := make(chan struct{}), make(chan struct{})
			var failOnce, releaseOnce sync.Once
			failA := func() { failOnce.Do(func() { close(failWatchA) }) }
			releaseB := func() { releaseOnce.Do(func() { close(releaseListB) }) }
			w := watch.NewRaceFreeFake()
			lists, watches := 0, 0
			c.PrependReactor("list", resource, func(kt.Action) (bool, runtime.Object, error) {
				lists++
				if lists == 2 {
					close(listBEntered)
					<-releaseListB
				}
				return false, nil, nil
			})
			c.PrependWatchReactor(resource, func(kt.Action) (bool, watch.Interface, error) {
				watches++
				if watches == 1 {
					<-failWatchA
					return true, nil, errors.New("fixture WATCH A establishment failed")
				}
				if watches == 2 {
					close(watchBEntered)
				}
				return true, w, nil
			})
			k := NewKubernetes(c, "")
			ctx, cancel := context.WithCancel(context.Background())
			ns := ""
			key := "a"
			if resource == "configmaps" {
				ns = "a"
				key = "a/temporal-worker-worker-a"
			}
			d := k.newCache(ctx, ns)
			t.Cleanup(func() {
				failA()
				releaseB()
				cancel()
				done := make(chan struct{})
				go func() { d.stop(); close(done) }()
				g.Eventually(done, 5*time.Second).Should(gomega.BeClosed())
			})
			g.Eventually(func() bool { return d.informer.HasSynced() }, 5*time.Second).Should(gomega.BeTrue())
			obj, exists, err := d.informer.GetStore().GetByKey(key)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(exists).To(gomega.BeTrue())
			g.Expect(obj).NotTo(gomega.BeNil())
			// A has applied, but ready has deliberately never been called.
			failA()
			g.Eventually(listBEntered, 5*time.Second).Should(gomega.BeClosed())
			ready, problem := d.ready(time.Now())
			g.Expect(ready).To(gomega.BeFalse(), "list A must not acknowledge in-flight list B: %s", problem)
			releaseB()
			g.Eventually(watchBEntered, 5*time.Second).Should(gomega.BeClosed())
			g.Eventually(func() bool { ok, _ := d.ready(time.Now()); return ok }, 5*time.Second).Should(gomega.BeTrue())
			var changed runtime.Object
			if resource == "namespaces" {
				n := namespace("a")
				n.ResourceVersion = "2"
				changed = n
			} else {
				cm := registration("a", "worker-a", "")
				cm.ResourceVersion = "2"
				changed = cm
			}
			w.Modify(changed)
			g.Eventually(func() string {
				obj, _, _ := d.informer.GetStore().GetByKey(key)
				switch obj := obj.(type) {
				case *core.Namespace:
					return obj.ResourceVersion
				case *core.ConfigMap:
					return obj.ResourceVersion
				}
				return ""
			}, 5*time.Second).Should(gomega.Equal("2"), "WATCH B must be released and its update applied")
		})
	}
}

// The same interleaving at the public provider boundary: the existing clock seam
// pauses the coordinator before acknowledgment, while native Reflector retries
// list/watch. No private cache fields are toggled or replacement cache supplied.
func TestKubernetesListAttemptPublishesUpdates(t *testing.T) {
	for _, resource := range []string{"namespaces", "configmaps"} {
		t.Run(resource, func(t *testing.T) {
			g := gomega.NewWithT(t)
			c := fake.NewClientset(namespace("a"), namespace("b"), registration("a", "worker-a", ""), registration("b", "worker-b", ""))
			pauseCoordinator, releaseCoordinator := make(chan struct{}), make(chan struct{})
			listBEntered, releaseListB, watchBEntered := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var armed atomic.Bool
			var coordinatorOnce, listOnce sync.Once
			resume := func() { coordinatorOnce.Do(func() { close(releaseCoordinator) }) }
			releaseB := func() { listOnce.Do(func() { close(releaseListB) }) }
			lists, watches := 0, 0
			target := func(a kt.Action) bool { return resource == "namespaces" || a.GetNamespace() == "a" }
			c.PrependReactor("list", resource, func(a kt.Action) (bool, runtime.Object, error) {
				if !target(a) {
					return false, nil, nil
				}
				lists++
				switch lists {
				case 1:
					armed.Store(true)
					<-pauseCoordinator
				case 2:
					close(listBEntered)
					<-releaseListB
				}
				return false, nil, nil
			})
			w := watch.NewRaceFreeFake()
			c.PrependWatchReactor(resource, func(a kt.Action) (bool, watch.Interface, error) {
				if !target(a) {
					return false, nil, nil
				}
				watches++
				if watches == 1 {
					return true, nil, errors.New("fixture WATCH A establishment failed")
				}
				if watches == 2 {
					close(watchBEntered)
				}
				return true, w, nil
			})
			k := NewKubernetes(c, "")
			k.now = func() time.Time {
				if armed.CompareAndSwap(true, false) {
					close(pauseCoordinator)
					<-releaseCoordinator
				}
				return time.Now()
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- k.Run(ctx) }()
			t.Cleanup(func() {
				resume()
				releaseB()
				cancel()
				g.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.BeNil()))
				g.Expect(k.Snapshot().Synced).To(gomega.BeFalse())
			})
			g.Eventually(listBEntered, 5*time.Second).Should(gomega.BeClosed())
			resume()
			g.Consistently(func() bool { return k.Snapshot().Synced }, 250*time.Millisecond, 5*time.Millisecond).Should(gomega.BeFalse(), "in-flight B must remain unsynchronized despite matching A in store")
			releaseB()
			g.Eventually(watchBEntered, 5*time.Second).Should(gomega.BeClosed())
			g.Eventually(validSnapshot(k, 2), 5*time.Second).Should(gomega.BeTrue())
			before := k.Snapshot().Plan.Hash
			if resource == "namespaces" {
				n := namespace("b")
				n.Labels = nil
				w.Modify(n)
				g.Eventually(validSnapshot(k, 1), 5*time.Second).Should(gomega.BeTrue())
			} else {
				w.Modify(registration("a", "worker-a", "worker-b"))
				g.Eventually(func() []string {
					s := k.Snapshot()
					if !s.Synced || len(s.Problems) > 0 || len(s.Plan.Nodes) != 2 {
						return nil
					}
					return s.Plan.Nodes[0].Dependencies
				}, 5*time.Second).Should(gomega.Equal([]string{"worker-b"}))
			}
			g.Expect(k.Snapshot().Plan.Hash).NotTo(gomega.Equal(before), "actual WATCH B must change the published graph")
		})
	}
}

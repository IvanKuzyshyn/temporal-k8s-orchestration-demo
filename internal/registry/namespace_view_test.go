package registry

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onsi/gomega"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

// Watch deltas can change a view without any new LIST attempt. Both membership
// and CM data must fence an assembly, and the returned view must stay immutable.
func TestKubernetesCacheViewFencesWatchChanges(t *testing.T) {
	for _, resource := range []string{"namespaces", "configmaps"} {
		t.Run(resource, func(t *testing.T) {
			g := gomega.NewWithT(t)
			c := fake.NewClientset(namespace("a"), registration("a", "worker-a", ""))
			w := watch.NewRaceFreeFake()
			c.PrependWatchReactor(resource, func(kt.Action) (bool, watch.Interface, error) { return true, w, nil })
			k := NewKubernetes(c, "")
			ctx, cancel := context.WithCancel(context.Background())
			ns := ""
			if resource == "configmaps" {
				ns = "a"
			}
			d := k.newCache(ctx, ns)
			t.Cleanup(func() {
				cancel()
				done := make(chan struct{})
				go func() { d.stop(); close(done) }()
				g.Eventually(done, 5*time.Second).Should(gomega.BeClosed())
			})
			var consumed cacheView
			g.Eventually(func() bool { consumed = d.view(time.Now()); return consumed.synced }, 5*time.Second).Should(gomega.BeTrue())
			g.Expect(consumed.objects).To(gomega.HaveLen(1))
			matches := func(v cacheView) bool { d.mu.Lock(); defer d.mu.Unlock(); return d.matchesLocked(v, time.Now()) }
			g.Expect(matches(consumed)).To(gomega.BeTrue())
			if resource == "namespaces" {
				w.Add(namespace("b"))
				g.Eventually(func() int { return len(d.informer.GetStore().List()) }, 5*time.Second).Should(gomega.Equal(2))
			} else {
				cm := registration("a", "worker-a", "worker-b")
				cm.ResourceVersion = "2"
				w.Modify(cm)
				g.Eventually(func() string {
					obj, _, _ := d.informer.GetStore().GetByKey("a/temporal-worker-worker-a")
					return obj.(*core.ConfigMap).ResourceVersion
				}, 5*time.Second).Should(gomega.Equal("2"))
			}
			g.Expect(matches(consumed)).To(gomega.BeFalse(), "a watch delta must invalidate a graph assembly even without a new list attempt")
			current := d.view(time.Now())
			g.Expect(current.synced).To(gomega.BeTrue())
			g.Expect(current.attempt).To(gomega.Equal(consumed.attempt), "fixture must exercise a view change, not LIST retry fencing")
			g.Expect(matches(current)).To(gomega.BeTrue())
			if resource == "namespaces" {
				g.Expect(consumed.objects).NotTo(gomega.HaveKey("b"))
				g.Expect(current.objects).To(gomega.HaveKey("b"))
			} else {
				g.Expect(consumed.objects["a/temporal-worker-worker-a"].(*core.ConfigMap).Data["runAfter"]).To(gomega.BeEmpty())
				g.Expect(current.objects["a/temporal-worker-worker-a"].(*core.ConfigMap).Data["runAfter"]).To(gomega.Equal("worker-b"))
			}
		})
	}
}

// Pause assembly after Namespace validation but before the participating CM view.
// Native RV410 starts a new Namespace list including inaccessible b during that
// assembly. Its entry barrier (rather than a timed DeltaFIFO-drain sleep) proves
// the old view is superseded. Inspect exactly the next publication, not the
// eventual failure a later iteration could use to mask an incomplete admission.
func TestKubernetesNamespaceViewPublicationBarrier(t *testing.T) {
	g := gomega.NewWithT(t)
	c := fake.NewClientset(namespace("a"), registration("a", "worker-a", ""), registration("b", "worker-b", ""))
	watches := make(chan *watch.RaceFreeFakeWatcher, 8)
	c.PrependWatchReactor("namespaces", func(kt.Action) (bool, watch.Interface, error) {
		w := watch.NewRaceFreeFake()
		watches <- w
		return true, w, nil
	})
	var denied atomic.Bool
	denied.Store(true)
	c.PrependReactor("list", "configmaps", func(a kt.Action) (bool, kruntime.Object, error) {
		if a.GetNamespace() == "b" && denied.Load() {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "b", nil)
		}
		return false, nil, nil
	})
	listBEntered, releaseListB := make(chan struct{}), make(chan struct{})
	lists := 0
	c.PrependReactor("list", "namespaces", func(kt.Action) (bool, kruntime.Object, error) {
		lists++
		if lists == 2 {
			close(listBEntered)
			<-releaseListB
		}
		return false, nil, nil
	})
	paused, resume, published, finish := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce, resumeOnce, finishOnce sync.Once
	releaseB := func() { releaseOnce.Do(func() { close(releaseListB) }) }
	resumeAssembly := func() { resumeOnce.Do(func() { close(resume) }) }
	finishProbe := func() { finishOnce.Do(func() { close(finish) }) }
	var namespacePC, configMapPC atomic.Uintptr
	var phase atomic.Int32
	k := NewKubernetes(c, "")
	k.now = func() time.Time {
		pc, _, _, _ := runtime.Caller(1)
		if strings.HasSuffix(runtime.FuncForPC(pc).Name(), ".(*Kubernetes).Run") {
			// Identify the stable call sites during the baseline, not a fragile total
			// clock-call count that changes when another namespace participates.
			namespacePC.CompareAndSwap(0, pc)
			if pc != namespacePC.Load() {
				configMapPC.CompareAndSwap(0, pc)
			}
			if pc == configMapPC.Load() && phase.CompareAndSwap(1, 2) {
				close(paused)
				<-resume
			}
			if pc == namespacePC.Load() && phase.CompareAndSwap(2, 3) {
				close(published)
				<-finish
			}
		}
		return time.Now()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx) }()
	t.Cleanup(func() {
		releaseB()
		resumeAssembly()
		finishProbe()
		cancel()
		g.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.BeNil()))
		g.Expect(k.Snapshot().Synced).To(gomega.BeFalse())
	})
	g.Eventually(validSnapshot(k, 1), 5*time.Second).Should(gomega.BeTrue())
	baseline := k.Snapshot().Plan.Hash
	g.Expect(configMapPC.Load()).NotTo(gomega.BeZero())
	w := <-watches
	phase.Store(1)
	g.Eventually(paused, 5*time.Second).Should(gomega.BeClosed())
	g.Expect(c.Tracker().Add(namespace("b"))).To(gomega.Succeed())
	w.Error(&metav1.Status{Status: "Failure", Code: 410, Reason: metav1.StatusReasonExpired})
	g.Eventually(listBEntered, 5*time.Second).Should(gomega.BeClosed())
	resumeAssembly()
	g.Eventually(published, 5*time.Second).Should(gomega.BeClosed())
	s := k.Snapshot()
	t.Logf("publication while Namespace list {a,b} supersedes consumed {a}: synced=%v problems=%v plan=%+v", s.Synced, s.Problems, s.Plan)
	g.Expect(s.Synced).To(gomega.BeFalse(), "must not publish a valid worker-a-only graph during the newer Namespace attempt")
	g.Expect(s.Plan.Hash).To(gomega.BeEmpty())
	releaseB()
	g.Eventually(watches, 5*time.Second).Should(gomega.Receive())
	finishProbe()
	g.Eventually(func() string { return problems(k) }, 10*time.Second).Should(gomega.ContainSubstring("forbidden"))
	g.Expect(k.Snapshot().Synced).To(gomega.BeFalse())
	g.Expect(k.Snapshot().Plan.Hash).To(gomega.BeEmpty())
	denied.Store(false)
	g.Eventually(validSnapshot(k, 2), 15*time.Second).Should(gomega.BeTrue())
	s = k.Snapshot()
	g.Expect(s.Plan.Nodes[0].Name).To(gomega.Equal("worker-a"))
	g.Expect(s.Plan.Nodes[1].Name).To(gomega.Equal("worker-b"))
	g.Expect(s.Plan.Hash).NotTo(gomega.Equal(baseline))
	t.Logf("permission grant recovered both selected namespaces: hash=%s", s.Plan.Hash)
}

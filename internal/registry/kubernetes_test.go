package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onsi/gomega"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

func namespace(name string) *core.Namespace {
	return &core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"temporal-demo.io/worker-namespace": "true"}}}
}
func registration(ns, name, after string) *core.ConfigMap {
	return &core.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "temporal-worker-" + name, Namespace: ns, Labels: map[string]string{"temporal-demo.io/registration": "true"}}, Data: map[string]string{"schemaVersion": "1", "workerName": name, "taskQueue": name + ".v1.temporal", "workflowType": "ExampleChildWorkflow", "runAfter": after, "sourceServiceName": name, "sourceServiceNamespace": ns, "sourceServiceUID": "uid-" + name}}
}
func startRegistry(t *testing.T, c *fake.Clientset) *Kubernetes {
	t.Helper()
	k := NewKubernetes(c, "temporal-demo.io/worker-namespace=true")
	g := gomega.NewWithT(t)
	g.Expect(k.Snapshot().Synced).To(gomega.BeFalse())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			g.Expect(err).NotTo(gomega.HaveOccurred())
		case <-time.After(5 * time.Second):
			t.Error("registry did not join canceled informers")
		}
		g.Expect(k.Snapshot().Synced).To(gomega.BeFalse())
	})
	return k
}
func validSnapshot(k *Kubernetes, n int) func() bool {
	return func() bool { s := k.Snapshot(); return s.Synced && len(s.Problems) == 0 && len(s.Plan.Nodes) == n }
}
func problems(k *Kubernetes) string { return strings.Join(k.Snapshot().Problems, "; ") }

// Missing updates, stale-valid fallback and shallow copies each break this test.
func TestKubernetesLifecycle(t *testing.T) {
	g := gomega.NewWithT(t)
	ctx := context.Background()
	c := fake.NewClientset(namespace("a"), namespace("b"), registration("a", "worker-a", ""), registration("b", "worker-b", "worker-a"))
	c.PrependReactor("*", "*", func(a kt.Action) (bool, runtime.Object, error) {
		if a.GetResource().Resource != "namespaces" && a.GetResource().Resource != "configmaps" {
			t.Errorf("unexpected access %v", a)
		}
		if a.GetResource().Resource == "configmaps" && a.GetNamespace() == "" {
			t.Error("all-namespace ConfigMap access")
		}
		return false, nil, nil
	})
	k := startRegistry(t, c)
	g.Eventually(validSnapshot(k, 2), 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
	s := k.Snapshot()
	hash := s.Plan.Hash
	s.Plan.Nodes[1].Dependencies[0] = "mutated"
	s.Plan.Nodes[0].Name = "mutated"
	g.Expect(k.Snapshot().Plan.Nodes[1].Dependencies).To(gomega.Equal([]string{"worker-a"}))
	cm := registration("a", "worker-a", "worker-b")
	_, err := c.CoreV1().ConfigMaps("a").Update(ctx, cm, metav1.UpdateOptions{})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Eventually(func() string { return problems(k) }, 5*time.Second, 10*time.Millisecond).Should(gomega.ContainSubstring("cycle"))
	g.Expect(k.Snapshot().Synced).To(gomega.BeTrue())
	g.Expect(k.Snapshot().Plan.Hash).To(gomega.BeEmpty())
	s = k.Snapshot()
	s.Problems[0] = "mutated"
	g.Expect(problems(k)).To(gomega.ContainSubstring("cycle"))
	cm.Data["runAfter"] = ""
	cm.Data["sourceServiceUID"] = "recreated-source"
	_, err = c.CoreV1().ConfigMaps("a").Update(ctx, cm, metav1.UpdateOptions{})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Eventually(validSnapshot(k, 2), 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
	g.Expect(k.Snapshot().Plan.Hash).To(gomega.Equal(hash))
	g.Expect(c.CoreV1().ConfigMaps("b").Delete(ctx, "temporal-worker-worker-b", metav1.DeleteOptions{})).To(gomega.Succeed())
	g.Eventually(validSnapshot(k, 1), 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
	// Dynamic namespace inclusion, label removal, relabel and deletion, no restart.
	_, err = c.CoreV1().Namespaces().Create(ctx, namespace("c"), metav1.CreateOptions{})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = c.CoreV1().ConfigMaps("c").Create(ctx, registration("c", "worker-c", ""), metav1.CreateOptions{})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Eventually(validSnapshot(k, 2), 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
	ns := namespace("c")
	ns.Labels = nil
	_, err = c.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Eventually(validSnapshot(k, 1), 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
	_, err = c.CoreV1().Namespaces().Update(ctx, namespace("c"), metav1.UpdateOptions{})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Eventually(validSnapshot(k, 2), 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
	g.Expect(c.CoreV1().Namespaces().Delete(ctx, "c", metav1.DeleteOptions{})).To(gomega.Succeed())
	g.Eventually(validSnapshot(k, 1), 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
}

func TestKubernetesDecodeRejectsObservedInvalidState(t *testing.T) {
	cases := map[string]func(*core.ConfigMap){
		"unknown schema":     func(c *core.ConfigMap) { c.Data["schemaVersion"] = "2" },
		"missing field":      func(c *core.ConfigMap) { delete(c.Data, "runAfter") },
		"extra field":        func(c *core.ConfigMap) { c.Data["surprise"] = "x" },
		"source namespace":   func(c *core.ConfigMap) { c.Data["sourceServiceNamespace"] = "other" },
		"source UID":         func(c *core.ConfigMap) { c.Data["sourceServiceUID"] = "" },
		"name":               func(c *core.ConfigMap) { c.Name = "unexpected" },
		"queue":              func(c *core.ConfigMap) { c.Data["taskQueue"] = "wrong" },
		"workflow":           func(c *core.ConfigMap) { c.Data["workflowType"] = "wrong" },
		"unknown dependency": func(c *core.ConfigMap) { c.Data["runAfter"] = "absent" },
		"self":               func(c *core.ConfigMap) { c.Data["runAfter"] = "worker-a" },
		"binary data":        func(c *core.ConfigMap) { c.BinaryData = map[string][]byte{"unexpected": {1}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			cm := registration("a", "worker-a", "")
			mutate(cm)
			k := startRegistry(t, fake.NewClientset(namespace("a"), cm))
			g.Eventually(func() bool { s := k.Snapshot(); return s.Synced && len(s.Problems) > 0 && s.Plan.Hash == "" }, 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
		})
	}
	for _, sameNamespace := range []bool{false, true} {
		t.Run(fmt.Sprint("duplicate/", sameNamespace), func(t *testing.T) {
			g := gomega.NewWithT(t)
			other := registration("b", "worker-a", "")
			if sameNamespace {
				other = registration("a", "worker-b", "")
			}
			k := startRegistry(t, fake.NewClientset(namespace("a"), namespace("b"), registration("a", "worker-a", ""), other))
			g.Eventually(func() string { return problems(k) }, 5*time.Second, 10*time.Millisecond).Should(gomega.ContainSubstring(map[bool]string{true: "multiple workers", false: "duplicate worker"}[sameNamespace]))
		})
	}
}

// Exercise actual list and watch denial, all-cache barrier, grant and revoke.
func TestKubernetesPermissionBarrierAndRecovery(t *testing.T) {
	for _, resource := range []string{"namespaces", "configmaps"} {
		for _, verb := range []string{"list", "watch"} {
			t.Run(resource+"/"+verb, func(t *testing.T) {
				g := gomega.NewWithT(t)
				var denied atomic.Bool
				denied.Store(true)
				c := fake.NewClientset(namespace("a"), namespace("b"), registration("a", "worker-a", ""), registration("b", "worker-b", ""))
				deny := func(a kt.Action) bool { return denied.Load() && (resource == "namespaces" || a.GetNamespace() == "b") }
				c.PrependReactor(verb, resource, func(a kt.Action) (bool, runtime.Object, error) {
					if deny(a) {
						return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "fixture", errors.New("missing RoleBinding"))
					}
					return false, nil, nil
				})
				watches := make(chan *watch.RaceFreeFakeWatcher, 20)
				c.PrependWatchReactor(resource, func(a kt.Action) (bool, watch.Interface, error) {
					if verb == "watch" && deny(a) {
						return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "fixture", errors.New("missing RoleBinding"))
					}
					if resource == "namespaces" || a.GetNamespace() == "b" {
						w := watch.NewRaceFreeFake()
						watches <- w
						return true, w, nil
					}
					return false, nil, nil
				})
				k := startRegistry(t, c)
				g.Eventually(func() string { return problems(k) }, 5*time.Second, 10*time.Millisecond).Should(gomega.ContainSubstring("forbidden"))
				g.Expect(k.Snapshot().Synced).To(gomega.BeFalse())
				denied.Store(false)
				g.Eventually(validSnapshot(k, 2), 15*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
				// End the actual watch, forcing reauthorization through the same API.
				denied.Store(true)
				w := <-watches
				if verb == "list" {
					w.Error(&metav1.Status{Status: "Failure", Code: 410, Reason: metav1.StatusReasonExpired})
				} else {
					w.Error(&metav1.Status{Status: "Failure", Code: 403, Reason: metav1.StatusReasonForbidden, Message: "watch forbidden"})
				}
				g.Eventually(func() bool { return k.Snapshot().Synced }, 5*time.Second, 10*time.Millisecond).Should(gomega.BeFalse())
				g.Eventually(func() string { return problems(k) }, 5*time.Second, 10*time.Millisecond).Should(gomega.ContainSubstring("forbidden"))
				denied.Store(false)
				g.Eventually(validSnapshot(k, 2), 15*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
			})
		}
	}
}

func TestKubernetesWatchHealthRelistAndCancellation(t *testing.T) {
	for _, resource := range []string{"namespaces", "configmaps"} {
		t.Run(resource, func(t *testing.T) {
			g := gomega.NewWithT(t)
			var fail atomic.Bool
			var now atomic.Int64
			now.Store(time.Now().UnixNano())
			c := fake.NewClientset(namespace("a"), registration("a", "worker-a", ""))
			watches := make(chan *watch.RaceFreeFakeWatcher, 20)
			c.PrependWatchReactor(resource, func(a kt.Action) (bool, watch.Interface, error) {
				if fail.Load() {
					return true, nil, errors.New("network unavailable")
				}
				w := watch.NewRaceFreeFake()
				watches <- w
				return true, w, nil
			})
			c.PrependReactor("list", resource, func(a kt.Action) (bool, runtime.Object, error) {
				if fail.Load() {
					return true, nil, errors.New("network unavailable")
				}
				return false, nil, nil
			})
			k := NewKubernetes(c, "")
			k.now = func() time.Time { return time.Unix(0, now.Load()) }
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- k.Run(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-done:
					g.Expect(err).NotTo(gomega.HaveOccurred())
				case <-time.After(5 * time.Second):
					t.Error("watch leaked")
				}
			})
			g.Eventually(validSnapshot(k, 1), 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
			w := <-watches
			// Advancing well past 30s on an idle but connected watch must NOT expire it.
			now.Add(int64(time.Hour))
			g.Consistently(validSnapshot(k, 1), 150*time.Millisecond, 10*time.Millisecond).Should(gomega.BeTrue())
			// 410 must relist, replacing deleted state, rather than retaining old registrations.
			if resource == "configmaps" {
				g.Expect(c.Tracker().Delete(core.SchemeGroupVersion.WithResource("configmaps"), "a", "temporal-worker-worker-a")).To(gomega.Succeed())
			} else {
				g.Expect(c.Tracker().Delete(core.SchemeGroupVersion.WithResource("namespaces"), "", "a")).To(gomega.Succeed())
			}
			w.Error(&metav1.Status{Status: "Failure", Code: 410, Reason: metav1.StatusReasonExpired})
			g.Eventually(func() int { return len(watches) }, 10*time.Second, 10*time.Millisecond).Should(gomega.BeNumerically(">", 0))
			w = <-watches
			g.Eventually(func() string { return problems(k) }, 5*time.Second, 10*time.Millisecond).Should(gomega.ContainSubstring("empty worker registry"))
			fail.Store(true)
			w.Stop()
			// Give the forwarding watch a chance to observe EOF before advancing time.
			time.Sleep(100 * time.Millisecond)
			now.Add(int64(31 * time.Second))
			g.Eventually(func() bool { return k.Snapshot().Synced }, 5*time.Second, 10*time.Millisecond).Should(gomega.BeFalse())
			fail.Store(false)
			g.Eventually(func() bool { return k.Snapshot().Synced }, 15*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
		})
	}
}

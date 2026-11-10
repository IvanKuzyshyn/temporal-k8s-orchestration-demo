package registry

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

const registrationSelector = "temporal-demo.io/registration=true"
const defaultNamespaceSelector = "temporal-demo.io/worker-namespace=true"
const connectionGrace = 30 * time.Second

// Kubernetes combines one Namespace informer with namespace-scoped ConfigMap
// informers. Run owns all informer lifetimes; Snapshot is always caller-owned.
// Namespace labels select discovery, never authorize it: each namespace requires
// a separate read-only grant. No all-namespace ConfigMap request is issued.
type Kubernetes struct {
	client   kubernetes.Interface
	selector string
	now      func() time.Time
	mu       sync.RWMutex
	snapshot Snapshot
}

func NewKubernetes(client kubernetes.Interface, namespaceSelector string) *Kubernetes {
	if namespaceSelector == "" {
		namespaceSelector = defaultNamespaceSelector
	}
	return &Kubernetes{client: client, selector: namespaceSelector, now: time.Now, snapshot: Snapshot{Problems: []string{"discovery has not synchronized"}}}
}
func (k *Kubernetes) Snapshot() Snapshot {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return cloneSnapshot(k.snapshot)
}

// Each informer tracks connections, not resource event age. A connected, idle
// watch never expires. Authorization errors invalidate immediately; transient
// disruption has at most 30s grace. Short server-side watches reauthorize at
// least every 10s even when idle (RBAC revocation doesn't close existing streams).
type discoveryCache struct {
	informer    cache.SharedIndexInformer
	cancel      context.CancelFunc
	done        chan struct{}
	mu          sync.Mutex
	connected   bool
	failedAt    time.Time
	denied      bool
	problem     string
	listing     bool
	listed      map[string]runtime.Object
	listApplied chan struct{}
	watchers    sync.WaitGroup
}

// Force the native list/watch path, including its normal RV-expiry relist. This
// also keeps initial cache barriers explicit, independent of WatchList support.
type discoveryListWatch struct{ *cache.ListWatch }

func (discoveryListWatch) IsWatchListSemanticsUnSupported() bool { return true }

func (k *Kubernetes) newCache(ctx context.Context, ns string) *discoveryCache {
	ctx, cancel := context.WithCancel(ctx)
	d := &discoveryCache{cancel: cancel, done: make(chan struct{})}
	failed := func(err error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.connected = false
		if d.failedAt.IsZero() {
			d.failedAt = k.now()
		}
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			d.denied = true
		}
		if !d.denied || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			d.problem = err.Error()
		}
	}
	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			d.mu.Lock()
			// A retry supersedes the entire pending attempt, even if its list
			// reached the store but its watch failed before acknowledgment.
			applied := make(chan struct{})
			d.listing = true
			d.listed = nil
			d.listApplied = applied
			d.mu.Unlock()
			// A full list is small in this demo and gives an explicit relist barrier.
			o.Limit = 0
			request, done := context.WithTimeout(ctx, 10*time.Second)
			defer done()
			var obj runtime.Object
			var err error
			if ns == "" {
				o.LabelSelector = k.selector
				obj, err = k.client.CoreV1().Namespaces().List(request, o)
			} else {
				o.LabelSelector = registrationSelector
				obj, err = k.client.CoreV1().ConfigMaps(ns).List(request, o)
			}
			if err != nil {
				failed(err)
				return nil, err
			}
			items, err := meta.ExtractList(obj)
			if err != nil {
				failed(err)
				return nil, err
			}
			expected := make(map[string]runtime.Object, len(items))
			for _, item := range items {
				key, err := cache.MetaNamespaceKeyFunc(item)
				if err != nil {
					failed(err)
					return nil, err
				}
				expected[key] = item
			}
			d.mu.Lock()
			if d.listApplied != applied {
				d.mu.Unlock()
				return nil, fmt.Errorf("list attempt superseded")
			}
			d.listed = expected
			d.mu.Unlock()
			return obj, nil
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			// Bound black-holed connection establishment and stream lifetime on the
			// client too: server timeoutSeconds alone cannot detect a broken TCP path.
			ctx, cancelWatch := context.WithTimeout(ctx, 15*time.Second)
			seconds := int64(10)
			o.TimeoutSeconds = &seconds
			var w watch.Interface
			var err error
			if ns == "" {
				o.LabelSelector = k.selector
				w, err = k.client.CoreV1().Namespaces().Watch(ctx, o)
			} else {
				o.LabelSelector = registrationSelector
				w, err = k.client.CoreV1().ConfigMaps(ns).Watch(ctx, o)
			}
			if err != nil {
				cancelWatch()
				failed(err)
				return nil, err
			}
			d.mu.Lock()
			d.connected = true
			d.denied = false
			d.failedAt = time.Time{}
			d.problem = ""
			applied := d.listApplied
			d.mu.Unlock()
			ch := make(chan watch.Event)
			proxy := watch.NewProxyWatcher(ch)
			d.watchers.Add(1)
			go func() {
				defer d.watchers.Done()
				defer cancelWatch()
				defer close(ch)
				defer w.Stop()
				defer failed(fmt.Errorf("watch disconnected"))
				if applied != nil {
					select {
					case <-applied:
					case <-ctx.Done():
						return
					case <-proxy.StopChan():
						return
					}
				}
				for {
					select {
					case <-ctx.Done():
						return
					case <-proxy.StopChan():
						return
					case event, ok := <-w.ResultChan():
						if !ok {
							return
						}
						if event.Type == watch.Error {
							failed(apierrors.FromObject(event.Object))
						}
						select {
						case ch <- event:
						case <-ctx.Done():
							return
						case <-proxy.StopChan():
							return
						}
					}
				}
			}()
			return proxy, nil
		},
	}
	var obj runtime.Object = &core.ConfigMap{}
	if ns == "" {
		obj = &core.Namespace{}
	}
	d.informer = cache.NewSharedIndexInformer(discoveryListWatch{lw}, obj, 0, cache.Indexers{})
	// Keep diagnostics in Snapshot; don't flood process logs during an outage.
	_ = d.informer.SetWatchErrorHandlerWithContext(func(_ context.Context, _ *cache.Reflector, err error) { failed(err) })
	go func() { defer close(d.done); d.informer.RunWithContext(ctx) }()
	return d
}

// A view binds consumed objects to the exact list attempt/readiness decision.
// Informer objects are immutable; callers never mutate these internal values.
type cacheView struct {
	objects map[string]runtime.Object
	attempt chan struct{}
	synced  bool
	problem string
}

func (d *discoveryCache) storeView() map[string]runtime.Object {
	objects := make(map[string]runtime.Object)
	for _, obj := range d.informer.GetStore().List() {
		key, _ := cache.MetaNamespaceKeyFunc(obj)
		objects[key] = obj.(runtime.Object)
	}
	return objects
}

func (d *discoveryCache) view(now time.Time) cacheView {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := cacheView{objects: d.storeView(), attempt: d.listApplied}
	if d.listing && d.listed != nil && reflect.DeepEqual(v.objects, d.listed) && d.informer.HasSynced() {
		d.listing = false
		d.listed = nil
		close(d.listApplied)
	}
	v.synced, v.problem = d.readinessLocked(now)
	return v
}

func (d *discoveryCache) ready(now time.Time) (bool, string) {
	v := d.view(now)
	return v.synced, v.problem
}

// Caller holds mu. Validation never acknowledges a newer list on behalf of an
// older consumed view; a changed attempt or store invalidates that assembly.
func (d *discoveryCache) matchesLocked(v cacheView, now time.Time) bool {
	ready, _ := d.readinessLocked(now)
	return ready && d.listApplied == v.attempt && reflect.DeepEqual(v.objects, d.storeView())
}

func (d *discoveryCache) readinessLocked(now time.Time) (bool, string) {
	if d.listing || !d.informer.HasSynced() {
		return false, "cache not synchronized: " + d.problem
	}
	if d.denied {
		return false, d.problem
	}
	if !d.connected && (d.failedAt.IsZero() || now.Sub(d.failedAt) >= connectionGrace) {
		return false, "no successful list/watch connection within 30s: " + d.problem
	}
	return true, ""
}
func (d *discoveryCache) stop() { d.cancel(); <-d.done; d.watchers.Wait() }

func (k *Kubernetes) Run(ctx context.Context) error {
	selector, err := labels.Parse(k.selector)
	if err != nil {
		return fmt.Errorf("namespace selector: %w", err)
	}
	namespaces := k.newCache(ctx, "")
	workers := map[string]*discoveryCache{}
	defer func() {
		namespaces.stop()
		for _, d := range workers {
			d.stop()
		}
		k.mu.Lock()
		k.snapshot = Snapshot{Problems: []string{"discovery stopped"}}
		k.mu.Unlock()
	}()
	// The coordinator is the only owner of namespace generations. Removed watchers
	// are joined before replacement; late events cannot resurrect an old cache.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		namespaceView := namespaces.view(k.now())
		selected := map[string]bool{}
		for _, obj := range namespaceView.objects {
			ns := obj.(*core.Namespace)
			if selector.Matches(labels.Set(ns.Labels)) {
				selected[ns.Name] = true
			}
		}
		for ns, d := range workers {
			if !selected[ns] {
				d.stop()
				delete(workers, ns)
			}
		}
		for ns := range selected {
			if workers[ns] == nil {
				workers[ns] = k.newCache(ctx, ns)
			}
		}
		s := Snapshot{Synced: true}
		if !namespaceView.synced {
			s.Synced = false
			s.Problems = append(s.Problems, "Namespaces: "+namespaceView.problem)
		}
		registrations := []plan.Registration{}
		workerViews := make(map[string]cacheView, len(workers))
		names := make([]string, 0, len(workers))
		for ns := range workers {
			names = append(names, ns)
		}
		sort.Strings(names)
		for _, ns := range names {
			d := workers[ns]
			v := d.view(k.now())
			workerViews[ns] = v
			if !v.synced {
				s.Synced = false
				s.Problems = append(s.Problems, "namespace "+ns+": "+v.problem)
				continue
			}
			for _, obj := range v.objects {
				cm := obj.(*core.ConfigMap)
				if cm.Labels["temporal-demo.io/registration"] != "true" {
					continue
				}
				r, err := decode(cm)
				if err != nil {
					s.Problems = append(s.Problems, err.Error())
				} else {
					registrations = append(registrations, r)
				}
			}
		}
		if s.Synced && len(s.Problems) == 0 {
			s.Plan, err = plan.Build(registrations)
			if err != nil {
				s.Problems = append(s.Problems, err.Error())
			}
		}
		sort.Strings(s.Problems)
		k.publish(s, namespaces, namespaceView, workers, workerViews, names)
	}
}

// Fence list attempts and revalidate the consumed store views through the
// publication. Namespace validation is last, so newly observed membership is
// never acknowledged with a graph assembled from an older selected set.
func (k *Kubernetes) publish(s Snapshot, namespaces *discoveryCache, namespaceView cacheView,
	workers map[string]*discoveryCache, workerViews map[string]cacheView, names []string) {
	now := k.now()
	namespaces.mu.Lock()
	defer namespaces.mu.Unlock()
	for _, ns := range names {
		workers[ns].mu.Lock()
		defer workers[ns].mu.Unlock()
	}
	if s.Synced {
		unchanged := true
		for _, ns := range names {
			if !workers[ns].matchesLocked(workerViews[ns], now) {
				unchanged = false
			}
		}
		if !namespaces.matchesLocked(namespaceView, now) {
			unchanged = false
		}
		if !unchanged {
			s = Snapshot{Problems: []string{"discovery changed while assembling snapshot"}}
		}
	}
	k.mu.Lock()
	k.snapshot = s
	k.mu.Unlock()
}

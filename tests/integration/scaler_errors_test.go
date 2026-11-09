//go:build integration

package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/onsi/gomega"
)

func TestScalerErrorsAreNotInactivity(t *testing.T) {
	h := newHarness(t)
	h.preflight()
	h.waitZero("worker-a", 3*time.Minute)
	t.Cleanup(func() { h.configure(120, false, "5s") })
	// Deliberately unreachable port on the existing in-cluster endpoint. No
	// infrastructure, credentials or production defaults are changed.
	h.k("-n", "temporal-demo-worker-a", "patch", "scaledobject", "worker-a", "--type=json", "-p", `[{"op":"replace","path":"/spec/triggers/0/metadata/endpoint","value":"temporal.temporal.svc.cluster.local:1"}]`)
	var conditions []struct{ Type, Status, Reason, Message string }
	read := func() {
		var r struct {
			Status struct {
				Conditions []struct{ Type, Status, Reason, Message string }
			}
		}
		h.g.Expect(json.Unmarshal(h.k("-n", "temporal-demo-worker-a", "get", "scaledobject", "worker-a", "-o", "json"), &r)).To(gomega.Succeed())
		conditions = r.Status.Conditions
	}
	h.g.Eventually(func() bool {
		read()
		for _, c := range conditions {
			if (c.Type == "Ready" && c.Status == "False") || (c.Type == "Active" && c.Status == "Unknown" && c.Reason != "") {
				return true
			}
		}
		return false
	}, 90*time.Second, time.Second).Should(gomega.BeTrue(), "unreachable Temporal must not look like a healthy empty queue")
	t.Logf("actual unreachable-endpoint conditions: %+v", conditions)
	h.g.Expect(h.zero("worker-a")).To(gomega.BeTrue())
	h.configure(120, false, "5s")
	h.g.Eventually(func() bool {
		read()
		ready, inactive := false, false
		for _, c := range conditions {
			if c.Type == "Ready" {
				ready = c.Status == "True"
			}
			if c.Type == "Active" {
				inactive = c.Status == "False" && c.Reason == "ScalerNotActive"
			}
		}
		return ready && inactive
	}, 90*time.Second, time.Second).Should(gomega.BeTrue())
	h.healthy("worker-a")
}

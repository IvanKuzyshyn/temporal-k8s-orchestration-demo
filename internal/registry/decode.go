package registry

import (
	"fmt"
	"strings"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/plan"
	core "k8s.io/api/core/v1"
)

// Registrations are trusted Kyverno output, not cryptographic Service provenance.
// No Service read is required or allowed here. Build is the sole graph authority.
func decode(cm *core.ConfigMap) (plan.Registration, error) {
	fields := []string{"schemaVersion", "workerName", "taskQueue", "workflowType", "runAfter", "sourceServiceName", "sourceServiceNamespace", "sourceServiceUID"}
	bad := func(reason string) (plan.Registration, error) {
		return plan.Registration{}, fmt.Errorf("ConfigMap %s/%s: %s", cm.Namespace, cm.Name, reason)
	}
	if len(cm.Data) != len(fields) || len(cm.BinaryData) != 0 {
		return bad("expected exactly eight string data fields")
	}
	for _, key := range fields {
		if _, ok := cm.Data[key]; !ok {
			return bad("missing " + key)
		}
	}
	r := plan.Registration{SchemaVersion: cm.Data["schemaVersion"], WorkerName: cm.Data["workerName"], TaskQueue: cm.Data["taskQueue"], WorkflowType: cm.Data["workflowType"], RunAfter: cm.Data["runAfter"], SourceServiceName: cm.Data["sourceServiceName"], SourceServiceNamespace: cm.Data["sourceServiceNamespace"], SourceServiceUID: cm.Data["sourceServiceUID"]}
	if cm.Namespace == "" || r.SourceServiceNamespace != cm.Namespace {
		return bad("source namespace must equal actual namespace")
	}
	if cm.Name != "temporal-worker-"+r.SourceServiceName {
		return bad("name must be temporal-worker-<source-service-name>")
	}
	if strings.TrimSpace(r.SourceServiceUID) == "" {
		return bad("source Service UID is required")
	}
	return r, nil
}

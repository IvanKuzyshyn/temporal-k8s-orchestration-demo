package contracts_test

import (
	"encoding/json"
	"testing"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/contracts"
	. "github.com/onsi/gomega"
)

// Adding business fields to these envelopes would leak them into Temporal history.
func TestSerializedWorkflowEnvelopesContainOnlyExecutionMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"parent input", contracts.ParentInput{}, `{"plan":{"schemaVersion":"","nodes":null,"hash":""}}`},
		{"child input", contracts.ChildInput{WorkerName: "worker-a"}, `{"workerName":"worker-a"}`},
		{"child result", contracts.ChildResult{WorkerName: "worker-a"}, `{"workerName":"worker-a"}`},
		{"parent result", contracts.ParentResult{Results: []contracts.ChildResult{{WorkerName: "worker-a"}}}, `{"results":[{"workerName":"worker-a"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			encoded, err := json.Marshal(tc.value)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(encoded)).To(MatchJSON(tc.want))
		})
	}
}

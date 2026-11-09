//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/onsi/gomega"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/proto"
)

// One pre-existing completed execution is a retained-history witness. It is
// hashed before fixture mutations and compared after restore, never deleted.
func (h *harness) retainedHistory() {
	file := os.Getenv("SCALING_HISTORY_FILE")
	if file == "" {
		return
	}
	var witness struct{ ID, RunID, Hash string }
	b, err := os.ReadFile(file)
	fresh := os.IsNotExist(err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if fresh {
		r, err := h.c.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{Namespace: "default", PageSize: 1, Query: "ExecutionStatus = 'Completed'"})
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		h.g.Expect(r.Executions).NotTo(gomega.BeEmpty(), "retained static baseline must have a completed history witness")
		witness.ID = r.Executions[0].Execution.WorkflowId
		witness.RunID = r.Executions[0].Execution.RunId
	} else {
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		h.g.Expect(json.Unmarshal(b, &witness)).To(gomega.Succeed())
	}
	history := &historypb.History{}
	iter := h.c.GetWorkflowHistory(ctx, witness.ID, witness.RunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iter.HasNext() {
		event, err := iter.Next()
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		history.Events = append(history.Events, event)
	}
	b, err = proto.MarshalOptions{Deterministic: true}.Marshal(history)
	h.g.Expect(err).NotTo(gomega.HaveOccurred())
	hash := fmt.Sprintf("%x", sha256.Sum256(b))
	if fresh {
		witness.Hash = hash
		b, err = json.Marshal(witness)
		h.g.Expect(err).NotTo(gomega.HaveOccurred())
		h.g.Expect(os.WriteFile(file, b, 0600)).To(gomega.Succeed())
	} else {
		h.g.Expect(hash).To(gomega.Equal(witness.Hash), "pre-existing completed history changed")
	}
	h.t.Logf("retained completed history %s/%s sha256=%s", witness.ID, witness.RunID, hash)
}

package registry

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/onsi/gomega"
)

// A generic Task7 run must never mutate policy/discovery fixtures. Dedicated
// public selectors keep their cases separate without dropping earlier cases.
func TestIntegrationSelectors(t *testing.T) {
	for _, tags := range []string{"integration", "integration,policies_integration", "integration,registry_integration", "integration,acceptance_integration", "integration,resilience_integration"} {
		t.Run(tags, func(t *testing.T) {
			g := gomega.NewWithT(t)
			cmd := exec.Command("go", "test", "-count=1", "-tags="+tags, "-list", "^Test", "../../tests/integration")
			out, err := cmd.CombinedOutput()
			g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
			list := "\n" + string(out)
			for _, name := range []string{"TestScalingPreflight", "TestInitialScalingConvergence", "TestInitialScalingConvergenceTimeout", "TestScalerErrorsAreNotInactivity", "TestActivationFromZero", "TestReplicaHandoffReapply", "TestInFlightScaleDown", "TestDelayedRetryReactivation", "TestRunningCountOptIn", "TestScalingCleanup"} {
				g.Expect(list).To(gomega.ContainSubstring("\n" + name + "\n"))
			}
			g.Expect(strings.Contains(list, "\nTestDiscoveryPolicy\n")).To(gomega.Equal(strings.Contains(tags, "policies_integration")))
			combined := strings.Contains(tags, "acceptance_integration") || strings.Contains(tags, "resilience_integration")
			for _, name := range []string{"TestDiamondFromZero", "TestPlanSnapshotIsolation", "TestHistoryAssertionsSensitivity", "TestAcceptanceDiagnostics"} {
				g.Expect(strings.Contains(list, "\n"+name+"\n")).To(gomega.Equal(combined))
			}
			for _, name := range []string{"TestRecoveryWorkerTermination", "TestRecoveryOrchestratorRestart", "TestRecoveryDelayedRetry", "TestRecoveryTemporalConnectivity", "TestTemporalRelayFixture"} {
				g.Expect(strings.Contains(list, "\n"+name+"\n")).To(gomega.Equal(strings.Contains(tags, "resilience_integration")))
			}
			for _, name := range []string{"TestLiveRegistry", "TestRegistryRBAC"} {
				g.Expect(strings.Contains(list, "\n"+name+"\n")).To(gomega.Equal(strings.Contains(tags, "registry_integration")))
			}
		})
	}
}

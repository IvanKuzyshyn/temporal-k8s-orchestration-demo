package chart

import (
	"encoding/json"
	"github.com/onsi/gomega"
	"os/exec"
	"strings"
	"testing"
)

// Kyverno intentionally leaves its resource mutating configuration empty until
// policies exist. That must not hide a missing required policy/admission hook.
func TestWebhookReadiness(t *testing.T) {
	for _, tc := range []struct {
		name             string
		missing, emptyCA bool
		wantError        bool
	}{
		{name: "policy-free baseline"}, {name: "required hook absent", missing: true, wantError: true}, {name: "CA not injected", emptyCA: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			items := []any{}
			for _, name := range []string{"keda-admission", "kyverno-policy-validating-webhook-cfg", "kyverno-policy-mutating-webhook-cfg", "kyverno-verify-mutating-webhook-cfg", "kyverno-cleanup-validating-webhook-cfg"} {
				if tc.missing && name == "keda-admission" {
					continue
				}
				ca := "cert"
				if tc.emptyCA && name == "keda-admission" {
					ca = ""
				}
				items = append(items, map[string]any{"metadata": map[string]any{"name": name}, "webhooks": []any{map[string]any{"clientConfig": map[string]any{"caBundle": ca}}}})
			}
			items = append(items, map[string]any{"metadata": map[string]any{"name": "kyverno-resource-mutating-webhook-cfg"}, "webhooks": nil})
			b, err := json.Marshal(map[string]any{"items": items})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			cmd := exec.Command("jq", "-e", "-f", "../../scripts/webhooks-ready.jq")
			cmd.Stdin = strings.NewReader(string(b))
			out, err := cmd.CombinedOutput()
			if tc.wantError {
				g.Expect(err).To(gomega.HaveOccurred())
			} else {
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
			}
		})
	}
}

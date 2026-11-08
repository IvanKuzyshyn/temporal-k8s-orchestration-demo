package chart

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/onsi/gomega"
)

// Run the real diagnostic entrypoint. Newly acquired SQL evidence must preserve
// the error/exit reason while credentials from either output stream never escape;
// failed credential lookup or failed redaction must not release buffered logs.
func TestSchemaDiagnosticsRedactOrFailClosed(t *testing.T) {
	for _, scenario := range []string{"redacted", "lookup-failed", "invalid-secret", "redaction-failed", "healthy"} {
		t.Run(scenario, func(t *testing.T) {
			g := gomega.NewWithT(t)
			root := t.TempDir()
			for _, dir := range []string{"scripts", "tools", ".local", "tmp"} {
				g.Expect(os.Mkdir(filepath.Join(root, dir), 0700)).To(gomega.Succeed())
			}
			for _, file := range []string{"kind-common.sh", "kind-diagnostics.sh"} {
				b, err := os.ReadFile("../../scripts/" + file)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(os.WriteFile(filepath.Join(root, "scripts", file), b, 0600)).To(gomega.Succeed())
			}
			g.Expect(os.WriteFile(filepath.Join(root, ".local/kubeconfig"), []byte("fixture"), 0600)).To(gomega.Succeed())
			const password = "fixture-password-87900ad31efdaeff"
			encoded := base64.StdEncoding.EncodeToString([]byte(password))
			body := `#!/usr/bin/env bash
set -euo pipefail
while [[ $1 == --* ]]; do case "$1" in --*=*) shift;; *) shift 2;; esac; done
if [[ $1 == config ]]; then echo kind-temporal-demo; exit; fi
case "$*" in
  *'get job temporal-schema'*)
    if [[ $SCENARIO == healthy ]]; then echo '{"kind":"Job","metadata":{"name":"temporal-schema"},"status":{"succeeded":1}}'; else echo '{"kind":"Job","metadata":{"name":"temporal-schema"},"status":{"failed":1,"conditions":[{"type":"Failed","status":"True","reason":"BackoffLimitExceeded","message":"PRIVATE NOT ALLOWLISTED"}]}}'; fi;;
  *'get pods'*'job-name=temporal-schema'*)
    echo '{"kind":"PodList","items":[{"metadata":{"name":"schema-failed"},"status":{"phase":"Failed","containerStatuses":[{"name":"schema","state":{"terminated":{"reason":"Error","exitCode":1,"message":"PRIVATE NOT ALLOWLISTED"}}}]}}]}' ;;
  *'get secret postgres-credentials'*)
    echo credential-lookup >> "$STATE/calls"
    if [[ $SCENARIO == lookup-failed ]]; then echo "$PASSWORD" >&2; exit 9; fi
    if [[ $SCENARIO == invalid-secret ]]; then echo '{"data":{"POSTGRES_PASSWORD":""}}'; else printf '{"data":{"POSTGRES_PASSWORD":"%s"}}\n' "$ENCODED"; fi;;
  *'logs job/temporal-schema'*)
    echo schema-logs >> "$STATE/calls"
    printf 'SQL connection refused raw=%s encoded=%s\n' "$PASSWORD" "$ENCODED"
    printf 'SQL stderr raw=%s encoded=%s\n' "$PASSWORD" "$ENCODED" >&2;;
esac
`
			g.Expect(os.WriteFile(filepath.Join(root, "tools/kubectl"), []byte(body), 0755)).To(gomega.Succeed())
			jq, err := exec.LookPath("jq")
			g.Expect(err).NotTo(gomega.HaveOccurred())
			if scenario == "redaction-failed" {
				wrapper := "#!/bin/sh\ncase \"$*\" in *--slurpfile*) printf '%s' \"$PASSWORD\"; printf '%s' \"$ENCODED\" >&2; exit 8;; esac\nexec \"$REAL_JQ\" \"$@\"\n"
				g.Expect(os.WriteFile(filepath.Join(root, "tools/jq"), []byte(wrapper), 0755)).To(gomega.Succeed())
			}
			cmd := exec.Command("bash", filepath.Join(root, "scripts/kind-diagnostics.sh"))
			cmd.Env = append(os.Environ(), "KUBECONFIG=", "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+filepath.Join(root, "tmp"), "STATE="+root, "SCENARIO="+scenario, "PASSWORD="+password, "ENCODED="+encoded, "REAL_JQ="+jq)
			out, err := cmd.CombinedOutput()
			text := string(out)
			g.Expect(text).NotTo(gomega.ContainSubstring(password))
			g.Expect(text).NotTo(gomega.ContainSubstring(encoded))
			g.Expect(text).NotTo(gomega.ContainSubstring("PRIVATE NOT ALLOWLISTED"))
			calls, _ := os.ReadFile(filepath.Join(root, "calls"))
			switch scenario {
			case "redacted":
				g.Expect(err).NotTo(gomega.HaveOccurred(), text)
				for _, evidence := range []string{"BackoffLimitExceeded", "Error", `"exitCode": 1`, "SQL connection refused", "SQL stderr", "[REDACTED]"} {
					g.Expect(text).To(gomega.ContainSubstring(evidence))
				}
			case "healthy":
				g.Expect(err).NotTo(gomega.HaveOccurred(), text)
				g.Expect(string(calls)).To(gomega.BeEmpty())
			default:
				g.Expect(err).To(gomega.HaveOccurred(), text)
				g.Expect(text).NotTo(gomega.ContainSubstring("SQL connection refused"))
				if scenario == "lookup-failed" || scenario == "invalid-secret" {
					g.Expect(string(calls)).NotTo(gomega.ContainSubstring("schema-logs"))
				}
			}
			remaining, err := os.ReadDir(filepath.Join(root, "tmp"))
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(remaining).To(gomega.BeEmpty(), "protected credential/log buffers must be removed")
		})
	}
}

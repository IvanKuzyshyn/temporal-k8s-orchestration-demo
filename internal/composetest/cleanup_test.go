package composetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/gomega"
)

func TestCleanupChecksDiscoveryAndAggregatesFailures(t *testing.T) {
	const job = "temporal-demo-ci-123-4"
	const first = job + "-persistence-100-200-aabbcc"
	const second = job + "-persistence-101-201-ddeeff"
	for _, tc := range []struct {
		name, list, failReset string
		discoveryFail         bool
		wantError             bool
		want                  []string
	}{
		{"discovery failure", "", "", true, true, []string{job}},
		{"malformed discovery JSON", "invalid", "", false, true, []string{job}},
		{"bad discovery shape", `{"Name":"not-an-array"}`, "", false, true, []string{job}},
		{"first fixture failure", `[{"Name":"` + first + `"},{"Name":"` + second + `"}]`, first, false, true, []string{first, second, job}},
		{"normal reset failure", `[{"Name":"` + first + `"}]`, job, false, true, []string{first, job}},
		{"exact job prefix only", `[{"Name":"` + first + `"},{"Name":"temporal-demo-ci-123-40-persistence-other"},{"Name":"temporal-demo-ci-123-4-other"},{"Name":"temporal-demo"},{"Name":"n8n-playground"}]`, "", false, false, []string{first, job}},
		{"no fixtures", "[]", "", false, false, []string{job}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			dir := t.TempDir()
			log := filepath.Join(dir, "resets")
			g.Expect(os.WriteFile(filepath.Join(dir, "docker"), []byte(`#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == 'compose ls --all --format json' ]]; then
 if [[ $STUB_DISCOVERY_FAIL == true ]]; then exit 17; fi
 printf '%s\n' "$STUB_LIST"
 exit 0
fi
project=''
while (($#)); do
 case "$1" in
  -p) project=$2; shift ;;
  down)
   printf '%s\n' "$project" >> "$STUB_RESET_LOG"
   if [[ $project == "$STUB_RESET_FAIL" ]]; then exit 23; fi
   exit 0 ;;
 esac
 shift
done
# Read-only diagnostics do not affect reset assertions.
exit 0
`), 0755)).To(gomega.Succeed())
			discoveryFail := "false"
			if tc.discoveryFail {
				discoveryFail = "true"
			}
			cmd := exec.Command("bash", "../../scripts/compose-cleanup.sh")
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "COMPOSE_PROJECT_NAME="+job, "COMPOSE_ENV_FILE="+filepath.Join(dir, "absent.env"), "STUB_LIST="+tc.list, "STUB_DISCOVERY_FAIL="+discoveryFail, "STUB_RESET_LOG="+log, "STUB_RESET_FAIL="+tc.failReset)
			output, err := cmd.CombinedOutput()
			if tc.wantError {
				g.Expect(err).To(gomega.HaveOccurred(), string(output))
			} else {
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(output))
			}
			data, readErr := os.ReadFile(log)
			g.Expect(readErr).NotTo(gomega.HaveOccurred(), string(output))
			g.Expect(strings.Fields(string(data))).To(gomega.Equal(tc.want), string(output))
		})
	}
}

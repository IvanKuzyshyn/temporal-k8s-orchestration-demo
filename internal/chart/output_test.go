package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalOutput(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "up.log")
			script := `set -euo pipefail
source ../../scripts/local-output.sh
trap 'rc=$?; demo_output_finish "$rc"; exit "$rc"' EXIT
demo_output_start "$LOG"
demo_phase 'Starting services'
echo routine-output
echo diagnostic-detail >&2
`
			if fail {
				script += "failing_phase() { echo full-error >&2; false; echo must-not-run; }; failing_phase\n"
			}
			script += "demo_phase_done\ndemo_command 'http://127.0.0.1:18080'\n"
			cmd := exec.Command("bash", "-c", script)
			for _, value := range os.Environ() {
				if !strings.HasPrefix(value, "NO_COLOR=") && !strings.HasPrefix(value, "TERM=") {
					cmd.Env = append(cmd.Env, value)
				}
			}
			cmd.Env = append(cmd.Env, "LOG="+log, "TERM=xterm")
			b, err := cmd.CombinedOutput()
			out := string(b)
			if (err != nil) != fail {
				t.Fatalf("error=%v output=%s", err, out)
			}
			for _, s := range []string{"Starting services"} {
				if !strings.Contains(out, s) {
					t.Fatalf("missing %q: %s", s, out)
				}
			}
			if strings.Contains(out, "Starting services ✅") == fail {
				t.Fatalf("incorrect completion mark: %s", out)
			}
			if !fail && !strings.Contains(out, "http://127.0.0.1:18080") {
				t.Fatal(out)
			}
			if strings.Contains(out, "\x1b[") || strings.Contains(out, "must-not-run") {
				t.Fatal(out)
			}
			if strings.Contains(out, "routine-output") != fail {
				t.Fatalf("wrong log visibility: %s", out)
			}
			if fail && (!strings.Contains(out, "full-error") || !strings.Contains(out, log)) {
				t.Fatal(out)
			}
			saved, e := os.ReadFile(log)
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(string(saved), "routine-output") || !strings.Contains(string(saved), "diagnostic-detail") {
				t.Fatalf("missing logs: %s", saved)
			}
		})
	}
}

package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestLocalOutputTerminalPlainText(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("PTY test requires script")
	}
	for _, tc := range []struct {
		name, term string
		noColor    bool
	}{
		{"terminal", "xterm", false},
		{"no-color", "xterm", true},
		{"dumb", "dumb", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "fixture.sh")
			body := "set -eu\nsource ../../scripts/local-output.sh\ndemo_output_start " + strconv.Quote(filepath.Join(dir, "up.log")) + "\ndemo_phase 'Starting services'\ndemo_phase_done\ndemo_command 'http://127.0.0.1:18080'\n"
			if err := os.WriteFile(file, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			var cmd *exec.Cmd
			switch runtime.GOOS {
			case "darwin":
				cmd = exec.Command("script", "-q", "/dev/null", "bash", file)
			case "linux":
				cmd = exec.Command("script", "-q", "-e", "-c", "bash "+strconv.Quote(file), "/dev/null")
			default:
				t.Skip("PTY command flags only defined for macOS/Linux")
			}
			for _, v := range os.Environ() {
				if !strings.HasPrefix(v, "NO_COLOR=") && !strings.HasPrefix(v, "TERM=") {
					cmd.Env = append(cmd.Env, v)
				}
			}
			cmd.Env = append(cmd.Env, "TERM="+tc.term)
			if tc.noColor {
				cmd.Env = append(cmd.Env, "NO_COLOR=")
			}
			b, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, b)
			}
			out := string(b)
			if !strings.Contains(out, "http://127.0.0.1:18080") || strings.Contains(out, "\x1b[") {
				t.Fatalf("unexpected styling: %q", out)
			}
			if !strings.Contains(out, "Starting services ✅") {
				t.Fatal("missing completion mark")
			}
		})
	}
}

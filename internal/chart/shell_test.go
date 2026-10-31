package chart

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/gomega"
)

// The actual cumulative task must reject a malformed later script, not just
// parse the first glob match. Unrelated Go/manifest checks are no-op executables
// in this fixture; Bash and its syntax-check behavior remain real.
func TestCumulativeShellSyntaxChecksEveryScript(t *testing.T) {
	g := gomega.NewWithT(t)
	info := exec.Command("mise", "tasks", "info", "validate", "--json")
	info.Dir = "../.."
	b, err := info.CombinedOutput()
	g.Expect(err).NotTo(gomega.HaveOccurred(), string(b))
	var task struct {
		Run []string `json:"run"`
	}
	g.Expect(json.Unmarshal(b, &task)).To(gomega.Succeed())
	g.Expect(task.Run).NotTo(gomega.BeEmpty())
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "all valid", true: "last script malformed"}[malformed], func(t *testing.T) {
			g := gomega.NewWithT(t)
			root := t.TempDir()
			for _, dir := range []string{"scripts", "tools"} {
				g.Expect(os.Mkdir(filepath.Join(root, dir), 0700)).To(gomega.Succeed())
			}
			for _, tool := range []string{"go", "gofmt", "git", "mise"} {
				g.Expect(os.WriteFile(filepath.Join(root, "tools", tool), []byte("#!/bin/sh\nexit 0\n"), 0755)).To(gomega.Succeed())
			}
			for _, script := range []string{"a-first.sh", "compose.sh", "z-last.sh"} {
				content := "#!/bin/bash\n:\n"
				if malformed && script == "z-last.sh" {
					content = "if\n"
				}
				g.Expect(os.WriteFile(filepath.Join(root, "scripts", script), []byte(content), 0600)).To(gomega.Succeed())
			}
			cmd := exec.Command("bash", "-c", strings.Join(task.Run, "\n"))
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if malformed {
				g.Expect(err).To(gomega.HaveOccurred(), string(out))
				g.Expect(string(out)).To(gomega.ContainSubstring("scripts/z-last.sh"))
			} else {
				g.Expect(err).NotTo(gomega.HaveOccurred(), string(out))
			}
		})
	}
}

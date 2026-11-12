package chart

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDemoLifecycleHasDedicatedTag(t *testing.T) {
	for _, tag := range []string{"integration", "demo_integration"} {
		cmd := exec.Command("go", "test", "-tags="+tag, "-list", "^TestDemoLifecycle$", "../../tests/integration")
		b, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%v: %s", e, b)
		}
		if strings.Contains(string(b), "TestDemoLifecycle\n") != (tag == "demo_integration") {
			t.Fatalf("wrong lifecycle selection for %s: %s", tag, b)
		}
	}
}

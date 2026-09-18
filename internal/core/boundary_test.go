package core

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCoreImportsOnlyStdlibAndItself(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./internal/core/...")
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}

	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "r-loop/internal/") && dep != "r-loop/internal/core" {
			t.Errorf("internal/core depends on %s", dep)
		}
	}
}

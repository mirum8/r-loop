package analyze

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func goDirective(t *testing.T, path string) []int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			var parts []int
			for _, p := range strings.Split(strings.TrimSpace(v), ".") {
				n, err := strconv.Atoi(p)
				if err != nil {
					t.Fatalf("%s: go directive %q", path, v)
				}
				parts = append(parts, n)
			}
			return append(parts, 0, 0)[:3]
		}
	}
	t.Fatalf("%s: no go directive", path)
	return nil
}

func newer(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func TestPinnedGoToolsBuildWithTheProjectsGoVersion(t *testing.T) {
	project := goDirective(t, "../../go.mod")
	for _, mod := range []string{"github.com/golangci/golangci-lint/v2@" + golangciLintVersion, "golang.org/x/vuln@" + govulncheckVersion} {
		t.Run(mod, func(t *testing.T) {
			// given
			cmd := exec.Command("go", "mod", "download", "-json", mod)
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=")
			out, _ := cmd.Output()
			var info struct{ GoMod, Error string }
			if err := json.Unmarshal(out, &info); err != nil || info.GoMod == "" {
				t.Skipf("module %s unavailable: %v %s", mod, err, info.Error)
			}

			// when
			actual := goDirective(t, info.GoMod)

			// then
			if newer(actual, project) {
				t.Fatalf("%s requires go %v, newer than the project's go %v", mod, actual, project)
			}
		})
	}
}

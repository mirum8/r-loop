package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestGateCommandKeepsALineOfOnlyCommands(t *testing.T) {
	cases := []struct{ input, want string }{
		{"`go test ./...` is green.", "go test ./..."},
		{"`test -f feature.txt` is green and\n`test -f fix1.txt` finds the fix.", "test -f feature.txt && test -f fix1.txt"},
		{"`go build ./cmd/r-loop && go test ./internal/app/...` is green and `go run ./cmd/r-loop x.md --dry-run --plain` prints the banner and run list.", "go build ./cmd/r-loop && go test ./internal/app/... && go run ./cmd/r-loop x.md --dry-run --plain"},
		{"`go vet ./...` is green, `make report` prints the summary and `test -f out.txt` exits 0.", "go vet ./... && make report && test -f out.txt"},
		{"`grep -n tool a.go b.md` prints the tool and the prompt line.", "grep -n tool a.go b.md"},
		{"  make check  ", "make check"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := gateCommand(tc.input); got != tc.want {
				t.Errorf("gateCommand(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestGateCommandWrapsAPrintCheck(t *testing.T) {
	cases := []struct{ input, want string }{
		{"`a` is green and `b x` prints `lit's`.", `a && { out=$( ( b x ) 2>&1; echo ".$?"); st=${out##*.}; out=${out%.*}; printf '%s' "$out"; test "$st" = 0 && printf '%s\n' "$out" | grep -qF -e 'lit'\''s'; }`},
		{"`a` is green and `b` prints nothing.", `a && { out=$( ( b ) 2>&1; echo ".$?"); st=${out##*.}; out=${out%.*}; printf '%s' "$out"; test -z "$out"; }`},
		{"`a` lists the `k` line.", `{ out=$( ( a ) 2>&1; echo ".$?"); st=${out##*.}; out=${out%.*}; printf '%s' "$out"; test "$st" = 0 && printf '%s\n' "$out" | grep -qF -e 'k'; }`},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := gateCommand(tc.input); got != tc.want {
				t.Errorf("gateCommand(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestGateCommandRunsACommandAfterAnOutputLiteral(t *testing.T) {
	for _, separator := range []string{" and ", ", "} {
		t.Run(strings.TrimSpace(separator), func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "ran")
			input := "`printf 'ok\\n'` prints `ok`" + separator + "`touch " + marker + "` is green."
			command := gateCommand(input)
			cmd := exec.Command("sh", "-c", command)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("gate %q failed: %v\n%s", command, err, out)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("following command did not run: %v", err)
			}
		})
	}
}

func TestGateCommandRunsOutputChecksWithShellCommentsAndHeredocs(t *testing.T) {
	for _, input := range []string{
		"`printf ok # trailing comment` prints `ok`.",
		"`cat <<EOF\nok\nEOF` prints `ok`.",
	} {
		t.Run(input, func(t *testing.T) {
			command := gateCommand(input)
			out, err := exec.Command("sh", "-c", command).CombinedOutput()
			if err != nil {
				t.Fatalf("gate %q failed: %v\n%s", command, err, out)
			}
		})
	}
}

func TestGateCommandFromTheRealTodoLinesPassesOnACorrectTree(t *testing.T) {
	data, err := os.ReadFile("../../docs/task-loop-driver/todo.md")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	goScript := "#!/bin/sh\n[ \"$1\" = test ] && exit 0\nexec '" + realGo + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "go"), []byte(goScript), 0755); err != nil {
		t.Fatal(err)
	}
	goEnv, err := exec.Command(realGo, "env", "GOCACHE", "GOMODCACHE", "GOPATH", "GOENV").Output()
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOENV"}
	values := strings.Split(strings.TrimSuffix(string(goEnv), "\n"), "\n")
	if len(values) != len(keys) {
		t.Fatalf("go env returned %q", goEnv)
	}
	env := append(os.Environ(), "PATH="+shim+":"+os.Getenv("PATH"), "HOME="+t.TempDir())
	for i, key := range keys {
		env = append(env, key+"="+values[i])
	}
	for _, n := range []int{535, 549, 565, 578, 607} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			line := lines[n-1]
			if !strings.HasPrefix(line, "**Done when:** ") {
				t.Fatalf("line %d is not a Done when line: %q", n, line)
			}
			command := gateCommand(strings.TrimPrefix(line, "**Done when:** "))
			cmd := exec.Command("sh", "-c", command)
			cmd.Dir = "../.."
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("gate %q failed: %v\n%s", command, err, out)
			}
		})
	}
}

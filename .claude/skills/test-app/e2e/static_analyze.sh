#!/bin/sh
# usage: static_analyze.sh
#   No-agent checks of internal/analyze through a throwaway probe (cmd/zz-static-analyze-probe-<pid>, built and
#   removed again at once): build output left by Analyze in repos that do not ignore it (Maven, Gradle), the
#   golangci-lint per-linter caps, GOTOOLCHAIN=local with the pinned tools, the Go changed-lines filter, untracked
#   files, a custom .semgrep.yml, the timeout, and the dry-run static banner. Each part skips when its tools are
#   missing.
set -u
ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
command -v go > /dev/null 2>&1 || { echo "SKIP: go not on PATH"; exit 0; }
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
HAVE_MVN=$(command -v mvn 2>/dev/null || true)
HAVE_GRADLE=$(command -v gradle 2>/dev/null || true)
command -v java > /dev/null 2>&1 || { HAVE_MVN=; HAVE_GRADLE=; }
HAVE_SEMGREP=$(command -v semgrep 2>/dev/null || true)

W=$(mktemp -d "${TMPDIR:-/tmp}/static-analyze-XXXXXX")
PROBE_SRC="$ROOT/cmd/zz-static-analyze-probe-$$"
cleanup() { rm -rf "$PROBE_SRC"; }
trap cleanup EXIT INT TERM
mkdir -p "$PROBE_SRC"
cat > "$PROBE_SRC/main.go" <<'EOF'
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"r-loop/internal/analyze"
)

func main() {
	timeout := 10 * time.Minute
	if len(os.Args) > 2 {
		d, err := time.ParseDuration(os.Args[2])
		if err != nil {
			panic(err)
		}
		timeout = d
	}
	start := time.Now()
	res, err := analyze.New(timeout).Analyze(context.Background(), os.Args[1])
	fmt.Fprintf(os.Stderr, "elapsed %s\n", time.Since(start).Round(time.Millisecond))
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
}
EOF
(cd "$ROOT" && go build -o "$W/probe" "./cmd/zz-static-analyze-probe-$$") || { echo "FAIL probe build"; exit 1; }
rm -rf "$PROBE_SRC"
echo "work $W"

fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
probe() { "$W/probe" "$@" > "$W/out" 2> "$W/err"; rc=$?; }
has() { grep -q -- "$1" "$W/out"; }
titles() { grep -o '"title": "[a-z-]*/[A-Za-z0-9_.-]*' "$W/out" | cut -d'"' -f4 | sort -u | tr '\n' ' '; }
cmdline() { grep -o '"Command": "[^"]*"' "$W/out" | cut -d'"' -f4; }
st() { git -C "$1" status --porcelain --untracked-files=all; }
filepaths() { sed -n '/"files": \[/,/\]/p' "$W/out" | grep -o '"[^"]*"' | grep -v '^"files"$' | tr -d '"'; }
same_status() {
  after=$(st "$1")
  [ "$2" = "$after" ] && ok "$3: status unchanged ($(echo "$2" | grep -c .) entries)" || {
    fail "$3: status changed"; echo "--- before"; echo "$2"; echo "--- after"; echo "$after"; }
}

newrepo() { d=$(mktemp -d "$W/repo-XXXXXX"); git -C "$d" init -q -b main; echo "$d"; }
commit() { git -C "$1" add -A && git -C "$1" -c user.name=t -c user.email=t@t commit -qm baseline; }
pom() {
  printf '<?xml version="1.0" encoding="UTF-8"?>\n<project xmlns="http://maven.apache.org/POM/4.0.0">\n  <modelVersion>4.0.0</modelVersion>\n  <groupId>demo</groupId>\n  <artifactId>app</artifactId>\n  <version>1.0</version>\n  <packaging>jar</packaging>\n  <properties>\n    <maven.compiler.release>21</maven.compiler.release>\n    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>\n  </properties>\n</project>\n'
}
app_clean() {
  mkdir -p "$1/src/main/java/demo"
  printf 'package demo;\n\npublic class App {\n    public int add(int a, int b) {\n        return a + b;\n    }\n}\n' > "$1/src/main/java/demo/App.java"
}
app_dirty() {
  mkdir -p "$1/src/main/java/demo"
  cat > "$1/src/main/java/demo/App.java" <<'J'
package demo;

import java.security.MessageDigest;
import java.util.Random;

public class App {
    public int add(int a, int b) {
        return a + b;
    }

    public byte[] hash(String s) throws Exception {
        return MessageDigest.getInstance("MD5").digest(s.getBytes("UTF-8"));
    }

    public int token() {
        return new Random().nextInt();
    }

    public boolean same(String a, String b) {
        return a == b;
    }
}
J
}
plan_files() {
  mkdir -p "$1/docs/plan" "$1/.r-loop"
  cp "$ROOT/testdata/sandbox/docs/plan/todo-tiny.md" "$1/docs/plan/"
  cp "$ROOT/testdata/sandbox/.r-loop/config.yaml" "$1/.r-loop/"
}
no_build_paths() {
  bad=$(filepaths | grep -E '^(target|build|\.gradle)/|/(target|build)/' || true)
  [ -z "$bad" ] && ok "$1: no finding path under target/ or build/ ($(filepaths | sort -u | tr '\n' ' '))" || fail "$1: findings in build output: $bad"
}
leftovers() { pgrep -f "$1" | sort; }

if [ -n "$HAVE_MVN" ]; then
  echo "== a maven, no .gitignore"
  R=$(newrepo); pom > "$R/pom.xml"; app_clean "$R"; commit "$R"; app_dirty "$R"
  B=$(st "$R"); probe "$R"
  [ "$rc" = 0 ] && ok "analyze ok, $(cat "$W/err"), Command '$(cmdline)'" || fail "analyze rc=$rc: $(head -5 "$W/out")"
  has '"title": "spotbugs/WEAK_MESSAGE_DIGEST_MD5' && has 'src/main/java/demo/App.java:' && ok "spotbugs findings: $(titles)" || fail "spotbugs findings missing: $(titles)"
  same_status "$R" "$B" "maven success"
  [ ! -e "$R/target" ] && ok "target/ removed" || echo "INFO target/ still exists: $(find "$R/target" -type f | wc -l | tr -d " ") files, $(find "$R/target" -type d | wc -l | tr -d " ") dirs"

  echo "== c second run on the same fixture"
  probe "$R"
  [ "$rc" = 0 ] && ok "second analyze ok, $(cat "$W/err")" || fail "second analyze rc=$rc: $(head -5 "$W/out")"
  no_build_paths "maven second run"
  same_status "$R" "$B" "maven second run"

  echo "== a maven, pre-existing untracked target/notes.txt"
  mkdir -p "$R/target"; echo keep > "$R/target/notes.txt"
  B=$(st "$R"); probe "$R"
  [ "$rc" = 0 ] && ok "analyze ok, $(cat "$W/err")" || fail "analyze rc=$rc: $(head -5 "$W/out")"
  [ "$(cat "$R/target/notes.txt" 2>/dev/null)" = keep ] && ok "target/notes.txt survived" || fail "target/notes.txt gone"
  no_build_paths "maven with target/notes.txt"
  same_status "$R" "$B" "maven with target/notes.txt"
  rm -rf "$R/target"

  echo "== a maven, 3s timeout"
  R=$(newrepo); pom > "$R/pom.xml"; app_clean "$R"; commit "$R"; app_dirty "$R"
  B=$(st "$R"); pb=$(leftovers 'maven|plexus-classworlds|semgrep')
  probe "$R" 3s
  [ "$rc" = 1 ] && has 'timed out after 3s' && ok "timeout: $(head -1 "$W/out")" || fail "timeout: rc=$rc $(head -2 "$W/out")"
  same_status "$R" "$B" "maven timeout"
  sleep 3
  same_status "$R" "$B" "maven timeout, 3s later"
  [ "$pb" = "$(leftovers 'maven|plexus-classworlds|semgrep')" ] && ok "no maven/semgrep process left" || fail "processes left: $(leftovers 'maven|plexus-classworlds|semgrep' | tr '\n' ' ')"

  echo "== 4 dry-run banner, maven"
  R=$(newrepo); pom > "$R/pom.xml"; app_clean "$R"; plan_files "$R"; commit "$R"
  (cd "$R" && "$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$W/dry" 2>&1); drc=$?
  want='static: maven, semgrep'; [ -n "$HAVE_SEMGREP" ] || want='static: maven (semgrep not installed)'
  grep -qx "$want" "$W/dry" && ok "dry-run rc=$drc: $want" || fail "dry-run rc=$drc static line: $(grep '^static' "$W/dry")"
else
  echo "SKIP maven parts: mvn or java missing"
fi

if [ -n "$HAVE_GRADLE" ]; then
  gradle_kts() { printf 'plugins {\n    java\n}\n\ntasks.withType<JavaCompile> {\n    options.release = 21\n}\n'; }
  gradle_repo() { r=$(newrepo); echo 'rootProject.name = "demo"' > "$r/settings.gradle.kts"; gradle_kts > "$r/build.gradle.kts"; app_clean "$r"; commit "$r"; echo "$r"; }
  echo "== b gradle, no .gitignore"
  R=$(gradle_repo); app_dirty "$R"
  B=$(st "$R"); probe "$R"
  [ "$rc" = 0 ] && ok "analyze ok, $(cat "$W/err"), Command '$(cmdline)'" || fail "analyze rc=$rc: $(head -8 "$W/out")"
  has '"title": "spotbugs/WEAK_MESSAGE_DIGEST_MD5' && ok "spotbugs findings: $(titles)" || fail "gradle findings: $(titles)"
  same_status "$R" "$B" "gradle success"
  [ ! -e "$R/build" ] && [ ! -e "$R/.gradle" ] && ok "build/ and .gradle/ removed" || echo "INFO build/ .gradle/ still exist: $(find "$R/build" "$R/.gradle" -type f 2>/dev/null | wc -l | tr -d " ") files, $(find "$R/build" "$R/.gradle" -type d 2>/dev/null | wc -l | tr -d " ") dirs"

  echo "== c gradle second run"
  probe "$R"
  [ "$rc" = 0 ] && ok "second analyze ok, $(cat "$W/err")" || fail "second analyze rc=$rc: $(head -5 "$W/out")"
  no_build_paths "gradle second run"
  same_status "$R" "$B" "gradle second run"

  echo "== b gradle, pre-existing untracked build/notes.txt and .gradle/notes.txt"
  mkdir -p "$R/build" "$R/.gradle"; echo keep > "$R/build/notes.txt"; echo keep > "$R/.gradle/notes.txt"
  B=$(st "$R"); probe "$R"
  [ "$rc" = 0 ] && ok "analyze ok, $(cat "$W/err")" || fail "analyze rc=$rc: $(head -5 "$W/out")"
  [ "$(cat "$R/build/notes.txt" 2>/dev/null)" = keep ] && [ "$(cat "$R/.gradle/notes.txt" 2>/dev/null)" = keep ] && ok "build/notes.txt and .gradle/notes.txt survived" || fail "pre-existing notes gone"
  no_build_paths "gradle with notes"
  same_status "$R" "$B" "gradle with notes"

  echo "== b gradle, 1s timeout"
  R=$(gradle_repo); app_dirty "$R"
  B=$(st "$R")
  probe "$R" 1s
  [ "$rc" = 1 ] && has 'timed out after 1s' && ok "timeout: $(head -1 "$W/out")" || fail "timeout: rc=$rc $(head -2 "$W/out")"
  same_status "$R" "$B" "gradle timeout"
  sleep 3
  same_status "$R" "$B" "gradle timeout, 3s later"

  echo "== 4 dry-run banner, gradle"
  R=$(gradle_repo); plan_files "$R"; commit "$R"
  (cd "$R" && "$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$W/dry" 2>&1); drc=$?
  want='static: gradle, semgrep'; [ -n "$HAVE_SEMGREP" ] || want='static: gradle (semgrep not installed)'
  grep -qx "$want" "$W/dry" && ok "dry-run rc=$drc: $want" || fail "dry-run rc=$drc static line: $(grep '^static' "$W/dry")"
else
  echo "SKIP gradle parts: gradle or java missing"
fi

sandbox() { "$ROOT/testdata/sandbox/make-sandbox.sh" "$(mktemp -d "$W/sb-XXXXXX")"; }

echo "== d golangci-lint caps: 60 gosec hits"
S=$(sandbox)
{ printf 'package calc\n\nimport "math/rand"\n\n// %s\n' "$W $$"; i=1; while [ $i -le 60 ]; do printf '\nfunc R%d() int { return rand.Int() }\n' $i; i=$((i + 1)); done; } > "$S/rnd.go"
probe "$S"
[ "$rc" = 0 ] && ok "analyze ok, $(cat "$W/err"), Command '$(cmdline)'" || fail "analyze rc=$rc: $(head -5 "$W/out")"
n=$(grep -c '"id": "s' "$W/out"); shown=$(grep -c '"title": "golangci-lint/gosec' "$W/out")
over=$(grep -o 'golangci-lint/gosec: [0-9][0-9]*' "$W/out" | head -1 | grep -o '[0-9]*$')
[ "$n" = 51 ] && has '"id": "s51"' && has '"title": "static: [0-9]* more findings"' && ok "50 findings + overflow s51: $(grep -o '"title": "static: [0-9]* more findings"' "$W/out")" || fail "finding count $n"
[ -n "$over" ] && [ $((shown + ${over:-0})) -ge 60 ] && ok "gosec hits: $shown shown + $over in overflow" || fail "gosec hits: $shown shown, overflow '${over:-none}' (cap not lifted?)"

echo "== g identical package content in a second directory (golangci-lint cache)"
S2=$(sandbox); cp "$S/rnd.go" "$S2/rnd.go"
probe "$S2"
n2=$(grep -c '"title": "golangci-lint/gosec' "$W/out")
[ "$rc" = 0 ] && [ "$n2" -ge 50 ] && ok "second dir: $n2 gosec findings" || fail "second dir with byte-identical files: rc=$rc, $n2 golangci-lint findings (golangci-lint cache replays the first dir's absolute paths, which the analyzer drops)"

echo "== e GOTOOLCHAIN=local"
S=$(sandbox)
printf '\nfunc Tok() int { return rand.Int() }\n\n// %s\n' "$W $$" >> "$S/calc.go"
perl -pi -e 's/^import \(/import (\n\t"math\/rand"/' "$S/calc.go"
GOTOOLCHAIN=local probe "$S"
[ "$rc" = 0 ] && has '"title": "golangci-lint/gosec' && ok "local: golangci-lint ok, $(cat "$W/err"), $(titles)" || fail "local golangci-lint: rc=$rc $(head -5 "$W/out")"
printf '\n// touched\n' >> "$S/go.mod"
GOTOOLCHAIN=local probe "$S"
case "$(cmdline)" in *govulncheck*) gv=1 ;; *) gv=0 ;; esac
[ "$rc" = 0 ] && [ "$gv" = 1 ] && ok "local: govulncheck ran, Command '$(cmdline)', $(cat "$W/err")" || fail "local govulncheck: rc=$rc Command '$(cmdline)' $(head -5 "$W/out")"
R=$(newrepo); printf 'module m126\n\ngo 1.26\n' > "$R/go.mod"; printf 'package m126\n\nfunc Add(a, b int) int { return a + b }\n' > "$R/calc.go"; commit "$R"
printf '\nfunc Ignore() {\n\tf := func() error { return nil }\n\tf()\n}\n\n// %s\n' "$W $$" >> "$R/calc.go"
GOTOOLCHAIN=auto probe "$R"
[ "$rc" = 0 ] && has '"title": "golangci-lint/errcheck' && ok "go 1.26 module under auto lints: $(titles), $(cat "$W/err")" || fail "go 1.26 under auto: rc=$rc $(head -5 "$W/out")"

echo "== f changed-lines filter, untracked file, .semgrep.yml"
S=$(sandbox)
cat > "$S/old.go" <<'G'
package calc

import "math/rand"

func Old() int { return rand.Int() }

func Keep(a int) int {
	return a
}
G
cat > "$S/.semgrep.yml" <<'Y'
rules:
  - id: rloop-marker
    languages: [go]
    severity: WARNING
    message: "test rule: Marker seen"
    pattern: |
      func Marker(...) $T { ... }
Y
commit "$S"
sed -i.bak 's/return a$/return a + 1/' "$S/old.go"; rm -f "$S/old.go.bak"
printf '\nfunc New() int { return rand.Intn(5) }\n' >> "$S/old.go"
printf 'package calc\n\nimport "math/rand"\n\nfunc Marker() int { return rand.Int() }\n\n// %s\n' "$W $$" > "$S/fresh.go"
probe "$S"
[ "$rc" = 0 ] || fail "analyze rc=$rc: $(head -5 "$W/out")"
has 'old.go:5' && fail "committed gosec hit on untouched old.go:5 reported" || ok "untouched old.go:5 dropped"
has 'old.go:11' && ok "new old.go:11 reported" || fail "changed old.go:11 not reported: $(grep -o '[a-z]*\.go:[0-9]*' "$W/out" | sort -u | tr '\n' ' ')"
has 'fresh.go:5' && ok "untracked fresh.go reported" || fail "untracked fresh.go not reported"
if [ -n "$HAVE_SEMGREP" ]; then
  has '"title": "semgrep/rloop-marker' && ok "custom .semgrep.yml rule: $(titles)" || fail "custom semgrep rule missing: $(titles)"
fi
pb=$(leftovers 'golangci-lint|govulncheck|semgrep')
probe "$S" 1s
[ "$rc" = 1 ] && has 'timed out after 1s' && ok "timeout: $(head -1 "$W/out")" || fail "1s timeout: rc=$rc $(head -2 "$W/out")"
sleep 2
[ "$pb" = "$(leftovers 'golangci-lint|govulncheck|semgrep')" ] && ok "no golangci-lint/semgrep process left" || fail "processes left: $(leftovers 'golangci-lint|govulncheck|semgrep' | tr '\n' ' ')"

echo "== 4 dry-run banner, go sandbox"
S=$(sandbox)
(cd "$S" && "$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$W/dry" 2>&1); drc=$?
want='static: go, semgrep'; [ -n "$HAVE_SEMGREP" ] || want='static: go (semgrep not installed)'
grep -qx "$want" "$W/dry" && ok "dry-run rc=$drc: $want" || fail "dry-run rc=$drc static line: $(grep '^static' "$W/dry")"

echo "failures: $fails"
[ "$fails" = 0 ] && rm -rf "$W"
exit $((fails > 0))

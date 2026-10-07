#!/bin/sh
# usage: static_java.sh
#   No-agent checks of the Java path of the static analyzer (internal/analyze): Maven and Gradle fixtures are
#   built in temp dirs, Analyze is called through a throwaway probe built from a temp package inside the module
#   (removed again on exit), and mvn/gradle go through PATH shims that log argv. Skips cleanly without mvn,
#   gradle or java. Takes a few minutes on a cold ~/.m2 / ~/.gradle.
set -u
ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
MVN=$(command -v mvn 2>/dev/null) || { echo "SKIP: mvn not on PATH"; exit 0; }
GRADLE=$(command -v gradle 2>/dev/null) || { echo "SKIP: gradle not on PATH"; exit 0; }
command -v java > /dev/null 2>&1 || { echo "SKIP: java not on PATH"; exit 0; }
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }

W=$(mktemp -d "${TMPDIR:-/tmp}/static-java-XXXXXX")
PROBE_SRC="$ROOT/cmd/zz-static-java-probe-$$"
cleanup() { rm -rf "$PROBE_SRC"; }
trap cleanup EXIT INT TERM
mkdir -p "$PROBE_SRC" "$W/shim"
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
(cd "$ROOT" && go build -o "$W/probe" "./cmd/zz-static-java-probe-$$") || { echo "FAIL probe build"; exit 1; }
rm -rf "$PROBE_SRC"
LOG="$W/shim/log"
for t in mvn gradle; do
  real=$MVN; [ "$t" = gradle ] && real=$GRADLE
  printf '#!/bin/sh\necho "%s cwd=$PWD argv: $*" >> %s\nexec %s "$@"\n' "$t" "$LOG" "$real" > "$W/shim/$t"
  chmod +x "$W/shim/$t"
done
SP="$W/shim:$PATH"
echo "work $W"

fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
probe() { : > "$LOG"; PATH="$SP" "$W/probe" "$@" > "$W/out" 2> "$W/err"; rc=$?; }
has() { grep -q -- "$1" "$W/out"; }
titles() { grep -o '"title": "[a-z-]*/[A-Za-z0-9_.-]*' "$W/out" | cut -d'"' -f4 | sort -u | tr '\n' ' '; }
cmdline() { grep -o '"Command": "[^"]*"' "$W/out" | cut -d'"' -f4; }

newrepo() { d=$(mktemp -d "$W/repo-XXXXXX"); git -C "$d" init -q -b main; echo "$d"; }
commit() { git -C "$1" add -A && git -C "$1" -c user.name=t -c user.email=t@t commit -qm baseline; }
pom() {
  a=$1; pk=$2; shift 2
  mods=""; for m in "$@"; do mods="$mods<module>$m</module>"; done
  [ -n "$mods" ] && mods="<modules>$mods</modules>"
  printf '<?xml version="1.0" encoding="UTF-8"?>\n<project xmlns="http://maven.apache.org/POM/4.0.0">\n  <modelVersion>4.0.0</modelVersion>\n  <groupId>demo</groupId>\n  <artifactId>%s</artifactId>\n  <version>1.0</version>\n  <packaging>%s</packaging>\n  %s\n  <properties>\n    <maven.compiler.release>21</maven.compiler.release>\n    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>\n  </properties>\n</project>\n' "$a" "$pk" "$mods"
}
child_pom() {
  printf '<?xml version="1.0" encoding="UTF-8"?>\n<project xmlns="http://maven.apache.org/POM/4.0.0">\n  <modelVersion>4.0.0</modelVersion>\n  <parent><groupId>demo</groupId><artifactId>parent</artifactId><version>1.0</version></parent>\n  <artifactId>%s</artifactId>\n</project>\n' "$1"
}
app_clean() {
  mkdir -p "$1/src/main/java/$2"
  printf 'package %s;\n\npublic class App {\n    public int add(int a, int b) {\n        return a + b;\n    }\n}\n' "$2" > "$1/src/main/java/$2/App.java"
}
app_dirty() {
  mkdir -p "$1/src/main/java/$2"
  cat > "$1/src/main/java/$2/App.java" <<J
package $2;

import java.security.MessageDigest;
import java.util.Random;

public class App {
    private int unusedField;

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

    public void quiet() {
        try {
            Integer.parseInt("x");
        } catch (NumberFormatException e) {
        }
    }
}
J
}
maven_repo() { r=$(newrepo); pom app jar > "$r/pom.xml"; app_clean "$r" demo; printf 'target/\n' > "$r/.gitignore"; commit "$r"; echo "$r"; }
gradle_kts() { printf 'plugins {\n    java\n}\n\ntasks.withType<JavaCompile> {\n    options.release = 21\n}\n'; }
gradle_groovy() { printf "plugins {\n    id 'java'\n}\n\ntasks.withType(JavaCompile).configureEach {\n    options.release = 21\n}\n"; }

echo "== 1 single-module maven"
R=$(maven_repo); app_dirty "$R" demo
JAR="$(go env GOOS 2>/dev/null | grep -q darwin && echo "$HOME/Library/Caches" || echo "${XDG_CACHE_HOME:-$HOME/.cache}")/r-loop/analyze/findsecbugs-plugin-1.14.0.jar"
probe "$R"
[ "$rc" = 0 ] && ok "analyze ok, $(cat "$W/err")" || { fail "analyze rc=$rc: $(head -5 "$W/out")"; }
[ "$(cmdline)" = "pmd, spotbugs, semgrep" ] || { command -v semgrep > /dev/null || [ "$(cmdline)" = "pmd, spotbugs" ]; } && ok "Command: $(cmdline)" || fail "Command: $(cmdline)"
has '"title": "pmd/UnusedPrivateField' && has '"title": "pmd/EmptyCatchBlock' && ok "pmd findings: $(titles)" || fail "pmd findings missing: $(titles)"
has 'src/main/java/demo/App.java:7' && ok "path src/main/java/demo/App.java:<line>" || fail "no src/main/java/demo/App.java:7 detail"
for rule in ES_COMPARING_PARAMETER_STRING_WITH_EQ WEAK_MESSAGE_DIGEST_MD5 PREDICTABLE_RANDOM; do
  has "\"title\": \"spotbugs/$rule" && ok "spotbugs/$rule reported" || fail "spotbugs/$rule missing (target/spotbugsSarif.json has: $(grep -o '"ruleId" *: *"[A-Z_0-9]*"' "$R/target/spotbugsSarif.json" 2>/dev/null | cut -d'"' -f4 | sort -u | tr '\n' ' ') uri $(grep -o '"uri" *: *"[^"]*"' "$R/target/spotbugsSarif.json" 2>/dev/null | head -1))"
done
[ -f "$JAR" ] && ok "find-sec-bugs jar cached at $JAR" || fail "no cached jar at $JAR"
probe "$R"
[ "$rc" = 0 ] && [ "$(grep -c . "$LOG")" = 1 ] && ! grep -q 'dependency' "$LOG" && ok "second run reuses the jar: one mvn call, $(cat "$W/err")" || fail "second run: rc=$rc, mvn calls: $(cat "$LOG")"

echo "== 2 changed-lines filter"
R=$(newrepo); pom app jar > "$R/pom.xml"; app_dirty "$R" demo; printf 'target/\n' > "$R/.gitignore"; commit "$R"
sed -i.bak 's/return a + b;/return b + a;/' "$R/src/main/java/demo/App.java"; rm -f "$R/src/main/java/demo/App.java.bak"
printf 'package demo;\n\npublic class Extra {\n    private int neverRead;\n\n    public boolean eq(String a, String b) {\n        return a == b;\n    }\n}\n' > "$R/src/main/java/demo/Extra.java"
probe "$R"
[ "$rc" = 0 ] || fail "analyze rc=$rc: $(head -5 "$W/out")"
has 'App.java:7' || has 'App.java:28' && fail "committed violation on an untouched line reported" || ok "committed violations on untouched lines dropped"
has 'src/main/java/demo/Extra.java:4' && ok "untracked Extra.java reported (pmd)" || fail "untracked Extra.java not reported"
has '"title": "spotbugs/ES_COMPARING_PARAMETER_STRING_WITH_EQ' && ok "untracked Extra.java reported (spotbugs)" || fail "spotbugs finding on untracked Extra.java:7 dropped"

echo "== 3 multi-module maven"
R=$(newrepo); pom parent pom a b > "$R/pom.xml"
for m in a b; do mkdir -p "$R/$m"; child_pom "$m" > "$R/$m/pom.xml"; app_clean "$R/$m" "$m"; done
printf 'target/\n' > "$R/.gitignore"; commit "$R"; app_dirty "$R/b" b
mkdir -p "$R/a/target"
printf '<?xml version="1.0"?>\n<pmd xmlns="http://pmd.sourceforge.net/report/2.0.0"><file name="%s/b/src/main/java/b/App.java"><violation beginline="7" endline="7" rule="StaleRuleFromA" priority="3">stale</violation></file></pmd>\n' "$R" > "$R/a/target/pmd.xml"
probe "$R"
[ "$rc" = 0 ] || fail "analyze rc=$rc: $(head -5 "$W/out")"
grep -q -- '-pl b -am compile' "$LOG" && ok "maven invoked with -pl b -am" || fail "maven argv: $(cat "$LOG")"
has 'StaleRuleFromA' && fail "stale a/target/pmd.xml read" || ok "stale report in untouched module a ignored"
grep '"detail"' "$W/out" | grep -v -q 'b/src/main/java/b/App.java' && fail "finding outside b: $(grep '"detail"' "$W/out" | grep -v 'b/src')" || ok "findings only from b: $(titles)"

echo "== 4 maven wrapper"
R=$(maven_repo)
(cd "$R" && "$MVN" -q -B wrapper:wrapper > /dev/null 2>&1) && [ -x "$R/mvnw" ] || fail "mvn wrapper:wrapper"
if [ -x "$R/mvnw" ]; then
  sed -i.bak "1a\\
echo \"mvnw cwd=\$PWD argv: \$*\" >> $LOG
" "$R/mvnw"; rm -f "$R/mvnw.bak"
  commit "$R"; app_dirty "$R" demo
  probe "$R"
  [ "$rc" = 0 ] && grep -q '^mvnw ' "$LOG" && ! grep -q '^mvn ' "$LOG" && ok "executable mvnw is the runner" || fail "runner: rc=$rc log: $(cat "$LOG")"
  chmod -x "$R/mvnw"
  probe "$R"
  [ "$rc" = 1 ] && has 'maven: fork/exec .*mvnw: permission denied' && ok "non-executable mvnw: $(head -1 "$W/out")" || fail "non-executable mvnw: rc=$rc $(head -2 "$W/out")"
  grep -q '^mvn ' "$LOG" && echo "INFO non-executable mvnw fell back to mvn" || echo "INFO non-executable mvnw: no fallback to mvn (preflight refuses this case with exit 2)"
fi

echo "== 5 compile failure"
R=$(maven_repo)
printf 'package demo;\npublic class Broken { int x = "nope"; }\n' > "$R/src/main/java/demo/Broken.java"
probe "$R"
[ "$rc" = 1 ] && has '^ERROR: maven: exit status 1' && has 'incompatible types' && ok "compile failure is an error naming maven with the output tail" || fail "compile failure: rc=$rc $(head -3 "$W/out")"

echo "== 6 no classes"
R=$(maven_repo); mkdir -p "$R/src/main/resources"; echo k=v > "$R/src/main/resources/app.properties"
probe "$R"
[ "$rc" = 0 ] && [ ! -s "$LOG" ] && ok "non-.java change: maven not run, Command '$(cmdline)'" || fail "non-.java change: rc=$rc mvn: $(cat "$LOG")"
R=$(newrepo); pom app pom > "$R/pom.xml"; app_clean "$R" demo; printf 'target/\n' > "$R/.gitignore"; commit "$R"; app_dirty "$R" demo
probe "$R"
[ "$rc" = 0 ] && ok "packaging pom with .java change: no error, findings '$(titles)', Command '$(cmdline)'" || fail "packaging pom: rc=$rc $(head -3 "$W/out")"
R=$(maven_repo); mkdir -p "$R/src/test/java/demo"
printf 'package demo;\npublic class AppTest {\n    private int unusedField;\n    boolean same(String a, String b) { return a == b; }\n}\n' > "$R/src/test/java/demo/AppTest.java"
probe "$R"
[ "$rc" = 0 ] && ok "src/test-only change: no error, findings '$(titles)' (pmd/spotbugs analyse main only)" || fail "test-only: rc=$rc $(head -3 "$W/out")"

echo "== 7 gradle single project (kotlin dsl, java $(java -version 2>&1 | head -1 | cut -d'"' -f2), $("$GRADLE" --version 2>/dev/null | grep '^Gradle'))"
R=$(newrepo); echo 'rootProject.name = "demo"' > "$R/settings.gradle.kts"; gradle_kts > "$R/build.gradle.kts"; app_clean "$R" demo
printf 'build/\n.gradle/\n' > "$R/.gitignore"; commit "$R"; app_dirty "$R" demo
probe "$R"
if [ "$rc" = 0 ]; then
  ok "gradle analyze ok, $(cat "$W/err"), Command '$(cmdline)'"
  has '"title": "pmd/' && has '"title": "spotbugs/' && ok "pmd + spotbugs findings: $(titles)" || fail "gradle findings: $(titles)"
  has '"title": "spotbugs/WEAK_MESSAGE_DIGEST_MD5' && ok "find-sec-bugs rule via spotbugs" || fail "no find-sec-bugs finding on gradle"
  grep -q ':pmdMain :spotbugsMain$' "$LOG" && ok "tasks :pmdMain :spotbugsMain" || fail "gradle argv: $(cat "$LOG")"
else
  fail "gradle on the default JDK: rc=$rc $(head -8 "$W/out")"
fi

echo "== 8 gradle multi-project"
R=$(newrepo); printf 'rootProject.name = "multi"\ninclude("app", "lib")\n' > "$R/settings.gradle.kts"
for m in app lib; do mkdir -p "$R/$m"; gradle_kts > "$R/$m/build.gradle.kts"; app_clean "$R/$m" "$m"; done
printf 'build/\n.gradle/\n' > "$R/.gitignore"; commit "$R"; app_dirty "$R/lib" lib
probe "$R"
[ "$rc" = 0 ] && grep -q ':lib:pmdMain :lib:spotbugsMain$' "$LOG" && ! grep -q ':app:' "$LOG" && ok "kts: only :lib:pmdMain :lib:spotbugsMain" || fail "kts multi: rc=$rc argv: $(cat "$LOG")"
grep '"detail"' "$W/out" | grep -v -q 'lib/src/main/java/lib/App.java' && fail "finding outside lib" || ok "findings only from lib: $(titles)"
R=$(newrepo); printf "rootProject.name = 'multi'\ninclude 'app', 'lib'\n" > "$R/settings.gradle"
for m in app lib; do mkdir -p "$R/$m"; gradle_groovy > "$R/$m/build.gradle"; app_clean "$R/$m" "$m"; done
printf 'build/\n.gradle/\n' > "$R/.gitignore"
(cd "$R" && "$GRADLE" -q wrapper > /dev/null 2>&1) && [ -x "$R/gradlew" ] || fail "gradle wrapper"
if [ -x "$R/gradlew" ]; then
  sed -i.bak "1a\\
echo \"gradlew cwd=\$PWD argv: \$*\" >> $LOG
" "$R/gradlew"; rm -f "$R/gradlew.bak"
fi
commit "$R"; app_dirty "$R/lib" lib
probe "$R"
[ "$rc" = 0 ] && has '"title": "spotbugs/' && ok "groovy build.gradle works: $(titles)" || fail "groovy: rc=$rc $(head -5 "$W/out")"
grep -q '^gradlew .*:lib:pmdMain :lib:spotbugsMain$' "$LOG" && ! grep -q '^gradle ' "$LOG" && ok "executable gradlew is the runner" || fail "gradle runner: $(cat "$LOG")"

echo "== 9 mixed go + maven"
if command -v go > /dev/null 2>&1; then
  R=$(newrepo); printf 'module mixed\n\ngo 1.25\n' > "$R/go.mod"; printf 'package mixed\n\nfunc Add(a, b int) int { return a + b }\n' > "$R/calc.go"
  mkdir -p "$R/java"; pom app jar > "$R/java/pom.xml"; app_clean "$R/java" demo; printf 'target/\n' > "$R/.gitignore"; commit "$R"; app_dirty "$R/java" demo
  printf '\nfunc Ignore() {\n\tf := func() error { return nil }\n\tf()\n}\n' >> "$R/calc.go"
  probe "$R"
  want="golangci-lint, pmd, spotbugs"; command -v semgrep > /dev/null && want="$want, semgrep"
  [ "$rc" = 0 ] && [ "$(cmdline)" = "$want" ] && ok "Command '$(cmdline)'" || fail "mixed: rc=$rc Command '$(cmdline)' $(head -3 "$W/out")"
  has '"title": "golangci-lint/' && has 'java/src/main/java/demo/App.java' && ok "both go and java findings: $(titles)" || fail "mixed findings: $(titles)"
  grep -q "$(basename "$R")/java argv" "$LOG" && ok "maven ran in java/" || fail "maven cwd: $(cat "$LOG")"
else
  echo "SKIP 9: go not on PATH"
fi

echo "== 10 timeout"
R=$(maven_repo); app_dirty "$R" demo
before=$(pgrep -f 'maven|plexus-classworlds' | sort)
probe "$R" 3s
[ "$rc" = 1 ] && has 'timed out after 3s' && ok "timeout: $(head -1 "$W/out")" || fail "timeout: rc=$rc $(head -2 "$W/out")"
sleep 2
after=$(pgrep -f 'maven|plexus-classworlds' | sort)
[ "$before" = "$after" ] && ok "no maven process left after the timeout" || fail "maven processes left: $(echo "$after" | tr '\n' ' ')"

echo "== info: build output without a .gitignore"
R=$(newrepo); pom app jar > "$R/pom.xml"; app_clean "$R" demo; commit "$R"; app_dirty "$R" demo
probe "$R"
n=$(git -C "$R" status --porcelain --untracked-files=all | grep -c '^?? target/')
echo "INFO analyze left $n untracked files under target/ in a repo that does not ignore it (the static reviewer runs between the round snapshot and checkTree)"

echo "failures: $fails"
[ "$fails" = 0 ] && rm -rf "$W"
exit $((fails > 0))

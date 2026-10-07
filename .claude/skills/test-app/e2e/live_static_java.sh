#!/bin/sh
# usage: live_static_java.sh [--unignored]
#   --unignored: target/ is NOT in .gitignore, and the plan also asks for an MD5 digest helper so SpotBugs fires on
#   new code; asserts the analyzer's build output never trips "reviewer modified the tree" and is never committed.
#   One live run on a Maven repo: a one-phase plan adds Calc.subtract with a JUnit test. The static reviewer runs
#   pmd + spotbugs (via maven) and semgrep (a repo .semgrep.yml rule that always matches `subtract`) in the
#   implement review round. Asserts the review-find event, the findings file, a verdict per static finding, a
#   clean landing with no build output committed, status/report, and a clean q quit.
set -u
UNIGNORED=""
[ "${1:-}" = "--unignored" ] && UNIGNORED=1
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }
command -v mvn > /dev/null 2>&1 || { echo "NOT RUN: mvn not on PATH"; exit 3; }
command -v java > /dev/null 2>&1 || { echo "NOT RUN: java not on PATH"; exit 3; }
command -v semgrep > /dev/null 2>&1 || { echo "NOT RUN: semgrep not on PATH"; exit 3; }

ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-3000}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

RAND=$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')
S="$HOME/r-loop-test-tjava-$RAND"
mkdir -p "$S/src/main/java/calc" "$S/src/test/java/calc" "$S/docs/plan" "$S/.r-loop" || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"

cat > pom.xml <<'P'
<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 https://maven.apache.org/xsd/maven-4.0.0.xsd">
  <modelVersion>4.0.0</modelVersion>
  <groupId>demo</groupId>
  <artifactId>calc</artifactId>
  <version>1.0</version>
  <properties>
    <maven.compiler.release>21</maven.compiler.release>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>
  <dependencies>
    <dependency>
      <groupId>org.junit.jupiter</groupId>
      <artifactId>junit-jupiter</artifactId>
      <version>5.11.4</version>
      <scope>test</scope>
    </dependency>
  </dependencies>
  <build>
    <plugins>
      <plugin>
        <groupId>org.apache.maven.plugins</groupId>
        <artifactId>maven-surefire-plugin</artifactId>
        <version>3.5.2</version>
      </plugin>
    </plugins>
  </build>
</project>
P
cat > src/main/java/calc/Calc.java <<'J'
package calc;

public final class Calc {
    private Calc() {
    }

    public static int add(int a, int b) {
        return a + b;
    }
}
J
cat > src/test/java/calc/CalcTest.java <<'J'
package calc;

import static org.junit.jupiter.api.Assertions.assertEquals;

import org.junit.jupiter.api.Test;

class CalcTest {
    @Test
    void addsTwoNumbers() {
        assertEquals(5, Calc.add(2, 3));
    }
}
J
cat > .semgrep.yml <<'R'
rules:
  - id: subtract-method
    languages: [java]
    severity: WARNING
    message: a subtract method was declared here; check it handles overflow
    pattern: |
      $RET subtract(...) { ... }
R
[ -n "$UNIGNORED" ] && printf '*.log\n' > .gitignore || printf 'target/\n' > .gitignore
cat > docs/plan/todo-java.md <<'M'
# Calc — Java Smoke Plan

Sources: this file · Status: draft
One phase, the cheapest full `plan → implement → land` run of the driver on a Maven repo.

## Waves
<!-- generated from the Depends on edges — regenerate, never hand-edit -->
- Wave 0: Phase 1

## Milestone 1 — Calculator

### Phase 1 — Subtract
**Implements:** Subtract numbers
**Depends on:** none
**Files:** `src/main/java/calc/Calc.java` · `src/test/java/calc/CalcTest.java`
- [ ] `Calc.subtract(int a, int b)` returns `a - b`, with a JUnit 5 test covering a negative result
**Done when:** `mvn -q test` is green.
M
[ -z "$UNIGNORED" ] || sed -i '' 's/^\(- \[ \] `Calc.subtract.*\)$/\1\
- [ ] `Calc.digest(String s)` returns the lowercase hex MD5 of `s` (UTF-8), built with `java.security.MessageDigest.getInstance("MD5")` exactly as written (a legacy checksum format, MD5 is required), with a JUnit 5 test for `digest("abc")`/' docs/plan/todo-java.md
sed "s/^label: test\$/label: ${LABEL:-tjava}/" "$ROOT/testdata/sandbox/.r-loop/config.yaml" > .r-loop/config.yaml
git init -q -b main
git add -A
git -c user.name=sandbox -c user.email=sandbox@localhost commit -q -m "java sandbox baseline"
mvn -q -B test > "$OUT/warm.out" 2>&1 || { echo "FAIL baseline mvn -q test"; cat "$OUT/warm.out"; exit 1; }
[ -z "$UNIGNORED" ] || rm -rf target
[ -z "$(git status --porcelain)" ] || { echo "FAIL tree dirty after warm-up: $(git status --porcelain)"; exit 1; }

"$BIN" docs/plan/todo-java.md --dry-run --plain > "$OUT/dryrun.out" 2>&1 || { echo "FAIL dry-run"; cat "$OUT/dryrun.out"; exit 1; }
grep -q '^static: maven, semgrep$' "$OUT/dryrun.out" && ok "dry-run: static: maven, semgrep" || fail "dry-run static line: $(grep '^static' "$OUT/dryrun.out")"
grep -q "^label: ${LABEL:-tjava}" "$OUT/dryrun.out" && ok "dry-run label ${LABEL:-tjava}" || fail "dry-run label is not ${LABEL:-tjava}"

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-staticjava}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-4200}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-java.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"

(
  while "$TUI" status "$H" 2>/dev/null | grep -q '^running'; do
    EV=$(ls .r-loop/runs/*/events.jsonl 2>/dev/null | head -1)
    if [ -n "$EV" ] && grep '"Kind":"review-find"' "$EV" | grep '"Step":"implement"' | grep -q '"reviewer":"static"'; then
      sleep 0.5
      cp "$("$TUI" capture "$H")" "$OUT/frames/review-120x40.txt"
      "$TUI" resize "$H" 80x24 > /dev/null && sleep 0.7 && cp "$("$TUI" capture "$H")" "$OUT/frames/review-80x24.txt"
      "$TUI" resize "$H" 120x40 > /dev/null
      break
    fi
    sleep 0.3
  done
) &
review_watch=$!

start=$(date +%s)
final=""
while :; do
  sleep 10
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed|blocked)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  [ $(( $(date +%s) - start )) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
done
kill "$review_watch" 2>/dev/null
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 5
cp "$("$TUI" capture "$H")" "$OUT/frames/end-120x40.txt"
"$BIN" status --plain > "$OUT/status.txt" 2>&1

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
RUN=$(dirname "$EV")
cp "$EV" "$OUT/events.jsonl"
cp "$RUN/report.md" "$OUT/report.md" 2>/dev/null
ls -R "$RUN" > "$OUT/rundir.txt"
ev() { grep "\"Kind\":\"$1\"" "$EV"; }

case "$final" in *finished*) ok "run ended: $final" ;; *) fail "run did not finish: $final" ;; esac

sf=$(ev review-find | grep '"Step":"implement"' | grep '"reviewer":"static"' | head -1)
[ -n "$sf" ] && ok "implement review-find static: $(echo "$sf" | grep -o '"Fields":{[^}]*}')" || fail "no implement review-find static"
echo "$sf" | grep -q '"state":"ok"' && ok "static state ok" || fail "static state not ok"
for t in pmd spotbugs semgrep; do
  echo "$sf" | grep -o '"command":"[^"]*"' | grep -q "$t" && ok "static command lists $t" || fail "static command lacks $t: $(echo "$sf" | grep -o '"command":"[^"]*"')"
done
mod=$(grep -o 'reviewer modified the tree: [^"]*' "$EV")
if [ -z "$mod" ]; then
  ok "no 'reviewer modified the tree'"
elif echo "$mod" | grep -qE 'pmd\.xml|spotbugs|target/pmd|\.gradle/|build/reports/(pmd|spotbugs)'; then
  fail "analyzer output tripped the tree check: $(echo "$mod" | head -1 | cut -c1-300)"
else
  echo "INFO tree check tripped by a reviewer's own build, not the analyzer: $(echo "$mod" | head -1 | cut -c1-300)"
fi
grep '^{"Kind":"step"' "$EV" | grep '"State":"failed"' | grep -v 'reviewer modified the tree' | grep -q . && fail "a step failed: $(grep '^{"Kind":"step"' "$EV" | grep '"State":"failed"' | grep -v 'reviewer modified the tree' | head -1 | cut -c1-300)" || ok "no step failed for a reason other than a reviewer's build"
ev review-find | grep '"Step":"plan"' | grep -q '"reviewer":"static"' && fail "plan (non-diff) round ran static" || ok "no static review on the plan step"

FS="$RUN/phase-1/implement-findings-static-r1.json"
if [ -f "$FS" ]; then
  cp "$FS" "$OUT/"
  python3 - "$FS" "$RUN/phase-1/implement-verdict-r1.json" > "$OUT/static-check.txt" <<'EOF'
import json, sys
fs = json.load(open(sys.argv[1]))
ids = [f["id"] for f in fs["findings"]]
tools = sorted({f["title"].split("/", 1)[0] for f in fs["findings"]})
print("static findings", len(ids), "tools", ",".join(tools))
for f in fs["findings"]:
    print("  ", f["id"], f["title"][:100], f.get("files"))
try:
    vd = json.load(open(sys.argv[2]))
except Exception as e:
    print("verdict-missing", e)
    sys.exit(0)
got = {v["id"]: v for v in vd["findings"]}
missing = [i for i in ids if i not in got]
print("verdict-missing-ids", ",".join(missing) if missing else "none")
for i in ids:
    if i in got:
        print("  verdict", i, got[i]["verdict"], got[i].get("severity"), got[i].get("fixed"))
EOF
  cat "$OUT/static-check.txt"
  grep -q '^static findings [1-9]' "$OUT/static-check.txt" && ok "static findings file has findings" || fail "static findings file empty"
  grep -q 'semgrep' "$OUT/static-check.txt" && ok "semgrep finding present (subtract-method)" || fail "no semgrep finding"
  grep '^static findings' "$OUT/static-check.txt" | grep -q 'pmd' && ok "pmd finding present" || echo "INFO no pmd finding (PMD may stay quiet on subtract)"
  if grep '^static findings' "$OUT/static-check.txt" | grep -q 'spotbugs'; then ok "spotbugs finding present: $(grep 'spotbugs/' "$OUT/static-check.txt" | head -3 | tr '\n' ';')"
elif [ -n "$UNIGNORED" ] && git log -p main | grep -q 'getInstance("MD5")'; then fail "step wrote MD5 but no spotbugs finding"
else echo "INFO no spotbugs finding"; fi
  grep -q '^verdict-missing-ids none' "$OUT/static-check.txt" && ok "every static finding has a verdict" || fail "static verdicts: $(grep verdict-missing "$OUT/static-check.txt")"
else
  fail "no $FS"
fi

LM=$(ev landed | grep -o '"merge":"[0-9a-f]*"' | head -1 | cut -d'"' -f4)
[ -n "$LM" ] && ok "phase landed: $(git log -1 --format='%h %s' "$LM")" || fail "no landed event"
git show --stat HEAD > "$OUT/show-stat.txt" 2>&1
git log --name-only --format= main > "$OUT/committed.txt"
grep -E '(^|/)target/|pmd\.xml|spotbugs|\.sarif|\.class$' "$OUT/committed.txt" && fail "build output or reports committed" || ok "no target/ or reports committed ($(sort -u "$OUT/committed.txt" | tr '\n' ' '))"
grep -q -- '- \[x\]' docs/plan/todo-java.md && ok "plan item ticked" || fail "plan item not ticked"
grep -q 'subtract' src/main/java/calc/Calc.java && ok "Calc.subtract on main" || fail "Calc.subtract not on main"
git status --porcelain --untracked-files=all > "$OUT/porcelain.txt"
if [ -n "$UNIGNORED" ]; then
  [ -n "$(grep -v '^?? target/' "$OUT/porcelain.txt")" ] && fail "primary tree dirty beyond target/: $(grep -v '^?? target/' "$OUT/porcelain.txt" | tr '\n' ' ')" || ok "primary tree clean apart from untracked target/ ($(grep -c '^?? target/' "$OUT/porcelain.txt") files)"
  git log -p main | grep -q 'getInstance("MD5")' && ok "step wrote the MD5 digest helper" || echo "INFO step did not write the MD5 helper"
else
  [ -z "$(cat "$OUT/porcelain.txt")" ] && ok "primary tree clean" || fail "primary tree dirty: $(tr '\n' ' ' < "$OUT/porcelain.txt")"
fi

grep -qE '^run [^ ]+ finished' "$OUT/status.txt" && ok "status: $(head -1 "$OUT/status.txt")" || fail "status: $(head -3 "$OUT/status.txt")"
[ -s "$OUT/report.md" ] && ok "report.md present ($(wc -l < "$OUT/report.md" | tr -d ' ') lines)" || fail "no report.md"
grep -qi static "$OUT/report.md" 2>/dev/null && ok "report mentions static: $(grep -i static "$OUT/report.md" | head -2 | tr '\n' ';' | cut -c1-200)" || echo "INFO report.md does not mention static"
ps -axo pid,command | grep -e "$S" | grep -v grep | grep -e maven -e java > "$OUT/ps-end.txt"
[ -s "$OUT/ps-end.txt" ] && fail "maven/java still running in the sandbox: $(head -2 "$OUT/ps-end.txt")" || ok "no maven/java process left in the sandbox"

"$TUI" send "$H" q > /dev/null 2>&1
sleep 3
"$TUI" status "$H" > "$OUT/tui-status.txt"
echo "tui     $(cat "$OUT/tui-status.txt")"
"$TUI" stop "$H" --expect-exited > /dev/null 2>&1
src=$?
[ "$src" = 0 ] && ok "q then stop --expect-exited 0" || fail "stop --expect-exited rc=$src ($(cat "$OUT/tui-status.txt"))"
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))

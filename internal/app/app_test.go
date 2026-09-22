package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"r-loop/internal/core"
	"r-loop/internal/face/tui"
	"r-loop/internal/store"
)

type fixture struct {
	t     *testing.T
	root  string
	env   Env
	out   *bytes.Buffer
	err   *bytes.Buffer
	todo  string
	herdr string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root, out: &bytes.Buffer{}, err: &bytes.Buffer{}, todo: filepath.Join(root, "docs", "topic", "todo.md")}
	git(t, root, "init", "-q", "-b", "main")
	data, err := os.ReadFile("../plan/testdata/todo.md")
	if err != nil {
		t.Fatal(err)
	}
	f.write("docs/topic/todo.md", string(data))
	f.herdr = filepath.Join(t.TempDir(), "herdr")
	f.fakeHerdr(0)
	f.env = Env{Dir: root, Home: t.TempDir(), Herdr: f.herdr, Git: "git", PID: 4242, Stdout: f.out, Stderr: f.err}
	return f
}

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	path := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) commit() {
	f.t.Helper()
	git(f.t, f.root, "add", "-A")
	git(f.t, f.root, "commit", "-q", "-m", "fixture")
}

func (f *fixture) fakeHerdr(exit int) {
	f.t.Helper()
	script := "#!/bin/sh\necho \"$@\" >> \"$0.calls\"\n"
	if exit == 0 {
		script += "echo '{}'\n"
	} else {
		script += "echo 'connection refused' >&2\nexit " + strconv.Itoa(exit) + "\n"
	}
	if err := os.WriteFile(f.herdr, []byte(script), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) herdrCalled() bool {
	_, err := os.Stat(f.herdr + ".calls")
	return err == nil
}

func (f *fixture) main(args ...string) int {
	return Main(args, f.env)
}

func (f *fixture) preflight(args ...string) (*Wiring, error) {
	f.t.Helper()
	opts, err := ParseArgs(args)
	if err != nil {
		f.t.Fatal(err)
	}
	w, err := Wire(opts, f.env)
	if err != nil {
		return nil, err
	}
	return w, Preflight(w)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	e, ok := err.(*ExitError)
	if !ok {
		t.Fatalf("not an exit error: %v", err)
	}
	return e.Code
}

func TestHerdrUnreachableIsRefusedWithExit4(t *testing.T) {
	f := newFixture(t)
	f.commit()
	f.fakeHerdr(1)

	_, err := f.preflight(f.todo, "--plain")

	if code := exitCode(t, err); code != 4 || !strings.Contains(err.Error(), "herdr server unreachable") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestDirtyPrimaryTreeIsRefusedWithExit4ListingPaths(t *testing.T) {
	f := newFixture(t)
	f.commit()
	f.write("notes.txt", "scratch")
	f.write("docs/topic/spec.md", "draft")

	_, err := f.preflight(f.todo, "--plain")

	if code := exitCode(t, err); code != 4 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	for _, want := range []string{"primary tree is not clean", "notes.txt", "docs/topic/spec.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q missing from %v", want, err)
		}
	}
}

func TestLiveRunIsRefusedWithExit4(t *testing.T) {
	f := newFixture(t)
	f.commit()
	if err := store.EnsureExcluded(f.root); err != nil {
		t.Fatal(err)
	}
	if err := store.New(f.root).SetCurrent("20260918-101500", os.Getpid()); err != nil {
		t.Fatal(err)
	}

	_, err := f.preflight(f.todo, "--plain")

	want := "run 20260918-101500 is live in pid " + strconv.Itoa(os.Getpid()) + "; use r-loop status, resume or abort"
	if code := exitCode(t, err); code != 4 || !strings.Contains(err.Error(), want) {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestResolveFirstEntryOutsideTheRunListDoesNotRefuse(t *testing.T) {
	f := newFixture(t)
	data, _ := os.ReadFile(f.todo)
	f.write("docs/topic/todo.md", strings.Replace(string(data), "## Waves",
		"## Resolve first\n- [ ] **Pick the database** — Owner: me · Blocks: Phase 2\n\n## Waves", 1))
	f.commit()

	_, err := f.preflight(f.todo, "--plain", "--from", "3")

	if err != nil {
		t.Fatal(err)
	}
}

func TestMissingHerdrBinaryExits127(t *testing.T) {
	f := newFixture(t)
	f.commit()
	f.env.Herdr = filepath.Join(t.TempDir(), "no-herdr")

	code := f.main(f.todo, "--plain")

	if code != 127 || !strings.Contains(f.err.String(), "herdr") {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
}

func TestMissingGitBinaryExits127(t *testing.T) {
	f := newFixture(t)
	f.commit()
	f.env.Git = "no-such-git-binary"

	code := f.main(f.todo, "--plain")

	if code != 127 || !strings.Contains(f.err.String(), "no-such-git-binary") {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
}

func TestConfigErrorExits2WithTheReadersMessage(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "pipeline: [plan, implement]\n")
	f.commit()

	code := f.main(f.todo, "--plain")

	if code != 2 || !strings.Contains(f.err.String(), ".r-loop/config.yaml:1") || strings.Count(f.err.String(), "\n") != 1 {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
}

func TestProviderBlockMissingKindIsRefusedInPreflight(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  claude:\n    modelFlag: --model {model}\n    doneSignal: sentinel\n")
	f.commit()

	_, err := f.preflight(f.todo, "--plain")

	if code := exitCode(t, err); code != 2 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	for _, want := range []string{"steps.plan.provider", "kind is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q missing from %v", want, err)
		}
	}
	if f.herdrCalled() {
		t.Fatal("herdr contacted before the provider check")
	}
}

func TestReviewerWithoutReviewCommandIsRefusedWithExit2(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  codex:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\n")
	f.commit()

	_, err := f.preflight(f.todo, "--plain")

	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), "steps.plan.reviewers: provider codex has no review command") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestUIReviewerNeedsNoReviewCommandAndReportsItsSkill(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  bare:\n    kind: bare\n    doneSignal: sentinel\n    ask: mcp\nsteps:\n  implement:\n    reviewers:\n      - claude\n      - name: ui\n        provider: bare\n        prompt: review-ui\n        requires: .claude/skills/test-app/SKILL.md\n")
	f.write(".claude/skills/test-app/SKILL.md", "# test-app\n")
	f.commit()
	f.fakeHerdr(1)

	code := f.main(f.todo, "--dry-run", "--plain")

	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
	if want := "reviewer ui requires .claude/skills/test-app/SKILL.md: found\n"; !strings.Contains(f.out.String(), want) {
		t.Fatalf("%q missing from:\n%s", want, f.out.String())
	}
}

func TestWatchdogProviderIsValidatedUnlessTheWatchdogIsOff(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "watchdog:\n  provider: nosuch\n")
	f.commit()

	_, err := f.preflight(f.todo, "--plain")
	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), "watchdog.provider") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestPreflightCreatesTheRunAndPrintsTheBanner(t *testing.T) {
	f := newFixture(t)
	f.commit()

	w, err := f.preflight(f.todo, "--plain")
	if err != nil {
		t.Fatal(err)
	}

	id, pid, ok := store.New(f.root).Current()
	if !ok || pid != 4242 || id != w.Loop.RunID || w.Gate.RunID != id {
		t.Fatalf("current=%q %d %v loop=%q gate=%q", id, pid, ok, w.Loop.RunID, w.Gate.RunID)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".r-loop", "runs", id, "config.resolved.yaml")); err != nil {
		t.Fatal(err)
	}
	exclude, _ := os.ReadFile(filepath.Join(f.root, ".git", "info", "exclude"))
	if !strings.Contains(string(exclude), ".r-loop/runs/") || !strings.Contains(string(exclude), ".r-loop/wt/") {
		t.Fatalf("exclude=%q", exclude)
	}
	for _, want := range []string{"face: plain\n", "implement  codex  gpt-5.6-sol  medium  4h  diff  ← default\n", "prompt plan: embedded\n", "prompt implement: embedded\n", "prompt milestone: embedded\n", "  fallback codex provider default provider default  ← default\n"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("%q missing from banner:\n%s", want, f.out.String())
		}
	}
}

func TestWireBuildsTheGateFixKindAndTheMilestoneBoundary(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "land:\n  gateTimeout: 12m\n")
	f.commit()

	w, err := f.preflight(f.todo, "--plain", "--model", "implement=gpt-x")
	if err != nil {
		t.Fatal(err)
	}

	g := w.Gate
	fix := g.FixKind
	if g.GateTimeout.String() != "12m0s" || fix.Name != "gatefix" || fix.Prompt != "gatefix" || fix.Check != "diff" || g.Runner == nil || g.FixRounds != 1 {
		t.Fatalf("gate=%+v", g)
	}
	if fix.Row.Provider != "codex" || fix.Row.Model != "gpt-x" || fix.Row.Effort != "medium" || fix.Row.Timeout.String() != "4h0m0s" || fix.Row.Rounds != 1 || len(fix.Row.Reviewers) != 2 || fix.Row.Reviewers[0].Provider != "claude" || fix.Row.Reviewers[1].ID() != "ui" {
		t.Fatalf("fix row=%+v", fix.Row)
	}
	if b := g.Boundary; b == nil || b.Kind.Name != "milestone" || b.Kind.Check != "report" || b.Kind.Row.Provider != "claude" || b.RunID != w.Loop.RunID || b.Topic != "topic" {
		t.Fatalf("boundary=%+v", g.Boundary)
	}
	if len(w.Loop.Kinds) != 2 || w.Loop.Kinds[1].Row.Model != "gpt-x" || w.Loop.Lander != g {
		t.Fatalf("loop kinds=%+v", w.Loop.Kinds)
	}
	args, err := w.Loop.Sessions.Resolve("codex", "gpt-x", "high", "", "")
	if err != nil || args.Kind != "codex" || strings.Join(args.Args, " ") != "-c model=gpt-x -c model_reasoning_effort=high" {
		t.Fatalf("args=%+v err=%v", args, err)
	}
}

func TestDryRunPrintsBannerWithOverridesAndTheRunList(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "steps:\n  implement:\n    fallback:\n")
	f.commit()
	f.fakeHerdr(1)

	code := f.main(f.todo, "--dry-run", "--plain", "--provider", "implement=claude", "--effort", "implement=high")

	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
	out := f.out.String()
	for _, want := range []string{
		"face: plain\n",
		"implement  claude  gpt-5.6-sol  high  4h  diff  ← provider flag:--provider model default effort flag:--effort\n",
		"override: implement provider claude (flag) replaces codex (default)\n",
		"override: implement effort high (flag) replaces medium (default)\n",
		"gatefix claude gpt-5.6-sol high  ← provider flag:--provider model default effort flag:--effort\n",
		"prompt implement: embedded\n",
		"prompt review-ui: embedded\n",
		"reviewer ui requires .claude/skills/test-app/SKILL.md: missing, reviewer skipped\n",
		"watchdog: claude opus high allow []  ← default\n",
		"phase 2  PlanReader: phases and milestones  plan (review ×2) → implement (review ×3) → land\n",
		"phase 31  Unattended mode  plan (review ×2) → implement (review ×3) → land\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing from:\n%s", want, out)
		}
	}
	if strings.Contains(out, "phase 1  ") {
		t.Fatalf("ticked phase 1 listed:\n%s", out)
	}
	if f.herdrCalled() {
		t.Fatal("dry run contacted herdr")
	}
	if _, err := os.Stat(filepath.Join(f.root, ".r-loop", "runs")); !os.IsNotExist(err) {
		t.Fatalf("dry run made a run directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".r-loop", "wt")); !os.IsNotExist(err) {
		t.Fatalf("dry run made a worktree: %v", err)
	}
}

func runList(out string) []string {
	var phases []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "phase ") && strings.HasSuffix(line, "→ land") {
			phases = append(phases, strings.Fields(line)[1])
		}
	}
	return phases
}

func TestDryRunListUnderFrom(t *testing.T) {
	f := newFixture(t)
	f.commit()

	code := f.main(f.todo, "--dry-run", "--plain", "--from", "29")

	if got := strings.Join(runList(f.out.String()), ","); code != 0 || got != "29,30,31" {
		t.Fatalf("code=%d list=%s stderr=%q", code, got, f.err.String())
	}
}

func TestDryRunListUnderPhases(t *testing.T) {
	f := newFixture(t)
	f.commit()

	code := f.main(f.todo, "--dry-run", "--plain", "--phases", "12,3")

	if got := strings.Join(runList(f.out.String()), ","); code != 0 || got != "3,12" {
		t.Fatalf("code=%d list=%s stderr=%q", code, got, f.err.String())
	}
}

func TestTickedPhaseInPhasesExits2(t *testing.T) {
	f := newFixture(t)
	f.commit()

	code := f.main(f.todo, "--dry-run", "--plain", "--phases", "1,2")

	if code != 2 || !strings.Contains(f.err.String(), "phase 1 is ticked or absent") {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
}

func TestUsageErrorsExit2WithOneLine(t *testing.T) {
	for _, args := range [][]string{
		{"--bogus"},
		{},
		{"a.md", "b.md"},
		{"docs/topic/todo.md", "--from", "x"},
		{"docs/topic/todo.md", "--provider", "implement"},
		{"docs/topic/missing.md", "--dry-run"},
	} {
		f := newFixture(t)
		f.commit()

		code := f.main(args...)

		if code != 2 || strings.Count(f.err.String(), "\n") != 1 {
			t.Fatalf("args=%v code=%d stderr=%q", args, code, f.err.String())
		}
	}
}

func TestParseArgsReadsEveryFlag(t *testing.T) {
	opts, err := ParseArgs([]string{"--from", "3", "todo.md", "--phases", "4,5", "--provider", "plan=codex", "--provider", "implement=claude",
		"--model", "plan=o3", "--effort", "plan=low", "--unattended", "--plain", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}

	if opts.Todo != "todo.md" || opts.From != 3 || len(opts.Phases) != 2 || opts.Phases[1] != 5 || len(opts.Overrides) != 4 ||
		!opts.Unattended || !opts.Plain || !opts.DryRun {
		t.Fatalf("opts=%+v", opts)
	}
	if o := opts.Overrides[1]; o.Key != "provider" || o.Step != "implement" || o.Value != "claude" {
		t.Fatalf("override=%+v", o)
	}
}

func TestBannerNamesTheReviewAndFixPromptSources(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/prompts/review.md", "{{.Round}} review {{template \"sentinel\" .}}")
	f.commit()

	code := f.main(f.todo, "--dry-run", "--plain")

	for _, want := range []string{"prompt review: " + filepath.Join(f.root, ".r-loop", "prompts", "review.md") + "\n", "prompt fix: embedded\n"} {
		if code != 0 || !strings.Contains(f.out.String(), want) {
			t.Fatalf("code=%d %q missing from:\n%s%s", code, want, f.out.String(), f.err.String())
		}
	}
}

func TestMalformedReviewPromptIsRefusedWithExit2(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/prompts/review.md", "{{.Round")
	f.commit()

	code := f.main(f.todo, "--dry-run", "--plain")

	if code != 2 || !strings.Contains(f.err.String(), "review") {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
}

func TestGateFixReviewersAreValidatedWhenImplementIsNotInThePipeline(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "pipeline:\n  - plan\nproviders:\n  bare:\n    kind: bare\n    doneSignal: sentinel\nsteps:\n  implement:\n    reviewers:\n      - bare\n")
	f.commit()

	_, err := f.preflight(f.todo, "--plain")

	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), "provider bare has no review command") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestWithoutATerminalOnStdoutTheFaceIsPlainEvenWithoutPlainFlag(t *testing.T) {
	f := newFixture(t)
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	f.env.Stdout = null

	w, err := Wire(Options{Todo: f.todo}, f.env)

	if err != nil || w.TUI != nil || w.Face != core.Face(w.Plain) {
		t.Fatalf("err %v tui %v face %T", err, w.TUI, w.Face)
	}
}

func TestTheBannerNamesTheTUIFace(t *testing.T) {
	f := newFixture(t)
	w, err := Wire(Options{Todo: f.todo, Plain: true}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	w.TUI = &tui.Face{}
	var out bytes.Buffer

	w.banner(&out, nil)

	if !strings.HasPrefix(out.String(), "face: tui\n") {
		t.Fatalf("banner:\n%s", out.String())
	}
}

func TestTheTUIIsChosenOnlyWithATerminalOnStdinAndStdoutAndNoPlainFlag(t *testing.T) {
	for _, tc := range []struct {
		plain, stdin, stdout, want bool
	}{
		{false, true, true, true},
		{true, true, true, false},
		{false, false, true, false},
		{false, true, false, false},
	} {
		if got := useTUI(tc.plain, tc.stdin, tc.stdout); got != tc.want {
			t.Errorf("plain=%v stdin=%v stdout=%v: got %v", tc.plain, tc.stdin, tc.stdout, got)
		}
	}
}

func TestAnUncommittedIssuesFileIsRefusedWithACommitHint(t *testing.T) {
	f := newFixture(t)
	f.commit()
	f.fakeHerdr(0)
	f.write("issues-polka-2026-08-18.md", "- [ ] [#1] one\n      - a criterion\n")
	f.write("issues-polka-2026-08-18-notes.md", "# Notes\n")

	_, err := f.preflight(filepath.Join(f.root, "issues-polka-2026-08-18.md"), "--plain")

	if code := exitCode(t, err); code != 4 || !strings.Contains(err.Error(), "; commit issues-polka-2026-08-18-notes.md and issues-polka-2026-08-18.md first") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestADirtyTreeBeyondThePlanGetsNoCommitHint(t *testing.T) {
	f := newFixture(t)
	f.commit()
	f.fakeHerdr(0)
	f.write("issues.md", "- [ ] [#1] one\n")
	f.write("notes.txt", "scratch")

	_, err := f.preflight(filepath.Join(f.root, "issues.md"), "--plain")

	if code := exitCode(t, err); code != 4 || strings.Contains(err.Error(), "first") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestDryRunOfAnIssuesFileWarnsAboutItemsWithoutCriteria(t *testing.T) {
	f := newFixture(t)
	f.write("issues.md", "- [ ] [#1] with criteria\n      - it works\n\n- [ ] [#2] bare title\n")
	f.commit()

	code := f.main(filepath.Join(f.root, "issues.md"), "--dry-run", "--plain")

	out := f.out.String()
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, f.err.String())
	}
	if !strings.Contains(out, "warning: phase 2 has no acceptance criteria") || strings.Contains(out, "warning: phase 1 ") {
		t.Errorf("out:\n%s", out)
	}
	if !strings.Contains(out, "gate  claude  sonnet") || !strings.Contains(out, "red at base") {
		t.Errorf("no gate row or item gate in the dry run:\n%s", out)
	}
}

func TestDryRunNamesAnOpenResolveFirstEntryThatBlocksTheRunList(t *testing.T) {
	f := newFixture(t)
	f.write("docs/topic/todo.md", "# Plan\n\n## Resolve first\n- [ ] **Measure it** — how fast?\n      Owner: me. Blocks: Phase 2.\n\n### Phase 1 — One\n- [ ] a\n\n### Phase 2 — Two\n- [ ] b\n")
	f.commit()

	code := f.main(f.todo, "--dry-run", "--plain", "--phases", "1")
	quiet := f.out.String()
	f.out.Reset()
	code2 := f.main(f.todo, "--dry-run", "--plain")

	if code != 0 || code2 != 0 {
		t.Fatalf("exit %d, %d: %s", code, code2, f.err.String())
	}
	if strings.Contains(quiet, "Resolve first") {
		t.Errorf("entry named for a run it does not block:\n%s", quiet)
	}
	if want := `open ## Resolve first: "Measure it" (unclassified) blocks phase 2 — the watchdog will walk it`; !strings.Contains(f.out.String(), want) {
		t.Errorf("out:\n%s", f.out.String())
	}
}

func TestNoWatchdogIsAUsageError(t *testing.T) {
	if _, err := ParseArgs([]string{"todo.md", "--no-watchdog"}); err == nil {
		t.Fatal("--no-watchdog accepted")
	}
}

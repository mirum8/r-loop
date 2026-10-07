package app

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/face/tui"
	"r-loop/internal/store"
)

type failingStore struct {
	core.Store
	fail func(core.Record) bool
}

func (s failingStore) Append(id string, rec core.Record) error {
	if s.fail(rec) {
		return errors.New("disk full")
	}
	return s.Store.Append(id, rec)
}

func TestRunNamesAFailedRunRecordOnStderrAndInTheFace(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	w, err := f.preflight(f.todo, "--plain")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, newSim())
	w.records = &core.RecordGuard{Store: failingStore{Store: w.Store, fail: func(rec core.Record) bool { return rec.Kind == core.RecordRun }}}
	w.Loop.Store = w.records
	w.Loop.Sessions.Store = w.records
	w.Gate.Store = w.records
	code := w.Execute(core.RunOptions{Phases: []string{"1"}})
	if code != 2 || !strings.Contains(f.err.String(), "r-loop: record: disk full") || !strings.Contains(f.out.String(), "!  record: disk full") {
		t.Fatalf("code=%d stderr=%q out=%q", code, f.err, f.out)
	}
}

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
	return newFixtureIn(t, root)
}

func newFixtureIn(t *testing.T, root string) *fixture {
	t.Helper()
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
	fakeProviders(t)
	return f
}

func fakeProviders(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	scripts := map[string]string{
		"claude": "#!/bin/sh\nexit 0\n",
		"codex": "#!/bin/sh\nif [ \"$1 $2\" = \"debug models\" ]; then\n  echo x >> \"$0.catalog-calls\"\n" +
			"  echo '{\"models\":[{\"slug\":\"gpt-6-sol\",\"visibility\":\"list\"},{\"slug\":\"gpt-6.1-sol\",\"visibility\":\"list\"},{\"slug\":\"gpt-6-luna\",\"visibility\":\"list\"}]}'\nfi\n" +
			"exit 0\n",
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tools := t.TempDir()
	semgrep := "#!/bin/sh\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = --output ]; then printf '{\"runs\":[]}' > \"$2\"; fi\n  shift\ndone\n"
	if err := os.WriteFile(filepath.Join(tools, "semgrep"), []byte(semgrep), 0o755); err != nil {
		t.Fatal(err)
	}
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", bin+sep+tools+sep+os.Getenv("PATH"))
}

func TestAMissingProviderBinaryExits127NamingTheProviderBinaryAndField(t *testing.T) {
	provider := "providers:\n  ghost:\n    kind: rloop-no-such-binary\n    doneSignal: sentinel\n    ask: mcp\n    review: ghost review\n"
	for field, cfg := range map[string]string{
		"watchdog.provider":    "watchdog:\n  provider: ghost\n",
		"intake.provider":      "intake:\n  provider: ghost\n",
		"steps.plan.provider":  "steps:\n  plan:\n    provider: ghost\n",
		"steps.plan.fallback":  "steps:\n  plan:\n    fallback:\n      provider: ghost\n      model: m\n      effort: e\n",
		"steps.plan.reviewers": "steps:\n  plan:\n    reviewers:\n      - provider: ghost\n        model: m\n        effort: e\n",
		"land.fix.provider":    "land:\n  fix:\n    provider: ghost\n    model: m\n    effort: e\n",
	} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			f.write(".r-loop/config.yaml", provider+cfg)
			f.commit()
			_, err := f.preflight(f.todo, "--plain")
			if code := exitCode(t, err); code != 127 || !strings.Contains(err.Error(), field+": provider ghost binary rloop-no-such-binary not found on PATH") || f.herdrCalled() {
				t.Fatalf("code=%d err=%v herdr called=%v", code, err, f.herdrCalled())
			}
		})
	}
}

func TestAMissingProviderBinaryIsNotCheckedInDryRun(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  ghost:\n    kind: rloop-no-such-binary\n    doneSignal: sentinel\n    ask: mcp\nwatchdog:\n  provider: ghost\n")
	f.commit()
	f.fakeHerdr(1)
	if code := f.main(f.todo, "--dry-run", "--plain"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
}

func TestARunWithEveryProviderBinaryOnPathPassesPreflight(t *testing.T) {
	f := newFixture(t)
	f.commit()
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	for _, name := range []string{"claude", "codex"} {
		path, err := exec.LookPath(name)
		if err != nil || filepath.Dir(path) != bin {
			t.Fatalf("%s path=%q err=%v, want directory %q", name, path, err, bin)
		}
	}
	w, err := f.preflight(f.todo, "--plain")
	if err != nil || w.Loop.RunID == "" || !f.herdrCalled() {
		t.Fatalf("w=%v err=%v herdr called=%v", w, err, f.herdrCalled())
	}
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
	st := store.New(f.root)
	lock, err := st.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release("", 0)
	if err := st.SetCurrent("20260918-101500", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	lock.Publish()

	_, err = f.preflight(f.todo, "--plain")

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

func TestImplementReviewerWithoutReviewCommandIsRefusedWithExit2(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  codex:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\nsteps:\n  implement:\n    reviewers:\n      - provider: codex\n        model: gpt-5.6-sol\n        effort: medium\n")
	f.commit()

	_, err := f.preflight(f.todo, "--plain")

	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), "steps.implement.reviewers: provider codex has no review command") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestPreflightPassesAReviewerWhoseProviderHasOnlyAReviewExec(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  bare:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\n    models: debug models\n    reviewExec: codex review --uncommitted\nsteps:\n  implement:\n    reviewers:\n      - provider: bare\n        model: gpt-6-sol\n        effort: e\n")
	f.commit()
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1 $2\" = \"debug models\" ]; then\n  echo '{\"models\":[{\"slug\":\"gpt-6-sol\",\"visibility\":\"list\"}]}'\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := f.preflight(f.todo, "--plain"); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightPassesASecurityReviewerOnAnyProviderAndInstallsNothing(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  bare:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\n    models: debug models\nsteps:\n  implement:\n    reviewers:\n      - name: security\n        provider: bare\n        model: gpt-6-sol\n        effort: e\n        prompt: review-security\n")
	f.commit()
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"$*\" >> \"$0.calls\"\nif [ \"$1 $2\" = \"debug models\" ]; then\n  echo '{\"models\":[{\"slug\":\"gpt-6-sol\",\"visibility\":\"list\"}]}'\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := f.preflight(f.todo, "--plain"); err != nil {
		t.Fatal(err)
	}

	calls, err := os.ReadFile(filepath.Join(bin, "codex.calls"))
	if err != nil || strings.Contains(string(calls), "plugin") {
		t.Fatalf("calls %q, err %v", calls, err)
	}
}

func TestPlanReviewerNeedsNoReviewCommand(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  codex:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\nsteps:\n  implement:\n    reviewers:\n      - provider: claude\n        model: opus\n        effort: medium\n")
	f.commit()
	f.fakeHerdr(1)

	code := f.main(f.todo, "--dry-run", "--plain")

	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
	if want := "prompt review-plan: embedded\n"; !strings.Contains(f.out.String(), want) {
		t.Fatalf("%q missing from:\n%s", want, f.out.String())
	}
}

func TestUIReviewerNeedsNoReviewCommandAndReportsItsSkill(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  bare:\n    kind: bare\n    doneSignal: sentinel\n    ask: mcp\nsteps:\n  implement:\n    reviewers:\n      - provider: claude\n        model: opus\n        effort: medium\n      - name: ui\n        provider: bare\n        model: m\n        effort: e\n        prompt: review-ui\n        requires: .claude/skills/test-app/SKILL.md\n")
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
	for _, want := range []string{"face: plain\n", "implement  claude  opus  medium  4h  diff  ← default\n", "prompt plan: embedded\n", "prompt implement: embedded\n", "prompt milestone: embedded\n", "  fallback codex sol medium  ← default\n"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("%q missing from banner:\n%s", want, f.out.String())
		}
	}
}

func TestWireBuildsTheGateFixKindAndTheMilestoneBoundary(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "land:\n  gateTimeout: 12m\n")
	f.commit()

	w, err := f.preflight(f.todo, "--plain", "--provider", "implement=codex", "--model", "implement=gpt-x")
	if err != nil {
		t.Fatal(err)
	}

	g := w.Gate
	fix := g.FixKind
	if g.GateTimeout.String() != "12m0s" || fix.Name != "gatefix" || fix.Prompt != "gatefix" || fix.Check != "diff" || g.Runner == nil || g.FixRounds != 1 {
		t.Fatalf("gate=%+v", g)
	}
	if fix.Row.Provider != "codex" || fix.Row.Model != "gpt-x" || fix.Row.Effort != "medium" || fix.Row.Timeout.String() != "4h0m0s" || fix.Row.Rounds != 1 || len(fix.Row.Reviewers) != 3 || fix.Row.Reviewers[0].Provider != "codex" || fix.Row.Reviewers[1].ID() != "ui" || fix.Row.Reviewers[2].ID() != "security" {
		t.Fatalf("fix row=%+v", fix.Row)
	}
	if b := g.Boundary; b == nil || b.Kind.Name != "milestone" || b.Kind.Check != "report" || b.Kind.Row.Provider != "claude" || b.RunID != w.Loop.RunID || b.Topic != "topic" {
		t.Fatalf("boundary=%+v", g.Boundary)
	}
	if len(w.Loop.Kinds) != 2 || w.Loop.Kinds[1].Row.Model != "gpt-x" || w.Loop.Lander != g {
		t.Fatalf("loop kinds=%+v", w.Loop.Kinds)
	}
	args, err := w.Loop.Sessions.Resolve("codex", "gpt-x", "high", "", "", "")
	if err != nil || args.Kind != "codex" || strings.Join(args.Args, " ") != "-c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true -c model=gpt-x -c model_reasoning_effort=high" {
		t.Fatalf("args=%+v err=%v", args, err)
	}
}

func TestACodexModelAliasResolvesToTheNewestCatalogVersionOncePerRun(t *testing.T) {
	f := newFixture(t)
	f.commit()

	w, err := f.preflight(f.todo, "--plain", "--provider", "implement=codex", "--model", "implement=sol")
	if err != nil {
		t.Fatal(err)
	}

	args, err := w.Loop.Sessions.Resolve("codex", "sol", "high", "", "", "")
	if err != nil || !slices.Contains(args.Args, "model=gpt-6.1-sol") {
		t.Fatalf("args=%+v err=%v", args, err)
	}
	if out := f.out.String(); !strings.Contains(out, "model: codex sol → gpt-6.1-sol") {
		t.Errorf("banner does not name the resolution:\n%s", out)
	}
	codex, _ := exec.LookPath("codex")
	calls, err := os.ReadFile(codex + ".catalog-calls")
	if err != nil || strings.Count(string(calls), "x") != 1 {
		t.Errorf("catalog calls %q, err %v; want one per run", calls, err)
	}
}

func TestACodexModelAliasWithoutACatalogMatchExitsTwoNamingTheField(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "watchdog:\n  provider: codex\n  model: nosuch\n  effort: medium\n")
	f.commit()

	_, err := f.preflight(f.todo, "--plain")

	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), `watchdog.provider: codex has no model matching "nosuch"`) || f.herdrCalled() {
		t.Fatalf("code=%d err=%v herdr called=%v", code, err, f.herdrCalled())
	}
}

func TestZeroGateTimeoutExitsTwoNamingTheFileLineAndKey(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "land:\n  gateTimeout: 0s\n")
	f.commit()

	_, err := f.preflight(f.todo, "--plain")
	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), `.r-loop/config.yaml:2: land.gateTimeout: "0s" is not a positive duration`) {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestNullGateTimeoutWiresTheDefaultIntoTheGate(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "land:\n  gateTimeout:\n")
	f.commit()

	w, err := f.preflight(f.todo, "--plain")
	if err != nil {
		t.Fatal(err)
	}
	if w.Gate.GateTimeout != 30*time.Minute || w.Probe.Timeout != 30*time.Minute {
		t.Errorf("gate timeout = %s, probe timeout = %s", w.Gate.GateTimeout, w.Probe.Timeout)
	}
	if !strings.Contains(f.out.String(), "land gateTimeout 30m  ← default\n") {
		t.Errorf("banner:\n%s", f.out.String())
	}
}

func TestDryRunPrintsBannerWithOverridesAndTheRunList(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "steps:\n  implement:\n    fallback:\n")
	f.commit()
	f.fakeHerdr(1)

	code := f.main(f.todo, "--dry-run", "--plain", "--provider", "implement=codex", "--effort", "implement=high")

	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
	out := f.out.String()
	for _, want := range []string{
		"face: plain\n",
		"implement  codex  opus  high  4h  diff  ← provider flag:--provider model default effort flag:--effort\n",
		"override: implement provider codex (flag) replaces claude (default)\n",
		"override: implement effort high (flag) replaces medium (default)\n",
		"gatefix codex opus high  ← provider flag:--provider model default effort flag:--effort\n",
		"prompt implement: embedded\n",
		"prompt review-ui: embedded\n",
		"reviewer ui requires .claude/skills/test-app/SKILL.md: missing, reviewer skipped\n",
		"watchdog: claude opus medium allow []  ← default\n",
		"intake: claude sonnet medium  ← default\n",
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
		{"a.md", "--phases"},
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

func TestParseArgsTakesPhaseLabels(t *testing.T) {
	opts, err := ParseArgs([]string{"todo.md", "--from", "10A", "--phases", "10c, 2"})
	if err != nil {
		t.Fatal(err)
	}

	if opts.From != "10a" || strings.Join(opts.Phases, ",") != "10c,2" {
		t.Fatalf("opts=%+v", opts)
	}
}

func TestParseArgsRejectsANonLabelPhase(t *testing.T) {
	for _, args := range [][]string{{"todo.md", "--from", "x"}, {"todo.md", "--phases", "1,10ab"}, {"todo.md", "--from", "0"}} {
		if _, err := ParseArgs(args); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
}

func TestParseArgsReadsEveryFlag(t *testing.T) {
	opts, err := ParseArgs([]string{"--from", "3", "todo.md", "--phases", "4,5", "--provider", "plan=codex", "--provider", "implement=claude",
		"--model", "plan=o3", "--effort", "plan=low", "--unattended", "--plain", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}

	if opts.Todo != "todo.md" || opts.From != "3" || len(opts.Phases) != 2 || opts.Phases[1] != "5" || len(opts.Overrides) != 4 ||
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
	f.write(".r-loop/config.yaml", "pipeline:\n  - plan\nproviders:\n  bare:\n    kind: bare\n    doneSignal: sentinel\nsteps:\n  implement:\n    reviewers:\n      - provider: bare\n        model: m\n        effort: e\n")
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

func TestDryRunPrintsNoControlBytesFromTitles(t *testing.T) {
	f := newFixture(t)
	f.write("issues.md", "- [ ] [#1] a | b \x1b[2J\x1b]0;pwned\x07 c\n      - it works\n")
	f.commit()

	code := f.main(filepath.Join(f.root, "issues.md"), "--dry-run", "--plain")

	out := f.out.String()
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, f.err.String())
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("control bytes in:\n%q", out)
	}
	if !strings.Contains(out, "| 1 | [#1] a \\| b [2J]0;pwned c |\n") {
		t.Errorf("table row malformed:\n%s", out)
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
	if strings.Contains(quiet, "Measure it") {
		t.Errorf("entry named for a run it does not block:\n%s", quiet)
	}
	if want := `open ## Resolve first: "Measure it" (unclassified) blocks phase 2 — the watchdog will walk it`; !strings.Contains(f.out.String(), want) {
		t.Errorf("out:\n%s", f.out.String())
	}
}

func TestAPhaseWithNoChecklistExits2(t *testing.T) {
	f := newFixture(t)
	f.write("docs/topic/todo.md", "# Plan\n\n### Phase 1 — One\n- [ ] a\n\n### Phase 2 — Two\n**Done when:** `go test ./...`\n")
	f.commit()

	code := f.main(f.todo, "--dry-run", "--plain")

	if code != 2 || !strings.Contains(f.err.String(), "Phase 2 — Two: no checklist items") {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
}

func TestDryRunPrintsThePlanCheckNotes(t *testing.T) {
	f := newFixture(t)
	f.write("docs/topic/todo.md", "# Plan\n\n### Phase 1 — One\n**Implements:** Story\n**Depends on:** —\n- [ ] a\n**Done when:** `go test ./...`\n\n### Phase 2 — Two\n**Implements:** Story\n**Depends on:** Phase 1\n- [ ] b\n")
	f.commit()

	code := f.main(f.todo, "--dry-run", "--plain")

	out := f.out.String()
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, f.err.String())
	}
	if want := "Plan check: 1 note\n- Phase 2 — Two: no 'Done when' check\n"; !strings.Contains(out, want) {
		t.Errorf("want %q in:\n%s", want, out)
	}
	if got := strings.Join(runList(out), ","); got != "1,2" {
		t.Errorf("run list = %s", got)
	}
}

func TestNoWatchdogIsAUsageError(t *testing.T) {
	if _, err := ParseArgs([]string{"todo.md", "--no-watchdog"}); err == nil {
		t.Fatal("--no-watchdog accepted")
	}
}

func TestCreateConfigWritesTheDefaultsToTheMachineFile(t *testing.T) {
	f := newFixture(t)

	code := f.main("--create-config")

	path := filepath.Join(f.env.Home, ".config", "r-loop", "config.yaml")
	if code != 0 || f.out.String() != "wrote "+path+"\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, f.out.String(), f.err.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestCreateConfigOverAnExistingFileResetsItsModels(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.env.Home, ".config", "r-loop", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("steps:\n  plan:\n    model: fable\n    timeout: 2h\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code := f.main("--create-config")

	if code != 0 || f.out.String() != path+": steps.plan: fable → claude opus high\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, f.out.String(), f.err.String())
	}
	data, _ := os.ReadFile(path)
	if want := "steps:\n  plan:\n    model: opus\n    timeout: 2h\n    provider: claude\n    effort: high\n"; string(data) != want {
		t.Errorf("file = %q", data)
	}
}

func TestCreateConfigOverAFileAtTheDefaultsSaysSo(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.env.Home, ".config", "r-loop", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("pipeline:\n  - plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code := f.main("--create-config")

	if code != 0 || f.out.String() != path+": models already at the defaults\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, f.out.String(), f.err.String())
	}
}

func TestMigrateConfigRewritesTheMachineAndProjectFiles(t *testing.T) {
	f := newFixture(t)
	home := filepath.Join(f.env.Home, ".config", "r-loop", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home, []byte("steps:\n  plan:\n    fallback: codex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.write(".r-loop/config.yaml", "steps:\n  implement:\n    reviewers:\n      - claude\n")

	code := f.main("--migrate-config")

	project := filepath.Join(f.root, ".r-loop", "config.yaml")
	want := home + ": steps.plan.fallback: codex → codex sol high\n" +
		project + ": steps.implement.reviewers.0: claude → claude opus high\n"
	if code != 0 || f.out.String() != want {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, f.out.String(), f.err.String())
	}
	f.out.Reset()
	if code := f.main("--migrate-config"); code != 0 || !strings.Contains(f.out.String(), home+": nothing to migrate\n") {
		t.Fatalf("second run code=%d stdout=%q", code, f.out.String())
	}
}

func TestMigrateConfigExits1OnAProviderItCannotFill(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "steps:\n  plan:\n    fallback: gemini\n")

	code := f.main("--migrate-config")

	if code != 1 || !strings.Contains(f.err.String(), "steps.plan.fallback: gemini has no built-in model and effort, set them by hand") {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
}

func TestMigrateConfigWithNoFiles(t *testing.T) {
	f := newFixture(t)

	if code := f.main("--migrate-config"); code != 0 || f.out.String() != "no config file to migrate\n" {
		t.Fatalf("code=%d stdout=%q", code, f.out.String())
	}
}

func toolchainPath(t *testing.T, tools ...string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	scripts := map[string]string{"git": "#!/bin/sh\nexec " + realGit + " \"$@\"\n"}
	for _, tool := range tools {
		scripts[tool] = "#!/bin/sh\nexit 0\n"
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	providers := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	t.Setenv("PATH", bin+string(os.PathListSeparator)+providers)
}

func (f *fixture) markers(rels ...string) {
	f.t.Helper()
	for _, rel := range rels {
		f.write(rel, "")
	}
	f.commit()
}

func TestPreflightPrintsTheStaticLine(t *testing.T) {
	for _, tc := range []struct {
		name     string
		markers  []string
		tools    []string
		expected string
	}{
		{"go with semgrep", []string{"go.mod"}, []string{"go", "semgrep"}, "static: go, semgrep\n"},
		{"go without semgrep", []string{"go.mod"}, []string{"go"}, "static: go (semgrep not installed)\n"},
		{"every language", []string{"go.mod", "svc/pom.xml", "app/build.gradle"}, []string{"go", "mvn", "gradle", "java"}, "static: go, maven, gradle (semgrep not installed)\n"},
		{"no language with semgrep", nil, []string{"semgrep"}, "static: semgrep\n"},
		{"nothing", nil, nil, "static: none (semgrep not installed)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// given
			f := newFixture(t)
			f.markers(tc.markers...)
			toolchainPath(t, tc.tools...)

			// when
			_, err := f.preflight(f.todo, "--plain")

			// then
			if err != nil || !strings.Contains(f.out.String(), tc.expected) {
				t.Fatalf("err %v, out %q, expected %q", err, f.out, tc.expected)
			}
		})
	}
}

func TestPreflightExitsTwoNamingAMissingToolchain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		markers  []string
		tools    []string
		expected string
	}{
		{"go", []string{"go.mod"}, nil, "analyze: go module . needs go, not found on PATH"},
		{"mvn", []string{"svc/pom.xml"}, []string{"java"}, "analyze: maven module svc needs mvn, not found on PATH"},
		{"maven java", []string{"svc/pom.xml"}, []string{"mvn"}, "analyze: maven module svc needs java, not found on PATH"},
		{"gradle", []string{"build.gradle"}, []string{"java"}, "analyze: gradle module . needs gradle, not found on PATH"},
		{"gradle java", []string{"build.gradle"}, []string{"gradle"}, "analyze: gradle module . needs java, not found on PATH"},
		{"gradlew", []string{"build.gradle", "settings.gradle", "gradlew"}, []string{"java"}, "analyze: gradle module . needs gradlew, which is not executable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// given
			f := newFixture(t)
			f.markers(tc.markers...)
			toolchainPath(t, tc.tools...)

			// when
			_, err := f.preflight(f.todo, "--plain")

			// then
			if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), tc.expected) || f.herdrCalled() {
				t.Fatalf("code=%d err=%v herdr called=%v, expected %q", code, err, f.herdrCalled(), tc.expected)
			}
		})
	}
}

func TestPreflightChecksNoToolchainWhenAnalyzeIsOff(t *testing.T) {
	// given
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "analyze:\n  enabled: false\n")
	f.markers("go.mod")
	toolchainPath(t)

	// when
	_, err := f.preflight(f.todo, "--plain")

	// then
	if err != nil || strings.Contains(f.out.String(), "static:") {
		t.Fatalf("err %v, out %q", err, f.out)
	}
}

func TestAMissingToolchainIsNotCheckedInDryRun(t *testing.T) {
	// given
	f := newFixture(t)
	f.markers("go.mod")
	toolchainPath(t)
	f.fakeHerdr(1)

	// when
	code := f.main(f.todo, "--dry-run", "--plain")

	// then
	if code != 0 || !strings.Contains(f.out.String(), "static: go (semgrep not installed)\n") {
		t.Fatalf("code=%d out=%q stderr=%q", code, f.out, f.err)
	}
}

func reviewFinds(st core.RunState, reviewer string) []map[string]string {
	var out []map[string]string
	for _, e := range stepEvents(st, "review-find") {
		if e.Fields["reviewer"] == reviewer {
			out = append(out, e.Fields)
		}
	}
	return out
}

func TestAWiredRunReviewsWithTheAnalyzerWhenAnalyzeIsEnabled(t *testing.T) {
	// given
	f := newResumeFixture(t, reviewConfig)

	// when
	id, code := f.firstRun(newSim(), "--phases", "1")

	// then
	expected := []map[string]string{{"step": "implement", "round": "1", "reviewer": "static", "state": "ok", "findings": "0", "command": "analyze semgrep"}}
	if actual := reviewFinds(f.load(id), "static"); !reflect.DeepEqual(actual, expected) {
		t.Fatalf("static finds = %+v\n%s", actual, f.out)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
}

func TestAWiredRunReviewsWithoutTheAnalyzerWhenAnalyzeIsOff(t *testing.T) {
	// given
	f := newResumeFixture(t, reviewConfig+"analyze:\n  enabled: false\n")

	// when
	id, code := f.firstRun(newSim(), "--phases", "1")

	// then
	st := f.load(id)
	if actual := reviewFinds(st, "static"); len(actual) != 0 {
		t.Fatalf("static finds = %+v", actual)
	}
	if actual := reviewFinds(st, "claude"); len(actual) != 1 || actual[0]["state"] != "ok" {
		t.Fatalf("claude finds = %+v\n%s", actual, f.out)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
}

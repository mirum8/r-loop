package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type roundRepo struct {
	fakeRepo
	diffs map[string][]string
}

func (r *roundRepo) TreeDiff(from, to string) ([]string, error) {
	r.record("Repo.TreeDiff %s %s", from, to)
	return r.diffs[from+" "+to], nil
}

type verdictFixture struct {
	dir, verdictPath string
	ctx              EvidenceContext
}

const goodVerdict = `{"findings":[
	{"id":"codex-r2-1","reviewer":"codex","title":"nil map","verdict":"real","severity":"P1","fixed":true,"files":["a.go"],"evidence":""},
	{"id":"codex-r2-2","reviewer":"codex","title":"leak","verdict":"not-real","severity":"P3","fixed":false,"files":[],"evidence":"b.go:12"},
	{"id":"claude-r2-1","reviewer":"claude","title":"rename","verdict":"out-of-scope","severity":"P4","fixed":false,"files":[],"evidence":""}]}`

func newVerdictFixture(t *testing.T, verdict string) verdictFixture {
	t.Helper()
	dir := t.TempDir()
	codex := filepath.Join(dir, "implement-findings-codex-r2.json")
	claude := filepath.Join(dir, "implement-findings-claude-r2.json")
	os.WriteFile(codex, []byte(`{"reviewer":"codex","findings":[
		{"id":"codex-r2-1","title":"nil map","detail":"d","files":["a.go"]},
		{"id":"codex-r2-2","title":"leak","detail":"d","files":["b.go"]}]}`), 0o644)
	os.WriteFile(claude, []byte(`{"reviewer":"claude","findings":[{"id":"claude-r2-1","title":"rename","detail":"d","files":["c.go"]}]}`), 0o644)
	verdictPath := filepath.Join(dir, "implement-verdict-r2.json")
	os.WriteFile(verdictPath, []byte(verdict), 0o644)
	repo := &roundRepo{fakeRepo: fakeRepo{Tree: "tree-now"}, diffs: map[string][]string{
		"tree-start tree-now": {"a.go", "b.go"},
		"tree-round tree-now": {"a.go"},
	}}
	return verdictFixture{dir: dir, verdictPath: verdictPath, ctx: EvidenceContext{
		Repo:          repo,
		Worktree:      "/wt",
		StartTree:     "tree-start",
		RoundTree:     "tree-round",
		VerdictPath:   verdictPath,
		FindingsFiles: []string{codex, claude},
		FS:            fstest.MapFS{"a.go": {}, "b.go": {}},
	}}
}

func TestVerdictCheckPassesOnATwoReviewerRound(t *testing.T) {
	f := newVerdictFixture(t, goodVerdict)

	ok, missing := runCheck(t, "verdict", f.ctx)

	if !ok || missing != "" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestVerdictCheckFails(t *testing.T) {
	for name, tc := range map[string]struct {
		from, to, missing string
	}{
		"finding with no verdict": {
			`,
	{"id":"claude-r2-1","reviewer":"claude","title":"rename","verdict":"out-of-scope","severity":"P4","fixed":false,"files":[],"evidence":""}]}`,
			`]}`,
			"no verdict for finding claude-r2-1",
		},
		"missing severity": {
			`"severity":"P3",`, ``,
			"implement-verdict-r2.json: entry codex-r2-2 has no severity",
		},
		"missing verdict": {
			`"verdict":"out-of-scope",`, ``,
			"implement-verdict-r2.json: entry claude-r2-1 has no verdict",
		},
		"not-real without evidence": {
			`"evidence":"b.go:12"`, `"evidence":""`,
			"not-real finding codex-r2-2 has no path:line evidence",
		},
		"not-real evidence without a line": {
			`"evidence":"b.go:12"`, `"evidence":"b.go"`,
			"not-real finding codex-r2-2 has no path:line evidence",
		},
		"not-real evidence on a path that does not exist": {
			`"evidence":"b.go:12"`, `"evidence":"gone.go:3"`,
			"evidence gone.go:3 of codex-r2-2 is not in the worktree",
		},
		"fixed entry whose file changed only before the round": {
			`"files":["a.go"]`, `"files":["a.go","b.go"]`,
			"fixed finding codex-r2-1: b.go did not change in this round",
		},
		"fixed entry with no files": {
			`"files":["a.go"]`, `"files":[]`,
			"fixed finding codex-r2-1 names no files",
		},
		"fixed entry that is not real P1 or P2": {
			`"severity":"P1"`, `"severity":"P3"`,
			"fixed finding codex-r2-1 is not real at P1 or P2",
		},
		"real P1 left unfixed": {
			`"fixed":true,"files":["a.go"]`, `"fixed":false,"files":[]`,
			"real P1 finding codex-r2-1 is not fixed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(goodVerdict, tc.from, tc.to, 1)
			if body == goodVerdict {
				t.Fatalf("fixture did not change")
			}
			f := newVerdictFixture(t, body)

			ok, missing := runCheck(t, "verdict", f.ctx)

			if ok || missing != tc.missing {
				t.Fatalf("ok = %v, missing = %q", ok, missing)
			}
		})
	}
}

func TestVerdictCheckFailsOnAVerdictForAnUnknownId(t *testing.T) {
	body := strings.Replace(goodVerdict, `]}`, `,
	{"id":"codex-r2-9","reviewer":"codex","title":"x","verdict":"out-of-scope","severity":"P4","fixed":false,"files":[],"evidence":""}]}`, 1)
	f := newVerdictFixture(t, body)

	ok, missing := runCheck(t, "verdict", f.ctx)

	if ok || missing != "verdict for unknown finding codex-r2-9" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestVerdictCheckFailsOnAnIdTwice(t *testing.T) {
	body := strings.Replace(goodVerdict, `]}`, `,
	{"id":"codex-r2-2","reviewer":"codex","title":"leak","verdict":"out-of-scope","severity":"P4","fixed":false,"files":[],"evidence":""}]}`, 1)
	f := newVerdictFixture(t, body)

	ok, missing := runCheck(t, "verdict", f.ctx)

	if ok || missing != "finding codex-r2-2 has two verdicts" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestVerdictCheckFailsWithNoVerdictFile(t *testing.T) {
	f := newVerdictFixture(t, goodVerdict)
	os.Remove(f.verdictPath)

	ok, missing := runCheck(t, "verdict", f.ctx)

	if ok || missing != "no verdict at implement-verdict-r2.json" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestReadVerdictRejectsUnknownValuesNamingTheEntry(t *testing.T) {
	for name, tc := range map[string]struct {
		from, to, err string
	}{
		"verdict":  {`"verdict":"not-real"`, `"verdict":"maybe"`, `entry codex-r2-2 has verdict "maybe"`},
		"severity": {`"severity":"P4"`, `"severity":"P5"`, `entry claude-r2-1 has severity "P5"`},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "v.json")
			os.WriteFile(path, []byte(strings.Replace(goodVerdict, tc.from, tc.to, 1)), 0o644)

			_, err := ReadVerdict(path)

			if err == nil || err.Error() != tc.err {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestReadVerdictParsesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.json")
	os.WriteFile(path, []byte(goodVerdict), 0o644)

	got, err := ReadVerdict(path)

	if err != nil || len(got.Findings) != 3 {
		t.Fatalf("got = %+v, err = %v", got, err)
	}
	want := VerdictEntry{ID: "codex-r2-1", Reviewer: "codex", Title: "nil map", Verdict: "real", Severity: "P1", Fixed: true, Files: []string{"a.go"}}
	if !reflect.DeepEqual(got.Findings[0], want) {
		t.Fatalf("first = %+v", got.Findings[0])
	}
}

func writeVerdict(t *testing.T, vars map[string]any, entries ...string) {
	t.Helper()
	body := `{"findings":[` + strings.Join(entries, ",") + `]}`
	if err := os.WriteFile(vars["VerdictPath"].(string), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sentinel := `{"outcome":"ok","reason":"","at":"2026-09-18T10:05:00Z"}`
	if err := os.WriteFile(vars["Sentinel"].(string), []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
}

func entry(id, verdict, severity string, fixed bool, evidence string) string {
	files := `[]`
	if fixed {
		files = `["a.go"]`
	}
	reviewer := strings.SplitN(id, "-r", 2)[0]
	return fmt.Sprintf(`{"id":%q,"reviewer":%q,"title":"t","verdict":%q,"severity":%q,"fixed":%t,"files":%s,"evidence":%q}`, id, reviewer, verdict, severity, fixed, files, evidence)
}

func TestFixHalfPromptsTheSameStepSessionWithTheRoundFiles(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"}, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 1) }
	r.repo.TreeChanges = nil
	reviewingAtFix := true
	r.onFix = func(vars map[string]any) {
		reviewingAtFix = r.worker.Reviewing.Load()
		r.repo.TreeChanges = []string{"a.go"}
		writeVerdict(t, vars, entry("claude-r1-1", "out-of-scope", "P3", false, ""), entry("codex-r1-1", "real", "P4", false, ""))
	}

	out := r.run()

	if out.State != StepOK || out.Warning != "" {
		t.Fatalf("outcome = %+v", out)
	}
	dir := filepath.Join(r.runDir, "phase-3")
	if len(r.fixes) != 1 || reviewingAtFix {
		t.Fatalf("fixes = %d, reviewing at fix = %v", len(r.fixes), reviewingAtFix)
	}
	fix := r.fixes[0]
	wantFiles := []FindingsFile{
		{Reviewer: "claude", Path: filepath.Join(dir, "implement-findings-claude-r1.json")},
		{Reviewer: "codex", Path: filepath.Join(dir, "implement-findings-codex-r1.json")},
	}
	if !reflect.DeepEqual(fix["FindingsFiles"], wantFiles) {
		t.Fatalf("FindingsFiles = %+v", fix["FindingsFiles"])
	}
	for k, v := range map[string]any{
		"VerdictPath": filepath.Join(dir, "implement-verdict-r1.json"),
		"Sentinel":    filepath.Join(dir, "implement-fix-r1-a1.sentinel"),
		"Round":       1,
		"Rounds":      2,
		"RoundTree":   "tree-start",
		"PhaseBlock":  "### Phase 3",
	} {
		if fix[k] != v {
			t.Errorf("%s = %v, want %v", k, fix[k], v)
		}
	}
	if n := len(r.callsFrom(`SessionHost.Prompt rloop-p3-implement "fix r1" false 0s`)); n != 1 {
		t.Fatalf("calls = %q", r.shared.Calls())
	}
	clean := r.events("review-clean")
	if len(clean) != 1 || clean[0].Fields["round"] != "1" || len(r.events("review-round")) != 1 {
		t.Fatalf("events = %+v", r.store.Records["run-1"])
	}
	findings := r.events("finding")
	want := map[string]string{"step": "implement", "round": "1", "reviewer": "claude", "id": "claude-r1-1", "title": "t", "verdict": "out-of-scope", "severity": "P3", "fixed": "false", "evidence": ""}
	if len(findings) != 2 || !reflect.DeepEqual(findings[0].Fields, want) || findings[1].Fields["id"] != "codex-r1-1" {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestCleanSecondRoundEndsTheHalf(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) {
		r.repo.TreeChanges = nil
		n := 0
		if vars["Round"] == 1 {
			n = 1
		}
		writeReview(t, vars, "ok", n)
	}
	r.onFix = func(vars map[string]any) {
		r.repo.TreeChanges = []string{"a.go"}
		writeVerdict(t, vars, entry("codex-r1-1", "real", "P2", true, ""))
	}

	out := r.run()

	if out.State != StepOK || out.Warning != "" {
		t.Fatalf("outcome = %+v", out)
	}
	clean := r.events("review-clean")
	if len(r.fixes) != 1 || len(clean) != 1 || clean[0].Fields["round"] != "2" {
		t.Fatalf("fixes = %d, clean = %+v", len(r.fixes), clean)
	}
}

func TestFixHalfWithNoRealP1OrP2InALaterRoundEndsClean(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) {
		r.repo.TreeChanges = nil
		writeReview(t, vars, "ok", 1)
	}
	r.onFix = func(vars map[string]any) {
		r.repo.TreeChanges = []string{"a.go"}
		if vars["Round"] == 1 {
			writeVerdict(t, vars, entry("codex-r1-1", "real", "P1", true, ""))
			return
		}
		writeVerdict(t, vars, entry("codex-r2-1", "real", "P3", false, ""))
	}

	out := r.run()

	clean := r.events("review-clean")
	if out.State != StepOK || out.Warning != "" || len(clean) != 1 || clean[0].Fields["round"] != "2" {
		t.Fatalf("outcome = %+v, clean = %+v", out, clean)
	}
}

func TestThreeRoundsWithFixesEndOKWithTheLimitWarningAndOneCommit(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	ref := r.worker.Ref
	ref.Key.Attempt = 2
	ref.Kind.Row.Rounds = 3
	r.repo.TreeChanges = []string{"a.go"}
	r.host.script = func(int) AgentState {
		os.WriteFile(filepath.Join(r.runDir, "phase-3", "implement-a2.sentinel"), []byte(`{"outcome":"ok","reason":"","at":"2026-09-18T10:05:00Z"}`), 0o644)
		return AgentWorking
	}
	r.behave = func(vars map[string]any) {
		r.repo.TreeChanges = nil
		writeReview(t, vars, "ok", 1)
	}
	r.onFix = func(vars map[string]any) {
		r.repo.TreeChanges = []string{"a.go"}
		writeVerdict(t, vars, entry(fmt.Sprintf("codex-r%d-1", vars["Round"]), "real", "P1", true, ""))
	}
	runner := DefaultRunners(r.sm, []StepKind{ref.Kind})["diff"]

	out := runner.Run(context.Background(), ref, &recObserver{})

	if out.State != StepOK || out.Warning != "review round limit reached; round 3 fixes unreviewed" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(r.fixes) != 3 || len(r.events("review-clean")) != 0 {
		t.Fatalf("fixes = %d, events = %+v", len(r.fixes), r.store.Records["run-1"])
	}
	calls := r.shared.Calls()
	commits := r.callsFrom("Repo.CommitAll")
	lastFix := slices.Index(calls, `SessionHost.Prompt rloop-p3-implement-a2 "fix r3" false 0s`)
	if len(commits) != 1 || lastFix < 0 || slices.Index(calls, commits[0]) < lastFix {
		t.Fatalf("calls = %q", calls)
	}
}

func TestFixHalfFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		fix    func(r *reviewRig, vars map[string]any)
		reason string
	}{
		"verdict check before the step's own check": {func(r *reviewRig, vars map[string]any) {
			writeVerdict(t, vars, entry("codex-r1-1", "real", "P1", false, ""))
		}, "evidence missing: real P1 finding codex-r1-1 is not fixed"},
		"head moved": {func(r *reviewRig, vars map[string]any) {
			r.repo.SHA = "sha-moved"
			writeVerdict(t, vars, entry("codex-r1-1", "real", "P1", false, ""))
		}, "step committed before review"},
		"own evidence after the verdict": {func(r *reviewRig, vars map[string]any) {
			writeVerdict(t, vars, entry("codex-r1-1", "out-of-scope", "P3", false, ""))
		}, "evidence missing: no change since the step started"},
		"failed sentinel": {func(r *reviewRig, vars map[string]any) {
			os.WriteFile(vars["Sentinel"].(string), []byte(`{"outcome":"failed","reason":"cannot fix","at":"2026-09-18T10:05:00Z"}`), 0o644)
		}, "cannot fix"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newReviewRig(t, Reviewer{Provider: "codex"})
			r.behave = func(vars map[string]any) {
				r.repo.TreeChanges = nil
				writeReview(t, vars, "ok", 1)
			}
			r.onFix = func(vars map[string]any) { tc.fix(r, vars) }

			out := r.run()

			if out.State != StepFailed || out.Reason != tc.reason {
				t.Fatalf("outcome = %+v", out)
			}
			if len(r.reviews) != 1 || r.count("Repo.CommitAll") != 0 {
				t.Fatalf("reviews = %d, calls = %q", len(r.reviews), r.shared.Calls())
			}
		})
	}
}

func TestReviewTimeoutBoundsReviewersAndFixHalfTogether(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.worker.Ref.Kind.Row.ReviewTimeout = 10 * time.Minute
	var fixPolls int
	r.host.script = func(n int) AgentState {
		if len(r.fixes) > 0 {
			fixPolls++
		} else if n == 6 {
			r.repo.TreeChanges = nil
			writeReview(t, r.reviews[0], "ok", 1)
		}
		return AgentWorking
	}

	out := r.run()

	if out.State != StepFailed || out.Reason != "backstop 10m0s" {
		t.Fatalf("outcome = %+v", out)
	}
	if fixPolls == 0 || fixPolls > 5 {
		t.Fatalf("fix polls = %d", fixPolls)
	}
}

type fixedRunner struct{ out Outcome }

func (f fixedRunner) Run(ctx context.Context, ref StepRef, obs Observer) Outcome { return f.out }

func TestLoopEmitsTheRoundLimitWarningAndFiresOnWarnWithoutHalting(t *testing.T) {
	r := newLoopRig(t)
	warning := "review round limit reached; round 3 fixes unreviewed"
	r.loop.Runners = map[string]StepRunner{
		"plan-file": fixedRunner{Outcome{State: StepOK}},
		"diff":      fixedRunner{Outcome{State: StepOK, Warning: warning}},
	}

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	warnings := r.events("warning")
	if len(warnings) != 1 || warnings[0].Step != "implement" || warnings[0].Phase != "2" || warnings[0].Fields["reason"] != warning {
		t.Fatalf("warnings = %+v", warnings)
	}
	if hooks := r.hooks(); !reflect.DeepEqual(hooks, []string{"warn-hook warning", "done-hook finished"}) {
		t.Fatalf("hooks = %q", hooks)
	}
	if env := r.notifier.Fired[0]; env["R_LOOP_REASON"] != warning || env["R_LOOP_STEP"] != "implement" {
		t.Fatalf("env = %v", env)
	}
	if !strings.Contains(r.report(t), "phase 2 implement: "+warning) {
		t.Fatalf("report =\n%s", r.report(t))
	}
}

func TestWaitAllReportsActiveTimeExcludingAnOpenQuestion(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	s.OpenQuestion.Store(true)
	r.host.script = func(n int) AgentState {
		if n == 20 {
			s.OpenQuestion.Store(false)
		}
		if n == 23 {
			r.writeSentinel(t, s, "failed", "done")
		}
		return AgentWorking
	}

	out := r.sm.WaitAll(context.Background(), []*Session{s})[0]

	if out.State != StepFailed || out.active == 0 || out.active > 5*time.Minute {
		t.Fatalf("outcome = %+v", out)
	}
}

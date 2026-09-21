package core

import (
	"strings"
	"testing"
	"time"
)

var checksT0 = time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)

func landedStore(t *testing.T) *fakeStore {
	t.Helper()
	store := &fakeStore{}
	for _, l := range []struct {
		phase          int
		impl           time.Duration
		added, deleted int
		start          time.Time
	}{
		{1, 10 * time.Minute, 100, 20, checksT0},
		{2, 20 * time.Minute, 200, 50, checksT0.Add(time.Hour)},
	} {
		plan := StepKey{Run: "run-1", Phase: l.phase, Kind: "plan", Attempt: 1}
		impl := StepKey{Run: "run-1", Phase: l.phase, Kind: "implement", Attempt: 1}
		store.Append("run-1", Record{Kind: RecordStep, At: l.start, Step: &plan, State: StepQueued})
		store.Append("run-1", Record{Kind: RecordStep, At: l.start.Add(time.Minute), Step: &plan, State: StepOK})
		store.Append("run-1", Record{Kind: RecordStep, At: l.start.Add(2 * time.Minute), Step: &impl, State: StepQueued})
		store.Append("run-1", Record{Kind: RecordStep, At: l.start.Add(5 * time.Minute), Step: &impl, State: StepRunning})
		store.Append("run-1", Record{Kind: RecordStep, At: l.start.Add(5*time.Minute + l.impl), Step: &impl, State: StepOK})
		store.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: l.phase, MergeSHA: "m", Added: l.added, Deleted: l.deleted}})
	}
	return store
}

func phaseThree() Phase {
	return Phase{Number: 3, Title: "Third thing", Files: []string{"internal/core/checks.go", "internal/core/checks_test.go", "internal/web/"}}
}

func checkCtx(store *fakeStore, repo *fakeRepo, kind string, attempt int, elapsed time.Duration) CheckContext {
	ref := StepRef{
		Key:      StepKey{Run: "run-1", Phase: 3, Kind: kind, Attempt: attempt},
		Phase:    phaseThree(),
		Worktree: ".r-loop/wt/phase-3",
		Base:     "main",
		Vars:     map[string]any{"TodoPath": "/repo/docs/demo/todo.md"},
	}
	start := checksT0.Add(5 * time.Hour)
	return CheckContext{Step: ref, Session: &Session{Ref: ref, Dir: "/repo/.r-loop/wt/phase-3"}, Started: start, Now: start.Add(elapsed), Repo: repo, Store: store}
}

func callsTo(repo *fakeRepo, prefix string) []string {
	var out []string
	for _, c := range repo.Calls() {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func shipped(t *testing.T, name string) Check {
	t.Helper()
	for _, c := range ShippedChecks(2, 3) {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("no shipped check %q", name)
	return nil
}

func oneWarning(t *testing.T, got []Signal, ctx CheckContext) Signal {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("signals = %+v, want one", got)
	}
	sig := got[0]
	if sig.Kind != SignalWarn || sig.Source != SourceDriver || sig.Step != ctx.Step.Key {
		t.Errorf("signal = %+v", sig)
	}
	return sig
}

func noWarning(t *testing.T, got []Signal) {
	t.Helper()
	if len(got) != 0 {
		t.Fatalf("signals = %+v, want none", got)
	}
}

func TestShippedChecksAreTheFive(t *testing.T) {
	var names []string
	for _, c := range ShippedChecks(2, 3) {
		names = append(names, c.Name())
	}
	if got := strings.Join(names, " "); got != "files-outside-plan step-overtime diff-oversize plan-touched foreign-test-edit" {
		t.Errorf("shipped checks = %s", got)
	}
}

func TestFilesOutsidePlanCatchesAnUncommittedEdit(t *testing.T) {
	repo := &fakeRepo{RootDir: "/repo", Changed: []string{"internal/core/checks.go", "cmd/r-loop/main.go"}}
	ctx := checkCtx(&fakeStore{}, repo, "implement", 1, time.Minute)

	sig := oneWarning(t, shipped(t, "files-outside-plan").Run(ctx), ctx)

	if !strings.Contains(sig.Reason, "cmd/r-loop/main.go") || strings.Contains(sig.Reason, "checks.go") {
		t.Errorf("reason = %q", sig.Reason)
	}
	if calls := callsTo(repo, "Repo.ChangedFiles "); len(calls) != 1 || calls[0] != "Repo.ChangedFiles /repo/.r-loop/wt/phase-3 main" {
		t.Errorf("calls = %v", calls)
	}
}

func TestFilesOutsidePlanAllowsPlanFilesDirectoriesAndTaskPlans(t *testing.T) {
	repo := &fakeRepo{RootDir: "/repo", Changed: []string{"internal/core/checks_test.go", "internal/web/page/view.go", ".task-plans/phase-3-third-thing.md"}}
	ctx := checkCtx(&fakeStore{}, repo, "implement", 1, time.Minute)

	noWarning(t, shipped(t, "files-outside-plan").Run(ctx))
}

func TestFilesOutsidePlanListsAtMostTenPaths(t *testing.T) {
	var changed []string
	for _, c := range "abcdefghijkl" {
		changed = append(changed, "stray/"+string(c)+".go")
	}
	repo := &fakeRepo{RootDir: "/repo", Changed: changed}
	ctx := checkCtx(&fakeStore{}, repo, "implement", 1, time.Minute)

	sig := oneWarning(t, shipped(t, "files-outside-plan").Run(ctx), ctx)

	if !strings.Contains(sig.Reason, "stray/j.go") || strings.Contains(sig.Reason, "stray/k.go") {
		t.Errorf("reason = %q", sig.Reason)
	}
}

func TestStepOvertimeFiresPastTheFactorOfTheLongestSameKindStep(t *testing.T) {
	ctx := checkCtx(landedStore(t), &fakeRepo{}, "implement", 1, 41*time.Minute)

	sig := oneWarning(t, shipped(t, "step-overtime").Run(ctx), ctx)

	if !strings.Contains(sig.Reason, "41m0s") || !strings.Contains(sig.Reason, "20m0s") {
		t.Errorf("reason = %q", sig.Reason)
	}
}

func TestStepOvertimeQuietWithinTheFactor(t *testing.T) {
	ctx := checkCtx(landedStore(t), &fakeRepo{}, "implement", 1, 39*time.Minute)

	noWarning(t, shipped(t, "step-overtime").Run(ctx))
}

func TestStepOvertimeQuietBeforeTwoPhasesHaveLanded(t *testing.T) {
	store := &fakeStore{}
	impl := StepKey{Run: "run-1", Phase: 1, Kind: "implement", Attempt: 1}
	store.Append("run-1", Record{Kind: RecordStep, At: checksT0, Step: &impl, State: StepRunning})
	store.Append("run-1", Record{Kind: RecordStep, At: checksT0.Add(time.Minute), Step: &impl, State: StepOK})
	store.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: 1}})
	ctx := checkCtx(store, &fakeRepo{}, "implement", 1, 10*time.Hour)

	noWarning(t, shipped(t, "step-overtime").Run(ctx))
}

func TestDiffOversizeFiresPastTheFactorOfTheLargestLandedPhase(t *testing.T) {
	repo := &fakeRepo{RootDir: "/repo", Added: 700, Deleted: 51}
	ctx := checkCtx(landedStore(t), repo, "implement", 1, time.Minute)

	sig := oneWarning(t, shipped(t, "diff-oversize").Run(ctx), ctx)

	if !strings.Contains(sig.Reason, "751") || !strings.Contains(sig.Reason, "250") {
		t.Errorf("reason = %q", sig.Reason)
	}
	if calls := callsTo(repo, "Repo.DiffStat "); len(calls) != 1 || calls[0] != "Repo.DiffStat /repo/.r-loop/wt/phase-3 main" {
		t.Errorf("calls = %v", calls)
	}
}

func TestDiffOversizeQuietWithinTheFactor(t *testing.T) {
	repo := &fakeRepo{RootDir: "/repo", Added: 700, Deleted: 50}
	ctx := checkCtx(landedStore(t), repo, "implement", 1, time.Minute)

	noWarning(t, shipped(t, "diff-oversize").Run(ctx))
}

func TestDiffOversizeQuietBeforeTwoPhasesHaveLanded(t *testing.T) {
	store := &fakeStore{}
	store.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: 1, Added: 1}})
	repo := &fakeRepo{RootDir: "/repo", Added: 10000}
	ctx := checkCtx(store, repo, "implement", 1, time.Minute)

	noWarning(t, shipped(t, "diff-oversize").Run(ctx))
}

func TestPlanTouchedFiresOnTheTodo(t *testing.T) {
	repo := &fakeRepo{RootDir: "/repo", Changed: []string{"internal/core/checks.go", "docs/demo/todo.md"}}
	ctx := checkCtx(&fakeStore{}, repo, "implement", 1, time.Minute)

	sig := oneWarning(t, shipped(t, "plan-touched").Run(ctx), ctx)

	if !strings.Contains(sig.Reason, "docs/demo/todo.md") {
		t.Errorf("reason = %q", sig.Reason)
	}
}

func TestPlanTouchedFiresOnAnotherPhasesPlan(t *testing.T) {
	repo := &fakeRepo{RootDir: "/repo", Changed: []string{".task-plans/phase-3-third-thing.md", ".task-plans/phase-2-second.md"}}
	ctx := checkCtx(&fakeStore{}, repo, "plan", 1, time.Minute)

	sig := oneWarning(t, shipped(t, "plan-touched").Run(ctx), ctx)

	if !strings.Contains(sig.Reason, ".task-plans/phase-2-second.md") || strings.Contains(sig.Reason, "phase-3-third-thing") {
		t.Errorf("reason = %q", sig.Reason)
	}
}

func TestPlanTouchedQuietOnThePhasesOwnPlan(t *testing.T) {
	repo := &fakeRepo{RootDir: "/repo", Changed: []string{".task-plans/phase-3-third-thing.md", "internal/core/checks.go"}}
	ctx := checkCtx(&fakeStore{}, repo, "plan", 1, time.Minute)

	noWarning(t, shipped(t, "plan-touched").Run(ctx))
}

func TestForeignTestEditFiresOnATestThatExistedAtBase(t *testing.T) {
	repo := &fakeRepo{RootDir: "/repo", Changed: []string{"internal/store/store_test.go", "internal/core/checks_test.go", "web/app.test.ts"}, RunOutput: "internal/store/store_test.go\n"}
	ctx := checkCtx(&fakeStore{}, repo, "implement", 1, time.Minute)

	sig := oneWarning(t, shipped(t, "foreign-test-edit").Run(ctx), ctx)

	if !strings.Contains(sig.Reason, "internal/store/store_test.go") || strings.Contains(sig.Reason, "app.test.ts") {
		t.Errorf("reason = %q", sig.Reason)
	}
	calls := callsTo(repo, "Repo.Run ")
	if len(calls) != 1 || !strings.Contains(calls[0], "git ls-tree -r --name-only 'main' -- 'internal/store/store_test.go' 'web/app.test.ts'") {
		t.Errorf("calls = %v", calls)
	}
}

func TestForeignTestEditQuietOnANewTestOrPlanFileOrNonTest(t *testing.T) {
	repo := &fakeRepo{RootDir: "/repo", Changed: []string{"test/fixtures/new.json", "internal/core/checks_test.go", "internal/store/store.go"}, RunOutput: ""}
	ctx := checkCtx(&fakeStore{}, repo, "implement", 1, time.Minute)

	noWarning(t, shipped(t, "foreign-test-edit").Run(ctx))
}

func TestForeignTestEditMatchesEveryTestPattern(t *testing.T) {
	for _, path := range []string{"a/b_test.go", "web/app.test.ts", "lib/thing_test.py", "test/unit/x.go"} {
		t.Run(path, func(t *testing.T) {
			repo := &fakeRepo{RootDir: "/repo", Changed: []string{path}, RunOutput: path + "\n"}
			ctx := checkCtx(&fakeStore{}, repo, "implement", 1, time.Minute)

			sig := oneWarning(t, shipped(t, "foreign-test-edit").Run(ctx), ctx)

			if !strings.Contains(sig.Reason, path) {
				t.Errorf("reason = %q", sig.Reason)
			}
		})
	}
}

func TestEachCheckFiresAtMostOncePerAttempt(t *testing.T) {
	repo := &fakeRepo{RootDir: "/repo", Changed: []string{"cmd/stray.go", "docs/demo/todo.md", "internal/x/old_test.go"}, RunOutput: "internal/x/old_test.go\n", Added: 5000}
	store := landedStore(t)
	for _, c := range ShippedChecks(2, 3) {
		t.Run(c.Name(), func(t *testing.T) {
			first := checkCtx(store, repo, "implement", 1, 5*time.Hour)
			oneWarning(t, c.Run(first), first)

			noWarning(t, c.Run(first))

			retry := checkCtx(store, repo, "implement", 2, 5*time.Hour)
			oneWarning(t, c.Run(retry), retry)
		})
	}
}

func TestFilesScopedChecksAreQuietForAPhaseWithoutFiles(t *testing.T) {
	repo := &fakeRepo{RootDir: "/repo", Changed: []string{"cmd/r-loop/main.go", "internal/store/store_test.go"}, RunOutput: "internal/store/store_test.go\n"}
	ctx := checkCtx(&fakeStore{}, repo, "implement", 1, time.Minute)
	ctx.Step.Phase.Files = nil

	noWarning(t, shipped(t, "files-outside-plan").Run(ctx))
	noWarning(t, shipped(t, "foreign-test-edit").Run(ctx))
}

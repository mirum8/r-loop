package core

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type dirRepo struct {
	*fakeRepo
	trees map[string]map[string]string
}

func (r *dirRepo) Snapshot(dir string) (string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		return "", err
	}
	for id, t := range r.trees {
		if reflect.DeepEqual(t, files) {
			return id, nil
		}
	}
	id := "tree-" + string(rune('a'+len(r.trees)))
	r.trees[id] = files
	return id, nil
}

func (r *dirRepo) TreeDiff(from, to string) ([]string, error) {
	a, b := r.trees[from], r.trees[to]
	var out []string
	for p, v := range b {
		if w, ok := a[p]; !ok || w != v {
			out = append(out, p)
		}
	}
	for p := range a {
		if _, ok := b[p]; !ok {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out, nil
}

type baselineRig struct {
	store    *fakeStore
	repo     *dirRepo
	worktree string
	runDir   string
}

func newBaselineRig(t *testing.T) *baselineRig {
	return &baselineRig{
		store:    &fakeStore{},
		repo:     &dirRepo{fakeRepo: &fakeRepo{RootDir: "/repo", SHA: "sha-start"}, trees: map[string]map[string]string{}},
		worktree: t.TempDir(),
		runDir:   t.TempDir(),
	}
}

func (b *baselineRig) manager() *SessionManager {
	return &SessionManager{
		Host:    &scriptedHost{},
		Repo:    b.repo,
		Prompts: &fakePrompts{Texts: map[string]string{"plan": "plan it", "implement": "build it"}},
		Store:   b.store,
		Resolve: func(provider, model, effort, askURL, mcpConfigPath, dir string) (ProviderArgs, error) {
			return ProviderArgs{Kind: "codex"}, nil
		},
		Poll:       time.Millisecond,
		StallGrace: time.Hour,
	}
}

func (b *baselineRig) ref(kind, check string, attempt int) StepRef {
	return StepRef{
		Key:      StepKey{Run: "run-1", Phase: "1", Kind: kind, Attempt: attempt},
		Kind:     StepKind{Name: kind, Prompt: kind, Check: check, Row: StepRow{Provider: "codex", Timeout: time.Hour}},
		Phase:    Phase{ID: "1", Title: "Greeting package"},
		Worktree: b.worktree,
		Branch:   "r-loop/phase-1",
		Base:     "main",
		RunDir:   b.runDir,
		Vars:     map[string]any{"PlanPath": planPath},
	}
}

func (b *baselineRig) write(t *testing.T, rel, body string) {
	t.Helper()
	p := filepath.Join(b.worktree, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (b *baselineRig) attempt(t *testing.T, sm *SessionManager, ref StepRef, outcome string, work func()) Outcome {
	t.Helper()
	s, err := sm.Spawn(context.Background(), ref)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	work()
	body := `{"outcome":"` + outcome + `","reason":"agent crashed"}`
	if err := os.WriteFile(s.Sentinel, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return sm.Finish(s, sm.Wait(context.Background(), s, &recObserver{}))
}

func (b *baselineRig) events(kind string) []Event {
	var out []Event
	for _, rec := range b.store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == kind {
			out = append(out, *rec.Event)
		}
	}
	return out
}

func nothing() {}

func TestARestartedPlanStepThatLeavesItsEarlierAttemptsPlanUnchangedIsOk(t *testing.T) {
	b := newBaselineRig(t)
	sm := b.manager()
	b.attempt(t, sm, b.ref("plan", "plan-file", 1), "failed", func() { b.write(t, planPath, goodPlan) })

	out := b.attempt(t, sm, b.ref("plan", "plan-file", 2), "ok", nothing)

	if out.State != StepOK {
		t.Fatalf("attempt 2 = %+v", out)
	}
}

func TestAResumedPlanStepThatLeavesItsEarlierAttemptsPlanUnchangedIsOk(t *testing.T) {
	b := newBaselineRig(t)
	b.attempt(t, b.manager(), b.ref("plan", "plan-file", 1), "failed", func() { b.write(t, planPath, goodPlan) })

	out := b.attempt(t, b.manager(), b.ref("plan", "plan-file", 2), "ok", nothing)

	if out.State != StepOK {
		t.Fatalf("attempt 2 = %+v", out)
	}
}

func TestARestartedImplementStepThatLeavesItsEarlierAttemptsWorkUnchangedIsOk(t *testing.T) {
	b := newBaselineRig(t)
	sm := b.manager()
	b.attempt(t, sm, b.ref("implement", "diff", 1), "failed", func() { b.write(t, "greet/greet.go", "package greet") })

	out := b.attempt(t, sm, b.ref("implement", "diff", 2), "ok", nothing)

	if out.State != StepOK {
		t.Fatalf("attempt 2 = %+v", out)
	}
}

func TestAResumedImplementStepThatLeavesItsEarlierAttemptsWorkUnchangedIsOk(t *testing.T) {
	b := newBaselineRig(t)
	b.attempt(t, b.manager(), b.ref("implement", "diff", 1), "failed", func() { b.write(t, "greet/greet.go", "package greet") })

	out := b.attempt(t, b.manager(), b.ref("implement", "diff", 2), "ok", nothing)

	if out.State != StepOK {
		t.Fatalf("attempt 2 = %+v", out)
	}
}

func TestAFirstAttemptOverAnEarlierStepsLeftoversThatChangesNothingFails(t *testing.T) {
	b := newBaselineRig(t)
	b.write(t, planPath, goodPlan)

	out := b.attempt(t, b.manager(), b.ref("implement", "diff", 1), "ok", nothing)

	if out.State != StepFailed || out.Reason != "evidence missing: no change since the step started" {
		t.Fatalf("attempt 1 = %+v", out)
	}
}

func TestAPlanStepWhoseFirstAttemptFindsThePlanAlreadyThereFails(t *testing.T) {
	b := newBaselineRig(t)
	b.write(t, planPath, goodPlan)

	out := b.attempt(t, b.manager(), b.ref("plan", "plan-file", 1), "ok", nothing)

	if out.State != StepFailed || out.Reason != "evidence missing: plan step did not change "+planPath {
		t.Fatalf("attempt 1 = %+v", out)
	}
}

func TestAnAttemptAfterTheStepsOkAttemptIsJudgedFromAFreshBaseline(t *testing.T) {
	b := newBaselineRig(t)
	sm := b.manager()
	first := b.attempt(t, sm, b.ref("plan", "plan-file", 1), "ok", func() { b.write(t, planPath, goodPlan) })
	if first.State != StepOK {
		t.Fatalf("attempt 1 = %+v", first)
	}

	out := b.attempt(t, sm, b.ref("plan", "plan-file", 2), "ok", nothing)

	if out.State != StepFailed || out.Reason != "evidence missing: plan step did not change "+planPath {
		t.Fatalf("attempt 2 = %+v", out)
	}
}

func TestTheBaselineIsRecordedBeforeSpawnedOnlyForTheFirstAttempt(t *testing.T) {
	b := newBaselineRig(t)
	sm := b.manager()
	b.attempt(t, sm, b.ref("implement", "diff", 1), "failed", func() { b.write(t, "greet/greet.go", "package greet") })
	b.attempt(t, b.manager(), b.ref("implement", "diff", 2), "failed", nothing)

	baselines := b.events("baseline")
	if len(baselines) != 1 || baselines[0].Step != "implement" || baselines[0].Phase != "1" || !reflect.DeepEqual(baselines[0].Fields, map[string]string{"step": "implement-a1", "tree": "tree-a"}) {
		t.Fatalf("baselines = %+v", baselines)
	}
	var order []string
	for _, rec := range b.store.Records["run-1"] {
		switch {
		case rec.Kind == RecordEvent && rec.Event.Kind == "baseline":
			order = append(order, "baseline")
		case rec.Kind == RecordStep:
			order = append(order, strings.Join([]string{string(rec.State), rec.Step.Kind}, " "))
		}
	}
	if len(order) < 2 || order[0] != "baseline" || order[1] != "spawned implement" {
		t.Fatalf("order = %v", order)
	}
}

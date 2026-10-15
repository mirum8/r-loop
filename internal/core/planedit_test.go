package core

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

type primaryRepo struct {
	*loopRepo
	pmu     sync.Mutex
	primary []string
	commits []string
}

func (p *primaryRepo) Dirty(dir string) ([]string, error) {
	if dir != "" {
		return p.loopRepo.Dirty(dir)
	}
	p.pmu.Lock()
	defer p.pmu.Unlock()
	return slices.Clone(p.primary), nil
}

func (p *primaryRepo) Commit(ctx context.Context, message string, paths ...string) (string, error) {
	p.pmu.Lock()
	p.commits = append(p.commits, fmt.Sprintf("%s %v", message, paths))
	p.primary = slices.DeleteFunc(p.primary, func(f string) bool { return slices.Contains(paths, f) })
	p.pmu.Unlock()
	return p.loopRepo.Commit(ctx, message, paths...)
}

func withPrimary(r *eventsRig, dirty ...string) *primaryRepo {
	p := &primaryRepo{loopRepo: r.repo, primary: dirty}
	r.loop.Sessions.Repo = p
	return p
}

func TestALandContinueCommitsThePlanEditBeforeLandingThePhaseReadAgain(t *testing.T) {
	r, _ := newSourceRig(t, resolveAs("continue"))
	r.loop.PlanSource = editedPlan("`go test ./store/...`")
	repo := withPrimary(r, "docs/x/todo.md")
	doneWhens := landingWith(r, fmt.Errorf("%w: app-api exited 127: %w", ErrGate, ErrGateCommandMissing))

	if code := r.run(RunOptions{Phases: []string{"2"}}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if want := []string{"docs(plan): correct phase 2 [docs/x/todo.md]"}; !reflect.DeepEqual(repo.commits, want) {
		t.Errorf("commits %q, want %q", repo.commits, want)
	}
	if !reflect.DeepEqual(*doneWhens, []string{"", "`go test ./store/...`"}) {
		t.Errorf("Done when lines handed to Land %q", *doneWhens)
	}
	if got := r.events(EventPlanCorrected); len(got) != 1 || got[0].Phase != "2" || got[0].Fields["path"] != "docs/x/todo.md" {
		t.Errorf("plan-corrected events %+v", got)
	}
}

func TestALandContinueWithAnUneditedPlanCommitsNothing(t *testing.T) {
	r, _ := newSourceRig(t, resolveAs("continue"))
	r.loop.PlanSource = editedPlan("")
	repo := withPrimary(r)
	landingWith(r, fmt.Errorf("%w: app-api exited 127: %w", ErrGate, ErrGateCommandMissing))

	if code := r.run(RunOptions{Phases: []string{"2"}}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(repo.commits) != 0 || len(r.events(EventPlanCorrected)) != 0 {
		t.Errorf("commits %q events %+v", repo.commits, r.events(EventPlanCorrected))
	}
}

func TestALandContinueWithChangesBesideThePlanIsRaisedAgainAndRevertsNothing(t *testing.T) {
	answers := []string{"continue", "block"}
	var mu sync.Mutex
	r, seen := newSourceRig(t, func(Blocker) Resolution {
		mu.Lock()
		defer mu.Unlock()
		a := answers[0]
		answers = answers[1:]
		return Resolution{Action: a, Citation: "always"}
	})
	r.loop.PlanSource = editedPlan("`go test ./store/...`")
	repo := withPrimary(r, "docs/x/todo.md", "src/a.go")
	doneWhens := landingWith(r, fmt.Errorf("%w: a.go", ErrMergeConflict))

	if code := r.run(RunOptions{Phases: []string{"2"}}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if len(*seen) != 2 || (*seen)[1].b.Reason != "primary tree has changes other than the plan: src/a.go" {
		t.Fatalf("blockers %+v", *seen)
	}
	if len(repo.commits) != 0 || len(*doneWhens) != 1 {
		t.Errorf("commits %q, Land called %d times", repo.commits, len(*doneWhens))
	}
	if dirty, _ := repo.Dirty(""); !reflect.DeepEqual(dirty, []string{"docs/x/todo.md", "src/a.go"}) {
		t.Errorf("primary tree %v", dirty)
	}
}

func TestALandContinueOnALocalTodoLeavesThePlanEditUncommitted(t *testing.T) {
	r, _ := newSourceRig(t, resolveAs("continue"))
	r.loop.PlanSource = editedPlan("`go test ./store/...`")
	r.loop.LocalTodo = true
	repo := withPrimary(r)
	doneWhens := landingWith(r, fmt.Errorf("%w: app-api exited 127: %w", ErrGate, ErrGateCommandMissing))

	if code := r.run(RunOptions{Phases: []string{"2"}}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(repo.commits) != 0 {
		t.Errorf("commits %q", repo.commits)
	}
	if !reflect.DeepEqual(*doneWhens, []string{"", "`go test ./store/...`"}) {
		t.Errorf("Done when lines handed to Land %q", *doneWhens)
	}
}

func failOnce(r *eventsRig) *[]StepRef {
	var refs []StepRef
	r.loop.Runners["diff"] = stepRunnerFunc(func(ctx context.Context, ref StepRef, obs Observer) Outcome {
		refs = append(refs, ref)
		if ref.Key.Attempt > 1 {
			return Outcome{State: StepOK}
		}
		return Outcome{State: StepFailed, Reason: "Done when is prose"}
	})
	return &refs
}

func TestAStepContinueCommitsThePlanEditAndRunsTheNextAttemptOnThePhaseReadAgain(t *testing.T) {
	r, _ := newSourceRig(t, resolveAs("continue"))
	r.loop.PlanSource = editedPlan("`go test ./store/...`")
	repo := withPrimary(r, "docs/x/todo.md")
	refs := failOnce(r)
	var landed []string
	r.loop.Lander = landerFunc(func(ctx context.Context, ph Phase) (Landing, error) {
		landed = append(landed, ph.DoneWhen)
		l := Landing{Phase: ph.ID, MergeSHA: "m"}
		r.store.Append("run-1", Record{Kind: RecordLanding, Landing: &l})
		return l, nil
	})

	if code := r.run(RunOptions{Phases: []string{"2"}}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(*refs) != 2 || (*refs)[0].Phase.DoneWhen != "" || (*refs)[1].Phase.DoneWhen != "`go test ./store/...`" {
		t.Fatalf("refs %+v", *refs)
	}
	if want := []string{"docs(plan): correct phase 2 [docs/x/todo.md]"}; !reflect.DeepEqual(repo.commits, want) {
		t.Errorf("commits %q, want %q", repo.commits, want)
	}
	if !reflect.DeepEqual(landed, []string{"`go test ./store/...`"}) {
		t.Errorf("Done when lines handed to Land %q", landed)
	}
}

func TestAStepContinueWithChangesBesideThePlanIsRaisedAgainUntilTheTreeIsClean(t *testing.T) {
	var repo *primaryRepo
	var mu sync.Mutex
	calls := 0
	r, seen := newSourceRig(t, func(Blocker) Resolution {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 2 {
			repo.pmu.Lock()
			repo.primary = []string{"docs/x/todo.md"}
			repo.pmu.Unlock()
		}
		return Resolution{Action: "continue", Citation: "always"}
	})
	r.loop.MaxRestarts = 3
	r.loop.PlanSource = editedPlan("`go test ./store/...`")
	repo = withPrimary(r, "docs/x/todo.md", "notes.txt")
	refs := failOnce(r)

	if code := r.run(RunOptions{Phases: []string{"2"}}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(*seen) != 2 || (*seen)[1].b.Reason != "primary tree has changes other than the plan: notes.txt" {
		t.Fatalf("blockers %+v", *seen)
	}
	if len(*refs) != 2 || (*refs)[1].Phase.DoneWhen != "`go test ./store/...`" {
		t.Errorf("refs %+v", *refs)
	}
	if want := []string{"docs(plan): correct phase 2 [docs/x/todo.md]"}; !reflect.DeepEqual(repo.commits, want) {
		t.Errorf("commits %q, want %q", repo.commits, want)
	}
}

func TestAStepContinueThatCannotReadThePlanStopsAtTheRestartLimit(t *testing.T) {
	r, seen := newSourceRig(t, resolveAs("continue"))
	r.loop.MaxRestarts = 2
	r.loop.PlanSource = editedPlan("`go test ./store/...`")
	repo := withPrimary(r, "docs/x/todo.md", "notes.txt")
	refs := failOnce(r)

	if code := r.run(RunOptions{Phases: []string{"2"}}); code == 0 {
		t.Fatal("exit 0, want a failed run")
	}
	if len(*refs) != 1 || len(repo.commits) != 0 {
		t.Errorf("attempts %d commits %q", len(*refs), repo.commits)
	}
	if len(*seen) != 3 {
		t.Errorf("%d blockers raised, want 3", len(*seen))
	}
	if !slices.ContainsFunc(r.face.events(), func(ev Event) bool {
		return strings.Contains(ev.Fields["reason"], "primary tree has changes other than the plan: notes.txt")
	}) {
		t.Errorf("no event carries the refusal: %+v", r.face.events())
	}
}

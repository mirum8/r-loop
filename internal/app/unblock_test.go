package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
)

const blockedTodo = `# t

## Resolve first
- [ ] **Pick the database** — which one backs the store?
      Owner: me. Blocks: Phase 1. Timebox: an hour. Output: a line in the spec.

### Phase 1 — one
**Depends on:** —
- [ ] a

### Phase 2 — two
**Depends on:** —
- [ ] b

### Phase 3 — three
**Depends on:** Phase 1
- [ ] c
`

type walk struct {
	f    *fixture
	w    *Wiring
	dog  *dogHost
	land *landRecorder
	hang bool
}

func startWalk(t *testing.T, onWalk func(w *Wiring), args ...string) *walk {
	t.Helper()
	f := newResumeFixture(t, noReviewConfig)
	f.write("docs/topic/todo.md", blockedTodo)
	f.commit()
	w, err := f.preflight(append([]string{f.todo, "--plain"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	land := f.sim(w, newSim())
	dog := &dogHost{}
	k := &walk{f: f, w: w, dog: dog, land: land}
	dog.onPrompt = func(text string) {
		if !strings.HasPrefix(text, "resolve first:") {
			return
		}
		if onWalk != nil {
			onWalk(w)
		}
		if !k.hang {
			if err := os.WriteFile(filepath.Join(w.Store.Dir(w.Loop.RunID), "unblock.done"), nil, 0o644); err != nil {
				t.Error(err)
			}
		}
	}
	w.Dog.Host = dog
	return k
}

func (k *walk) edit(old, new string) {
	k.f.t.Helper()
	data, err := os.ReadFile(k.f.todo)
	if err != nil {
		k.f.t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		k.f.t.Fatalf("todo has no %q", old)
	}
	if err := os.WriteFile(k.f.todo, []byte(strings.Replace(string(data), old, new, 1)), 0o644); err != nil {
		k.f.t.Fatal(err)
	}
}

func TestTheWatchdogWalksABlockerAndTheDriverCommitsOnlyThePlan(t *testing.T) {
	var k *walk
	k = startWalk(t, func(w *Wiring) {
		k.edit("- [ ] **Pick the database** — which one backs the store?\n      Owner: me. Blocks: Phase 1. Timebox: an hour. Output: a line in the spec.\n",
			"- [x] **Pick the database** — which one backs the store?\n      Owner: me. Blocks: Phase 1. Timebox: an hour. Output: a line in the spec.\n      Resolved: 2026-09-21 — Postgres; the team already runs it.\n      Alternative: SQLite. Outstanding: a line in the spec.\n")
	})

	code := k.w.Execute(core.RunOptions{Phases: []string{"1", "2", "3"}})

	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, k.f.out, k.f.err)
	}
	if !k.dog.prompted("resolve first: 1 open entries") {
		t.Fatalf("no walk request: %q", k.dog.Calls())
	}
	walkText := ""
	for _, c := range k.dog.Calls() {
		if strings.Contains(c, "resolve first:") {
			walkText = c
		}
	}
	for _, want := range []string{"## R1 — Pick the database", "kind: decision", "blocks: Phase 1", "Blocked phase 1:", "- [ ] a", "When the walk is done, write an empty file at " + filepath.Join(k.w.Store.Dir(k.w.Loop.RunID), "unblock.done") + "."} {
		if !strings.Contains(walkText, want) {
			t.Errorf("walk request missing %q:\n%s", want, walkText)
		}
	}
	if got := git(t, k.f.root, "log", "--format=%s", "-1", "--", "docs/topic/todo.md"); got != "docs: resolve 1 plan blockers" {
		t.Errorf("plan commit %q", got)
	}
	if got := git(t, k.f.root, "show", "--name-only", "--format=", ":/docs: resolve 1 plan blockers"); got != "docs/topic/todo.md" {
		t.Errorf("the resolve commit touches %q", got)
	}
	if len(k.land.landed) != 3 {
		t.Errorf("landed %v, want 1, 2 and 3", k.land.landed)
	}
	run := k.f.load(k.w.Loop.RunID)
	if got := stepEvents(run, "entry-resolved"); len(got) != 1 || got[0].Fields["resolved"] != "2026-09-21 — Postgres; the team already runs it" {
		t.Errorf("entry-resolved %+v", got)
	}
	if rep := core.Report(run, k.w.Plan); !strings.Contains(rep, "## Blockers\n\n- Pick the database → 2026-09-21 — Postgres; the team already runs it\n") {
		t.Errorf("report:\n%s", rep)
	}
}

func TestAnEntryLeftOpenSkipsItsPhasesAndTheirDependents(t *testing.T) {
	k := startWalk(t, nil)

	code := k.w.Execute(core.RunOptions{Phases: []string{"1", "2", "3"}})

	if code != 0 {
		t.Fatalf("exit %d\n%s", code, k.f.out)
	}
	if len(k.land.landed) != 1 || k.land.landed[0] != "2" {
		t.Fatalf("landed %v, want only 2", k.land.landed)
	}
	run := k.f.load(k.w.Loop.RunID)
	if got := stepEvents(run, "entry-deferred"); len(got) != 1 || got[0].Fields["phases"] != "1, 3" {
		t.Errorf("entry-deferred %+v", got)
	}
	if got := recordedRunList(run); len(got) != 1 || got[0] != "2" {
		t.Errorf("run list %v", got)
	}
	if rep := core.Report(run, k.w.Plan); !strings.Contains(rep, "- Pick the database still open: phase 1, 3 skipped\n") {
		t.Errorf("report:\n%s", rep)
	}
	if got := git(t, k.f.root, "status", "--porcelain"); got != "" {
		t.Errorf("tree %q", got)
	}
}

func TestAnEditOutsideResolveFirstIsUndoneAndStopsTheRun(t *testing.T) {
	var k *walk
	k = startWalk(t, func(w *Wiring) { k.edit("- [ ] b", "- [x] b") })
	head := git(t, k.f.root, "rev-parse", "HEAD")

	code := k.w.Execute(core.RunOptions{Phases: []string{"1", "2", "3"}})

	if code != 4 || !strings.Contains(k.f.err.String(), "outside ## Resolve first; the edit was undone") {
		t.Fatalf("exit %d: %s", code, k.f.err)
	}
	if data, _ := os.ReadFile(k.f.todo); string(data) != blockedTodo {
		t.Errorf("todo not restored:\n%s", data)
	}
	if got := git(t, k.f.root, "rev-parse", "HEAD"); got != head || len(k.land.landed) != 0 {
		t.Errorf("head moved or landed %v", k.land.landed)
	}
	if run := k.f.load(k.w.Loop.RunID); run.Status != core.RunHalted {
		t.Errorf("run %s", run.Status)
	}
}

func TestAChangeOutsideThePlanStopsTheRun(t *testing.T) {
	var k *walk
	k = startWalk(t, func(w *Wiring) { k.f.write("docs/topic/spec.md", "a measurement") })

	code := k.w.Execute(core.RunOptions{Phases: []string{"1", "2"}})

	if code != 4 || !strings.Contains(k.f.err.String(), "the watchdog left changes outside the plan: docs/topic/spec.md") {
		t.Fatalf("exit %d: %s", code, k.f.err)
	}
}

func TestAStopDuringTheWalkHaltsTheRun(t *testing.T) {
	k := startWalk(t, func(w *Wiring) { w.Store.MarkAbort(w.Loop.RunID) })

	code := k.w.Execute(core.RunOptions{Phases: []string{"1", "2"}})

	if code != 4 || !strings.Contains(k.f.err.String(), "stopped during the ## Resolve first walk") || len(k.land.landed) != 0 {
		t.Fatalf("exit %d landed %v: %s", code, k.land.landed, k.f.err)
	}
}

func TestUnattendedSkipsTheBlockedPhasesWithoutAWalk(t *testing.T) {
	k := startWalk(t, nil, "--unattended")

	code := k.w.Execute(core.RunOptions{Phases: []string{"1", "2", "3"}})

	if code != 0 || k.dog.prompted("resolve first:") {
		t.Fatalf("exit %d, walked %v", code, k.dog.prompted("resolve first:"))
	}
	if len(k.land.landed) != 1 || k.land.landed[0] != "2" {
		t.Errorf("landed %v", k.land.landed)
	}
}

func TestARunWhoseEveryPhaseIsBlockedFinishesWithNothingToRun(t *testing.T) {
	k := startWalk(t, nil)

	code := k.w.Execute(core.RunOptions{Phases: []string{"1", "3"}})

	if code != 0 || len(k.land.landed) != 0 {
		t.Fatalf("exit %d landed %v", code, k.land.landed)
	}
	if run := k.f.load(k.w.Loop.RunID); run.Status != core.RunFinished {
		t.Errorf("run %s", run.Status)
	}
}

func TestAWatchdogThatFailsToStartBlocksTheRunWithoutTryingAnotherProvider(t *testing.T) {
	k := startWalk(t, nil)
	k.dog.startErr = errors.New("usage limit")

	code := k.w.Execute(core.RunOptions{Phases: []string{"2"}})

	if code != 4 || !strings.Contains(k.f.err.String(), "watchdog did not start: ") || !strings.Contains(k.f.err.String(), "usage limit") {
		t.Fatalf("exit %d: %s", code, k.f.err)
	}
	var starts []string
	for _, c := range k.dog.Calls() {
		if strings.HasPrefix(c, "Start ") {
			starts = append(starts, c)
		}
	}
	if len(starts) != 1 || !strings.Contains(starts[0], " claude ") {
		t.Fatalf("starts %q", starts)
	}
	if len(k.land.landed) != 0 {
		t.Errorf("landed %v", k.land.landed)
	}
}

func TestTheDriverWaitsForTheWalkToFinishNotForThePromptToReturn(t *testing.T) {
	var k *walk
	k = startWalk(t, func(w *Wiring) {
		go func() {
			time.Sleep(300 * time.Millisecond)
			k.edit("- [ ] **Pick the database** — which one backs the store?\n      Owner: me. Blocks: Phase 1. Timebox: an hour. Output: a line in the spec.\n",
				"- [x] **Pick the database** — which one backs the store?\n      Owner: me. Blocks: Phase 1. Timebox: an hour. Output: a line in the spec.\n      Resolved: 2026-09-21 — Postgres; the team already runs it.\n")
			if err := os.WriteFile(filepath.Join(w.Store.Dir(w.Loop.RunID), "unblock.done"), nil, 0o644); err != nil {
				t.Error(err)
			}
		}()
	})
	k.hang = true

	code := k.w.Execute(core.RunOptions{Phases: []string{"1", "2"}})

	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, k.f.out, k.f.err)
	}
	if got := stepEvents(k.f.load(k.w.Loop.RunID), "entry-deferred"); len(got) != 0 {
		t.Fatalf("the driver moved on before the walk finished: %+v", got)
	}
	if len(k.land.landed) != 2 {
		t.Errorf("landed %v, want 1 and 2", k.land.landed)
	}
}

func TestAWalkThatNeverFinishesTimesOutAndSkipsTheBlockedPhases(t *testing.T) {
	k := startWalk(t, nil)
	k.hang = true
	k.w.Config.Watchdog.UnblockTimeout = 200 * time.Millisecond

	code := k.w.Execute(core.RunOptions{Phases: []string{"1", "2"}})

	if code != 0 || len(k.land.landed) != 1 || k.land.landed[0] != "2" {
		t.Fatalf("exit %d landed %v", code, k.land.landed)
	}
	if got := stepEvents(k.f.load(k.w.Loop.RunID), "warning"); len(got) == 0 || !strings.Contains(got[0].Fields["reason"], "did not finish") {
		t.Errorf("warnings %+v", got)
	}
}

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type agentSim struct {
	fakeSessionHost
	mu        sync.Mutex
	behaviour map[string]string
	idle      map[string]bool
	repo      *loopRepo
	store     *loopStore
}

func (h *agentSim) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	h.record("SessionHost.Prompt %s", agent)
	if strings.HasPrefix(text, "r-loop:") {
		return nil
	}
	var spec OpenSpec
	for _, o := range h.Opened {
		if o.Label == agent {
			spec = o
		}
	}
	sentinel := spec.Env["R_LOOP_SENTINEL"]
	h.mu.Lock()
	behaviour := h.behaviour[agent]
	h.mu.Unlock()
	switch behaviour {
	case "hold":
	case "fail":
		writeFile(sentinel, `{"outcome":"failed","reason":"tests red","at":"2026-09-18T10:05:00Z"}`)
	case "stall":
		h.mu.Lock()
		h.idle[agent] = true
		h.mu.Unlock()
	case "abort":
		h.store.MarkAbort(spec.Env["R_LOOP_RUN"])
	default:
		changed := "code.go"
		if spec.Env["R_LOOP_STEP"] == "plan" {
			changed = text
			writeFile(filepath.Join(spec.CWD, text), "status: planned\n\n## Summary\nx\n## Changes\nx\n## Tests\n- a test\n## Assumptions\n- phase "+spec.Env["R_LOOP_PHASE"]+" keeps state in memory\n")
		}
		h.repo.setChanges(changed)
		writeFile(sentinel, `{"outcome":"ok","reason":"","at":"2026-09-18T10:05:00Z"}`)
	}
	return nil
}

func (h *agentSim) State(agent string) (AgentState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.idle[agent] {
		return AgentIdle, nil
	}
	return AgentWorking, nil
}

func writeFile(path, body string) {
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(body), 0o644)
}

type loopRepo struct {
	fakeRepo
	mu      sync.Mutex
	changes []string
}

func (r *loopRepo) setChanges(p string) {
	r.mu.Lock()
	r.changes = []string{p}
	r.mu.Unlock()
}

func (r *loopRepo) TreeDiff(from, to string) ([]string, error) {
	r.record("Repo.TreeDiff %s %s", from, to)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.changes, nil
}

type loopStore struct {
	fakeStore
	dir string
}

func (s *loopStore) Dir(runID string) string { return s.dir }

type fakeLander struct {
	store  Store
	log    *callLog
	failOn int
}

func (f *fakeLander) Land(ctx context.Context, ph Phase) (Landing, error) {
	f.log.record("Land %d", ph.Number)
	if ph.Number == f.failOn {
		return Landing{}, errors.New("gate red")
	}
	l := Landing{Phase: ph.Number, MergeSHA: "merge-" + ph.Title}
	f.store.Append("run-1", Record{Kind: RecordLanding, Landing: &l})
	return l, nil
}

type pathPrompts struct{}

func (pathPrompts) Render(name string, vars map[string]any) (string, string, error) {
	return vars["PlanPath"].(string), "embedded:" + name, nil
}

type loopRig struct {
	shared   *callLog
	host     *agentSim
	repo     *loopRepo
	store    *loopStore
	face     *fakeFace
	notifier *fakeNotifier
	lander   *fakeLander
	loop     *RunLoop
}

func threePhasePlan() Plan {
	item := []Item{{Text: "do it"}}
	return Plan{
		Path: "docs/x/todo.md", Topic: "x",
		Phases: []Phase{
			{Number: 1, Title: "Core types", Items: item, Milestone: 1},
			{Number: 2, Title: "Store", Items: item, Milestone: 1},
			{Number: 3, Title: "Loop", DependsOn: []int{1}, Items: item, Milestone: 1},
			{Number: 4, Title: "Done already", Items: []Item{{Text: "x", Done: true}}, Milestone: 1},
		},
		Milestones: []Milestone{{Number: 1, Name: "Core", Phases: []int{1, 2, 3, 4}}},
	}
}

func newLoopRig(t *testing.T) *loopRig {
	shared := &callLog{}
	r := &loopRig{shared: shared}
	r.repo = &loopRepo{fakeRepo: fakeRepo{callLog: callLog{Shared: shared}, RootDir: t.TempDir(), Branch: "main", SHA: "sha", Tree: "tree"}}
	r.store = &loopStore{fakeStore: fakeStore{callLog: callLog{Shared: shared}}, dir: t.TempDir()}
	r.host = &agentSim{fakeSessionHost: fakeSessionHost{callLog: callLog{Shared: shared}}, behaviour: map[string]string{}, idle: map[string]bool{}, repo: r.repo, store: r.store}
	r.face = &fakeFace{callLog: callLog{Shared: shared}}
	r.notifier = &fakeNotifier{callLog: callLog{Shared: shared}}
	r.lander = &fakeLander{store: r.store, log: shared}
	clock := &stepClock{t: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC), step: time.Minute}
	sm := &SessionManager{
		Host: r.host, Repo: r.repo, Prompts: pathPrompts{}, Store: r.store,
		Resolve: func(provider, model, effort, askURL, mcp string) (ProviderArgs, error) {
			return ProviderArgs{Kind: provider}, nil
		},
		Now: clock.Now, Poll: time.Millisecond, StallGrace: 2 * time.Minute,
	}
	row := StepRow{Provider: "codex", Timeout: 1000 * time.Hour}
	kinds := []StepKind{
		{Name: "plan", Prompt: "plan", Check: "plan-file", Row: row},
		{Name: "implement", Prompt: "implement", Check: "diff", Row: row},
	}
	r.loop = &RunLoop{
		Plan: threePhasePlan(), TodoPath: "docs/x/todo.md", Kinds: kinds, Sessions: sm,
		Store: r.store, Face: r.face, Notifier: r.notifier,
		Hooks:  Hooks{OnHalt: "halt-hook", OnWarn: "warn-hook", OnDone: "done-hook"},
		Lander: r.lander, Runners: DefaultRunners(sm, kinds), RunID: "run-1",
	}
	return r
}

func (r *loopRig) run(opts RunOptions) int {
	return r.loop.Run(context.Background(), opts)
}

func (r *loopRig) calls(prefix string) []string {
	var out []string
	for _, c := range r.shared.Calls() {
		if strings.HasPrefix(c, prefix) {
			out = append(out, strings.TrimPrefix(c, prefix))
		}
	}
	return out
}

func (r *loopRig) events(kind string) []Event {
	var out []Event
	for _, ev := range r.face.Events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func (r *loopRig) kinds() []string {
	var out []string
	for _, ev := range r.face.Events {
		if ev.Kind != "step" {
			out = append(out, ev.Kind)
		}
	}
	return out
}

func (r *loopRig) hooks() []string {
	var out []string
	for i, h := range r.calls("Notifier.Fire ") {
		out = append(out, h+" "+r.notifier.Fired[i]["R_LOOP_STATUS"])
	}
	return out
}

func (r *loopRig) runRecords() []Record {
	var out []Record
	for _, rec := range r.store.Records["run-1"] {
		if rec.Kind == RecordRun {
			out = append(out, rec)
		}
	}
	return out
}

func (r *loopRig) report(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.store.dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCleanRunLandsEveryUntickedPhaseInOrder(t *testing.T) {
	r := newLoopRig(t)

	code := r.run(RunOptions{})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	wantPhase := []string{"phase-start", "phase-state", "assumption", "phase-state", "phase-state", "landed"}
	var want []string
	for range 3 {
		want = append(want, wantPhase...)
	}
	want = append(want, "finished")
	if got := r.kinds(); !reflect.DeepEqual(got, want) {
		t.Errorf("events\n got %v\nwant %v", got, want)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Errorf("landed %v", got)
	}
	if got := r.hooks(); !reflect.DeepEqual(got, []string{"done-hook finished"}) {
		t.Errorf("hooks %v", got)
	}
	var states []string
	for _, ev := range r.events("phase-state") {
		if ev.Phase == 1 {
			states = append(states, ev.Fields["state"])
		}
	}
	if !reflect.DeepEqual(states, []string{"planned", "implemented", "landed"}) {
		t.Errorf("phase 1 states %v", states)
	}
	runs := r.runRecords()
	if len(runs) == 0 || runs[len(runs)-1].Run != RunFinished {
		t.Errorf("run records %+v", runs)
	}
}

func TestEveryStateChangeIsAStepEvent(t *testing.T) {
	r := newLoopRig(t)

	r.run(RunOptions{Phases: []int{2}})

	var got []string
	for _, ev := range r.events("step") {
		got = append(got, ev.Step+" "+ev.Fields["state"])
	}
	want := []string{"plan queued", "plan spawned", "plan running", "plan ok", "implement queued", "implement spawned", "implement running", "implement ok"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("step events\n got %v\nwant %v", got, want)
	}
}

func TestStepEventsAreStoredWithProviderAndWorkspace(t *testing.T) {
	r := newLoopRig(t)

	r.run(RunOptions{Phases: []int{2}})

	var got []string
	for _, rec := range r.store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == "step" && rec.Event.Step == "plan" {
			f := rec.Event.Fields
			got = append(got, f["state"]+" "+f["attempt"]+" "+f["provider"]+" "+f["workspace"])
		}
	}
	want := []string{"queued 1 codex ", "spawned 1 codex ws-1", "running 1 codex ws-1", "ok 1 codex ws-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stored step events\n got %q\nwant %q", got, want)
	}
}

func TestStepEventsCarryModelEffortBackstopAndRounds(t *testing.T) {
	r := newLoopRig(t)
	r.loop.Kinds[0].Row.Model, r.loop.Kinds[0].Row.Effort = "opus", "high"
	r.loop.Kinds[0].Row.Timeout = time.Hour
	r.loop.Kinds[0].Row.Rounds = 2

	r.run(RunOptions{Phases: []int{2}})

	f := r.events("step")[0].Fields
	if f["model"] != "opus" || f["effort"] != "high" || f["backstop"] != "1h0m0s" || f["rounds"] != "2" {
		t.Errorf("step fields %v", f)
	}
}

func TestAReviewRoundIsARunningStepEventNamingTheRound(t *testing.T) {
	r := newLoopRig(t)
	r.loop.runDir = t.TempDir()
	ref := StepRef{Key: StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}, Kind: r.loop.Kinds[1]}
	ref.Kind.Row.Rounds = 3

	(&loopObserver{l: r.loop, ref: ref}).Reviewing(&Session{Workspace: "ws-9"}, 2)

	ev := r.events("step")[0]
	if ev.Phase != 2 || ev.Step != "implement" || ev.Fields["state"] != "running" || ev.Fields["round"] != "2" || ev.Fields["rounds"] != "3" || ev.Fields["workspace"] != "ws-9" {
		t.Errorf("event %+v", ev)
	}
}

func TestStepRefUsesPhaseBranchWorktreeAndBase(t *testing.T) {
	r := newLoopRig(t)

	r.run(RunOptions{Phases: []int{2}})

	want := []string{".r-loop/wt/phase-2 r-loop/phase-2 main", ".r-loop/wt/phase-2 r-loop/phase-2 main"}
	if got := r.calls("Repo.AddWorktree "); !reflect.DeepEqual(got, want) {
		t.Errorf("worktrees %v", got)
	}
	sentinel := r.host.Opened[0].Env["R_LOOP_SENTINEL"]
	if !strings.HasPrefix(sentinel, r.store.dir) {
		t.Errorf("sentinel %s not under run dir %s", sentinel, r.store.dir)
	}
}

func TestOneCommitPerOkStep(t *testing.T) {
	r := newLoopRig(t)
	r.host.behaviour["rloop-p1-implement"] = "fail"

	r.run(RunOptions{})

	var got []string
	for _, c := range r.calls("Repo.CommitAll ") {
		got = append(got, c[strings.Index(c, `"`):])
	}
	want := []string{`"r-loop: phase 1 plan"`, `"r-loop: phase 2 plan"`, `"r-loop: phase 2 implement"`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commits\n got %v\nwant %v", got, want)
	}
}

func TestFailedImplementBlocksThePhaseAndItsDependents(t *testing.T) {
	r := newLoopRig(t)
	r.host.behaviour["rloop-p1-implement"] = "fail"

	code := r.run(RunOptions{})

	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	blocked := r.events("phase-blocked")
	if len(blocked) != 1 {
		t.Fatalf("blocked %+v", blocked)
	}
	f := blocked[0].Fields
	wantWT := filepath.Join(r.repo.RootDir, ".r-loop/wt/phase-1")
	if f["phase"] != "1" || f["reason"] != "tests red" || f["workspace"] != "ws-2" || f["worktree"] != wantWT {
		t.Errorf("phase-blocked fields %v", f)
	}
	skipped := r.events("phase-skipped")
	if len(skipped) != 1 || skipped[0].Fields["phase"] != "3" || skipped[0].Fields["because"] != "1" {
		t.Errorf("phase-skipped %+v", skipped)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"2"}) {
		t.Errorf("landed %v", got)
	}
	for _, c := range r.calls("SessionHost.Open ") {
		if strings.Contains(c, "rloop-p3") {
			t.Errorf("phase 3 spawned: %s", c)
		}
	}
	halt := r.events("halt")
	if len(halt) != 1 || halt[0].Fields["blocked"] != "1, 3" || halt[0].Fields["resume"] != "r-loop resume" {
		t.Errorf("halt %+v", halt)
	}
	if got := r.hooks(); !reflect.DeepEqual(got, []string{"warn-hook blocked", "halt-hook halted"}) {
		t.Errorf("hooks %v", got)
	}
	env := r.notifier.Fired[0]
	if env["R_LOOP_PHASE"] != "1" || env["R_LOOP_STEP"] != "implement" || env["R_LOOP_REASON"] != "tests red" || env["R_LOOP_RUN"] != "run-1" || env["R_LOOP_TODO"] != "docs/x/todo.md" || env["R_LOOP_REPORT"] != filepath.Join(r.store.dir, "report.md") {
		t.Errorf("warn env %v", env)
	}
	runs := r.runRecords()
	if last := runs[len(runs)-1]; last.Run != RunHalted {
		t.Errorf("last run record %+v", last)
	}
}

func TestLandingErrorBlocksThePhase(t *testing.T) {
	r := newLoopRig(t)
	r.lander.failOn = 2

	code := r.run(RunOptions{Phases: []int{2}})

	blocked := r.events("phase-blocked")
	if code != 1 || len(blocked) != 1 || blocked[0].Fields["reason"] != "land: gate red" {
		t.Fatalf("exit %d blocked %+v", code, blocked)
	}
	wantWT := filepath.Join(r.repo.RootDir, ".r-loop/wt/phase-2")
	if f := blocked[0].Fields; f["workspace"] != "ws-2" || f["worktree"] != wantWT {
		t.Errorf("landing block names %v", f)
	}
}

func TestQueuedIsRecordedBeforeTheStepSpawns(t *testing.T) {
	r := newLoopRig(t)

	r.run(RunOptions{Phases: []int{2}})

	var got []string
	for _, rec := range r.store.Records["run-1"] {
		if rec.Kind == RecordStep && rec.Step.Kind == "plan" {
			got = append(got, string(rec.State))
		}
	}
	if len(got) < 2 || got[0] != "queued" || got[1] != "spawned" {
		t.Errorf("plan step records %v", got)
	}
}

type abortingLander struct {
	fakeLander
}

func (a *abortingLander) Land(ctx context.Context, ph Phase) (Landing, error) {
	a.store.MarkAbort("run-1")
	return a.fakeLander.Land(ctx, ph)
}

func TestAbortBetweenPhasesStopsScheduling(t *testing.T) {
	r := newLoopRig(t)
	r.loop.Lander = &abortingLander{fakeLander: *r.lander}

	code := r.run(RunOptions{})

	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if n := len(r.host.Opened); n != 2 {
		t.Errorf("%d sessions opened after the abort, want 2", n)
	}
}

type abortingRunner struct{ store Store }

func (a abortingRunner) Run(ctx context.Context, ref StepRef, obs Observer) Outcome {
	a.store.MarkAbort("run-1")
	return Outcome{State: StepOK}
}

func TestAbortBeforeLandingDoesNotLand(t *testing.T) {
	r := newLoopRig(t)
	r.loop.Runners = map[string]StepRunner{"plan-file": abortingRunner{store: r.store}}

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if got := r.calls("Land "); len(got) != 0 {
		t.Errorf("landed after abort: %v", got)
	}
	if got := r.calls("SessionHost.Open "); len(got) != 0 {
		t.Errorf("spawned after abort: %v", got)
	}
}

func TestAssumptionsAreEmittedAfterThePlanStepAndReported(t *testing.T) {
	r := newLoopRig(t)

	r.run(RunOptions{Phases: []int{2}})

	got := r.events("assumption")
	if len(got) != 1 || got[0].Phase != 2 || got[0].Fields["phase"] != "2" || got[0].Fields["text"] != "phase 2 keeps state in memory" {
		t.Fatalf("assumptions %+v", got)
	}
	if rep := r.report(t); !strings.Contains(rep, "## Assumptions\n\n### Phase 2\n\n- phase 2 keeps state in memory\n") {
		t.Errorf("report:\n%s", rep)
	}
}

func TestResumeSkipsLandedPhasesAndOkStepsAndRerunsTheStoppedStep(t *testing.T) {
	r := newLoopRig(t)
	r.store.Append("run-1", Record{Kind: RecordStep, Step: &StepKey{Run: "run-1", Phase: 1, Kind: "plan", Attempt: 1}, State: StepOK})
	r.store.Append("run-1", Record{Kind: RecordStep, Step: &StepKey{Run: "run-1", Phase: 1, Kind: "implement", Attempt: 1}, State: StepFailed})
	r.store.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: 2, MergeSHA: "m2"}})

	code := r.run(RunOptions{Resume: true})

	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var agents []string
	for _, o := range r.host.Opened {
		agents = append(agents, o.Label)
	}
	want := []string{"rloop-p1-implement-a2", "rloop-p3-plan", "rloop-p3-implement"}
	if !reflect.DeepEqual(agents, want) {
		t.Errorf("spawned %v, want %v", agents, want)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"1", "3"}) {
		t.Errorf("landed %v", got)
	}
	human := r.events("human")
	if len(human) != 1 || human[0].Fields["what"] != "resume" {
		t.Errorf("human events %+v", human)
	}
}

func TestStallThatIgnoresTheNudgeFailsWithExit3(t *testing.T) {
	r := newLoopRig(t)
	r.host.behaviour["rloop-p2-plan"] = "stall"

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	stalled := r.events("stalled")
	wantWT := filepath.Join(r.repo.RootDir, ".r-loop/wt/phase-2")
	if len(stalled) != 1 || stalled[0].Fields["workspace"] != "ws-1" || stalled[0].Fields["worktree"] != wantWT {
		t.Errorf("stalled %+v", stalled)
	}
	blocked := r.events("phase-blocked")
	if len(blocked) != 1 || blocked[0].Fields["reason"] != "stalled: no response to nudge" {
		t.Errorf("blocked %+v", blocked)
	}
}

func TestFirstBlockDecidesTheExitCode(t *testing.T) {
	r := newLoopRig(t)
	r.host.behaviour["rloop-p1-implement"] = "fail"
	r.host.behaviour["rloop-p2-plan"] = "stall"

	if code := r.run(RunOptions{}); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
}

func TestAbortMidStepStopsTheRunNamingTheLiveSession(t *testing.T) {
	r := newLoopRig(t)
	r.host.behaviour["rloop-p1-implement"] = "abort"

	code := r.run(RunOptions{})

	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	runs := r.runRecords()
	last := runs[len(runs)-1]
	if last.Run != RunHalted || last.Reason != ReasonAborted {
		t.Errorf("last run record %+v", last)
	}
	aborted := r.events("aborted")
	wantWT := filepath.Join(r.repo.RootDir, ".r-loop/wt/phase-1")
	if len(aborted) != 1 || aborted[0].Fields["workspace"] != "ws-2" || aborted[0].Fields["worktree"] != wantWT {
		t.Errorf("aborted %+v", aborted)
	}
	if n := len(r.host.Opened); n != 2 {
		t.Errorf("%d sessions opened, want 2", n)
	}
	if got := r.calls("SessionHost.Interrupt "); len(got) != 0 {
		t.Errorf("session interrupted: %v", got)
	}
	if got := r.calls("Notifier.Fire "); len(got) != 0 {
		t.Errorf("hooks fired on abort: %v", got)
	}
}

func TestFromNarrowsTheRunList(t *testing.T) {
	r := newLoopRig(t)

	r.run(RunOptions{From: 2})

	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"2", "3"}) {
		t.Errorf("landed %v", got)
	}
}

func TestPhasesNarrowsTheRunList(t *testing.T) {
	r := newLoopRig(t)

	code := r.run(RunOptions{Phases: []int{3, 1}})

	if got := r.calls("Land "); code != 0 || !reflect.DeepEqual(got, []string{"1", "3"}) {
		t.Errorf("exit %d landed %v", code, got)
	}
}

func TestPhasesNamingATickedOrAbsentPhaseIsExit2(t *testing.T) {
	for _, n := range []int{4, 9} {
		r := newLoopRig(t)

		code := r.run(RunOptions{Phases: []int{1, n}})

		if code != 2 {
			t.Errorf("phase %d: exit %d, want 2", n, code)
		}
		errs := r.events("error")
		if len(errs) != 1 || !strings.Contains(errs[0].Fields["reason"], "phase "+strconv.Itoa(n)) {
			t.Errorf("phase %d: error events %+v", n, errs)
		}
		if len(r.host.Opened) != 0 {
			t.Errorf("phase %d: spawned %d sessions", n, len(r.host.Opened))
		}
	}
}

func TestReportSectionsOfAFailedRun(t *testing.T) {
	r := newLoopRig(t)
	r.host.behaviour["rloop-p1-implement"] = "fail"

	r.run(RunOptions{})

	rep := r.report(t)
	wantWT := filepath.Join(r.repo.RootDir, ".r-loop/wt/phase-1")
	for _, want := range []string{
		"human touches: 0\n",
		"## Automatic decisions\n\n- phase 1 blocked: tests red\n- phase 3 skipped: depends on blocked phase 1\n",
		"## Landed\n\n- phase 2 merge-Store\n",
		"## Halt\n\n- phase 1 implement: tests red — workspace ws-2, worktree " + wantWT + "\n- phase 3 skipped: depends on blocked phase 1\n\nr-loop resume\n",
		"## Assumptions\n\n### Phase 1\n\n- phase 1 keeps state in memory\n\n### Phase 2\n\n- phase 2 keeps state in memory\n",
	} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
	if !strings.HasPrefix(rep, "human touches: 0\n") {
		t.Errorf("report does not open with human touches:\n%s", rep)
	}
	for _, absent := range []string{"## Questions", "## Signals", "## Remedies", "## Findings"} {
		if strings.Contains(rep, absent) {
			t.Errorf("empty section %s present", absent)
		}
	}
}

func TestReportCountsHumanTouchesAndListsAutomaticDecisions(t *testing.T) {
	ev := func(kind string, phase int, step string, fields map[string]string) Event {
		return Event{Kind: kind, Phase: phase, Step: step, Fields: fields}
	}
	st := RunState{
		ID: "run-1", Status: RunFinished,
		Events: []Event{
			ev("human", 0, "", map[string]string{"what": "resume"}),
			ev("human", 1, "implement", map[string]string{"what": "answer"}),
			ev("human", 1, "implement", map[string]string{"what": "consent"}),
			ev("nudge", 1, "implement", nil),
			ev("restart", 1, "implement", map[string]string{"step": "phase-1/implement", "attempt": "2", "addendum": "use the fake", "provider": "claude", "model": "sonnet", "remedy": "rm lock"}),
			ev("gate-fix", 1, "", map[string]string{"phase": "1", "round": "1"}),
			ev("warning", 1, "implement", map[string]string{"reason": "review round limit reached; round 3 fixes unreviewed"}),
			ev("warning", 1, "implement", map[string]string{"reason": "files outside plan"}),
			ev("gate-skipped", 2, "", nil),
			ev("finding", 1, "implement", map[string]string{"step": "implement", "round": "1", "reviewer": "codex", "id": "codex-r1-1", "title": "nil map", "verdict": "real", "severity": "P1", "fixed": "true", "evidence": "x.go:3"}),
		},
		Questions: []Question{
			{ID: "q1", Step: StepKey{Phase: 1, Kind: "implement"}, Text: "which db?", Answer: "sqlite", AnsweredBy: "timeout"},
			{ID: "q2", Step: StepKey{Phase: 1, Kind: "implement"}, Text: "keep api?", Answer: "yes", AnsweredBy: "maintainer"},
		},
		Signals:  []Signal{{Kind: SignalWarn, Source: SourceWatchdog, Step: StepKey{Phase: 1, Kind: "implement"}, Reason: "drifting"}},
		Remedies: []Remedy{{Step: StepKey{Phase: 1, Kind: "implement"}, Class: "lock", Command: "rm lock", Consent: "authorised"}},
		Landed:   []Landing{{Phase: 2, MergeSHA: "abc", GateSkipped: true}},
	}

	rep := Report(st, threePhasePlan())

	for _, want := range []string{
		"human touches: 3\n",
		"## Automatic decisions\n\n" +
			"- phase 1 implement: nudge\n" +
			"- phase 1 implement: restart as attempt 2 on claude model sonnet effort provider default — use the fake (remedy: rm lock)\n" +
			"- phase 1: gate-fix round 1\n" +
			"- phase 1 implement: review round limit reached; round 3 fixes unreviewed\n" +
			"- q1 phase 1 implement: timed out; the agent took sqlite\n",
		"## Landed\n\n- phase 2 abc gate skipped\n",
		"## Questions\n\n- q1 phase 1 implement: which db? → sqlite (timeout, waited 0s)\n- q2 phase 1 implement: keep api? → yes (maintainer, waited 0s)\n",
		"## Signals\n\n- warn from watchdog, phase 1 implement: drifting\n",
		"## Remedies\n\n- phase 1 implement: lock `rm lock` — authorised; no restart\n",
		"## Findings\n\n- phase 1 implement r1 codex codex-r1-1 nil map: real P1 fixed true — x.go:3\n",
		"## Skips\n\n- phase 2: gate-skipped\n",
	} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
	if strings.Contains(rep, "files outside plan") && strings.Index(rep, "files outside plan") < strings.Index(rep, "## Landed") {
		t.Errorf("a watchdog warning is listed as an automatic decision:\n%s", rep)
	}
	if strings.Contains(rep, "## Halt") {
		t.Errorf("finished run has a Halt section:\n%s", rep)
	}
}

func TestReportQuestionsNameTheAnswererCitationAndWaitAndSkipsListAskNonePerStep(t *testing.T) {
	asked := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	ev := func(kind string, phase int, step string, fields map[string]string) Event {
		return Event{Kind: kind, Phase: phase, Step: step, Fields: fields}
	}
	st := RunState{
		ID: "run-1", Status: RunRunning,
		Events: []Event{
			ev("ask-none", 1, "plan", map[string]string{"provider": "gemini"}),
			ev("ask-none", 1, "plan", map[string]string{"provider": "gemini"}),
			ev("ask-none", 2, "implement", map[string]string{"provider": "gemini"}),
		},
		Questions: []Question{
			{ID: "q1", Step: StepKey{Phase: 1, Kind: "implement"}, Text: "which db?", Answer: "postgres", AnsweredBy: "maintainer", AskedAt: asked, AnsweredAt: asked.Add(4 * time.Minute)},
			{ID: "q2", Step: StepKey{Phase: 1, Kind: "implement-rv-codex"}, Text: "keep api?", Answer: "yes", AnsweredBy: "watchdog", Citation: "spec.html#adr-12", AskedAt: asked, AnsweredAt: asked.Add(30 * time.Second)},
			{ID: "q3", Step: StepKey{Phase: 2, Kind: "plan"}, Text: "rename?", AskedAt: asked},
		},
	}

	rep := Report(st, threePhasePlan())

	for _, want := range []string{
		"## Questions\n\n" +
			"- q1 phase 1 implement: which db? → postgres (maintainer, waited 4m0s)\n" +
			"- q2 phase 1 implement-rv-codex: keep api? → yes (watchdog, cites spec.html#adr-12, waited 30s)\n" +
			"- q3 phase 2 plan: rename? (open)\n",
		"## Skips\n\n- phase 1 plan: ask: none (gemini)\n- phase 2 implement: ask: none (gemini)\n",
	} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
}

func TestStepVarsFillsEveryTemplateVariable(t *testing.T) {
	plan := threePhasePlan()
	ph := plan.Phases[2]
	ph.Title = "RunLoop: phases, steps, halts and the report"
	ph.Block = "### Phase 3 — RunLoop"
	ph.Items = []Item{{Text: "loop", Done: false}, {Text: "done", Done: true}, {Text: "report"}}
	ref := StepRef{Key: StepKey{Run: "run-1", Phase: 3, Kind: "plan", Attempt: 1}, Phase: ph, Worktree: ".r-loop/wt/phase-3", Branch: "r-loop/phase-3", Base: "main"}

	vars := StepVars(ref, plan, "docs/x/todo.md", "/runs/run-1")

	want := map[string]any{
		"PhaseNumber": 3, "PhaseTitle": ph.Title, "PhaseBlock": "### Phase 3 — RunLoop",
		"Criteria": "- [ ] loop\n- [ ] report", "TodoPath": "docs/x/todo.md", "SpecDir": "docs/x",
		"PlanPath": ".task-plans/phase-3-runloop-phases-steps-halts-and-the-re.md",
		"Branch":   "r-loop/phase-3", "Base": "main", "Worktree": ".r-loop/wt/phase-3", "Sentinel": "",
		"RunDir": "/runs/run-1", "AskURL": "", "PhaseWarnings": "", "ReviewedKind": "", "Round": 0, "Rounds": 0,
		"ReviewCommand": "", "FindingsPath": "", "FindingsFiles": []FindingsFile(nil), "PriorFindings": "", "PriorVerdicts": "",
		"RoundTree": "", "VerdictPath": "", "ReportPath": "", "MilestoneName": "Core", "MilestonePhases": "1, 2, 3, 4",
		"Addendum": "",
	}
	if !reflect.DeepEqual(vars, want) {
		for k, v := range want {
			if !reflect.DeepEqual(vars[k], v) {
				t.Errorf("%s = %#v, want %#v", k, vars[k], v)
			}
		}
		for k := range vars {
			if _, ok := want[k]; !ok {
				t.Errorf("unexpected var %s", k)
			}
		}
	}
	if n := len(vars["PlanPath"].(string)); n > 60 {
		t.Errorf("PlanPath is %d characters", n)
	}
}

func TestPlanPathIsTheKebabTitle(t *testing.T) {
	ref := StepRef{Phase: Phase{Number: 7, Title: "Prompt  Renderer — v2!"}}

	got := StepVars(ref, Plan{}, "todo.md", "")["PlanPath"]

	if got != ".task-plans/phase-7-prompt-renderer-v2.md" {
		t.Errorf("PlanPath %v", got)
	}
}

func TestDefaultRunnersAreKeyedByCheck(t *testing.T) {
	sm := &SessionManager{}
	kinds := []StepKind{{Name: "plan", Check: "plan-file"}, {Name: "implement", Check: "diff"}}

	runners := DefaultRunners(sm, kinds)

	keys := make([]string, 0, len(runners))
	for k := range runners {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !reflect.DeepEqual(keys, []string{"diff", "plan-file"}) {
		t.Errorf("runner keys %v", keys)
	}
}

func TestLoopFallsBackToTheSingleRunner(t *testing.T) {
	r := newLoopRig(t)
	r.loop.Runners = nil

	if code := r.run(RunOptions{Phases: []int{2}}); code != 0 {
		t.Errorf("exit %d", code)
	}
}

func TestReviewHookRunsOnlyForAnOkStepWithReviewersAndRounds(t *testing.T) {
	cases := []struct {
		name      string
		reviewers []Reviewer
		rounds    int
		behaviour string
		want      int
	}{
		{"reviewers and rounds", []Reviewer{{Provider: "claude"}}, 2, "", 1},
		{"no reviewers", nil, 2, "", 0},
		{"no rounds", []Reviewer{{Provider: "claude"}}, 0, "", 0},
		{"failed step", []Reviewer{{Provider: "claude"}}, 2, "fail", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newLoopRig(t)
			r.host.behaviour["rloop-p2-implement"] = c.behaviour
			kind := r.loop.Kinds[1]
			kind.Row.Reviewers, kind.Row.Rounds = c.reviewers, c.rounds
			r.loop.Kinds[1] = kind
			calls := 0
			r.loop.Runners = map[string]StepRunner{"diff": singleRunner{sm: r.loop.Sessions, review: func(ctx context.Context, ref StepRef, s *Session, obs Observer) Outcome {
				calls++
				return Outcome{State: StepOK, Session: s}
			}}}

			r.run(RunOptions{Phases: []int{2}})

			if calls != c.want {
				t.Errorf("review called %d times, want %d", calls, c.want)
			}
		})
	}
}

func TestAGateFixReviewRoundIsARunningStepEventNamingTheRound(t *testing.T) {
	store := &fakeStore{}
	face := &fakeFace{}
	s := &Session{Workspace: "ws-5", Ref: StepRef{
		Key:  StepKey{Run: "run-1", Phase: 3, Kind: "gatefix", Attempt: 1},
		Kind: StepKind{Name: "gatefix", Row: StepRow{Provider: "codex", Model: "gpt-5", Effort: "high", Timeout: time.Hour, Rounds: 1}},
	}}

	stepRecorder{store, face}.Reviewing(s, 1)

	f := face.Events[0].Fields
	if face.Events[0].Kind != "step" || f["state"] != "running" || f["round"] != "1" || f["rounds"] != "1" || f["model"] != "gpt-5" || f["effort"] != "high" || f["backstop"] != "1h0m0s" || f["workspace"] != "ws-5" {
		t.Fatalf("event %+v", face.Events)
	}
}

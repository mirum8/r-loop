package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/gitrepo"
	"r-loop/internal/store"
)

const resumeTodo = `# t

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

const noReviewConfig = `steps:
  plan:
    rounds: 0
  implement:
    rounds: 0
`

var (
	sentinelRe = regexp.MustCompile("[^\\s`]+\\.sentinel")
	planRe     = regexp.MustCompile(`\.task-plans/[a-z0-9-]+\.md`)
	findingsRe = regexp.MustCompile("into `([^`]+-findings-([a-z0-9]+)-r[0-9]+\\.json)`")
)

type simHost struct {
	mu      sync.Mutex
	opened  map[string]core.OpenSpec
	panes   map[string]core.OpenSpec
	started []string
	prompts []string
	texts   map[string]string
	fail    map[string]bool
	hang    map[string]bool
	edit    map[string]string
	ws      int
}

func newSim() *simHost {
	return &simHost{opened: map[string]core.OpenSpec{}, panes: map[string]core.OpenSpec{}, texts: map[string]string{}, fail: map[string]bool{}, hang: map[string]bool{}, edit: map[string]string{}}
}

func (h *simHost) Reachable() error { return nil }

func (h *simHost) Open(spec core.OpenSpec) (core.Workspace, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ws++
	pane := "p" + strconv.Itoa(h.ws)
	h.panes[pane] = spec
	return core.Workspace{ID: "w" + strconv.Itoa(h.ws), RootPane: pane}, nil
}

func (h *simHost) Start(pane, name, kind string, args []string) (core.Agent, error) {
	h.mu.Lock()
	h.started = append(h.started, name)
	if spec, ok := h.panes[pane]; ok {
		h.opened[name] = spec
	}
	h.mu.Unlock()
	return core.Agent{Name: name, Pane: pane}, nil
}

func (h *simHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	if strings.HasPrefix(text, "r-loop:") {
		return nil
	}
	h.mu.Lock()
	h.prompts = append(h.prompts, agent)
	h.texts[agent] = text
	spec, fail, hang, edit := h.opened[agent], h.fail[agent], h.hang[agent], h.edit[agent]
	h.mu.Unlock()
	sentinel := sentinelRe.FindString(text)
	switch {
	case hang:
		return nil
	case strings.Contains(agent, "-rv-"):
		m := findingsRe.FindStringSubmatch(text)
		writeTo(m[1], `{"reviewer":"`+m[2]+`","findings":[]}`)
	case fail:
		wip := "wip.txt"
		if spec.Env["R_LOOP_STEP"] == "plan" {
			wip = planRe.FindString(text)
		}
		writeTo(filepath.Join(spec.CWD, wip), "half done by "+agent)
		writeTo(sentinel, `{"outcome":"failed","reason":"tests red","at":"2026-09-18T10:05:00Z"}`)
		return nil
	case spec.Env["R_LOOP_STEP"] == "plan":
		writeTo(filepath.Join(spec.CWD, planRe.FindString(text)), "status: planned\n\n## Summary\nx\n## Changes\nx\n## Tests\n- a test by "+agent+"\n## Assumptions\nnone\n")
	default:
		name := "code.txt"
		if edit != "" {
			name = edit
		}
		writeTo(filepath.Join(spec.CWD, name), "written by "+agent)
	}
	writeTo(sentinel, `{"outcome":"ok","reason":"","at":"2026-09-18T10:05:00Z"}`)
	return nil
}

func (h *simHost) State(agent string) (core.AgentState, error)            { return core.AgentWorking, nil }
func (h *simHost) AgentPane(agent string) (string, error)                 { return "", nil }
func (h *simHost) Read(agent string, lines int) (string, error)           { return "", nil }
func (h *simHost) Interrupt(agent string) error                           { return nil }
func (h *simHost) Tag(workspaceID string, tokens map[string]string) error { return nil }
func (h *simHost) Close(workspaceID string) error                         { return nil }
func (h *simHost) ClosePane(pane string) error                            { return nil }
func (h *simHost) Split(pane, direction, cwd string) (string, error)      { return pane + "-split", nil }

func (h *simHost) promptedAgents() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.prompts)
}

func (h *simHost) startedAgents() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.started)
}

func (h *simHost) text(agent string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.texts[agent]
}

func writeTo(path, body string) {
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(body), 0o644)
}

type landRecorder struct {
	st     *store.Store
	mu     sync.Mutex
	landed []string
}

func (l *landRecorder) Land(ctx context.Context, ph core.Phase) (core.Landing, error) {
	l.mu.Lock()
	l.landed = append(l.landed, ph.ID)
	l.mu.Unlock()
	landing := core.Landing{Phase: ph.ID}
	id, _, _ := l.st.Current()
	return landing, l.st.Append(id, core.Record{Kind: core.RecordLanding, At: time.Now(), Landing: &landing})
}

func newResumeFixture(t *testing.T, config string) *fixture {
	t.Helper()
	f := newFixture(t)
	f.write("docs/topic/todo.md", resumeTodo)
	f.write(".r-loop/config.yaml", config)
	f.commit()
	f.env.PID = os.Getpid()
	f.env.Pane = "driver-pane"
	return f
}

func (f *fixture) sim(w *Wiring, sim *simHost) *landRecorder {
	w.Loop.Sessions.Host = sim
	w.Loop.Sessions.Poll = 5 * time.Millisecond
	w.Dog.Host = answeringDog(w, "sqlite", "docs/topic/todo.md:1")
	w.Loop.RemedyWindow = 0
	lander := &landRecorder{st: w.Store}
	w.Loop.Lander = lander
	return lander
}

func (f *fixture) firstRun(sim *simHost, args ...string) (string, int) {
	f.t.Helper()
	w, err := f.preflight(append([]string{f.todo, "--plain"}, args...)...)
	if err != nil {
		f.t.Fatal(err)
	}
	f.sim(w, sim)
	code := w.Execute(core.RunOptions{From: w.Opts.From, Phases: w.Opts.Phases})
	return w.Loop.RunID, code
}

func (f *fixture) resume(sim *simHost, args ...string) (int, *landRecorder, error) {
	f.t.Helper()
	f.out.Reset()
	w, opts, err := PrepareResume(args, f.env)
	if err != nil {
		return 0, nil, err
	}
	lander := f.sim(w, sim)
	return w.Execute(opts), lander, nil
}

func (f *fixture) load(id string) core.RunState {
	f.t.Helper()
	st, err := store.New(f.root).Load(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func stepEvents(st core.RunState, kind string) []core.Event {
	var out []core.Event
	for _, e := range st.Events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestResumeAfterFailedImplementRerunsOnlyImplementAsAttempt2OverItsWork(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	id, code := f.firstRun(first, "--phases", "1")
	if code != 1 {
		t.Fatalf("first run exit %d\n%s", code, f.err)
	}

	sim := newSim()
	code, lander, err := f.resume(sim)

	if err != nil || code != 0 {
		t.Fatalf("resume code=%d err=%v\n%s", code, err, f.out)
	}
	if got := sim.promptedAgents(); !slices.Equal(got, []string{"rloop-p1-implement-a2"}) {
		t.Fatalf("prompted %v", got)
	}
	if st := f.load(id); st.Steps[core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 2}] != core.StepOK {
		t.Fatalf("attempt 2 = %v", st.Steps)
	}
	if !slices.Equal(lander.landed, []string{"1"}) {
		t.Fatalf("landed %v", lander.landed)
	}
	if b := git(t, f.root, "show", "r-loop/phase-1:wip.txt"); b != "half done by rloop-p1-implement" {
		t.Fatalf("attempt 1 work lost: %q", b)
	}
	if log := git(t, f.root, "log", "--format=%s", "-1", "r-loop/phase-1"); log != "r-loop: phase 1 implement" {
		t.Fatalf("last commit %q", log)
	}
	out := f.out.String()
	banner := strings.Index(out, "previous session rloop-p1-implement left in workspace w2")
	if banner < 0 || banner > strings.Index(out, "face: plain") {
		t.Fatalf("resume banner missing or below the banner:\n%s", out)
	}
	if id2, _, ok := store.New(f.root).Current(); ok {
		t.Fatalf("current left set to %s", id2)
	}
}

func TestResumeClaimsATreeMatchingTheRecordedSnapshot(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	f.firstRun(first, "--phases", "1")
	wt := filepath.Join(f.root, ".r-loop/wt/phase-1")
	if dirty := git(t, wt, "status", "--porcelain"); !strings.Contains(dirty, "wip.txt") {
		t.Fatalf("expected leftovers, got %q", dirty)
	}

	code, _, err := f.resume(newSim())

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestResumeRefusesATreeEditedAfterTheSnapshotNamingTheFile(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	f.firstRun(first, "--phases", "1")
	f.write(".r-loop/wt/phase-1/notes.txt", "mine")
	sim := newSim()

	_, _, err := f.resume(sim)

	if code := exitCode(t, err); code != 2 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	for _, want := range []string{"notes.txt", ".r-loop/wt/phase-1", "commit or discard them, then resume"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q missing from %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "wip.txt") {
		t.Fatalf("recorded work named as unclaimed: %v", err)
	}
	if len(sim.startedAgents()) != 0 {
		t.Fatalf("agents started: %v", sim.startedAgents())
	}
	if b, _ := os.ReadFile(filepath.Join(f.root, ".r-loop/wt/phase-1/notes.txt")); string(b) != "mine" {
		t.Fatal("unclaimed file touched")
	}
}

func TestResumeRefusesUnrecordedChangesInAWorktreeWithNoSnapshot(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	id, _ := f.firstRun(first, "--phases", "1")
	events := filepath.Join(store.New(f.root).Dir(id), "events.jsonl")
	b, _ := os.ReadFile(events)
	var kept []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.Contains(line, `"Kind":"snapshot"`) {
			kept = append(kept, line)
		}
	}
	os.WriteFile(events, []byte(strings.Join(kept, "\n")+"\n"), 0o644)

	_, _, err := f.resume(newSim())

	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), "wip.txt") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestResumeRunsBothHaltedPhasesInOrderThenTheSkippedDependent(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	first.fail["rloop-p2-implement"] = true
	first.edit["rloop-p2-implement"] = "other.txt"
	id, code := f.firstRun(first)
	if code != 1 {
		t.Fatalf("first run exit %d", code)
	}
	if skipped := stepEvents(f.load(id), "phase-skipped"); len(skipped) != 1 || skipped[0].Phase != "3" {
		t.Fatalf("skipped %+v", skipped)
	}
	f.write(".r-loop/wt/phase-2/notes.txt", "mine")

	_, _, err := f.resume(newSim())
	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), ".r-loop/wt/phase-2") {
		t.Fatalf("claim check skipped phase 2: code=%d err=%v", code, err)
	}
	os.Remove(filepath.Join(f.root, ".r-loop/wt/phase-2/notes.txt"))

	sim := newSim()
	code, lander, err := f.resume(sim)

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v\n%s", code, err, f.out)
	}
	want := []string{"rloop-p1-implement-a2", "rloop-p2-implement-a2", "rloop-p3-plan", "rloop-p3-implement"}
	if got := sim.promptedAgents(); !slices.Equal(got, want) {
		t.Fatalf("prompted %v, want %v", got, want)
	}
	if !slices.Equal(lander.landed, []string{"1", "2", "3"}) {
		t.Fatalf("landed %v", lander.landed)
	}
	out := f.out.String()
	for _, want := range []string{"previous session rloop-p1-implement left in workspace", "previous session rloop-p2-implement left in workspace"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing:\n%s", want, out)
		}
	}
}

const reviewConfig = `steps:
  plan:
    rounds: 0
  implement:
    rounds: 3
`

func TestResumeDuringAReviewContinuesAtTheRecordedRound(t *testing.T) {
	f := newResumeFixture(t, reviewConfig)
	st := store.New(f.root)
	id, err := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureExcluded(f.root); err != nil {
		t.Fatal(err)
	}
	repo, err := gitrepo.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddWorktree(".r-loop/wt/phase-1", "r-loop/phase-1", "main"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(f.root, ".r-loop/wt/phase-1")
	f.write(".r-loop/wt/phase-1/code.txt", "work")
	tree, err := repo.Snapshot(wt)
	if err != nil {
		t.Fatal(err)
	}
	plan := core.StepKey{Run: id, Phase: "1", Kind: "plan", Attempt: 1}
	impl := core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 1}
	for _, r := range []core.Record{
		ev(t0, "run-list", 0, "", map[string]string{"phases": "1"}),
		{Kind: core.RecordStep, Step: &plan, State: core.StepOK},
		{Kind: core.RecordStep, Step: &impl, State: core.StepRunning},
		ev(t0, "step", 1, "implement", map[string]string{"state": "running", "attempt": "1", "workspace": "w7"}),
		ev(t0, "review-round", 1, "implement", map[string]string{"step": "implement", "attempt": "1", "round": "1", "tree": "tree-r1"}),
		ev(t0, "review-round", 1, "implement", map[string]string{"step": "implement", "attempt": "1", "round": "2", "tree": tree}),
		{Kind: core.RecordStep, Step: &impl, State: core.StepFailed, Reason: "reviewer claude: backstop"},
		{Kind: core.RecordRun, Run: core.RunHalted},
	} {
		if err := st.Append(id, r); err != nil {
			t.Fatal(err)
		}
	}
	sim := newSim()

	code, _, err := f.resume(sim)

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v\n%s", code, err, f.out)
	}
	started := sim.startedAgents()
	if !slices.Contains(started, "rloop-p1-implement-a2") || !slices.Contains(started, "rloop-p1-implement-rv-clau-r2-a2") {
		t.Fatalf("started %v", started)
	}
	if slices.Contains(started, "rloop-p1-implement-rv-clau-r1-a2") {
		t.Fatalf("round 1 re-run: %v", started)
	}
	if slices.Contains(sim.promptedAgents(), "rloop-p1-implement-a2") {
		t.Fatalf("work half re-run: %v", sim.promptedAgents())
	}
	rv := sim.text("rloop-p1-implement-rv-clau-r2-a2")
	if !strings.Contains(rv, "implement-findings-claude-r1.json") || !strings.Contains(rv, "tree-r1") {
		t.Fatalf("round 2 reviewer lacks round 1 context:\n%s", rv)
	}
	var rounds []string
	for _, e := range stepEvents(f.load(id), "review-round") {
		if e.Fields["attempt"] == "2" {
			rounds = append(rounds, e.Fields["round"])
		}
	}
	if !slices.Equal(rounds, []string{"2"}) {
		t.Fatalf("attempt 2 rounds %v", rounds)
	}
	if !strings.Contains(f.out.String(), "previous session rloop-p1-implement left in workspace w7") {
		t.Fatalf("banner:\n%s", f.out)
	}
}

func TestReplanRerunsPlanWithTheAddendumThenImplement(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	id, _ := f.firstRun(first, "--phases", "1")
	sim := newSim()

	code, _, err := f.resume(sim, "--replan")

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v\n%s", code, err, f.out)
	}
	if got := sim.promptedAgents(); !slices.Equal(got, []string{"rloop-p1-plan-a2", "rloop-p1-implement-a2"}) {
		t.Fatalf("prompted %v", got)
	}
	plan := sim.text("rloop-p1-plan-a2")
	if !strings.Contains(plan, "Note from the previous attempt:") || !strings.Contains(plan, "tests red") {
		t.Fatalf("plan prompt lacks the addendum:\n%s", plan)
	}
	if strings.Contains(sim.text("rloop-p1-implement-a2"), "Note from the previous attempt:") {
		t.Fatal("implement got the addendum")
	}
	if log := git(t, f.root, "log", "--format=%s", "r-loop/phase-1", "--", "wip.txt"); log != "r-loop: phase 1 implement" {
		t.Fatalf("failed work committed by %q", log)
	}
	st := f.load(id)
	for _, k := range []core.StepKey{{Run: id, Phase: "1", Kind: "plan", Attempt: 2}, {Run: id, Phase: "1", Kind: "implement", Attempt: 2}} {
		if st.Steps[k] != core.StepOK {
			t.Fatalf("%v = %q", k, st.Steps[k])
		}
	}
}

func TestReplanOnAPhaseStoppedAtPlanIsAnOrdinaryResume(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-plan"] = true
	f.firstRun(first, "--phases", "1")
	sim := newSim()

	code, _, err := f.resume(sim, "--replan")

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if got := sim.promptedAgents(); !slices.Equal(got, []string{"rloop-p1-plan-a2", "rloop-p1-implement"}) {
		t.Fatalf("prompted %v", got)
	}
	if strings.Contains(sim.text("rloop-p1-plan-a2"), "Note from the previous attempt:") {
		t.Fatal("ordinary resume carried an addendum")
	}
}

func TestResumeRefusedOnAFinishedRun(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	_, code := f.firstRun(newSim(), "--phases", "1")
	if code != 0 {
		t.Fatalf("first run exit %d", code)
	}

	_, _, err := f.resume(newSim())

	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), "nothing to resume") {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if got := Main([]string{"resume"}, f.env); got != 2 {
		t.Fatalf("Main resume exit %d", got)
	}
}

func TestResumeRefusedWhileTheRunIsLive(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	st := store.New(f.root)
	id, err := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	st.SetCurrent(id, os.Getpid())

	_, _, err = f.resume(newSim())

	want := fmt.Sprintf("run %s is live in pid %d", id, os.Getpid())
	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), want) {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestResumeReadsTheNewestRunWhenCurrentIsAbsent(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	if err := store.EnsureExcluded(f.root); err != nil {
		t.Fatal(err)
	}
	st := store.New(f.root)
	old, _ := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	st.Append(old, core.Record{Kind: core.RecordRun, Run: core.RunFinished})
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	id, _ := f.firstRun(first, "--phases", "1")
	if id == old {
		t.Fatal("same run id")
	}

	code, _, err := f.resume(newSim())

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if f.load(id).Status != core.RunFinished {
		t.Fatal("newest run not resumed")
	}
}

func TestAbortEndsALiveRunNamingTheSessionAndClearsCurrent(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.hang["rloop-p1-plan"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	for deadline := time.Now().Add(5 * time.Second); !slices.Contains(sim.promptedAgents(), "rloop-p1-plan"); {
		if time.Now().After(deadline) {
			t.Fatal("plan never prompted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var out, errOut strings.Builder
	env := f.env
	env.Stdout, env.Stderr = &out, &errOut

	abort := Main([]string{"abort"}, env)

	if abort != 0 {
		t.Fatalf("abort exit %d: %s", abort, errOut.String())
	}
	want := "abort requested for run " + w.Loop.RunID + "; the live step's session and worktree are left standing"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("abort printed %q", out.String())
	}
	var code int
	select {
	case code = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
	if code != 1 {
		t.Fatalf("run exit %d", code)
	}
	aborted := stepEvents(f.load(w.Loop.RunID), "aborted")
	if len(aborted) != 1 || aborted[0].Fields["workspace"] != "w1" {
		t.Fatalf("aborted events %+v", aborted)
	}
	if _, _, ok := store.New(f.root).Current(); ok {
		t.Fatal("current not cleared")
	}
}

func TestAbortWithNoLiveRunExits2(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	st := store.New(f.root)
	id, _ := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	st.SetCurrent(id, 999999)

	if code := Main([]string{"abort"}, f.env); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if st.Aborted(id) {
		t.Fatal("dead run marked")
	}
}

func TestNewRunClearsACurrentWhosePidIsDeadWithANote(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	if err := store.EnsureExcluded(f.root); err != nil {
		t.Fatal(err)
	}
	st := store.New(f.root)
	st.SetCurrent("20260918-090000", 999999)

	w, err := f.preflight(f.todo, "--plain")

	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "cleared run 20260918-090000: pid 999999 is gone") {
		t.Fatalf("no note:\n%s", f.out)
	}
	if id, _, _ := st.Current(); id != w.Loop.RunID {
		t.Fatalf("current = %s", id)
	}
}

func TestResumeAfterAbortRunsTheStoppedStep(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.hang["rloop-p1-plan"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	for !slices.Contains(sim.promptedAgents(), "rloop-p1-plan") {
		time.Sleep(5 * time.Millisecond)
	}
	env := f.env
	env.Stdout, env.Stderr = &strings.Builder{}, &strings.Builder{}
	if code := Main([]string{"abort"}, env); code != 0 {
		t.Fatalf("abort exit %d", code)
	}
	if code := <-done; code != 1 {
		t.Fatalf("run exit %d", code)
	}
	os.RemoveAll(filepath.Join(f.root, ".r-loop/wt/phase-1/.task-plans"))

	next := newSim()
	code, lander, err := f.resume(next)

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v\n%s", code, err, f.out)
	}
	if !slices.Equal(lander.landed, []string{"1"}) {
		t.Fatalf("landed %v", lander.landed)
	}
}

func TestClaimComparesAgainstTheStoppedStepsOwnTree(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	if err := store.EnsureExcluded(f.root); err != nil {
		t.Fatal(err)
	}
	repo, err := gitrepo.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddWorktree(".r-loop/wt/phase-1", "r-loop/phase-1", "main"); err != nil {
		t.Fatal(err)
	}
	f.write(".r-loop/wt/phase-1/code.txt", "unrecorded")
	tree, err := repo.Snapshot(filepath.Join(f.root, ".r-loop/wt/phase-1"))
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(f.root)
	id, _ := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	plan := core.StepKey{Run: id, Phase: "1", Kind: "plan", Attempt: 1}
	impl := core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 1}
	for _, r := range []core.Record{
		ev(t0, "run-list", 0, "", map[string]string{"phases": "1"}),
		ev(t0, "step", 1, "plan", map[string]string{"state": "running", "attempt": "1", "workspace": "w1"}),
		ev(t0, "snapshot", 1, "plan", map[string]string{"step": "plan-a1", "tree": tree}),
		{Kind: core.RecordStep, Step: &plan, State: core.StepOK},
		ev(t0, "step", 1, "implement", map[string]string{"state": "running", "attempt": "1", "workspace": "w2"}),
		{Kind: core.RecordStep, Step: &impl, State: core.StepRunning},
	} {
		if err := st.Append(id, r); err != nil {
			t.Fatal(err)
		}
	}

	_, _, err = f.resume(newSim())

	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), "code.txt") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestResumeUsageNamesEveryFlag(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)

	code := f.main("resume", "--bogus")

	if code != 2 || !strings.Contains(f.err.String(), "usage: r-loop resume [--replan] [--unattended] [--plain]") {
		t.Fatalf("code=%d stderr=%q", code, f.err)
	}
}

func (f *fixture) seedKilledImplement() (string, string) {
	f.t.Helper()
	t := f.t
	if err := store.EnsureExcluded(f.root); err != nil {
		t.Fatal(err)
	}
	repo, err := gitrepo.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddWorktree(".r-loop/wt/phase-1", "r-loop/phase-1", "main"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(f.root, ".r-loop/wt/phase-1")
	baseline, err := repo.Snapshot(wt)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(f.root)
	id, _ := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	plan := core.StepKey{Run: id, Phase: "1", Kind: "plan", Attempt: 1}
	impl := core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 1}
	for _, r := range []core.Record{
		ev(t0, "run-list", 0, "", map[string]string{"phases": "1"}),
		{Kind: core.RecordStep, Step: &plan, State: core.StepOK},
		ev(t0, "baseline", 1, "implement", map[string]string{"step": "implement-a1", "tree": baseline}),
		{Kind: core.RecordStep, Step: &impl, State: core.StepSpawned},
		ev(t0, "step", 1, "implement", map[string]string{"state": "running", "attempt": "1", "workspace": "w2"}),
		{Kind: core.RecordStep, Step: &impl, State: core.StepRunning},
		{Kind: core.RecordRun, Run: core.RunRunning},
	} {
		if err := st.Append(id, r); err != nil {
			t.Fatal(err)
		}
	}
	st.SetCurrent(id, 999999)
	f.write(".r-loop/wt/phase-1/wip.txt", "half done by the killed attempt")
	return id, wt
}

func TestResumeClaimsTheLeftoversOfAStepKilledInItsWorkHalfAndBuildsOnThem(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _ := f.seedKilledImplement()
	sim := newSim()

	code, lander, err := f.resume(sim)

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v\n%s", code, err, f.out)
	}
	if got := sim.promptedAgents(); !slices.Equal(got, []string{"rloop-p1-implement-a2"}) {
		t.Fatalf("prompted %v", got)
	}
	if !slices.Equal(lander.landed, []string{"1"}) {
		t.Fatalf("landed %v", lander.landed)
	}
	files := git(t, f.root, "show", "--name-only", "--format=%s", "r-loop/phase-1")
	if !strings.Contains(files, "r-loop: phase 1 implement") || !strings.Contains(files, "wip.txt") || !strings.Contains(files, "code.txt") {
		t.Fatalf("implement commit:\n%s", files)
	}
	if got := stepEvents(f.load(id), "baseline"); len(got) != 1 {
		t.Fatalf("baseline re-taken: %+v", got)
	}
}

func TestResumeRefusesChangesUnderARunningStepWithNoBaseline(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	if err := store.EnsureExcluded(f.root); err != nil {
		t.Fatal(err)
	}
	repo, err := gitrepo.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddWorktree(".r-loop/wt/phase-1", "r-loop/phase-1", "main"); err != nil {
		t.Fatal(err)
	}
	st := store.New(f.root)
	id, _ := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	impl := core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 1}
	for _, r := range []core.Record{
		ev(t0, "run-list", 0, "", map[string]string{"phases": "1"}),
		ev(t0, "baseline", 1, "plan", map[string]string{"step": "plan-a1", "tree": "whatever"}),
		ev(t0, "step", 1, "implement", map[string]string{"state": "running", "attempt": "1", "workspace": "w2"}),
		{Kind: core.RecordStep, Step: &impl, State: core.StepRunning},
	} {
		if err := st.Append(id, r); err != nil {
			t.Fatal(err)
		}
	}
	f.write(".r-loop/wt/phase-1/wip.txt", "someone's")

	_, _, err = f.resume(newSim())

	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), "unclaimed changes") || !strings.Contains(err.Error(), "wip.txt") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestResumeInterruptsTheKilledDriversStillWorkingStepAgentBeforeItClaims(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _ := f.seedKilledImplement()
	script := "#!/bin/sh\necho \"$@\" >> \"$0.calls\"\n" +
		"if [ \"$1 $2 $3\" = \"agent get rloop-p1-implement\" ]; then echo '{\"result\":{\"agent\":{\"name\":\"rloop-p1-implement\",\"pane_id\":\"w2:p1\",\"agent_status\":\"working\"}}}'; exit 0; fi\n" +
		"echo '{}'\n"
	if err := os.WriteFile(f.herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	code, _, err := f.resume(newSim())

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v\n%s", code, err, f.out)
	}
	calls, _ := os.ReadFile(f.herdr + ".calls")
	if !strings.Contains(string(calls), "agent send-keys rloop-p1-implement esc\nagent send-keys rloop-p1-implement ctrl+c\n") {
		t.Fatalf("herdr calls:\n%s", calls)
	}
	stale := stepEvents(f.load(id), "stale-interrupted")
	if len(stale) != 1 || stale[0].Phase != "1" || stale[0].Step != "implement" || stale[0].Fields["agent"] != "rloop-p1-implement" || stale[0].Fields["state"] != "working" {
		t.Fatalf("stale-interrupted events %+v", stale)
	}
	if !strings.Contains(f.out.String(), "interrupted previous session rloop-p1-implement: still working") {
		t.Fatalf("out:\n%s", f.out)
	}
}

func TestResumeLeavesAPreviousSessionThatIsNoLongerWorkingAlone(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _ := f.seedKilledImplement()

	code, _, err := f.resume(newSim())

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v\n%s", code, err, f.out)
	}
	calls, _ := os.ReadFile(f.herdr + ".calls")
	if !strings.Contains(string(calls), "agent get rloop-p1-implement\n") || strings.Contains(string(calls), "send-keys") {
		t.Fatalf("herdr calls:\n%s", calls)
	}
	if stale := stepEvents(f.load(id), "stale-interrupted"); len(stale) != 0 {
		t.Fatalf("stale-interrupted events %+v", stale)
	}
}

func TestResumeAfterAKilledDriverClosesItsStaleWatchdogAndStartsItsOwn(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _ := f.seedKilledImplement()

	w, opts, err := PrepareResume(nil, f.env)
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, newSim())
	dog := &dogHost{stale: map[string]string{
		core.WatchdogName(id):                "old-wd",
		core.WatchdogName("20260101-000000"): "other-run-wd",
		"rloop-watchdog":                     "legacy-wd",
	}}
	w.Dog.Host = dog
	code := w.Execute(opts)

	if code != 0 {
		t.Fatalf("resume exit %d\n%s%s", code, f.out, f.err)
	}
	calls := dog.Calls()
	if len(calls) < 3 || calls[0] != "ClosePane old-wd" || !strings.HasPrefix(calls[1], `Split "driver-pane" right`) || !strings.HasPrefix(calls[2], "Start wd-pane "+core.WatchdogName(id)+" ") {
		t.Fatalf("watchdog calls %q", calls)
	}
	for _, c := range calls {
		if c == "ClosePane other-run-wd" || c == "ClosePane legacy-wd" {
			t.Errorf("closed a watchdog that is not this run's: %q", calls)
		}
	}
}

func TestResumeClosesTheAttemptTheKilledDriverLeftRunningBeforeTheRerun(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _ := f.seedKilledImplement()

	code, _, err := f.resume(newSim())

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v\n%s", code, err, f.out)
	}
	a1 := core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 1}
	if got := f.load(id).Steps[a1]; got != core.StepFailed {
		t.Fatalf("attempt 1 is %s", got)
	}
	data, err := os.ReadFile(filepath.Join(store.New(f.root).Dir(id), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var trail []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec core.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Kind == core.RecordStep && rec.Step.Phase == "1" && rec.Step.Kind == "implement" {
			trail = append(trail, fmt.Sprintf("a%d %s %s", rec.Step.Attempt, rec.State, rec.Reason))
		}
	}
	closed := slices.Index(trail, "a1 failed interrupted: driver died")
	rerun := slices.Index(trail, "a2 queued ")
	if closed < 0 || rerun < closed || slices.Contains(trail[closed+1:], "a1 running ") {
		t.Fatalf("implement records %q", trail)
	}
}

func TestResumeWithdrawsAQuestionTheKilledDriverLeftOpen(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _ := f.seedKilledImplement()
	impl := core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 1}
	st := store.New(f.root)
	for _, r := range []core.Record{
		{Kind: core.RecordStep, Step: &impl, State: core.StepWaitingInput},
		{Kind: core.RecordQuestion, Question: &core.Question{ID: "q1", Step: impl, Text: "Which database?", AskedAt: t0}},
	} {
		if err := st.Append(id, r); err != nil {
			t.Fatal(err)
		}
	}

	code, _, err := f.resume(newSim())

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v\n%s", code, err, f.out)
	}
	qs := f.load(id).Questions
	if len(qs) != 1 || qs[0].ID != "q1" || qs[0].AnsweredBy != "withdrawn" || qs[0].Answer != "step failed" {
		t.Fatalf("questions %+v", qs)
	}
}

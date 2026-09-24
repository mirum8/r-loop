package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	nativeRe   = regexp.MustCompile("`([^`]+/native-review\\.txt)`")
	tokenRe    = regexp.MustCompile(`-[0-9a-z]{5}(-p[1-9])`)
)

func agentRole(name string) string { return tokenRe.ReplaceAllString(name, "$1") }

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
	h.started = append(h.started, agentRole(name))
	if spec, ok := h.panes[pane]; ok {
		h.opened[agentRole(name)] = spec
	}
	h.mu.Unlock()
	return core.Agent{Name: name, Pane: pane}, nil
}

func (h *simHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	if strings.HasPrefix(text, "r-loop:") {
		return nil
	}
	h.mu.Lock()
	agent = agentRole(agent)
	h.prompts = append(h.prompts, agent)
	h.texts[agent] = text
	spec, fail, hang, edit := h.opened[agent], h.fail[agent], h.hang[agent], h.edit[agent]
	h.mu.Unlock()
	sentinel := sentinelRe.FindString(text)
	switch {
	case hang:
		return nil
	case findingsRe.MatchString(text):
		m := findingsRe.FindStringSubmatch(text)
		writeTo(m[1], `{"reviewer":"`+m[2]+`","findings":[]}`)
		writeTo(nativeRe.FindStringSubmatch(text)[1], "native review output")
	case fail:
		wip := "wip.txt"
		if spec.Env["R_LOOP_STEP"] == "plan" {
			wip = planRe.FindString(text)
		}
		writeTo(filepath.Join(spec.CWD, wip), "half done by "+agent)
		writeTo(sentinel, `{"outcome":"failed","reason":"tests red"}`)
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
	writeTo(sentinel, `{"outcome":"ok","reason":""}`)
	return nil
}

func (h *simHost) State(agent string) (core.AgentState, error)            { return core.AgentWorking, nil }
func (h *simHost) AgentPane(agent string) (string, error)                 { return "", nil }
func (h *simHost) Read(agent string, lines int) (string, error)           { return "", nil }
func (h *simHost) Interrupt(agent string) error                           { return nil }
func (h *simHost) Tag(workspaceID string, tokens map[string]string) error { return nil }
func (h *simHost) Close(workspaceID string) error                         { return nil }
func (h *simHost) ClosePane(pane string) error                            { return nil }
func (h *simHost) Split(pane, direction, cwd string, env map[string]string) (string, error) {
	return pane + "-split", nil
}

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
	triagers.Store(core.WatchdogName(w.Loop.RunID), w)
	w.Config.Watchdog.TriageTimeout = 10 * time.Second
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

func TestResumeContinuesSignalSequenceFromTheHighestStoredSignal(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	w, err := f.preflight(f.todo, "--plain")
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.Store.Create(core.RunMeta{Todo: f.todo})
	if err != nil {
		t.Fatal(err)
	}
	key := core.StepKey{Run: id, Phase: "2", Kind: "implement", Attempt: 1}
	for _, seq := range []int{1, 65} {
		sig := core.Signal{Seq: seq, Kind: core.SignalWarn, Source: core.SourceWatchdog, Step: key}
		if err := w.Store.Append(id, core.Record{Kind: core.RecordSignal, Signal: &sig}); err != nil {
			t.Fatal(err)
		}
	}
	w.bind(id)
	got, err := w.Watch.Accept(core.Signal{Kind: core.SignalWarn, Source: core.SourceWatchdog, Step: key})
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != 66 {
		t.Fatalf("resumed signal sequence = %d, want 66", got.Seq)
	}
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
	out := agentRole(f.out.String())
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
	out := agentRole(f.out.String())
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
	reviewer := ""
	for _, agent := range started {
		if strings.HasPrefix(agent, "rloop-p1-imple-") && strings.HasSuffix(agent, "-r2-a2") {
			reviewer = agent
		}
	}
	if !slices.Contains(started, "rloop-p1-implement-a2") || reviewer == "" {
		t.Fatalf("started %v", started)
	}
	if slices.ContainsFunc(started, func(a string) bool { return strings.HasSuffix(a, "-r1-a2") }) {
		t.Fatalf("round 1 re-run: %v", started)
	}
	if slices.Contains(sim.promptedAgents(), "rloop-p1-implement-a2") {
		t.Fatalf("work half re-run: %v", sim.promptedAgents())
	}
	rv := sim.text(reviewer)
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
	if !strings.Contains(agentRole(f.out.String()), "previous session rloop-p1-implement left in workspace w7") {
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
	lock, err := st.Lock()
	if err != nil {
		t.Fatal(err)
	}
	lock.Publish()
	defer lock.Release("", 0)

	_, _, err = f.resume(newSim())

	want := fmt.Sprintf("run %s is live in pid %d", id, os.Getpid())
	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), want) {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestResumeANamedRunOverANewerOne(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	a, code := f.firstRun(first, "--phases", "1")
	if code != 1 {
		t.Fatalf("first run exit %d", code)
	}
	st := store.New(f.root)
	b, err := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(b, core.Record{Kind: core.RecordRun, Run: core.RunHalted}); err != nil {
		t.Fatal(err)
	}
	code, _, err = f.resume(newSim(), a)
	if code != 0 || err != nil || f.load(a).Status != core.RunFinished || f.load(b).Status != core.RunHalted {
		t.Fatalf("code=%d err=%v A=%s B=%s", code, err, f.load(a).Status, f.load(b).Status)
	}
}

func TestResumeUnknownRunIDExits2NamingIt(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	_, _, err := f.resume(newSim(), "20990101-000000")
	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), "no run 20990101-000000") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestResumeSkipsANewerRunThatRecordedNoProgress(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	a, code := f.firstRun(first, "--phases", "1")
	if code != 1 {
		t.Fatalf("first run exit %d", code)
	}
	if _, err := store.New(f.root).Create(core.RunMeta{Todo: f.todo, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	code, _, err := f.resume(newSim())
	if code != 0 || err != nil || f.load(a).Status != core.RunFinished {
		t.Fatalf("code=%d err=%v A=%s", code, err, f.load(a).Status)
	}
}

func TestResumeSkipsANewerCreatedRunWithOnlyWatchdogStart(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	older, code := f.firstRun(first, "--phases", "1")
	if code != 1 {
		t.Fatalf("first run exit %d", code)
	}
	st := store.New(f.root)
	newer, err := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(newer, core.Record{Kind: core.RecordEvent, Event: &core.Event{Kind: "watchdog-start"}}); err != nil {
		t.Fatal(err)
	}
	code, _, err = f.resume(newSim())
	if code != 0 || err != nil || f.load(older).Status != core.RunFinished || f.load(newer).Status != core.RunCreated {
		t.Fatalf("code=%d err=%v older=%s newer=%s", code, err, f.load(older).Status, f.load(newer).Status)
	}
}

func TestResumeSkipsARunWithUnreadableMetaWithAWarning(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	a, code := f.firstRun(first, "--phases", "1")
	if code != 1 {
		t.Fatalf("first run exit %d", code)
	}
	st := store.New(f.root)
	b, err := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir(b), "meta.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, err = f.resume(newSim())
	if code != 0 || err != nil || f.load(a).Status != core.RunFinished || !strings.Contains(f.err.String(), "skipped run "+b) {
		t.Fatalf("code=%d err=%v stderr=%q", code, err, f.err)
	}
}

func TestResumeAndStatusOfARunWithATornLastLineExitNormally(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	a, code := f.firstRun(first, "--phases", "1")
	if code != 1 {
		t.Fatalf("first run exit %d", code)
	}
	path := filepath.Join(store.New(f.root).Dir(a), "events.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"Kind":"event","Ev`); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if code := f.main("status", "--plain"); code != 0 {
		t.Fatalf("status exit %d: %s", code, f.err)
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || b[len(b)-1] == '\n' {
		t.Fatalf("tail changed: %v %q", err, b)
	}
	code, _, err = f.resume(newSim())
	if code != 0 || err != nil || f.load(a).Status != core.RunFinished {
		t.Fatalf("code=%d err=%v status=%s", code, err, f.load(a).Status)
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
	if !strings.Contains(f.out.String(), "cleared stale run pointer 20260918-090000: pid 999999 holds no run lock") {
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

	if code != 2 || !strings.Contains(f.err.String(), "usage: r-loop resume [--replan] [--unattended] [--yes] [--plain]") {
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

func (f *fixture) seedStepCommitIntent(id, wt string) (string, string) {
	f.t.Helper()
	repo, err := gitrepo.Open(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	head, err := repo.HeadSHA(wt)
	if err != nil {
		f.t.Fatal(err)
	}
	tree, err := repo.Snapshot(wt)
	if err != nil {
		f.t.Fatal(err)
	}
	f.appendRunEvent(id, ev(t0, "commit-intent", 1, "implement", map[string]string{
		"attempt": "1", "head": head, "tree": tree, "dir": wt, "message": "r-loop: phase 1 implement",
	}))
	return head, tree
}

func TestResumeAfterACrashBetweenTheStepCommitAndItsOkRecordDoesNotRerunIt(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, wt := f.seedKilledImplement()
	f.seedStepCommitIntent(id, wt)
	git(t, wt, "add", "-A")
	git(t, wt, "commit", "-q", "-m", "r-loop: phase 1 implement")
	sim := newSim()
	code, lander, err := f.resume(sim)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v out=%s", code, err, f.out)
	}
	key := core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 1}
	if f.load(id).Steps[key] != core.StepOK || len(sim.promptedAgents()) != 0 || !slices.Equal(lander.landed, []string{"1"}) || !strings.Contains(f.out.String(), "its work was committed before the crash") {
		t.Fatalf("step=%s prompts=%v landed=%v out=%s", f.load(id).Steps[key], sim.promptedAgents(), lander.landed, f.out)
	}
}

func TestResumeCommitsAStepsWorkWhenTheCrashCameBeforeItsCommit(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, wt := f.seedKilledImplement()
	head, _ := f.seedStepCommitIntent(id, wt)
	sim := newSim()
	code, _, err := f.resume(sim)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v out=%s", code, err, f.out)
	}
	key := core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 1}
	branch := "r-loop/phase-1"
	if f.load(id).Steps[key] != core.StepOK || len(sim.promptedAgents()) != 0 || git(t, f.root, "rev-parse", branch) == head || git(t, f.root, "log", "-1", "--format=%s", branch) != "r-loop: phase 1 implement" || !strings.Contains(f.out.String(), "committed the work the crash left uncommitted") {
		t.Fatalf("step=%s prompts=%v head=%s out=%s", f.load(id).Steps[key], sim.promptedAgents(), git(t, f.root, "rev-parse", branch), f.out)
	}
}

func TestResumeRefusesAStepWhoseWorktreeChangedAfterTheCrash(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, wt := f.seedKilledImplement()
	head, _ := f.seedStepCommitIntent(id, wt)
	f.write(".r-loop/wt/phase-1/extra.txt", "later edit")
	_, _, err := f.resume(newSim())
	key := core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 1}
	if exitCode(t, err) != 2 || !strings.Contains(err.Error(), "extra.txt") || f.load(id).Steps[key] == core.StepOK || git(t, wt, "rev-parse", "HEAD") != head {
		t.Fatalf("err=%v step=%s head=%s", err, f.load(id).Steps[key], git(t, wt, "rev-parse", "HEAD"))
	}
}

func TestResumeRefusesAStepWhoseHeadMovedToAnUnrelatedCommit(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, wt := f.seedKilledImplement()
	f.seedStepCommitIntent(id, wt)
	f.write(".r-loop/wt/phase-1/extra.txt", "other work")
	git(t, wt, "add", "extra.txt")
	git(t, wt, "commit", "-q", "-m", "other")
	_, _, err := f.resume(newSim())
	key := core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 1}
	if exitCode(t, err) != 2 || !strings.Contains(err.Error(), "extra.txt") || f.load(id).Steps[key] == core.StepOK {
		t.Fatalf("err=%v step=%s", err, f.load(id).Steps[key])
	}
}

func TestResumeRefusesAStepWhoseWorkIsStillUncommittedUnderAnEmptyCommit(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, wt := f.seedKilledImplement()
	f.seedStepCommitIntent(id, wt)
	git(t, wt, "commit", "--allow-empty", "-q", "-m", "other")
	head := git(t, wt, "rev-parse", "HEAD")
	_, _, err := f.resume(newSim())
	key := core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 1}
	if exitCode(t, err) != 2 || !strings.Contains(err.Error(), "does not hold the work the crash left") || !strings.Contains(err.Error(), "wip.txt") || f.load(id).Steps[key] == core.StepOK || git(t, wt, "rev-parse", "HEAD") != head {
		t.Fatalf("err=%v step=%s head=%s", err, f.load(id).Steps[key], git(t, wt, "rev-parse", "HEAD"))
	}
}

func (f *fixture) seedKilledLand(phases string) (string, string, string) {
	f.t.Helper()
	if err := store.EnsureExcluded(f.root); err != nil {
		f.t.Fatal(err)
	}
	repo, err := gitrepo.Open(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := repo.AddWorktree(".r-loop/wt/phase-1", "r-loop/phase-1", "main"); err != nil {
		f.t.Fatal(err)
	}
	wt := filepath.Join(f.root, ".r-loop/wt/phase-1")
	f.write(".r-loop/wt/phase-1/one.txt", "one\n")
	git(f.t, wt, "add", "one.txt")
	git(f.t, wt, "commit", "-q", "-m", "r-loop: phase 1 implement")
	base := git(f.t, f.root, "rev-parse", "HEAD")
	st := store.New(f.root)
	id, err := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now(), Branch: "main"})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, rec := range []core.Record{
		ev(t0, "run-list", 0, "", map[string]string{"phases": phases}),
		{Kind: core.RecordStep, Step: &core.StepKey{Run: id, Phase: "1", Kind: "plan", Attempt: 1}, State: core.StepOK},
		{Kind: core.RecordStep, Step: &core.StepKey{Run: id, Phase: "1", Kind: "implement", Attempt: 1}, State: core.StepOK},
		{Kind: core.RecordRun, Run: core.RunRunning},
	} {
		if err := st.Append(id, rec); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := st.SetCurrent(id, 999999); err != nil {
		f.t.Fatal(err)
	}
	return id, wt, base
}

func (f *fixture) seedMergeIntent(id, base string) {
	f.t.Helper()
	f.appendRunEvent(id, ev(t0, "merge-intent", 1, "land", map[string]string{
		"phase": "1", "branch": "r-loop/phase-1", "base": base, "message": "phase 1: one",
	}))
}

func (f *fixture) beginLandMerge() {
	f.t.Helper()
	git(f.t, f.root, "merge", "--no-ff", "--no-commit", "r-loop/phase-1")
}

func (f *fixture) tickPhaseOne() {
	f.t.Helper()
	data, err := os.ReadFile(f.todo)
	if err != nil {
		f.t.Fatal(err)
	}
	ticked := strings.Replace(string(data), "- [ ] a", "- [x] a", 1)
	if ticked == string(data) {
		f.t.Fatal("phase 1 checkbox not found")
	}
	if err := os.WriteFile(f.todo, []byte(ticked), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) seedLandCommitIntent(id string) string {
	f.t.Helper()
	git(f.t, f.root, "add", "--", "docs/topic/todo.md")
	tree := git(f.t, f.root, "write-tree")
	f.appendRunEvent(id, ev(t0, "commit-intent", 1, "land", map[string]string{
		"phase": "1", "tree": tree, "gateSkipped": "false", "added": "1", "deleted": "0",
	}))
	return tree
}

func TestResumeAbortsAMergeTheCrashLeftDuringTheGate(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	code, lander, err := f.resume(newSim())
	if err != nil || code != 0 || lander == nil || !slices.Equal(lander.landed, []string{"1"}) || !strings.Contains(f.out.String(), "aborted phase 1's merge") {
		t.Fatalf("code=%d err=%v landed=%v out=%s", code, err, lander, f.out)
	}
	if git(t, f.root, "rev-parse", "HEAD") != base {
		t.Fatal("HEAD moved")
	}
	if _, err := os.Stat(filepath.Join(f.root, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("MERGE_HEAD still exists: %v", err)
	}
}

func TestResumeCompletesAMergeWhoseCommitWasIntended(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	f.tickPhaseOne()
	f.seedLandCommitIntent(id)
	code, lander, err := f.resume(newSim())
	if exitCode(t, err) != 2 || code != 0 || lander != nil && len(lander.landed) != 0 {
		t.Fatalf("code=%d err=%v landed=%v out=%s", code, err, lander, f.out)
	}
	sha := git(t, f.root, "rev-parse", "HEAD")
	if sha == base || git(t, f.root, "log", "-1", "--format=%s") != "phase 1: one" || len(strings.Fields(git(t, f.root, "rev-list", "--parents", "-n", "1", "HEAD"))) != 3 || !strings.Contains(f.out.String(), "completed phase 1's merge") || len(f.load(id).Landed) != 1 || f.load(id).Landed[0].MergeSHA != sha {
		t.Fatalf("head=%s landed=%+v out=%s", sha, f.load(id).Landed, f.out)
	}
}

func TestResumeRefusesAMergeWithAnUnrelatedStagedChange(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	f.tickPhaseOne()
	f.seedLandCommitIntent(id)
	f.write("other.txt", "keep this\n")
	git(t, f.root, "add", "other.txt")
	_, _, err := f.resume(newSim())
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "other.txt") || !strings.Contains(err.Error(), "MERGE_HEAD") || !strings.Contains(err.Error(), "git merge --abort") || git(t, f.root, "rev-parse", "HEAD") != base {
		t.Fatalf("err=%v head=%s out=%s", err, git(t, f.root, "rev-parse", "HEAD"), f.out)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".git", "MERGE_HEAD")); err != nil {
		t.Fatalf("MERGE_HEAD missing: %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(f.root, "other.txt"))
	if readErr != nil || string(data) != "keep this\n" {
		t.Fatalf("other.txt=%q err=%v", data, readErr)
	}
}

func TestResumeRefusesAStagedMaintainerFileBeforeLandCommitIntent(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	f.write("other.txt", "mine\n")
	git(t, f.root, "add", "other.txt")
	_, _, err := f.resume(newSim())
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "other.txt") || !strings.Contains(err.Error(), "MERGE_HEAD") || !strings.Contains(err.Error(), "git merge --abort") || git(t, f.root, "rev-parse", "HEAD") != base {
		t.Fatalf("err=%v out=%s", err, f.out)
	}
	if got, readErr := os.ReadFile(filepath.Join(f.root, "other.txt")); readErr != nil || string(got) != "mine\n" {
		t.Fatalf("other.txt=%q err=%v", got, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(f.root, ".git", "MERGE_HEAD")); statErr != nil {
		t.Fatalf("MERGE_HEAD missing: %v", statErr)
	}
}

func TestResumeRefusesAnUnstagedEditBeforeLandCommitIntentEvenAfterItIsStaged(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	f.write("calc.go", "original\n")
	f.commit()
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	f.write("calc.go", "mine\n")
	for _, stage := range []bool{false, true} {
		if stage {
			git(t, f.root, "add", "calc.go")
		}
		_, _, err := f.resume(newSim())
		if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "calc.go") || !strings.Contains(err.Error(), "MERGE_HEAD") || !strings.Contains(err.Error(), "git merge --abort") || git(t, f.root, "rev-parse", "HEAD") != base {
			t.Fatalf("stage=%t err=%v out=%s", stage, err, f.out)
		}
		if got, readErr := os.ReadFile(filepath.Join(f.root, "calc.go")); readErr != nil || string(got) != "mine\n" {
			t.Fatalf("stage=%t calc.go=%q err=%v", stage, got, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(f.root, ".git", "MERGE_HEAD")); statErr != nil {
			t.Fatalf("stage=%t MERGE_HEAD missing: %v", stage, statErr)
		}
	}
}

func TestResumeNamesAConflictedDriverMerge(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, _ := f.seedKilledLand("1")
	f.write("one.txt", "main\n")
	f.commit()
	base := git(t, f.root, "rev-parse", "HEAD")
	f.seedMergeIntent(id, base)
	cmd := exec.Command("git", "-C", f.root, "merge", "--no-ff", "--no-commit", "r-loop/phase-1")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", output)
	}
	_, _, err := f.resume(newSim())
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "one.txt") || !strings.Contains(err.Error(), "unfinished merge") || !strings.Contains(err.Error(), "git merge --abort") || git(t, f.root, "rev-parse", "HEAD") != base {
		t.Fatalf("err=%v out=%s", err, f.out)
	}
	if _, statErr := os.Stat(filepath.Join(f.root, ".git", "MERGE_HEAD")); statErr != nil {
		t.Fatalf("MERGE_HEAD missing: %v", statErr)
	}
}

func TestResumeRefusesAMergeWithAStagedOnlyChange(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	f.write("notes.txt", "original\n")
	f.commit()
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	f.tickPhaseOne()
	f.seedLandCommitIntent(id)
	f.write("notes.txt", "staged\n")
	git(t, f.root, "add", "notes.txt")
	f.write("notes.txt", "original\n")
	_, _, err := f.resume(newSim())
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "notes.txt") || !strings.Contains(err.Error(), "git merge --abort by hand") || git(t, f.root, "rev-parse", "HEAD") != base || git(t, f.root, "diff", "--cached", "--name-only", "--", "notes.txt") != "notes.txt" || len(f.load(id).Landed) != 0 {
		t.Fatalf("err=%v head=%s landed=%+v", err, git(t, f.root, "rev-parse", "HEAD"), f.load(id).Landed)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".git", "MERGE_HEAD")); err != nil {
		t.Fatalf("MERGE_HEAD missing: %v", err)
	}
}

func TestResumeRefusesAForeignMergeInProgress(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	git(t, f.root, "branch", "foreign", "main")
	git(t, f.root, "checkout", "-q", "foreign")
	f.write("foreign.txt", "foreign branch\n")
	git(t, f.root, "add", "foreign.txt")
	git(t, f.root, "commit", "-q", "-m", "foreign tip")
	git(t, f.root, "checkout", "-q", "main")
	git(t, f.root, "merge", "--no-ff", "--no-commit", "foreign")
	_, _, err := f.resume(newSim())
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "unfinished merge") {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".git", "MERGE_HEAD")); err != nil {
		t.Fatalf("MERGE_HEAD missing: %v", err)
	}
}

func TestResumeRefusesAnAbortedMergesUnprovenTodoChange(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	f.tickPhaseOne()
	ticked, _ := os.ReadFile(f.todo)
	f.write("docs/topic/todo.md", string(ticked)+"maintainer note\n")
	want, _ := os.ReadFile(f.todo)
	_, _, err := f.resume(newSim())
	got, _ := os.ReadFile(f.todo)
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "docs/topic/todo.md") || !strings.Contains(err.Error(), "MERGE_HEAD") || string(got) != string(want) || git(t, f.root, "rev-parse", "HEAD") != base {
		t.Fatalf("err=%v todo=%q out=%s", err, got, f.out)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".git", "MERGE_HEAD")); err != nil {
		t.Fatalf("MERGE_HEAD missing: %v", err)
	}
}

func TestResumeRestoresTheProvenTickBeforeAbortingTheMerge(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	before, err := os.ReadFile(f.todo)
	if err != nil {
		t.Fatal(err)
	}
	f.tickPhaseOne()
	code, lander, err := f.resume(newSim())
	if err != nil || code != 0 || lander == nil || !slices.Equal(lander.landed, []string{"1"}) || !strings.Contains(f.out.String(), "aborted phase 1's merge") || git(t, f.root, "rev-parse", "HEAD") != base {
		t.Fatalf("code=%d err=%v landed=%v out=%s", code, err, lander, f.out)
	}
	got, err := os.ReadFile(f.todo)
	if err != nil || string(got) != string(before) {
		t.Fatalf("todo=%q err=%v", got, err)
	}
}

func TestResumeNamesThePhaseAndPathWhenAbortFails(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	before, err := os.ReadFile(f.todo)
	if err != nil {
		t.Fatal(err)
	}
	f.tickPhaseOne()
	git(t, f.root, "add", "docs/topic/todo.md")
	if err := os.WriteFile(f.todo, before, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.resume(newSim())
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "phase 1") || !strings.Contains(err.Error(), "docs/topic/todo.md") || !strings.Contains(err.Error(), "MERGE_HEAD") || !strings.Contains(err.Error(), "stage or restore") || git(t, f.root, "rev-parse", "HEAD") != base {
		t.Fatalf("err=%v out=%s", err, f.out)
	}
	if _, statErr := os.Stat(filepath.Join(f.root, ".git", "MERGE_HEAD")); statErr != nil {
		t.Fatalf("MERGE_HEAD missing: %v", statErr)
	}
}

func TestResumeNamesAMissingPhaseBranchDuringMerge(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, wt, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	git(t, wt, "checkout", "--detach", "-q")
	git(t, f.root, "branch", "-D", "r-loop/phase-1")
	_, _, err := f.resume(newSim())
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "phase 1") || !strings.Contains(err.Error(), "r-loop/phase-1") || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.root, ".git", "MERGE_HEAD")); statErr != nil {
		t.Fatalf("MERGE_HEAD missing: %v", statErr)
	}
}

func TestResumeNamesAMissingPhaseBranchAfterLandingCommit(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, wt, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	f.tickPhaseOne()
	f.seedLandCommitIntent(id)
	git(t, f.root, "commit", "-q", "-m", "phase 1: one")
	git(t, wt, "checkout", "--detach", "-q")
	git(t, f.root, "branch", "-D", "r-loop/phase-1")
	_, _, err := f.resume(newSim())
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "phase 1") || !strings.Contains(err.Error(), "r-loop/phase-1") || !strings.Contains(err.Error(), "restore") || len(f.load(id).Landed) != 0 {
		t.Fatalf("err=%v landed=%+v", err, f.load(id).Landed)
	}
}

func TestResumeRefusesAStagedMaintainerEditToTheTodoBeforeLandCommitIntent(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	f.tickPhaseOne()
	ticked, readErr := os.ReadFile(f.todo)
	if readErr != nil {
		t.Fatal(readErr)
	}
	f.write("docs/topic/todo.md", string(ticked)+"maintainer note\n")
	git(t, f.root, "add", "docs/topic/todo.md")
	want, readErr := os.ReadFile(f.todo)
	if readErr != nil {
		t.Fatal(readErr)
	}
	_, _, err := f.resume(newSim())
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "docs/topic/todo.md") || !strings.Contains(err.Error(), "MERGE_HEAD") || !strings.Contains(err.Error(), "git merge --abort") || git(t, f.root, "rev-parse", "HEAD") != base {
		t.Fatalf("err=%v out=%s", err, f.out)
	}
	got, readErr := os.ReadFile(f.todo)
	if readErr != nil || string(got) != string(want) {
		t.Fatalf("todo=%q err=%v", got, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(f.root, ".git", "MERGE_HEAD")); statErr != nil {
		t.Fatalf("MERGE_HEAD missing: %v", statErr)
	}
}

func TestResumeAbortsAMergeWithOnlyTheStagedPhaseTick(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	f.tickPhaseOne()
	git(t, f.root, "add", "docs/topic/todo.md")
	code, lander, err := f.resume(newSim())
	if err != nil || code != 0 || lander == nil || !slices.Equal(lander.landed, []string{"1"}) || !strings.Contains(f.out.String(), "aborted phase 1's merge") || git(t, f.root, "rev-parse", "HEAD") != base {
		t.Fatalf("code=%d err=%v landed=%v out=%s", code, err, lander, f.out)
	}
}

func TestResumeRecordsTheLandingOfACommitMadeBeforeTheCrash(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1,2")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	f.tickPhaseOne()
	f.seedLandCommitIntent(id)
	git(t, f.root, "commit", "-q", "-m", "phase 1: one")
	sha := git(t, f.root, "rev-parse", "HEAD")
	code, lander, err := f.resume(newSim())
	if err != nil || code != 0 || lander == nil || !slices.Equal(lander.landed, []string{"2"}) || !strings.Contains(f.out.String(), "phase 1 landed as "+sha[:7]+" before the crash") {
		t.Fatalf("code=%d err=%v lander=%v out=%s", code, err, lander, f.out)
	}
	landed := f.load(id).Landed
	if len(landed) == 0 || landed[0].MergeSHA != sha {
		t.Fatalf("landed=%+v", landed)
	}
	report, err := os.ReadFile(filepath.Join(store.New(f.root).Dir(id), "report.md"))
	if err != nil || !strings.Contains(string(report), "phase 1 "+sha) {
		t.Fatalf("report=%q err=%v", report, err)
	}
}

func TestResumeOfARunWhoseLastPhaseLandedBeforeTheCrashReportsItLanded(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	f.beginLandMerge()
	f.tickPhaseOne()
	f.seedLandCommitIntent(id)
	git(t, f.root, "commit", "-q", "-m", "phase 1: one")
	sha := git(t, f.root, "rev-parse", "HEAD")
	_, _, err := f.resume(newSim())
	if exitCode(t, err) != 2 || !strings.Contains(err.Error(), "landed every phase") {
		t.Fatalf("err=%v out=%s", err, f.out)
	}
	report, readErr := os.ReadFile(filepath.Join(store.New(f.root).Dir(id), "report.md"))
	if readErr != nil || !strings.Contains(string(report), "phase 1 "+sha) {
		t.Fatalf("report=%q err=%v", report, readErr)
	}
}

func TestResumeWithAMergeIntentButNoMergeLandsThePhaseAgain(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _, base := f.seedKilledLand("1")
	f.seedMergeIntent(id, base)
	code, lander, err := f.resume(newSim())
	if err != nil || code != 0 || lander == nil || !slices.Equal(lander.landed, []string{"1"}) || strings.Contains(f.out.String(), "phase 1's merge") {
		t.Fatalf("code=%d err=%v lander=%v out=%s", code, err, lander, f.out)
	}
}

func (f *fixture) appendRunEvent(id string, event core.Record) {
	f.t.Helper()
	if err := store.New(f.root).Append(id, event); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) herdrWorking(t *testing.T, agents ...string) {
	t.Helper()
	script := "#!/bin/sh\necho \"$@\" >> \"$0.calls\"\n"
	for _, agent := range agents {
		script += fmt.Sprintf("if [ \"$1 $2 $3\" = \"agent get %s\" ]; then echo '{\"result\":{\"agent\":{\"name\":\"%s\",\"pane_id\":\"w2:p1\",\"agent_status\":\"working\"}}}'; exit 0; fi\n", agent, agent)
	}
	script += "echo '{}'\n"
	if err := os.WriteFile(f.herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestResumeInterruptsTheRecordedStepAgent(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _ := f.seedKilledImplement()
	agent := "rloop-k3x9q-p1-implement"
	f.appendRunEvent(id, ev(t0, "agent-named", 1, "implement", map[string]string{"attempt": "1", "agent": agent}))
	f.herdrWorking(t, agent)
	code, _, err := f.resume(newSim())
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v stderr=%s stdout=%s", code, err, f.err.String(), f.out.String())
	}
	calls, _ := os.ReadFile(f.herdr + ".calls")
	stale := stepEvents(f.load(id), "stale-interrupted")
	if !strings.Contains(string(calls), "agent send-keys "+agent+" esc") || len(stale) != 1 || stale[0].Fields["agent"] != agent || !strings.Contains(f.out.String(), "interrupted previous session "+agent+": still working") || !strings.Contains(f.out.String(), "previous session "+agent+" left in workspace w2") {
		t.Fatalf("calls %s; stale %+v; out %s", calls, stale, f.out.String())
	}
}

func TestResumeFindsTheRecordedAgentOfTheLastAttempt(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _ := f.seedKilledImplement()
	for _, pair := range [][2]string{{"1", "rloop-k3x9q-p1-implement"}, {"2", "rloop-k3x9q-p1-implement-a2"}} {
		f.appendRunEvent(id, ev(t0, "agent-named", 1, "implement", map[string]string{"attempt": pair[0], "agent": pair[1]}))
	}
	f.appendRunEvent(id, ev(t0, "step", 1, "implement", map[string]string{"state": "running", "attempt": "2", "workspace": "w3"}))
	f.herdrWorking(t, "rloop-k3x9q-p1-implement-a2")
	code, _, err := f.resume(newSim())
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	stale := stepEvents(f.load(id), "stale-interrupted")
	if len(stale) != 1 || stale[0].Fields["agent"] != "rloop-k3x9q-p1-implement-a2" {
		t.Fatalf("stale %+v", stale)
	}
}

func TestResumeInterruptsARecordedReviewerStillWorking(t *testing.T) {
	f := newResumeFixture(t, reviewConfig)
	id, _ := f.seedKilledImplement()
	f.appendRunEvent(id, ev(t0, "review-round", 1, "implement", map[string]string{"round": "2", "tree": "tree", "attempt": "1"}))
	f.appendRunEvent(id, ev(t0, "agent-named", 1, "implement", map[string]string{"attempt": "1", "agent": "rloop-k3x9q-p1-implement"}))
	agent := "rloop-k3x9q-p1-implemen-abcde-r2"
	f.appendRunEvent(id, ev(t0, "agent-named", 1, "implement", map[string]string{"attempt": "1", "agent": agent, "reviewer": "claude", "round": "2"}))
	f.herdrWorking(t, agent)
	code, _, err := f.resume(newSim())
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	stale := stepEvents(f.load(id), "stale-interrupted")
	if len(stale) != 1 || stale[0].Fields["agent"] != agent {
		t.Fatalf("stale %+v", stale)
	}
}

func TestResumeOfARunStartedBeforeTheChangeInterruptsItsLegacyReviewer(t *testing.T) {
	f := newResumeFixture(t, reviewConfig)
	id, _ := f.seedKilledImplement()
	f.appendRunEvent(id, ev(t0, "review-round", 1, "implement", map[string]string{"round": "2", "tree": "tree", "attempt": "1"}))
	agent := "rloop-p1-implement-rv-claude-r2"
	f.herdrWorking(t, agent)
	code, _, err := f.resume(newSim())
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	calls, _ := os.ReadFile(f.herdr + ".calls")
	stale := stepEvents(f.load(id), "stale-interrupted")
	if len(stale) != 1 || stale[0].Fields["agent"] != agent || !strings.Contains(string(calls), "agent get rloop-p1-implement\n") || !strings.Contains(string(calls), "agent get rloop-p1-implement-rv-ui-r2\n") {
		t.Fatalf("calls %s; stale %+v", calls, stale)
	}
}

func TestResumeCrashedRightAfterStartFindsTheRecordedAgent(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	id, _ := f.seedKilledImplement()
	agent := "rloop-k3x9q-p1-implement"
	f.appendRunEvent(id, ev(t0, "agent-named", 1, "implement", map[string]string{"attempt": "1", "agent": agent}))
	f.herdrWorking(t, agent)
	code, _, err := f.resume(newSim())
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	stale := stepEvents(f.load(id), "stale-interrupted")
	if len(stale) != 1 || stale[0].Fields["agent"] != agent {
		t.Fatalf("stale %+v", stale)
	}
}

func TestAResumedRunFindsTheStepAgentItNamedItself(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	id, code := f.firstRun(first, "--phases", "1")
	if code != 1 {
		t.Fatalf("first code %d", code)
	}
	var agent string
	for _, e := range stepEvents(f.load(id), "agent-named") {
		if e.Phase == "1" && e.Step == "implement" {
			agent = e.Fields["agent"]
		}
	}
	if !regexp.MustCompile(`^rloop-[0-9a-z]{5}-p1-implement$`).MatchString(agent) {
		t.Fatalf("agent %q", agent)
	}
	f.herdrWorking(t, agent)
	code, _, err := f.resume(newSim())
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	stale := stepEvents(f.load(id), "stale-interrupted")
	if len(stale) != 1 || stale[0].Fields["agent"] != agent {
		t.Fatalf("stale %+v", stale)
	}
	if !strings.Contains(f.out.String(), "previous session "+agent+" left in workspace w2") {
		t.Fatalf("out %s", f.out.String())
	}
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

func TestResumeOfALabelledRunInterruptsTheLabelledStepAgent(t *testing.T) {
	f := newResumeFixture(t, "label: test\n"+noReviewConfig)
	id, _ := f.seedKilledImplement()
	script := "#!/bin/sh\necho \"$@\" >> \"$0.calls\"\n" +
		"if [ \"$1 $2 $3\" = \"agent get rloop-test-p1-implement\" ]; then echo '{\"result\":{\"agent\":{\"name\":\"rloop-test-p1-implement\",\"pane_id\":\"w2:p1\",\"agent_status\":\"working\"}}}'; exit 0; fi\n" +
		"echo '{}'\n"
	if err := os.WriteFile(f.herdr, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	code, _, err := f.resume(newSim())

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v\n%s", code, err, f.out)
	}
	calls, _ := os.ReadFile(f.herdr + ".calls")
	if !strings.Contains(string(calls), "agent send-keys rloop-test-p1-implement esc\n") {
		t.Fatalf("herdr calls:\n%s", calls)
	}
	stale := stepEvents(f.load(id), "stale-interrupted")
	if len(stale) != 1 || stale[0].Fields["agent"] != "rloop-test-p1-implement" {
		t.Fatalf("stale-interrupted events %+v", stale)
	}
	if !strings.Contains(f.out.String(), "previous session rloop-test-p1-implement left in workspace w2") {
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

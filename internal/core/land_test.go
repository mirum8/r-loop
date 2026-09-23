package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/gitrepo"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@local",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const todoText = "# Plan\n\n## Milestone 1 — The core\n\n### Phase 1 — First\n- [ ] p1 item\n\n### Phase 2 — Second\n- [ ] p2 item\n"

type tickPlan struct {
	root  string
	ticks []string
}

func (p *tickPlan) Read(path string) (core.Plan, error) { return core.Plan{}, nil }

func (p *tickPlan) Tick(path, phase string) error {
	p.ticks = append(p.ticks, phase)
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.root, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	box := fmt.Sprintf("- [ ] p%s ", phase)
	return os.WriteFile(path, []byte(strings.ReplaceAll(string(data), box, fmt.Sprintf("- [x] p%s ", phase))), 0o644)
}

type memStore struct {
	mu      sync.Mutex
	dir     string
	records []core.Record
}

func (s *memStore) Create(meta core.RunMeta) (string, error) { return "run1", nil }

func (s *memStore) Append(runID string, rec core.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, rec)
	return nil
}

func (s *memStore) Load(runID string) (core.RunState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := core.RunState{ID: runID, Steps: map[core.StepKey]core.StepState{}}
	for _, r := range s.records {
		if r.Kind == core.RecordStep {
			st.Steps[*r.Step] = r.State
		}
		if r.Kind == core.RecordEvent {
			st.Events = append(st.Events, *r.Event)
		}
	}
	return st, nil
}
func (s *memStore) Current() (string, int, bool)           { return "", 0, false }
func (s *memStore) SetCurrent(runID string, pid int) error { return nil }
func (s *memStore) ClearCurrent() error                    { return nil }
func (s *memStore) Aborted(runID string) bool              { return false }
func (s *memStore) MarkAbort(runID string) error           { return nil }
func (s *memStore) Dir(runID string) string                { return s.dir }

func (s *memStore) events(kind string) []core.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.Event
	for _, r := range s.records {
		if r.Kind == core.RecordEvent && r.Event.Kind == kind {
			out = append(out, *r.Event)
		}
	}
	return out
}

func (s *memStore) landings() []core.Landing {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.Landing
	for _, r := range s.records {
		if r.Kind == core.RecordLanding {
			out = append(out, *r.Landing)
		}
	}
	return out
}

type memFace struct {
	mu     sync.Mutex
	events []core.Event
}

func (f *memFace) Emit(ev core.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
}

func (f *memFace) Close() {}

type runnerFunc func(ctx context.Context, ref core.StepRef, obs core.Observer) core.Outcome

func (f runnerFunc) Run(ctx context.Context, ref core.StepRef, obs core.Observer) core.Outcome {
	return f(ctx, ref, obs)
}

type landEnv struct {
	t     *testing.T
	repo  *gitrepo.Repo
	root  string
	todo  string
	plan  *tickPlan
	store *memStore
	face  *memFace
}

func newLandEnv(t *testing.T) *landEnv {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "docs/demo/todo.md"), todoText)
	writeFile(t, filepath.Join(dir, "a.txt"), "one\n")
	writeFile(t, filepath.Join(dir, ".git/info/exclude"), ".r-loop/\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "init")
	repo, err := gitrepo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &landEnv{
		t:     t,
		repo:  repo,
		root:  repo.Root(),
		todo:  filepath.Join(repo.Root(), "docs/demo/todo.md"),
		plan:  &tickPlan{root: repo.Root()},
		store: &memStore{dir: t.TempDir()},
		face:  &memFace{},
	}
}

func (e *landEnv) worktree(n int) string {
	return filepath.Join(e.root, fmt.Sprintf(".r-loop/wt/phase-%d", n))
}

func (e *landEnv) phaseWork(n int, path, content string) {
	e.t.Helper()
	wt := fmt.Sprintf(".r-loop/wt/phase-%d", n)
	if err := e.repo.AddWorktree(wt, fmt.Sprintf("r-loop/phase-%d", n), "main"); err != nil {
		e.t.Fatal(err)
	}
	writeFile(e.t, filepath.Join(e.worktree(n), path), content)
	if _, err := e.repo.CommitAll(wt, fmt.Sprintf("r-loop: phase %d implement", n)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *landEnv) gate() *core.LandGate {
	return &core.LandGate{
		Repo:        e.repo,
		Plan:        e.plan,
		Store:       e.store,
		Face:        e.face,
		RunID:       "run1",
		TodoPath:    e.todo,
		GateTimeout: time.Minute,
		FixKind:     core.StepKind{Name: "gatefix", Prompt: "gatefix", Check: "diff"},
	}
}

func (e *landEnv) head() string {
	e.t.Helper()
	return gitCmd(e.t, e.root, "rev-parse", "HEAD")
}

func (e *landEnv) assertUntouched(head string) {
	e.t.Helper()
	if got := e.head(); got != head {
		e.t.Errorf("HEAD moved from %s to %s", head, got)
	}
	if st := gitCmd(e.t, e.root, "status", "--porcelain"); st != "" {
		e.t.Errorf("primary tree not clean:\n%s", st)
	}
	if got := readFile(e.t, e.todo); got != todoText {
		e.t.Errorf("todo touched:\n%s", got)
	}
	if len(e.store.landings()) != 0 {
		e.t.Errorf("landing recorded: %+v", e.store.landings())
	}
}

func phaseOne(doneWhen string) core.Phase {
	return core.Phase{ID: "1", Title: "First", Milestone: 1, DoneWhen: doneWhen, Items: []core.Item{{Text: "p1 item"}}}
}

func phaseTwo(doneWhen string) core.Phase {
	return core.Phase{ID: "2", Title: "Second", Milestone: 1, DoneWhen: doneWhen, Items: []core.Item{{Text: "p2 item"}}}
}

func TestLandGatePassesOnlyBecauseItRunsAfterTheMerge(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")

	landing, err := e.gate().Land(context.Background(), phaseOne("test -f feature.txt && echo gate green"))

	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	head := e.head()
	if landing.MergeSHA != head || landing.Phase != "1" || landing.GateSkipped {
		t.Errorf("landing = %+v, head %s", landing, head)
	}
	if !strings.Contains(landing.GateOutput, "gate green") {
		t.Errorf("gate output = %q", landing.GateOutput)
	}
	if msg := gitCmd(t, e.root, "log", "-1", "--format=%s"); msg != "phase 1: First" {
		t.Errorf("commit message = %q", msg)
	}
	if parents := strings.Fields(gitCmd(t, e.root, "log", "-1", "--format=%P")); len(parents) != 2 {
		t.Errorf("want a merge commit, parents %v", parents)
	}
	touched := strings.Fields(gitCmd(t, e.root, "show", "--name-only", "--format=", "--diff-merges=first-parent", "HEAD"))
	if !slices.Equal(touched, []string{"docs/demo/todo.md", "feature.txt"}) {
		t.Errorf("merge commit touches %v", touched)
	}
	if !strings.Contains(readFile(t, e.todo), "- [x] p1 item") {
		t.Errorf("todo not ticked:\n%s", readFile(t, e.todo))
	}
	if got := e.store.landings(); len(got) != 1 || got[0] != landing {
		t.Errorf("landings recorded = %+v", got)
	}
	if _, err := os.Stat(e.worktree(1)); err != nil {
		t.Errorf("phase worktree removed: %v", err)
	}
}

func TestLandGateRecordsThePhasesDiffSize(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "one\ntwo\nthree\n")
	writeFile(t, filepath.Join(e.worktree(1), "a.txt"), "uno\n")
	if _, err := e.repo.CommitAll(".r-loop/wt/phase-1", "r-loop: phase 1 review"); err != nil {
		t.Fatal(err)
	}

	landing, err := e.gate().Land(context.Background(), phaseOne(""))

	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if landing.Added != 4 || landing.Deleted != 1 {
		t.Errorf("landing size = +%d -%d, want +4 -1", landing.Added, landing.Deleted)
	}
	if got := e.store.landings(); len(got) != 1 || got[0] != landing {
		t.Errorf("landings recorded = %+v", got)
	}
}

type diffStatFails struct {
	*gitrepo.Repo
}

func (r diffStatFails) DiffStat(dir, ref string) (int, int, error) {
	return 0, 0, errors.New("numstat broke")
}

func TestLandGateRefusesToLandWhenTheDiffSizeCannotBeMeasured(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	gate := e.gate()
	gate.Repo = diffStatFails{e.repo}

	_, err := gate.Land(context.Background(), phaseOne(""))

	if err == nil || !strings.Contains(err.Error(), "numstat broke") {
		t.Fatalf("Land err = %v", err)
	}
	e.assertUntouched(head)
}

func TestLandGateRedAbortsTheMergeWithTheTodoUntouched(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()

	_, err := e.gate().Land(context.Background(), phaseOne("echo boom; exit 3"))

	if !errors.Is(err, core.ErrGate) {
		t.Fatalf("err = %v, want ErrGate", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("err does not carry the output: %v", err)
	}
	e.assertUntouched(head)
	if len(e.plan.ticks) != 0 {
		t.Errorf("ticked %v", e.plan.ticks)
	}
}

func fixingRunner(e *landEnv, refs *[]core.StepRef) runnerFunc {
	return func(ctx context.Context, ref core.StepRef, obs core.Observer) core.Outcome {
		*refs = append(*refs, ref)
		wt := filepath.Join(e.root, ref.Worktree)
		writeFile(e.t, filepath.Join(wt, fmt.Sprintf("fix%d.txt", ref.Key.Attempt)), "fixed\n")
		if _, err := e.repo.CommitAll(wt, fmt.Sprintf("r-loop: phase %s gatefix", ref.Key.Phase)); err != nil {
			return core.Outcome{State: core.StepFailed, Reason: err.Error()}
		}
		return core.Outcome{State: core.StepOK}
	}
}

func TestLandGateRedFixedByOneGateFixRoundThenLands(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	var refs []core.StepRef
	g := e.gate()
	g.FixRounds = 1
	g.Runner = fixingRunner(e, &refs)

	landing, err := g.Land(context.Background(), phaseOne("test -f fix1.txt || { echo missing fix1; exit 1; }"))

	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("gate-fix runs = %d, want 1", len(refs))
	}
	ref := refs[0]
	if ref.Key != (core.StepKey{Run: "run1", Phase: "1", Kind: "gatefix", Attempt: 1}) {
		t.Errorf("key = %+v", ref.Key)
	}
	if ref.Kind.Prompt != "gatefix" || ref.Kind.Check != "diff" || ref.InPrimary {
		t.Errorf("kind = %+v, in primary %v", ref.Kind, ref.InPrimary)
	}
	if ref.Worktree != ".r-loop/wt/phase-1" || ref.Branch != "r-loop/phase-1" {
		t.Errorf("worktree %s branch %s", ref.Worktree, ref.Branch)
	}
	if ref.Vars["GateCommand"] != "test -f fix1.txt || { echo missing fix1; exit 1; }" {
		t.Errorf("GateCommand = %v", ref.Vars["GateCommand"])
	}
	if out, _ := ref.Vars["GateOutput"].(string); !strings.Contains(out, "missing fix1") {
		t.Errorf("GateOutput = %q", out)
	}
	if ref.Vars["PhaseNumber"] != "1" || ref.Vars["TodoPath"] != e.todo {
		t.Errorf("vars = %v", ref.Vars)
	}
	fixes := e.store.events("gate-fix")
	if len(fixes) != 1 || fixes[0].Fields["phase"] != "1" || fixes[0].Fields["round"] != "1" {
		t.Errorf("gate-fix events = %+v", fixes)
	}
	if landing.MergeSHA != e.head() {
		t.Errorf("landing = %+v", landing)
	}
	touched := strings.Fields(gitCmd(t, e.root, "show", "--name-only", "--format=", "--diff-merges=first-parent", "HEAD"))
	if !slices.Equal(touched, []string{"docs/demo/todo.md", "feature.txt", "fix1.txt"}) {
		t.Errorf("merge commit touches %v", touched)
	}
	if n := gitCmd(t, e.root, "log", "--oneline", "--grep", "gatefix", "r-loop/phase-1"); strings.Count(n, "\n") != 0 || n == "" {
		t.Errorf("gatefix commits on branch:\n%s", n)
	}
}

func TestLandGateSecondRedGateGetsASecondRound(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	var refs []core.StepRef
	g := e.gate()
	g.FixRounds = 2
	g.Runner = fixingRunner(e, &refs)

	_, err := g.Land(context.Background(), phaseOne("test -f fix2.txt"))

	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if len(refs) != 2 || refs[1].Key.Attempt != 2 {
		t.Fatalf("gate-fix runs = %+v", refs)
	}
	fixes := e.store.events("gate-fix")
	if len(fixes) != 2 || fixes[1].Fields["round"] != "2" {
		t.Errorf("gate-fix events = %+v", fixes)
	}
}

func TestLandGateFixOnResumeIsANewAttempt(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	e.store.Append("run1", core.Record{Kind: core.RecordStep, Step: &core.StepKey{Run: "run1", Phase: "1", Kind: "gatefix", Attempt: 1}, State: core.StepFailed})
	var refs []core.StepRef
	g := e.gate()
	g.FixRounds = 1
	g.Runner = fixingRunner(e, &refs)

	_, err := g.Land(context.Background(), phaseOne("test -f fix2.txt"))

	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if len(refs) != 1 || refs[0].Key.Attempt != 2 {
		t.Fatalf("gate-fix runs = %+v, want one run as attempt 2", refs)
	}
}

func TestLandGateStillRedWithNoRoundLeftBlocksThePhase(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	var refs []core.StepRef
	g := e.gate()
	g.FixRounds = 1
	g.Runner = fixingRunner(e, &refs)

	_, err := g.Land(context.Background(), phaseOne("test -f fix2.txt"))

	if !errors.Is(err, core.ErrGate) {
		t.Fatalf("err = %v, want ErrGate", err)
	}
	if len(refs) != 1 {
		t.Errorf("gate-fix runs = %d, want 1", len(refs))
	}
	e.assertUntouched(head)
}

func TestLandGateFixStepNotOKBlocksThePhase(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	g := e.gate()
	g.FixRounds = 2
	calls := 0
	g.Runner = runnerFunc(func(ctx context.Context, ref core.StepRef, obs core.Observer) core.Outcome {
		calls++
		return core.Outcome{State: core.StepFailed, Reason: "could not fix"}
	})

	_, err := g.Land(context.Background(), phaseOne("exit 1"))

	if !errors.Is(err, core.ErrGate) || !strings.Contains(err.Error(), "could not fix") {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Errorf("gate-fix runs = %d, want 1", calls)
	}
	e.assertUntouched(head)
}

func TestLandGateRunsTheCodeSpanOfAMarkdownDoneWhen(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")

	landing, err := e.gate().Land(context.Background(), phaseOne("`test -f feature.txt && echo ok  demo/greet` is green."))

	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if landing.MergeSHA != e.head() || landing.GateSkipped {
		t.Errorf("landing = %+v", landing)
	}
}

func TestLandGateJoinsSeveralCodeSpansAndHandsThemToTheGateFix(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	var refs []core.StepRef
	g := e.gate()
	g.FixRounds = 1
	g.Runner = fixingRunner(e, &refs)

	landing, err := g.Land(context.Background(), phaseOne("`test -f feature.txt` is green and\n`test -f fix1.txt` finds the fix."))

	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if len(refs) != 1 || refs[0].Vars["GateCommand"] != "test -f feature.txt && test -f fix1.txt" {
		t.Fatalf("gate-fix refs = %+v", refs)
	}
	if landing.MergeSHA != e.head() {
		t.Errorf("landing = %+v", landing)
	}
}

func TestLandGateWithoutDoneWhenRecordsASkip(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")

	landing, err := e.gate().Land(context.Background(), phaseOne(""))

	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if !landing.GateSkipped || landing.MergeSHA != e.head() {
		t.Errorf("landing = %+v", landing)
	}
	skips := e.store.events("gate-skipped")
	if len(skips) != 1 || skips[0].Phase != "1" {
		t.Errorf("gate-skipped events = %+v", skips)
	}
	if len(e.face.events) == 0 || e.face.events[0].Kind != "gate-skipped" {
		t.Errorf("face events = %+v", e.face.events)
	}
}

func TestLandGateConflictLeavesTheTodoUntouched(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "a.txt", "branch\n")
	writeFile(t, filepath.Join(e.root, "a.txt"), "main\n")
	gitCmd(t, e.root, "commit", "-q", "-am", "main edit")
	head := e.head()

	_, err := e.gate().Land(context.Background(), phaseOne("true"))

	if !errors.Is(err, core.ErrMergeConflict) || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("err = %v, want ErrMergeConflict naming a.txt", err)
	}
	e.assertUntouched(head)
}

func TestLandGateRefusesAMergeWithoutCode(t *testing.T) {
	e := newLandEnv(t)
	if err := e.repo.AddWorktree(".r-loop/wt/phase-1", "r-loop/phase-1", "main"); err != nil {
		t.Fatal(err)
	}
	head := e.head()

	_, err := e.gate().Land(context.Background(), phaseOne("true"))

	if !errors.Is(err, core.ErrLanding) || !strings.Contains(err.Error(), "code and ticks land as one commit") {
		t.Fatalf("err = %v, want ErrLanding", err)
	}
	if got := e.head(); got != head {
		t.Errorf("HEAD = %s, want reset to %s", got, head)
	}
}

type reportHost struct {
	mu      sync.Mutex
	outcome string
	opened  []core.OpenSpec
}

func (h *reportHost) Reachable() error { return nil }

func (h *reportHost) Open(spec core.OpenSpec) (core.Workspace, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opened = append(h.opened, spec)
	return core.Workspace{ID: "w1", RootPane: "p1"}, nil
}

func (h *reportHost) Start(pane, name, kind string, args []string) (core.Agent, error) {
	return core.Agent{Name: name, Pane: pane}, nil
}

func (h *reportHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	h.mu.Lock()
	cwd := h.opened[len(h.opened)-1].CWD
	outcome := h.outcome
	h.mu.Unlock()
	report, sentinel, _ := strings.Cut(text, "\n")
	if outcome == "ok" || outcome == "partial" {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(cwd, report)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(cwd, report), []byte("# Report\n"), 0o644); err != nil {
			return err
		}
	}
	if outcome == "partial" {
		outcome = "failed"
		if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("scribbled\n"), 0o644); err != nil {
			return err
		}
	}
	data, _ := json.Marshal(map[string]string{"outcome": outcome, "reason": "report failed"})
	return os.WriteFile(sentinel, data, 0o644)
}

func (h *reportHost) State(agent string) (core.AgentState, error)            { return core.AgentWorking, nil }
func (h *reportHost) AgentPane(agent string) (string, error)                 { return "", nil }
func (h *reportHost) Read(agent string, lines int) (string, error)           { return "", nil }
func (h *reportHost) Interrupt(agent string) error                           { return nil }
func (h *reportHost) Tag(workspaceID string, tokens map[string]string) error { return nil }
func (h *reportHost) Close(workspaceID string) error                         { return nil }
func (h *reportHost) ClosePane(pane string) error                            { return nil }
func (h *reportHost) Split(pane, direction, cwd string) (string, error)      { return "", nil }

type reportPrompts struct {
	mu   sync.Mutex
	vars []map[string]any
}

func (p *reportPrompts) Render(name string, vars map[string]any) (string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.vars = append(p.vars, vars)
	return fmt.Sprintf("%s\n%s", vars["ReportPath"], vars["Sentinel"]), "embedded", nil
}

func (e *landEnv) boundaryGate(outcome string) (*core.LandGate, *reportHost, *reportPrompts) {
	host := &reportHost{outcome: outcome}
	prompts := &reportPrompts{}
	sm := &core.SessionManager{
		Host:    host,
		Repo:    e.repo,
		Prompts: prompts,
		Store:   e.store,
		Resolve: func(provider, model, effort, askURL, mcp string) (core.ProviderArgs, error) {
			return core.ProviderArgs{Kind: provider}, nil
		},
		Poll: 5 * time.Millisecond,
	}
	plan := core.Plan{
		Path:       e.todo,
		Topic:      "demo",
		Milestones: []core.Milestone{{Number: 1, Name: "The core", Phases: []string{"1", "2"}}},
		Phases:     []core.Phase{phaseOne(""), phaseTwo("")},
	}
	g := e.gate()
	g.Boundary = &core.MilestoneBoundary{
		Plan:     plan,
		Sessions: sm,
		Repo:     e.repo,
		Kind:     core.StepKind{Name: "milestone", Prompt: "milestone", Check: "report", Row: core.StepRow{Provider: "claude"}},
		Topic:    "demo",
		RunDir:   e.store.dir,
		RunID:    "run1",
		Face:     e.face,
	}
	return g, host, prompts
}

func TestMilestoneReportSpawnedOnlyAfterTheLastPhaseInThePrimaryTree(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, host, prompts := e.boundaryGate("ok")

	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatalf("Land 1: %v", err)
	}
	if len(host.opened) != 0 {
		t.Fatalf("report session opened after phase 1: %+v", host.opened)
	}
	if _, err := g.Land(context.Background(), phaseTwo("")); err != nil {
		t.Fatalf("Land 2: %v", err)
	}

	if len(host.opened) != 1 || host.opened[0].CWD != e.root {
		t.Fatalf("opened = %+v, want one session in %s", host.opened, e.root)
	}
	vars := prompts.vars[0]
	report := "docs/demo/reports/milestone-1-the-core.md"
	if vars["ReportPath"] != report || vars["MilestoneName"] != "The core" || vars["MilestonePhases"] != "1, 2" {
		t.Errorf("vars = %v", vars)
	}
	if msg := gitCmd(t, e.root, "log", "-1", "--format=%s"); msg != "docs(report): milestone 1" {
		t.Errorf("head commit = %q", msg)
	}
	if touched := gitCmd(t, e.root, "show", "--name-only", "--format=", "HEAD"); touched != report {
		t.Errorf("report commit touches %q", touched)
	}
	if st := gitCmd(t, e.root, "status", "--porcelain"); st != "" {
		t.Errorf("primary tree not clean:\n%s", st)
	}
}

func TestMilestoneReportFailedIsRecordedAsASkip(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, host, _ := e.boundaryGate("failed")
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatalf("Land 1: %v", err)
	}

	landing, err := g.Land(context.Background(), phaseTwo(""))

	if err != nil {
		t.Fatalf("Land 2: %v", err)
	}
	if len(host.opened) != 1 {
		t.Fatalf("opened = %+v", host.opened)
	}
	if landing.MergeSHA != e.head() {
		t.Errorf("HEAD %s is not the landing %s", e.head(), landing.MergeSHA)
	}
	skips := e.store.events("report-skipped")
	if len(skips) != 1 || !strings.Contains(skips[0].Fields["reason"], "report failed") {
		t.Errorf("report-skipped events = %+v", skips)
	}
}

func TestMilestoneReportFailedLeavesThePrimaryTreeClean(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, _, _ := e.boundaryGate("partial")
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatalf("Land 1: %v", err)
	}

	landing, err := g.Land(context.Background(), phaseTwo(""))

	if err != nil {
		t.Fatalf("Land 2: %v", err)
	}
	if landing.MergeSHA != e.head() {
		t.Errorf("HEAD %s is not the landing %s", e.head(), landing.MergeSHA)
	}
	if st := gitCmd(t, e.root, "status", "--porcelain"); st != "" {
		t.Errorf("primary tree not clean after a skipped report:\n%s", st)
	}
	if len(e.store.events("report-skipped")) != 1 {
		t.Errorf("report-skipped not recorded")
	}
}

func TestGateFixAndMilestoneSessionsStoreTheirLiveStepMetadata(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, _, _ := e.boundaryGate("ok")
	g.FixRounds = 1
	g.FixKind.Row.Provider = "codex"
	g.Runner = runnerFunc(func(ctx context.Context, ref core.StepRef, obs core.Observer) core.Outcome {
		s := &core.Session{Ref: ref, Workspace: "w9"}
		obs.Started(s)
		return core.Outcome{State: core.StepFailed, Reason: "no fix", Session: s}
	})
	g.Land(context.Background(), phaseOne("exit 1"))
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatalf("Land 1: %v", err)
	}
	if _, err := g.Land(context.Background(), phaseTwo("")); err != nil {
		t.Fatalf("Land 2: %v", err)
	}

	var got []string
	for _, ev := range e.store.events("step") {
		f := ev.Fields
		got = append(got, fmt.Sprintf("%s %s %s %s %s %s", ev.Phase, ev.Step, f["state"], f["attempt"], f["provider"], f["workspace"]))
	}
	want := []string{
		"1 gatefix running 1 codex w9",
		"1 gatefix failed 1 codex w9",
		"2 milestone running 1 claude w1",
		"2 milestone ok 1 claude w1",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("step events %q, want %q", got, want)
	}
}

package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/gitrepo"
	"r-loop/internal/plan"
)

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			var pid int
			if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pid); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid not written to %s", path)
	return 0
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("process %d remains", pid)
}

func assertNoMergeHead(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, ".git", "MERGE_HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("MERGE_HEAD remains: %v", err)
	}
}

func TestLandGateCancelledWhileTheGateRunsKillsItsGroupAndAbortsTheMerge(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	pidFile := filepath.Join(t.TempDir(), "gate.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := e.gate().Land(ctx, phaseOne("echo $$ > '"+pidFile+"'; sleep 300"))
		result <- err
	}()
	pid := waitForPID(t, pidFile)
	cancel()
	select {
	case err := <-result:
		if err == nil || errors.Is(err, core.ErrGate) {
			t.Errorf("Land err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("land did not stop")
	}
	assertProcessGone(t, pid)
	e.assertUntouched(head)
	assertNoMergeHead(t, e.root)
}

type cancelAfterGate struct {
	*gitrepo.Repo
	cancel context.CancelFunc
}

func (r cancelAfterGate) Run(ctx context.Context, dir, command string, timeout time.Duration) (int, string, error) {
	code, output, err := r.Repo.Run(ctx, dir, command, timeout)
	r.cancel()
	return code, output, err
}

func TestLandCancelledAsTheGatePassesNeitherTicksNorCommits(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := e.gate()
	g.Repo = cancelAfterGate{Repo: e.repo, cancel: cancel}
	_, err := g.Land(ctx, phaseOne("true"))
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("Land err = %v", err)
	}
	if len(e.plan.ticks) != 0 {
		t.Errorf("ticks = %v", e.plan.ticks)
	}
	e.assertUntouched(head)
	assertNoMergeHead(t, e.root)
}

func TestLandPassingGateAndCancelledDuringAHangingCommitHookLeavesACleanTree(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	pidFile := filepath.Join(t.TempDir(), "hook.pid")
	hook := filepath.Join(e.root, ".git", "hooks", "pre-commit")
	writeFile(t, hook, "#!/bin/sh\necho $$ > '"+pidFile+"'\nsleep 300\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := e.gate().Land(ctx, phaseOne("true"))
		result <- err
	}()
	pid := waitForPID(t, pidFile)
	cancel()
	select {
	case err := <-result:
		if err == nil || errors.Is(err, core.ErrGate) {
			t.Errorf("Land err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("land did not stop")
	}
	assertProcessGone(t, pid)
	e.assertUntouched(head)
	assertNoMergeHead(t, e.root)
}

func TestLandCancelledDuringPostCommitHookRecordsTheLanding(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	before := e.head()
	pidFile := filepath.Join(t.TempDir(), "hook.pid")
	hook := filepath.Join(e.root, ".git", "hooks", "post-commit")
	writeFile(t, hook, "#!/bin/sh\necho $$ > '"+pidFile+"'\nsleep 300\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		landing core.Landing
		err     error
	}
	done := make(chan result, 1)
	go func() { landing, err := e.gate().Land(ctx, phaseOne("true")); done <- result{landing, err} }()
	pid := waitForPID(t, pidFile)
	cancel()
	select {
	case got := <-done:
		if got.err != nil || got.landing.MergeSHA == "" || got.landing.MergeSHA != e.head() || got.landing.MergeSHA == before {
			t.Fatalf("Land = %+v, %v; HEAD = %s", got.landing, got.err, e.head())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Land did not stop")
	}
	assertProcessGone(t, pid)
	if len(e.store.landings()) != 1 || gitCmd(t, e.root, "status", "--porcelain") != "" {
		t.Fatalf("landings = %+v; status = %q", e.store.landings(), gitCmd(t, e.root, "status", "--porcelain"))
	}
	assertNoMergeHead(t, e.root)
}

func TestLandGatePassingWithABackgroundChildLands(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	start := time.Now()
	landing, err := e.gate().Land(context.Background(), phaseOne("sleep 30 & echo green"))
	if err != nil || landing.MergeSHA != e.head() || time.Since(start) >= 5*time.Second {
		t.Fatalf("landing = %+v, err = %v, elapsed = %s", landing, err, time.Since(start))
	}
}

func TestLandGateRedWithABackgroundChildIsAGateFailureWithItsExitCode(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	_, err := e.gate().Land(context.Background(), phaseOne("sleep 30 & exit 3"))
	if !errors.Is(err, core.ErrGate) || !strings.Contains(err.Error(), "exited 3") {
		t.Fatalf("Land err = %v", err)
	}
}

func TestLandGateRedWithABackgroundChildGetsAGateFixRound(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	var refs []core.StepRef
	g := e.gate()
	g.FixRounds = 1
	g.Runner = fixingRunner(e, &refs)
	landing, err := g.Land(context.Background(), phaseOne("test -f fix1.txt || { sleep 30 & exit 3; }"))
	if err != nil || landing.MergeSHA != e.head() || len(refs) != 1 || len(e.store.events("gate-fix")) != 1 {
		t.Fatalf("landing = %+v, err = %v, rounds = %d", landing, err, len(refs))
	}
}

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
	ticks [][]string
}

func (p *tickPlan) Read(path string) (core.Plan, error) { return core.Plan{}, nil }

func (p *tickPlan) Tick(path string, ph core.Phase) error {
	p.ticks = append(p.ticks, ph.TickIDs())
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.root, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	for _, id := range ph.TickIDs() {
		text = strings.ReplaceAll(text, fmt.Sprintf("- [ ] p%s ", id), fmt.Sprintf("- [x] p%s ", id))
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

type memStore struct {
	mu      sync.Mutex
	dir     string
	branch  string
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
	st := core.RunState{ID: runID, Branch: s.branch, Steps: map[core.StepKey]core.StepState{}}
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
func (s *memStore) ClearCurrent(string, int) error         { return nil }
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

func TestLandGatePrintsNothingPassesWhenTheCommandPrintsNothing(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	landing, err := e.gate().Land(context.Background(), phaseOne("`test -f feature.txt` is green and `grep -n TODO feature.txt` prints nothing."))
	if err != nil {
		t.Fatal(err)
	}
	if landing.MergeSHA != e.head() {
		t.Errorf("landing = %+v", landing)
	}
}

func TestLandGatePrintsNothingFailsWhenTheCommandPrints(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "TODO\n")
	head := e.head()
	_, err := e.gate().Land(context.Background(), phaseOne("`test -f feature.txt` is green and `grep -n TODO feature.txt` prints nothing."))
	if !errors.Is(err, core.ErrGate) || !strings.Contains(err.Error(), "1:TODO") {
		t.Errorf("err = %v", err)
	}
	e.assertUntouched(head)
}

func TestLandGatePrintsNothingFailsOnAnErrorMessage(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	_, err := e.gate().Land(context.Background(), phaseOne("`grep -n TODO missing.txt` prints nothing."))
	if !errors.Is(err, core.ErrGate) {
		t.Errorf("err = %v", err)
	}
	e.assertUntouched(head)
}

func TestLandGatePrintsNothingFailsOnNewlineOnlyOutput(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	_, err := e.gate().Land(context.Background(), phaseOne("`printf '\n\n'` prints nothing."))
	if !errors.Is(err, core.ErrGate) {
		t.Errorf("err = %v", err)
	}
	e.assertUntouched(head)
}

func TestLandGatePrintsALiteralPassesWhenTheOutputContainsItAndNeverRunsIt(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "touch ran.txt\n")
	_, err := e.gate().Land(context.Background(), phaseOne("`cat feature.txt` prints `touch ran.txt`."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.root, "ran.txt")); !os.IsNotExist(err) {
		t.Errorf("ran.txt exists or stat failed: %v", err)
	}
}

func TestLandGatePrintsALiteralFailsWhenTheCommandFails(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	_, err := e.gate().Land(context.Background(), phaseOne("`sh -c 'printf hello; exit 7'` prints `hello`."))
	if !errors.Is(err, core.ErrGate) || !strings.Contains(err.Error(), "hello") {
		t.Errorf("err = %v", err)
	}
	e.assertUntouched(head)
}

func TestLandGatePrintsALiteralFailsWhenTheOutputLacksIt(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "goodbye\n")
	head := e.head()
	_, err := e.gate().Land(context.Background(), phaseOne("`cat feature.txt` prints `hello`."))
	if !errors.Is(err, core.ErrGate) || !strings.Contains(err.Error(), "goodbye") || strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
	e.assertUntouched(head)
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

func TestLandGateLandsSubmodulePointerBump(t *testing.T) {
	e := newLandEnv(t)
	source := t.TempDir()
	gitCmd(t, source, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(source, "version.txt"), "one\n")
	gitCmd(t, source, "add", "-A")
	gitCmd(t, source, "commit", "-q", "-m", "one")
	first := gitCmd(t, source, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(source, "version.txt"), "two\n")
	gitCmd(t, source, "commit", "-q", "-am", "two")
	second := gitCmd(t, source, "rev-parse", "HEAD")
	gitCmd(t, e.root, "-c", "protocol.file.allow=always", "submodule", "add", "-q", source, "sub")
	gitCmd(t, filepath.Join(e.root, "sub"), "checkout", "-q", first)
	gitCmd(t, e.root, "add", "-A")
	gitCmd(t, e.root, "commit", "-q", "-m", "add submodule")
	if err := e.repo.AddWorktree(".r-loop/wt/phase-1", "r-loop/phase-1", "main"); err != nil {
		t.Fatal(err)
	}
	wt := e.worktree(1)
	gitCmd(t, wt, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init")
	gitCmd(t, filepath.Join(wt, "sub"), "checkout", "-q", second)
	gitCmd(t, wt, "add", "sub")
	gitCmd(t, wt, "commit", "-q", "-m", "bump submodule")

	landing, err := e.gate().Land(context.Background(), phaseOne(""))
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if landing.MergeSHA != e.head() {
		t.Errorf("landing = %+v, HEAD = %s", landing, e.head())
	}
	if got := gitCmd(t, e.root, "rev-parse", "HEAD:sub"); got != second {
		t.Errorf("landed gitlink = %s, want %s", got, second)
	}
}

type firstSnapshotRepo struct {
	*gitrepo.Repo
	tree  string
	calls int
}

func (r *firstSnapshotRepo) Snapshot(dir string) (string, error) {
	r.calls++
	if r.calls == 1 {
		return r.tree, nil
	}
	return r.Repo.Snapshot(dir)
}

func TestLandRefusesAnOrdinaryIndexWorktreeMismatchAfterMerge(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	base := e.head()
	g := e.gate()
	g.Repo = &firstSnapshotRepo{Repo: e.repo, tree: gitCmd(t, e.root, "rev-parse", "HEAD^{tree}")}

	_, err := g.Land(context.Background(), phaseOne(""))
	if !errors.Is(err, core.ErrDirtyTree) || !strings.Contains(err.Error(), "feature.txt") {
		t.Fatalf("Land err = %v", err)
	}
	if e.head() != base || len(e.store.landings()) != 0 || len(e.store.events(core.EventCommitIntent)) != 0 {
		t.Fatalf("head = %s, landings = %+v, intents = %+v", e.head(), e.store.landings(), e.store.events(core.EventCommitIntent))
	}
}

func TestLandGateTicksEveryGroupMemberInTheOneLandingCommit(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	before := e.head()
	group := phaseOne("")
	group.Members = []string{"1", "2"}

	landing, err := e.gate().Land(context.Background(), group)

	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if len(e.plan.ticks) != 1 || !slices.Equal(e.plan.ticks[0], []string{"1", "2"}) {
		t.Errorf("ticks = %v, want one call with [1 2]", e.plan.ticks)
	}
	todo := readFile(t, e.todo)
	if !strings.Contains(todo, "- [x] p1 item") || !strings.Contains(todo, "- [x] p2 item") {
		t.Errorf("members not ticked:\n%s", todo)
	}
	if landing.MergeSHA != e.head() || gitCmd(t, e.root, "rev-parse", "HEAD^1") != before {
		t.Errorf("landing %+v is not one commit on %s", landing, before)
	}
	touched := strings.Fields(gitCmd(t, e.root, "show", "--name-only", "--format=", "--diff-merges=first-parent", "HEAD"))
	if !slices.Equal(touched, []string{"docs/demo/todo.md", "feature.txt"}) {
		t.Errorf("landing commit touches %v", touched)
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

func TestLandRestoresATodoThePhaseBranchTickedAndTicksItOnce(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	writeFile(t, filepath.Join(e.worktree(1), "docs/demo/todo.md"), strings.Replace(todoText, "- [ ] p1 item", "- [x] p1 item", 1))
	if _, err := e.repo.CommitAll(".r-loop/wt/phase-1", "self tick"); err != nil {
		t.Fatal(err)
	}
	g := e.gate()
	g.Plan = plan.Reader{}
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}
	got := gitCmd(t, e.root, "show", "HEAD:docs/demo/todo.md")
	if got != strings.TrimSpace(strings.Replace(todoText, "- [ ] p1 item", "- [x] p1 item", 1)) || strings.Count(got, "- [x]") != 1 {
		t.Fatalf("todo = %q", got)
	}
	if gitCmd(t, e.root, "show", "HEAD:feature.txt") != "new" || gitCmd(t, e.root, "status", "--porcelain") != "" {
		t.Fatal("feature missing or tree dirty")
	}
}

func TestLandTicksOnceWhenTheBranchSelfTickConflictsWithMainsTodo(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	writeFile(t, filepath.Join(e.worktree(1), "docs/demo/todo.md"), strings.Replace(todoText, "- [ ] p1 item", "- [x] p1 item", 1))
	if _, err := e.repo.CommitAll(".r-loop/wt/phase-1", "self tick"); err != nil {
		t.Fatal(err)
	}
	mainTodo := strings.Replace(todoText, "- [ ] p1 item", "- [ ] p1 item, reworded", 1)
	writeFile(t, e.todo, mainTodo)
	gitCmd(t, e.root, "add", "-A")
	gitCmd(t, e.root, "commit", "-q", "-m", "reword")
	g := e.gate()
	g.Plan = plan.Reader{}
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}
	got := gitCmd(t, e.root, "show", "HEAD:docs/demo/todo.md")
	if got != strings.TrimSpace(strings.Replace(mainTodo, "- [ ] p1 item, reworded", "- [x] p1 item, reworded", 1)) || strings.Count(got, "- [x]") != 1 {
		t.Fatalf("todo = %q", got)
	}
	if gitCmd(t, e.root, "show", "HEAD:feature.txt") != "new" || gitCmd(t, e.root, "status", "--porcelain") != "" {
		t.Fatal("feature missing or tree dirty")
	}
}

func TestLandRestoresATodoThePhaseBranchDeleted(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	gitCmd(t, e.worktree(1), "rm", "-q", "docs/demo/todo.md")
	if _, err := e.repo.CommitAll(".r-loop/wt/phase-1", "delete todo"); err != nil {
		t.Fatal(err)
	}
	g := e.gate()
	g.Plan = plan.Reader{}
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}
	got := gitCmd(t, e.root, "show", "HEAD:docs/demo/todo.md")
	if got != strings.TrimSpace(strings.Replace(todoText, "- [ ] p1 item", "- [x] p1 item", 1)) {
		t.Fatalf("todo = %q", got)
	}
}

func TestLandTicksTheSameBacklogItemWhenThePhaseBranchInsertedOne(t *testing.T) {
	e := newLandEnv(t)
	backlog := "# Backlog\n\n- [ ] first item\n- [ ] second item\n- [ ] third item\n"
	writeFile(t, e.todo, backlog)
	gitCmd(t, e.root, "add", "-A")
	gitCmd(t, e.root, "commit", "-q", "-m", "backlog")
	if err := e.repo.AddWorktree(".r-loop/wt/phase-2", "r-loop/phase-2", "main"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.worktree(2), "feature.txt"), "new\n")
	writeFile(t, filepath.Join(e.worktree(2), "docs/demo/todo.md"), strings.Replace(backlog, "- [ ] first item", "- [ ] inserted item\n- [ ] first item", 1))
	if _, err := e.repo.CommitAll(".r-loop/wt/phase-2", "insert backlog item"); err != nil {
		t.Fatal(err)
	}
	g := e.gate()
	g.Plan = plan.Reader{}
	if _, err := g.Land(context.Background(), core.Phase{ID: "2", Title: "second item", Items: []core.Item{{Text: "second item"}}}); err != nil {
		t.Fatal(err)
	}
	got := gitCmd(t, e.root, "show", "HEAD:docs/demo/todo.md")
	want := "# Backlog\n\n- [ ] first item\n- [x] second item  <!-- fixed: r-loop/phase-2 -->\n- [ ] third item"
	if got != want {
		t.Fatalf("todo = %q, want %q", got, want)
	}
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
	stopped []string
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
	if outcome == "panic" {
		if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("scribbled\n"), 0o644); err != nil {
			return err
		}
		panic("boom")
	}
	report, sentinel, _ := strings.Cut(text, "\n")
	if outcome == "ok" || outcome == "partial" || outcome == "extra" || outcome == "staged" || outcome == "commit" || outcome == "checkout" {
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
	if outcome == "extra" {
		if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("scribbled\n"), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(cwd, "stray.txt"), []byte("stray\n"), 0o644); err != nil {
			return err
		}
		outcome = "ok"
	}
	if outcome == "staged" {
		if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("scribbled\n"), 0o644); err != nil {
			return err
		}
		if out, err := exec.Command("git", "-C", cwd, "add", "a.txt").CombinedOutput(); err != nil {
			return fmt.Errorf("git add: %w: %s", err, out)
		}
		if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("one\n"), 0o644); err != nil {
			return err
		}
		outcome = "ok"
	}
	if outcome == "commit" || outcome == "checkout" {
		if outcome == "checkout" {
			if out, err := exec.Command("git", "-C", cwd, "checkout", "-qb", "other").CombinedOutput(); err != nil {
				return fmt.Errorf("git checkout: %w: %s", err, out)
			}
		}
		if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("scribbled\n"), 0o644); err != nil {
			return err
		}
		if out, err := exec.Command("git", "-C", cwd, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "-qam", "agent").CombinedOutput(); err != nil {
			return fmt.Errorf("git commit: %w: %s", err, out)
		}
		outcome = "ok"
	}
	data, _ := json.Marshal(map[string]string{"outcome": outcome, "reason": "report failed"})
	return os.WriteFile(sentinel, data, 0o644)
}

func (h *reportHost) State(agent string) (core.AgentState, error)  { return core.AgentWorking, nil }
func (h *reportHost) AgentPane(agent string) (string, error)       { return "", nil }
func (h *reportHost) Read(agent string, lines int) (string, error) { return "", nil }
func (h *reportHost) Screen(agent string) (string, error)          { return "", nil }
func (h *reportHost) SendKeys(agent string, keys ...string) error  { return nil }
func (h *reportHost) SendText(agent, text string) error            { return nil }
func (h *reportHost) Interrupt(agent string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = append(h.stopped, agent)
	return nil
}
func (h *reportHost) Tag(workspaceID string, tokens map[string]string) error { return nil }
func (h *reportHost) Close(workspaceID string) error                         { return nil }
func (h *reportHost) ClosePane(pane string) error                            { return nil }
func (h *reportHost) Split(pane, direction, cwd string, env map[string]string) (string, error) {
	return "", nil
}

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
		Resolve: func(provider, model, effort, askURL, mcp, dir string) (core.ProviderArgs, error) {
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

func TestMilestoneReportThatChangedAnotherPathIsSkippedAndDiscarded(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, _, _ := e.boundaryGate("extra")
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}
	landing, err := g.Land(context.Background(), phaseTwo(""))
	if err != nil {
		t.Fatal(err)
	}
	if e.head() != landing.MergeSHA || readFile(t, filepath.Join(e.root, "a.txt")) != "one\n" || gitCmd(t, e.root, "status", "--porcelain") != "" {
		t.Fatal("report changes remain")
	}
	if _, err := os.Stat(filepath.Join(e.root, "stray.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stray remains: %v", err)
	}
	events := e.store.events("report-skipped")
	if len(events) != 1 || !strings.Contains(events[0].Fields["reason"], "a.txt") || !strings.Contains(events[0].Fields["reason"], "stray.txt") {
		t.Fatalf("events = %+v", events)
	}
}

func TestMilestoneReportCommitCarryingAStagedPathIsUndone(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, _, _ := e.boundaryGate("staged")
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}
	landing, err := g.Land(context.Background(), phaseTwo(""))
	if err != nil {
		t.Fatal(err)
	}
	if e.head() != landing.MergeSHA || readFile(t, filepath.Join(e.root, "a.txt")) != "one\n" || gitCmd(t, e.root, "status", "--porcelain") != "" {
		t.Fatal("staged changes remain")
	}
	events := e.store.events("report-skipped")
	if len(events) != 1 || !strings.Contains(events[0].Fields["reason"], "touched") || !strings.Contains(events[0].Fields["reason"], "a.txt") {
		t.Fatalf("events = %+v", events)
	}
}

func TestMilestoneReportAgentCommitIsUndone(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, _, _ := e.boundaryGate("commit")
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}
	landing, err := g.Land(context.Background(), phaseTwo(""))
	if err != nil {
		t.Fatal(err)
	}
	if e.head() != landing.MergeSHA || readFile(t, filepath.Join(e.root, "a.txt")) != "one\n" || gitCmd(t, e.root, "status", "--porcelain") != "" {
		t.Fatal("agent commit remains")
	}
	if _, err := os.Stat(filepath.Join(e.root, "docs/demo/reports/milestone-1-the-core.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("report remains: %v", err)
	}
	if len(e.store.events("report-skipped")) != 1 {
		t.Fatal("missing report-skipped")
	}
}

func TestMilestoneReportThatLeftTheBranchResetsNothing(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, _, _ := e.boundaryGate("checkout")
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}
	landing, err := g.Land(context.Background(), phaseTwo(""))
	if err != nil {
		t.Fatal(err)
	}
	if gitCmd(t, e.root, "rev-parse", "main") != landing.MergeSHA || gitCmd(t, e.root, "show", "other:a.txt") != "scribbled" {
		t.Fatal("branch commit lost")
	}
	events := e.store.events("report-skipped")
	if len(events) != 1 || !strings.Contains(events[0].Fields["reason"], "left main for other") {
		t.Fatalf("events = %+v", events)
	}
}

func TestLandRefusesAChangeStagedApartFromTheWorkingTreeDuringTheGate(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	base := e.head()
	command := "git show :one.txt > .o && printf 'x\\n' >> one.txt && git add one.txt && mv .o one.txt"
	_, err := e.gate().Land(context.Background(), phaseOne("`"+command+"`"))
	if !errors.Is(err, core.ErrDirtyTree) || !errors.Is(err, core.ErrUnfinishedMerge) || !strings.Contains(err.Error(), "one.txt") || !strings.Contains(err.Error(), "unfinished merge") {
		t.Fatalf("Land err = %v", err)
	}
	if e.head() != base || len(e.store.landings()) != 0 || len(e.store.events(core.EventCommitIntent)) != 0 {
		t.Fatalf("head = %s, landings = %+v, intents = %+v", e.head(), e.store.landings(), e.store.events(core.EventCommitIntent))
	}
	if _, statErr := os.Stat(filepath.Join(e.root, ".git", "MERGE_HEAD")); statErr != nil {
		t.Fatalf("MERGE_HEAD missing: %v", statErr)
	}
	if got := readFile(t, e.todo); got != todoText {
		t.Fatalf("todo changed: %q", got)
	}
}

type failStepEvents struct{ core.Store }

func (s failStepEvents) Append(runID string, rec core.Record) error {
	if rec.Kind == core.RecordEvent && rec.Event != nil && rec.Event.Kind == "step" && rec.Event.Step == "milestone" {
		return errors.New("disk full")
	}
	return s.Store.Append(runID, rec)
}

func TestMilestoneReportCommitsNothingOnceAStepRecordHasFailed(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, _, _ := e.boundaryGate("ok")
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatalf("Land 1: %v", err)
	}
	g.Boundary.Sessions.Store = &core.RecordGuard{Store: failStepEvents{e.store}}
	if _, err := g.Land(context.Background(), phaseTwo("")); err != nil {
		t.Fatalf("Land 2: %v", err)
	}
	if got := gitCmd(t, e.root, "log", "-1", "--format=%s"); got == "docs(report): milestone 1" {
		t.Fatalf("report was committed")
	}
	if got := gitCmd(t, e.root, "status", "--porcelain"); got != "" {
		t.Fatalf("primary tree dirty: %q", got)
	}
	skips := e.store.events("report-skipped")
	if len(skips) != 1 || !strings.Contains(skips[0].Fields["reason"], "record: disk full") {
		t.Fatalf("skips = %+v", skips)
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

type panicTickPlan struct{ *tickPlan }

func (p panicTickPlan) Tick(path string, ph core.Phase) error {
	if err := p.tickPlan.Tick(path, ph); err != nil {
		return err
	}
	panic("boom")
}

func TestAPanicAfterTheMergeRestoresTheTodoAndAbortsTheMerge(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	head := e.head()
	g := e.gate()
	g.Plan = panicTickPlan{e.plan}
	_, err := g.Land(context.Background(), phaseOne(""))
	if err == nil || !strings.Contains(err.Error(), "panic in land: boom") {
		t.Fatalf("Land err = %v", err)
	}
	e.assertUntouched(head)
	assertNoMergeHead(t, e.root)
	if ev := e.face.events; !slices.ContainsFunc(ev, func(v core.Event) bool {
		return v.Kind == "error" && v.Fields["reason"] == "panic in land: boom"
	}) {
		t.Fatalf("panic error event missing: %+v", ev)
	}
}

func TestAPanicAfterAMergeThatChangedTodoAbortsBeforeRestoringIt(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	writeFile(t, filepath.Join(e.worktree(1), "docs/demo/todo.md"), todoText+"branch edit\n")
	if _, err := e.repo.CommitAll(".r-loop/wt/phase-1", "edit todo"); err != nil {
		t.Fatal(err)
	}
	head := e.head()
	g := e.gate()
	g.Plan = panicTickPlan{e.plan}
	_, err := g.Land(context.Background(), phaseOne(""))
	if err == nil || !strings.Contains(err.Error(), "panic in land: boom") {
		t.Fatalf("Land err = %v", err)
	}
	e.assertUntouched(head)
	assertNoMergeHead(t, e.root)
}

type panicFinishedFace struct {
	*memFace
	panicked bool
}

func (f *panicFinishedFace) Emit(ev core.Event) {
	if ev.Kind == "step" && ev.Fields["state"] == string(core.StepOK) && !f.panicked {
		f.panicked = true
		panic("finished event")
	}
	f.memFace.Emit(ev)
}

func TestAPanicReportingAnAlreadyOKGateFixDoesNotFailIt(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	g := e.gate()
	g.FixRounds = 1
	g.Face = &panicFinishedFace{memFace: e.face}
	g.Runner = runnerFunc(func(ctx context.Context, ref core.StepRef, obs core.Observer) core.Outcome {
		out := fixingRunner(e, new([]core.StepRef))(ctx, ref, obs)
		if out.State == core.StepOK {
			key := ref.Key
			if err := e.store.Append(ref.Key.Run, core.Record{Kind: core.RecordStep, Step: &key, State: core.StepOK}); err != nil {
				t.Fatal(err)
			}
		}
		return out
	})
	landing, err := g.Land(context.Background(), phaseOne("test -f fix1.txt"))
	if err != nil || landing.MergeSHA != e.head() {
		t.Fatalf("Land = %+v, %v", landing, err)
	}
	st, err := e.store.Load("run1")
	if err != nil {
		t.Fatal(err)
	}
	key := core.StepKey{Run: "run1", Phase: "1", Kind: "gatefix", Attempt: 1}
	if got := st.Steps[key]; got != core.StepOK {
		t.Fatalf("gate-fix state = %s, want ok", got)
	}
	for _, rec := range e.store.records {
		if rec.Kind == core.RecordStep && rec.Step != nil && *rec.Step == key && rec.State == core.StepFailed {
			t.Fatalf("failed after ok: %+v", rec)
		}
	}
}

func TestAPanickingGateFixRunnerFailsItsStepAndTheLand(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	head := e.head()
	g := e.gate()
	g.FixRounds = 1
	g.Runner = runnerFunc(func(context.Context, core.StepRef, core.Observer) core.Outcome { panic("boom") })
	_, err := g.Land(context.Background(), phaseOne("false"))
	if !errors.Is(err, core.ErrGate) || !strings.Contains(err.Error(), "gate-fix round 1 ended failed: panic in gate-fix: boom") {
		t.Fatalf("Land err = %v", err)
	}
	var last *core.Record
	for i := range e.store.records {
		rec := &e.store.records[i]
		if rec.Kind == core.RecordStep && rec.Step != nil && rec.Step.Kind == "gatefix" && rec.Step.Attempt == 1 {
			last = rec
		}
	}
	if last == nil || last.State != core.StepFailed || !strings.Contains(last.Reason, "panic in gate-fix: boom") {
		t.Fatalf("last gatefix record = %+v", last)
	}
	e.assertUntouched(head)
}

func TestAPanickingGateFixAfterFailureReportsReasonAndFinishedEvent(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	g := e.gate()
	g.FixRounds = 1
	g.Runner = runnerFunc(func(_ context.Context, ref core.StepRef, _ core.Observer) core.Outcome {
		key := ref.Key
		if err := e.store.Append(ref.Key.Run, core.Record{Kind: core.RecordStep, Step: &key, State: core.StepFailed}); err != nil {
			t.Fatal(err)
		}
		panic("after failure")
	})
	_, err := g.Land(context.Background(), phaseOne("false"))
	if !errors.Is(err, core.ErrGate) || !strings.Contains(err.Error(), "panic in gate-fix: after failure") {
		t.Fatalf("Land err = %v", err)
	}
	var finished bool
	for _, ev := range e.store.events("step") {
		if ev.Step == "gatefix" && ev.Fields["state"] == string(core.StepFailed) && strings.Contains(ev.Fields["reason"], "panic in gate-fix: after failure") {
			finished = true
		}
	}
	if !finished {
		t.Fatal("gate-fix failed event with panic reason missing")
	}
}

func TestAPanickingMilestoneReportIsSkippedAndThePrimaryTreeIsClean(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, _, _ := e.boundaryGate("panic")
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatalf("Land 1: %v", err)
	}
	landing, err := g.Land(context.Background(), phaseTwo(""))
	if err != nil || landing.MergeSHA != e.head() {
		t.Fatalf("Land 2 = %+v, %v", landing, err)
	}
	if status := gitCmd(t, e.root, "status", "--porcelain"); status != "" {
		t.Fatalf("primary tree dirty: %q", status)
	}
	skips := e.store.events("report-skipped")
	if len(skips) != 1 || skips[0].Fields["reason"] != "panic in milestone report: boom" {
		t.Fatalf("report skips = %+v", skips)
	}
	var last *core.Record
	for i := range e.store.records {
		rec := &e.store.records[i]
		if rec.Kind == core.RecordStep && rec.Step != nil && rec.Step.Kind == "milestone" && rec.Step.Attempt == 1 {
			last = rec
		}
	}
	if last == nil || last.State != core.StepFailed || !strings.Contains(last.Reason, "panic in milestone report: boom") {
		t.Fatalf("last milestone record = %+v", last)
	}
}

type panicCommitRepo struct{ *gitrepo.Repo }

func (r panicCommitRepo) Commit(ctx context.Context, message string, paths ...string) (string, error) {
	sha, err := r.Repo.Commit(ctx, message, paths...)
	if err == nil {
		panic("boom")
	}
	return sha, err
}

type panicTouchesRepo struct{ *gitrepo.Repo }

func (r panicTouchesRepo) CommitTouches(commit string) ([]string, error) { panic("boom") }

func TestAPanicAfterTheLandingCommitKeepsItUnrecorded(t *testing.T) {
	for _, tc := range []struct {
		name string
		repo func(*gitrepo.Repo) core.Repo
	}{
		{"commit", func(r *gitrepo.Repo) core.Repo { return panicCommitRepo{r} }},
		{"touches", func(r *gitrepo.Repo) core.Repo { return panicTouchesRepo{r} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newLandEnv(t)
			e.phaseWork(1, "one.txt", "1\n")
			before := e.head()
			g := e.gate()
			g.Repo = tc.repo(e.repo)
			_, err := g.Land(context.Background(), phaseOne(""))
			if err == nil || !strings.Contains(err.Error(), "panic in land: boom") {
				t.Fatalf("Land err = %v", err)
			}
			if got := e.store.landings(); len(got) != 0 {
				t.Fatalf("landing recorded: %+v", got)
			}
			if head := e.head(); head == before || gitCmd(t, e.root, "rev-parse", "HEAD^1") != before {
				t.Fatalf("HEAD = %s, before = %s", head, before)
			}
			if status := gitCmd(t, e.root, "status", "--porcelain"); status != "" {
				t.Fatalf("primary tree dirty: %q", status)
			}
			assertNoMergeHead(t, e.root)
			intents := e.store.events(core.EventCommitIntent)
			if len(intents) == 0 || intents[len(intents)-1].Phase != "1" || intents[len(intents)-1].Fields["tree"] != gitCmd(t, e.root, "rev-parse", "HEAD^{tree}") {
				t.Fatalf("commit intents = %+v", intents)
			}
			if ev := e.face.events; !slices.ContainsFunc(ev, func(v core.Event) bool {
				return v.Kind == "error" && v.Fields["reason"] == "panic in land: boom"
			}) {
				t.Fatalf("panic error event missing: %+v", ev)
			}
		})
	}
}

type panicLandingStore struct{ core.Store }

func (s panicLandingStore) Append(runID string, rec core.Record) error {
	if rec.Kind == core.RecordLanding {
		panic("boom")
	}
	return s.Store.Append(runID, rec)
}

func TestAPanicRecordingTheLandingLeavesTheStateResumeReconciles(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	before := e.head()
	g := e.gate()
	g.Store = panicLandingStore{e.store}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		g.Land(context.Background(), phaseOne(""))
	}()
	if recovered != "boom" {
		t.Fatalf("recovered = %v", recovered)
	}
	if head := e.head(); head == before || gitCmd(t, e.root, "rev-parse", "HEAD^1") != before {
		t.Fatalf("HEAD = %s, before = %s", head, before)
	}
	if got := e.store.landings(); len(got) != 0 {
		t.Fatalf("landing recorded: %+v", got)
	}
	merges := e.store.events(core.EventMergeIntent)
	commits := e.store.events(core.EventCommitIntent)
	if len(merges) == 0 || merges[len(merges)-1].Fields["base"] != before || len(commits) == 0 || commits[len(commits)-1].Fields["tree"] != gitCmd(t, e.root, "rev-parse", "HEAD^{tree}") {
		t.Fatalf("merge intents = %+v; commit intents = %+v", merges, commits)
	}
}

type panicResetRepo struct {
	*gitrepo.Repo
	resetCalls int
}

func (r *panicResetRepo) CommitTouches(commit string) ([]string, error) {
	return []string{"one.txt"}, nil
}

func (r *panicResetRepo) ResetKeep(ref string) error {
	r.resetCalls++
	if r.resetCalls == 1 {
		panic("boom")
	}
	return r.Repo.ResetKeep(ref)
}

func TestAPanicWhileRejectingTheLandingCommitResetsIt(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	head := e.head()
	g := e.gate()
	g.Repo = &panicResetRepo{Repo: e.repo}
	_, err := g.Land(context.Background(), phaseOne(""))
	if err == nil || !strings.Contains(err.Error(), "panic in land: boom") {
		t.Fatalf("Land err = %v", err)
	}
	e.assertUntouched(head)
}

type panicReportCommitRepo struct{ *gitrepo.Repo }

func (r panicReportCommitRepo) Commit(ctx context.Context, message string, paths ...string) (string, error) {
	if strings.HasPrefix(message, "docs(report):") {
		panic("report commit")
	}
	return r.Repo.Commit(ctx, message, paths...)
}

func TestMilestoneReportPanicAfterOKDoesNotEmitFailedStep(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, _, _ := e.boundaryGate("ok")
	g.Boundary.Repo = panicReportCommitRepo{e.repo}
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatalf("Land 1: %v", err)
	}
	if _, err := g.Land(context.Background(), phaseTwo("")); err != nil {
		t.Fatalf("Land 2: %v", err)
	}
	var terminal []string
	for _, ev := range e.store.events("step") {
		if ev.Step == "milestone" && (ev.Fields["state"] == string(core.StepOK) || ev.Fields["state"] == string(core.StepFailed)) {
			terminal = append(terminal, ev.Fields["state"])
		}
	}
	if !slices.Equal(terminal, []string{string(core.StepOK)}) {
		t.Fatalf("milestone terminal events = %v", terminal)
	}
	if skips := e.store.events("report-skipped"); len(skips) != 1 || !strings.Contains(skips[0].Fields["reason"], "panic in milestone report: report commit") {
		t.Fatalf("report skips = %+v", skips)
	}
}

func TestMilestoneReportPanicAfterTerminalStepStopsSession(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, host, _ := e.boundaryGate("ok")
	g.Boundary.Face = &panicFinishedFace{memFace: e.face}
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatalf("Land 1: %v", err)
	}
	if _, err := g.Land(context.Background(), phaseTwo("")); err != nil {
		t.Fatalf("Land 2: %v", err)
	}
	host.mu.Lock()
	stopped := slices.Clone(host.stopped)
	host.mu.Unlock()
	if len(stopped) != 1 {
		t.Fatalf("stopped sessions = %v, want milestone session", stopped)
	}
}

type panicMilestoneLoadStore struct {
	core.Store
	panicLoad bool
}

func (s *panicMilestoneLoadStore) Load(runID string) (core.RunState, error) {
	if s.panicLoad {
		panic("load in recovery")
	}
	return s.Store.Load(runID)
}

type panicMilestoneLoadFace struct {
	*memFace
	store *panicMilestoneLoadStore
}

func (f *panicMilestoneLoadFace) Emit(ev core.Event) {
	if ev.Kind == "step" && ev.Step == "milestone" && ev.Fields["state"] == string(core.StepOK) {
		f.store.panicLoad = true
		panic("finished event")
	}
	f.memFace.Emit(ev)
}

func TestMilestoneReportRecoverySurvivesStoreLoadPanic(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, host, _ := e.boundaryGate("ok")
	store := &panicMilestoneLoadStore{Store: e.store}
	g.Boundary.Sessions.Store = store
	g.Boundary.Face = &panicMilestoneLoadFace{memFace: e.face, store: store}
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatalf("Land 1: %v", err)
	}
	if _, err := g.Land(context.Background(), phaseTwo("")); err != nil {
		t.Fatalf("Land 2: %v", err)
	}
	host.mu.Lock()
	stopped := slices.Clone(host.stopped)
	host.mu.Unlock()
	if len(stopped) != 1 {
		t.Fatalf("stopped sessions = %v, want milestone session", stopped)
	}
}

type blockerScript struct {
	mu       sync.Mutex
	blockers []core.Blocker
	answers  []core.Resolution
	before   func(n int)
}

func (s *blockerScript) raise(ctx context.Context, b core.Blocker) (core.Resolution, bool) {
	s.mu.Lock()
	s.blockers = append(s.blockers, b)
	n := len(s.blockers)
	s.mu.Unlock()
	if s.before != nil {
		s.before(n)
	}
	res := s.answers[min(n, len(s.answers))-1]
	res.ID = fmt.Sprintf("b%d", n)
	return res, true
}

func failingFixThen(e *landEnv, refs *[]core.StepRef) runnerFunc {
	fixing := fixingRunner(e, refs)
	return func(ctx context.Context, ref core.StepRef, obs core.Observer) core.Outcome {
		if len(*refs) == 0 {
			*refs = append(*refs, ref)
			return core.Outcome{State: core.StepFailed, Reason: "could not fix"}
		}
		return fixing(ctx, ref, obs)
	}
}

func TestAGatefixThatDoesNotEndOKRaisesAGatefixBlockerAndSwitchRunsAnotherRoundOnTheProvider(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	var refs []core.StepRef
	g := e.gate()
	g.FixRounds = 1
	g.FixKind.Row.Provider = "codex"
	g.Runner = failingFixThen(e, &refs)
	script := &blockerScript{answers: []core.Resolution{{Action: "switch", By: "maintainer", Provider: "gemini", Model: "pro", Effort: "high"}}}
	g.Raise = script.raise

	landing, err := g.Land(context.Background(), phaseOne("test -f fix2.txt"))

	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	want := core.Blocker{Source: "gatefix", Phase: "1", Step: "gatefix", Reason: "gate failed: gate-fix round 1 ended failed: could not fix", Actions: []string{"retry", "switch", "block", "stop"}}
	if len(script.blockers) != 1 || !reflect.DeepEqual(script.blockers[0], want) {
		t.Fatalf("blockers = %+v", script.blockers)
	}
	if len(refs) != 2 || refs[1].Key.Attempt != 2 || refs[1].Kind.Row.Provider != "gemini" || refs[1].Kind.Row.Model != "pro" || refs[1].Kind.Row.Effort != "high" {
		t.Fatalf("gate-fix runs = %+v", refs)
	}
	if landing.MergeSHA != e.head() {
		t.Errorf("landing = %+v", landing)
	}
}

func TestAGatefixBlockerRetryRunsAnotherRoundWithTheAddendum(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	var refs []core.StepRef
	g := e.gate()
	g.FixRounds = 1
	g.Runner = failingFixThen(e, &refs)
	script := &blockerScript{answers: []core.Resolution{{Action: "retry", By: "watchdog", Addendum: "the lint step needs gofmt"}}}
	g.Raise = script.raise

	if _, err := g.Land(context.Background(), phaseOne("test -f fix2.txt")); err != nil {
		t.Fatalf("Land: %v", err)
	}
	if len(refs) != 2 || refs[1].Vars["Addendum"] != "the lint step needs gofmt" || refs[1].Kind.Row.Provider != g.FixKind.Row.Provider {
		t.Fatalf("gate-fix runs = %+v", refs)
	}
}

func TestAGatefixBlockerResolvedBlockLeavesTheTreeUntouched(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	var refs []core.StepRef
	g := e.gate()
	g.FixRounds = 1
	g.Runner = failingFixThen(e, &refs)
	g.Raise = (&blockerScript{answers: []core.Resolution{{Action: "block", By: "timeout"}}}).raise

	_, err := g.Land(context.Background(), phaseOne("test -f fix2.txt"))

	if !errors.Is(err, core.ErrGate) || !strings.Contains(err.Error(), "could not fix") {
		t.Fatalf("err = %v", err)
	}
	if len(refs) != 1 {
		t.Errorf("gate-fix runs = %d", len(refs))
	}
	e.assertUntouched(head)
}

type stepWatcher struct {
	mu     sync.Mutex
	events []string
}

func (w *stepWatcher) BeforePhase(context.Context, core.Phase, string) core.CheckOutcome {
	return core.CheckOutcome{}
}
func (w *stepWatcher) StepStarted(ref core.StepRef, s *core.Session) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, fmt.Sprintf("started %s %d", ref.Key.Kind, ref.Key.Attempt))
}
func (w *stepWatcher) StepEnded(ref core.StepRef, out core.Outcome) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, fmt.Sprintf("ended %s %d %s", ref.Key.Kind, ref.Key.Attempt, out.State))
}
func (w *stepWatcher) Signals() <-chan core.Signal                     { return nil }
func (w *stepWatcher) Restarts() <-chan core.Restart                   { return nil }
func (w *stepWatcher) Route(ctx context.Context, q core.Question) bool { return false }

func TestAGatefixStepIsPostedToTheWatcherAsItStartsAndEnds(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	var refs []core.StepRef
	fixing := fixingRunner(e, &refs)
	g := e.gate()
	g.FixRounds = 1
	g.Runner = runnerFunc(func(ctx context.Context, ref core.StepRef, obs core.Observer) core.Outcome {
		obs.Started(&core.Session{Ref: ref})
		return fixing(ctx, ref, obs)
	})
	w := &stepWatcher{}
	g.Watcher = w

	if _, err := g.Land(context.Background(), phaseOne("test -f fix1.txt")); err != nil {
		t.Fatalf("Land: %v", err)
	}
	if want := []string{"started gatefix 1", "ended gatefix 1 ok"}; !reflect.DeepEqual(w.events, want) {
		t.Errorf("watcher = %v, want %v", w.events, want)
	}
}

func TestAFailedMilestoneReportRaisesAMilestoneBlockerAndRetryRunsTheReportAgain(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, host, prompts := e.boundaryGate("failed")
	script := &blockerScript{answers: []core.Resolution{{Action: "retry", By: "watchdog", Addendum: "write the report only"}}, before: func(int) {
		host.mu.Lock()
		host.outcome = "ok"
		host.mu.Unlock()
	}}
	g.Boundary.Raise = script.raise
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Land(context.Background(), phaseTwo("")); err != nil {
		t.Fatalf("Land 2: %v", err)
	}

	want := core.Blocker{Source: "milestone", Phase: "2", Step: "milestone", Reason: "failed: report failed", Actions: []string{"retry", "skip", "stop"}}
	if len(script.blockers) != 1 || !reflect.DeepEqual(script.blockers[0], want) {
		t.Fatalf("blockers = %+v", script.blockers)
	}
	if len(host.opened) != 2 || len(prompts.vars) != 2 || prompts.vars[1]["Addendum"] != "write the report only" {
		t.Fatalf("opened %d, vars %+v", len(host.opened), prompts.vars)
	}
	if !strings.HasSuffix(prompts.vars[1]["Sentinel"].(string), "-a2.sentinel") {
		t.Errorf("second report sentinel %v", prompts.vars[1]["Sentinel"])
	}
	if msg := gitCmd(t, e.root, "log", "-1", "--format=%s"); msg != "docs(report): milestone 1" {
		t.Errorf("head commit = %q", msg)
	}
	if skips := e.store.events("report-skipped"); len(skips) != 0 {
		t.Errorf("report-skipped = %+v", skips)
	}
}

func TestAMilestoneBlockerResolvedSkipRecordsTheSkipAsBefore(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, host, _ := e.boundaryGate("failed")
	g.Boundary.Raise = (&blockerScript{answers: []core.Resolution{{Action: "skip", By: "timeout"}}}).raise
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}

	landing, err := g.Land(context.Background(), phaseTwo(""))

	if err != nil || landing.MergeSHA != e.head() {
		t.Fatalf("Land 2: %v %+v", err, landing)
	}
	if len(host.opened) != 1 {
		t.Errorf("opened = %d", len(host.opened))
	}
	if skips := e.store.events("report-skipped"); len(skips) != 1 || !strings.Contains(skips[0].Fields["reason"], "report failed") {
		t.Errorf("report-skipped = %+v", skips)
	}
}

func TestAMilestoneBlockerResolvedStopRecordsNoSkip(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "1\n")
	e.phaseWork(2, "two.txt", "2\n")
	g, _, _ := e.boundaryGate("failed")
	g.Boundary.Raise = (&blockerScript{answers: []core.Resolution{{Action: "stop", By: "watchdog"}}}).raise
	if _, err := g.Land(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Land(context.Background(), phaseTwo("")); err != nil {
		t.Fatalf("Land 2: %v", err)
	}
	if skips := e.store.events("report-skipped"); len(skips) != 0 {
		t.Errorf("report-skipped = %+v", skips)
	}
	if st := gitCmd(t, e.root, "status", "--porcelain"); st != "" {
		t.Errorf("primary tree not clean:\n%s", st)
	}
}

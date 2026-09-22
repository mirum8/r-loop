package core

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type callLog struct {
	mu     sync.Mutex
	calls  []string
	Shared *callLog
}

func (l *callLog) record(format string, args ...any) {
	call := fmt.Sprintf(format, args...)
	l.mu.Lock()
	l.calls = append(l.calls, call)
	l.mu.Unlock()
	if l.Shared != nil {
		l.Shared.record("%s", call)
	}
}

func (l *callLog) Calls() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

type fakePlanSource struct {
	callLog
	Plan  Plan
	Err   error
	Ticks []int
}

func (f *fakePlanSource) Read(path string) (Plan, error) {
	f.record("PlanSource.Read %s", path)
	return f.Plan, f.Err
}

func (f *fakePlanSource) Tick(path string, phase int) error {
	f.record("PlanSource.Tick %s %d", path, phase)
	f.Ticks = append(f.Ticks, phase)
	return f.Err
}

type fakeSessionHost struct {
	callLog
	Opened  []OpenSpec
	States  map[string]AgentState
	Panes   map[string]string
	Screens map[string]string
	Err     error
	next    int
}

func (f *fakeSessionHost) Reachable() error {
	f.record("SessionHost.Reachable")
	return f.Err
}

func (f *fakeSessionHost) Open(spec OpenSpec) (Workspace, error) {
	f.record("SessionHost.Open %s %s %v", spec.CWD, spec.Label, spec.Env)
	f.Opened = append(f.Opened, spec)
	f.next++
	return Workspace{ID: fmt.Sprintf("ws-%d", f.next), RootPane: fmt.Sprintf("pane-%d", f.next)}, f.Err
}

func (f *fakeSessionHost) Start(pane, name, kind string, args []string) (Agent, error) {
	f.record("SessionHost.Start %s %s %s %v", pane, name, kind, args)
	return Agent{Name: name, Pane: pane}, f.Err
}

func (f *fakeSessionHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	f.record("SessionHost.Prompt %s %q %t %s", agent, text, wait, timeout)
	return f.Err
}

func (f *fakeSessionHost) State(agent string) (AgentState, error) {
	f.record("SessionHost.State %s", agent)
	if s, ok := f.States[agent]; ok {
		return s, f.Err
	}
	return AgentUnknown, f.Err
}

func (f *fakeSessionHost) AgentPane(agent string) (string, error) {
	f.record("SessionHost.AgentPane %s", agent)
	return f.Panes[agent], f.Err
}

func (f *fakeSessionHost) Read(agent string, lines int) (string, error) {
	f.record("SessionHost.Read %s %d", agent, lines)
	return f.Screens[agent], f.Err
}

func (f *fakeSessionHost) Interrupt(agent string) error {
	f.record("SessionHost.Interrupt %s", agent)
	return f.Err
}

func (f *fakeSessionHost) Close(workspaceID string) error {
	f.record("SessionHost.Close %s", workspaceID)
	return f.Err
}

func (f *fakeSessionHost) ClosePane(pane string) error {
	f.record("SessionHost.ClosePane %s", pane)
	return f.Err
}

func (f *fakeSessionHost) Split(pane, direction, cwd string) (string, error) {
	f.record("SessionHost.Split %s %s %s", pane, direction, cwd)
	f.next++
	return fmt.Sprintf("pane-%d", f.next), f.Err
}

type fakeRepo struct {
	callLog
	RootDir     string
	Branch      string
	SHA         string
	DirtyFiles  []string
	Changed     []string
	Added       int
	Deleted     int
	Tree        string
	TreeChanges []string
	Touched     []string
	MergeErr    error
	RunExit     int
	RunOutput   string
	Err         error
}

func (f *fakeRepo) Root() string {
	f.record("Repo.Root")
	return f.RootDir
}

func (f *fakeRepo) Clean() ([]string, error) {
	f.record("Repo.Clean")
	return f.DirtyFiles, f.Err
}

func (f *fakeRepo) HeadBranch() (string, error) {
	f.record("Repo.HeadBranch")
	return f.Branch, f.Err
}

func (f *fakeRepo) HeadSHA(dir string) (string, error) {
	f.record("Repo.HeadSHA %s", dir)
	return f.SHA, f.Err
}

func (f *fakeRepo) AddWorktree(dir, branch, base string) error {
	f.record("Repo.AddWorktree %s %s %s", dir, branch, base)
	return f.Err
}

func (f *fakeRepo) RemoveWorktree(dir string) error {
	f.record("Repo.RemoveWorktree %s", dir)
	return f.Err
}

func (f *fakeRepo) DeleteBranch(branch string) error {
	f.record("Repo.DeleteBranch %s", branch)
	return f.Err
}

func (f *fakeRepo) Dirty(dir string) ([]string, error) {
	f.record("Repo.Dirty %s", dir)
	return f.DirtyFiles, f.Err
}

func (f *fakeRepo) CommitAll(dir, message string) (string, error) {
	f.record("Repo.CommitAll %s %q", dir, message)
	return f.SHA, f.Err
}

func (f *fakeRepo) DiffNonEmpty(dir, ref string) (bool, error) {
	f.record("Repo.DiffNonEmpty %s %s", dir, ref)
	return len(f.Changed) > 0, f.Err
}

func (f *fakeRepo) ChangedFiles(dir, ref string) ([]string, error) {
	f.record("Repo.ChangedFiles %s %s", dir, ref)
	return f.Changed, f.Err
}

func (f *fakeRepo) DiffStat(dir, ref string) (int, int, error) {
	f.record("Repo.DiffStat %s %s", dir, ref)
	return f.Added, f.Deleted, f.Err
}

func (f *fakeRepo) Snapshot(dir string) (string, error) {
	f.record("Repo.Snapshot %s", dir)
	return f.Tree, f.Err
}

func (f *fakeRepo) TreeDiff(from, to string) ([]string, error) {
	f.record("Repo.TreeDiff %s %s", from, to)
	return f.TreeChanges, f.Err
}

func (f *fakeRepo) MergeNoFF(branch string) error {
	f.record("Repo.MergeNoFF %s", branch)
	return f.MergeErr
}

func (f *fakeRepo) AbortMerge() error {
	f.record("Repo.AbortMerge")
	return f.Err
}

func (f *fakeRepo) Commit(message string) (string, error) {
	f.record("Repo.Commit %q", message)
	return f.SHA, f.Err
}

func (f *fakeRepo) CommitTouches(sha string) ([]string, error) {
	f.record("Repo.CommitTouches %s", sha)
	return f.Touched, f.Err
}

func (f *fakeRepo) ResetHard(ref string) error {
	f.record("Repo.ResetHard %s", ref)
	return f.Err
}

func (f *fakeRepo) Run(dir, command string, timeout time.Duration) (int, string, error) {
	f.record("Repo.Run %s %q %s", dir, command, timeout)
	return f.RunExit, f.RunOutput, f.Err
}

type fakeStore struct {
	callLog
	mu         sync.Mutex
	Records    map[string][]Record
	Metas      map[string]RunMeta
	CurrentRun string
	CurrentPID int
	Aborts     map[string]bool
	Err        error
	next       int
}

func (f *fakeStore) Create(meta RunMeta) (string, error) {
	f.record("Store.Create %s", meta.Todo)
	f.next++
	id := fmt.Sprintf("run-%d", f.next)
	if f.Metas == nil {
		f.Metas = map[string]RunMeta{}
	}
	f.Metas[id] = meta
	return id, f.Err
}

func (f *fakeStore) Append(runID string, rec Record) error {
	f.record("Store.Append %s %s", runID, rec.Kind)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Records == nil {
		f.Records = map[string][]Record{}
	}
	f.Records[runID] = append(f.Records[runID], rec)
	return f.Err
}

func (f *fakeStore) Load(runID string) (RunState, error) {
	f.record("Store.Load %s", runID)
	f.mu.Lock()
	defer f.mu.Unlock()
	st := RunState{ID: runID, Todo: f.Metas[runID].Todo, Started: f.Metas[runID].Started, Status: RunCreated, Steps: map[StepKey]StepState{}}
	for _, rec := range f.Records[runID] {
		switch rec.Kind {
		case RecordStep:
			st.Steps[*rec.Step] = rec.State
			st.Span(*rec.Step, rec.State, rec.At)
			key := *rec.Step
			st.LastStep = &key
		case RecordRun:
			st.Status = rec.Run
		case RecordLanding:
			st.Landed = append(st.Landed, *rec.Landing)
		case RecordQuestion:
			st.Questions = upsertQuestion(st.Questions, *rec.Question)
		case RecordSignal:
			st.Signals = append(st.Signals, *rec.Signal)
		case RecordRemedy:
			st.Remedies = append(st.Remedies, *rec.Remedy)
		case RecordEvent:
			st.Events = append(st.Events, *rec.Event)
		}
	}
	return st, f.Err
}

func upsertQuestion(qs []Question, q Question) []Question {
	for i := range qs {
		if qs[i].ID == q.ID {
			qs[i] = q
			return qs
		}
	}
	return append(qs, q)
}

func (f *fakeStore) Current() (string, int, bool) {
	f.record("Store.Current")
	return f.CurrentRun, f.CurrentPID, f.CurrentRun != ""
}

func (f *fakeStore) SetCurrent(runID string, pid int) error {
	f.record("Store.SetCurrent %s %d", runID, pid)
	f.CurrentRun, f.CurrentPID = runID, pid
	return f.Err
}

func (f *fakeStore) ClearCurrent() error {
	f.record("Store.ClearCurrent")
	f.CurrentRun, f.CurrentPID = "", 0
	return f.Err
}

func (f *fakeStore) Aborted(runID string) bool {
	f.record("Store.Aborted %s", runID)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Aborts[runID]
}

func (f *fakeStore) MarkAbort(runID string) error {
	f.record("Store.MarkAbort %s", runID)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Aborts == nil {
		f.Aborts = map[string]bool{}
	}
	f.Aborts[runID] = true
	return f.Err
}

func (f *fakeStore) Dir(runID string) string {
	f.record("Store.Dir %s", runID)
	return "/runs/" + runID
}

type fakePrompts struct {
	callLog
	Texts map[string]string
	Err   error
}

func (f *fakePrompts) Render(name string, vars map[string]any) (string, string, error) {
	f.record("Prompts.Render %s", name)
	return f.Texts[name], "embedded:" + name, f.Err
}

type fakeAskChannel struct {
	callLog
	BaseURL string
	Asked   chan Question
	Err     error
}

func (f *fakeAskChannel) Serve(ctx context.Context) (string, error) {
	f.record("AskChannel.Serve")
	return f.BaseURL, f.Err
}

func (f *fakeAskChannel) StepURL(key StepKey) string {
	f.record("AskChannel.StepURL %s/%d/%s/%d", key.Run, key.Phase, key.Kind, key.Attempt)
	return fmt.Sprintf("%s/%s/%d/%s/%d", f.BaseURL, key.Run, key.Phase, key.Kind, key.Attempt)
}

func (f *fakeAskChannel) Questions() <-chan Question {
	f.record("AskChannel.Questions")
	return f.Asked
}

func (f *fakeAskChannel) Answer(id, answer, by, citation string) error {
	f.record("AskChannel.Answer %s %q %s %q", id, answer, by, citation)
	return f.Err
}

type fakeFace struct {
	callLog
	Events []Event
	Closed bool
}

func (f *fakeFace) Emit(ev Event) {
	f.record("Face.Emit %s", ev.Kind)
	f.Events = append(f.Events, ev)
}

func (f *fakeFace) Close() {
	f.record("Face.Close")
	f.Closed = true
}

type fakeNotifier struct {
	callLog
	Fired []map[string]string
}

func (f *fakeNotifier) Fire(hook string, env map[string]string) {
	f.record("Notifier.Fire %s", hook)
	f.Fired = append(f.Fired, env)
}

var (
	_ PlanSource  = (*fakePlanSource)(nil)
	_ SessionHost = (*fakeSessionHost)(nil)
	_ Repo        = (*fakeRepo)(nil)
	_ Store       = (*fakeStore)(nil)
	_ Prompts     = (*fakePrompts)(nil)
	_ AskChannel  = (*fakeAskChannel)(nil)
	_ Face        = (*fakeFace)(nil)
	_ Notifier    = (*fakeNotifier)(nil)
)

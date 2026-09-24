package core

import (
	"context"
	"errors"
	"time"
)

var ErrMergeConflict = errors.New("merge conflict")

type PlanSource interface {
	Read(path string) (Plan, error)
	Tick(path string, ph Phase) error
}

type OpenSpec struct {
	CWD, Label string
	Env        map[string]string
}

type Workspace struct {
	ID, RootPane string
}

type Agent struct {
	Name, Pane string
}

type AgentState string

const (
	AgentIdle    AgentState = "idle"
	AgentWorking AgentState = "working"
	AgentBlocked AgentState = "blocked"
	AgentDone    AgentState = "done"
	AgentUnknown AgentState = "unknown"
	AgentGone    AgentState = "gone"
)

type SessionHost interface {
	Reachable() error
	Open(spec OpenSpec) (Workspace, error)
	Start(pane, name, kind string, args []string) (Agent, error)
	Prompt(agent, text string, wait bool, timeout time.Duration) error
	State(agent string) (AgentState, error)
	AgentPane(agent string) (string, error)
	Read(agent string, lines int) (string, error)
	Interrupt(agent string) error
	Close(workspaceID string) error
	Tag(workspaceID string, tokens map[string]string) error
	Split(pane, direction, cwd string, env map[string]string) (string, error)
	ClosePane(pane string) error
}

type Repo interface {
	Root() string
	Clean() ([]string, error)
	HeadBranch() (string, error)
	HeadSHA(dir string) (string, error)
	AddWorktree(dir, branch, base string) error
	RemoveWorktree(dir string) error
	DeleteBranch(branch string, force bool) error
	Dirty(dir string) ([]string, error)
	CommitAll(dir, message string) (string, error)
	DiffNonEmpty(dir, ref string) (bool, error)
	ChangedFiles(dir, ref string) ([]string, error)
	DiffStat(dir, ref string) (int, int, error)
	Snapshot(dir string) (string, error)
	IndexTree(paths ...string) (string, error)
	TreeDiff(from, to string) ([]string, error)
	GitlinkPaths(tree string) ([]string, error)
	MergeNoFF(ctx context.Context, branch string, keep ...string) error
	AbortMerge() error
	MergeInProgress() (bool, error)
	Commit(ctx context.Context, message string, paths ...string) (string, error)
	CommitTouches(sha string) ([]string, error)
	ResetHard(ref string) error
	ResetKeep(ref string) error
	Run(ctx context.Context, dir, command string, timeout time.Duration) (int, string, error)
}

type Store interface {
	Create(meta RunMeta) (string, error)
	Append(runID string, rec Record) error
	Load(runID string) (RunState, error)
	Current() (string, int, bool)
	SetCurrent(runID string, pid int) error
	ClearCurrent(runID string, pid int) error
	Aborted(runID string) bool
	MarkAbort(runID string) error
	Dir(runID string) string
}

type Prompts interface {
	Render(name string, vars map[string]any) (string, string, error)
}

type AskChannel interface {
	Serve(ctx context.Context) (string, error)
	StepURL(key StepKey) string
	Questions() <-chan Question
	Answer(id, answer, by, citation string) error
}

type Face interface {
	Emit(ev Event)
	Close()
}

type Notifier interface {
	Fire(hook string, env map[string]string)
}

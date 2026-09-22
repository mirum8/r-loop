package core

import (
	"context"
	"errors"
	"time"
)

var ErrMergeConflict = errors.New("merge conflict")

type PlanSource interface {
	Read(path string) (Plan, error)
	Tick(path string, phase int) error
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
	Split(pane, direction, cwd string) (string, error)
	ClosePane(pane string) error
}

type Repo interface {
	Root() string
	Clean() ([]string, error)
	HeadBranch() (string, error)
	HeadSHA(dir string) (string, error)
	AddWorktree(dir, branch, base string) error
	RemoveWorktree(dir string) error
	DeleteBranch(branch string) error
	Dirty(dir string) ([]string, error)
	CommitAll(dir, message string) (string, error)
	DiffNonEmpty(dir, ref string) (bool, error)
	ChangedFiles(dir, ref string) ([]string, error)
	DiffStat(dir, ref string) (int, int, error)
	Snapshot(dir string) (string, error)
	TreeDiff(from, to string) ([]string, error)
	MergeNoFF(branch string) error
	AbortMerge() error
	Commit(message string) (string, error)
	CommitTouches(sha string) ([]string, error)
	ResetHard(ref string) error
	Run(dir, command string, timeout time.Duration) (int, string, error)
}

type Store interface {
	Create(meta RunMeta) (string, error)
	Append(runID string, rec Record) error
	Load(runID string) (RunState, error)
	Current() (string, int, bool)
	SetCurrent(runID string, pid int) error
	ClearCurrent() error
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

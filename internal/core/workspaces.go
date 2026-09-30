package core

import (
	"fmt"
	"slices"
	"strconv"
)

type StepWorkspace struct {
	Kind, ID string
}

func EveryWorkspace(string, int) bool { return true }

func (l *RunLoop) removeWorktree(phase string, force bool) error {
	wt, branch := fmt.Sprintf(".r-loop/wt/phase-%s", phase), fmt.Sprintf("r-loop/phase-%s", phase)
	l.emit(Event{Kind: "worktree-removed", Phase: phase, Fields: map[string]string{"worktree": wt, "branch": branch}})
	repo := l.Sessions.Repo
	if err := repo.RemoveWorktree(wt); err != nil {
		l.emit(Event{Kind: "warning", Phase: phase, Fields: map[string]string{"reason": "remove worktree " + wt + ": " + err.Error()}})
		return fmt.Errorf("remove worktree %s: %w", wt, err)
	}
	if err := repo.DeleteBranch(branch, force); err != nil {
		l.emit(Event{Kind: "warning", Phase: phase, Fields: map[string]string{"reason": "delete branch " + branch + ": " + err.Error()}})
		return fmt.Errorf("delete branch %s: %w", branch, err)
	}
	return nil
}

func (l *RunLoop) closeWorkspaces(phase string, match func(kind string, attempt int) bool) {
	st, err := l.Store.Load(l.RunID)
	if err != nil {
		l.emit(Event{Kind: "warning", Phase: phase, Fields: map[string]string{"reason": "close workspaces: " + err.Error()}})
		return
	}
	for _, ws := range OpenWorkspaces(st, phase, match) {
		l.emit(Event{Kind: "workspace-closed", Phase: phase, Step: ws.Kind, Fields: map[string]string{"workspace": ws.ID}})
		if err := l.Sessions.Host.Close(ws.ID); err != nil {
			l.emit(Event{Kind: "warning", Phase: phase, Step: ws.Kind, Fields: map[string]string{"reason": "close workspace " + ws.ID + ": " + err.Error()}})
		}
	}
}

func OpenWorkspaces(st RunState, phase string, match func(kind string, attempt int) bool) []StepWorkspace {
	closed := map[string]bool{}
	for _, ev := range st.Events {
		if ev.Kind == "workspace-closed" {
			closed[ev.Fields["workspace"]] = true
		}
	}
	var out []StepWorkspace
	for _, ev := range st.Events {
		id := ev.Fields["workspace"]
		if ev.Kind != "step" || ev.Phase != phase || id == "" || closed[id] {
			continue
		}
		attempt, _ := strconv.Atoi(ev.Fields["attempt"])
		if !match(ev.Step, attempt) || slices.ContainsFunc(out, func(w StepWorkspace) bool { return w.ID == id }) {
			continue
		}
		out = append(out, StepWorkspace{Kind: ev.Step, ID: id})
	}
	return out
}

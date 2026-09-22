package core

import (
	"slices"
	"strconv"
)

type stepWorkspace struct {
	kind, id string
}

func (l *RunLoop) closeWorkspaces(phase int, match func(kind string, attempt int) bool) {
	st, err := l.Store.Load(l.RunID)
	if err != nil {
		l.emit(Event{Kind: "warning", Phase: phase, Fields: map[string]string{"reason": "close workspaces: " + err.Error()}})
		return
	}
	for _, ws := range openWorkspaces(st, phase, match) {
		l.emit(Event{Kind: "workspace-closed", Phase: phase, Step: ws.kind, Fields: map[string]string{"workspace": ws.id}})
		if err := l.Sessions.Host.Close(ws.id); err != nil {
			l.emit(Event{Kind: "warning", Phase: phase, Step: ws.kind, Fields: map[string]string{"reason": "close workspace " + ws.id + ": " + err.Error()}})
		}
	}
}

func openWorkspaces(st RunState, phase int, match func(kind string, attempt int) bool) []stepWorkspace {
	closed := map[string]bool{}
	for _, ev := range st.Events {
		if ev.Kind == "workspace-closed" {
			closed[ev.Fields["workspace"]] = true
		}
	}
	var out []stepWorkspace
	for _, ev := range st.Events {
		id := ev.Fields["workspace"]
		if ev.Kind != "step" || ev.Phase != phase || id == "" || closed[id] {
			continue
		}
		attempt, _ := strconv.Atoi(ev.Fields["attempt"])
		if !match(ev.Step, attempt) || slices.ContainsFunc(out, func(w stepWorkspace) bool { return w.id == id }) {
			continue
		}
		out = append(out, stepWorkspace{kind: ev.Step, id: id})
	}
	return out
}

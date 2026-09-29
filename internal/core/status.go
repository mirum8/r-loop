package core

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
)

type StepInfo struct {
	Step                            string
	Attempt                         int
	Agent, Worktree, Base, StartSHA string
}

type StepStatus struct {
	Step        string
	Attempt     int
	State       StepState
	RetriesLeft int
}

type RunStatusView struct {
	Run       string
	Status    RunStatus
	Live      string
	Steps     []StepStatus
	Landed    []string
	Open      []Question
	Fallbacks map[string]Fallback
}

func BuildStatus(st RunState, open []Question, live string, fallbacks map[string]Fallback, maxRestarts int) RunStatusView {
	latest := map[[2]string]StepKey{}
	for key := range st.Steps {
		id := [2]string{key.Phase, key.Kind}
		if prev, ok := latest[id]; !ok || key.Attempt > prev.Attempt {
			latest[id] = key
		}
	}
	keys := make([]StepKey, 0, len(latest))
	for _, key := range latest {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b StepKey) int {
		return cmp.Or(ComparePhaseIDs(a.Phase, b.Phase), cmp.Compare(a.Kind, b.Kind))
	})
	view := RunStatusView{Run: st.ID, Status: st.Status, Live: live, Open: open, Fallbacks: fallbacks}
	for _, key := range keys {
		step := fmt.Sprintf("phase-%s/%s", key.Phase, key.Kind)
		left := max(0, maxRestarts-retriesUsed(st.Events, step))
		view.Steps = append(view.Steps, StepStatus{Step: step, Attempt: key.Attempt, State: st.Steps[key], RetriesLeft: left})
	}
	for _, landing := range st.Landed {
		view.Landed = append(view.Landed, landing.Phase)
	}
	return view
}

func StepInfoFromEvents(events []Event, step string) (StepInfo, bool) {
	var info StepInfo
	found := false
	for _, ev := range events {
		f := ev.Fields
		if ev.Kind != "agent-named" || f["reviewer"] != "" || fmt.Sprintf("phase-%s/%s", ev.Phase, ev.Step) != step {
			continue
		}
		attempt, err := strconv.Atoi(f["attempt"])
		if err != nil || (found && attempt < info.Attempt) {
			continue
		}
		info = StepInfo{Step: step, Attempt: attempt, Agent: f["agent"], Worktree: f["worktree"], Base: f["base"], StartSHA: f["start_sha"]}
		found = true
	}
	return info, found
}

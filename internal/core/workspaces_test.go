package core

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
)

var errWorkspaceNotFound = errors.New("workspace_not_found")

type closeFailHost struct {
	*agentSim
}

func (h closeFailHost) Close(workspaceID string) error {
	h.agentSim.Close(workspaceID)
	return errWorkspaceNotFound
}

func (r *loopRig) seedStep(key StepKey, state StepState, workspace string) {
	r.store.Append("run-1", Record{Kind: RecordEvent, Event: &Event{Kind: "step", Phase: key.Phase, Step: key.Kind, Fields: map[string]string{"attempt": strconv.Itoa(key.Attempt), "state": "running", "workspace": workspace}}})
	r.store.Append("run-1", Record{Kind: RecordStep, Step: &key, State: state})
}

func TestALandedPhaseClosesEveryWorkspaceItsStepsOpened(t *testing.T) {
	r := newLoopRig(t)

	r.run(RunOptions{Phases: []int{2}})

	if got := r.calls("SessionHost.Close "); !reflect.DeepEqual(got, []string{"ws-1", "ws-2"}) {
		t.Fatalf("closed %v", got)
	}
	var closed []string
	for _, ev := range r.events("workspace-closed") {
		closed = append(closed, ev.Step+" "+ev.Fields["workspace"])
	}
	if !reflect.DeepEqual(closed, []string{"plan ws-1", "implement ws-2"}) {
		t.Fatalf("workspace-closed %v", closed)
	}
	calls := r.shared.Calls()
	if land, first := indexOf(calls, "Land 2"), indexOf(calls, "SessionHost.Close ws-1"); land < 0 || first < land {
		t.Fatalf("calls %v", calls)
	}
}

func TestABlockedPhaseLeavesItsWorkspacesStanding(t *testing.T) {
	r := newLoopRig(t)
	r.host.behaviour["rloop-p1-implement"] = "fail"

	r.run(RunOptions{Phases: []int{1}})

	if got := r.calls("SessionHost.Close "); len(got) != 0 {
		t.Fatalf("closed %v", got)
	}
}

func TestANewAttemptClosesTheEarlierAttemptsWorkspaceOnce(t *testing.T) {
	r := newLoopRig(t)
	r.seedStep(StepKey{Run: "run-1", Phase: 1, Kind: "plan", Attempt: 1}, StepOK, "ws-plan")
	r.seedStep(StepKey{Run: "run-1", Phase: 1, Kind: "implement", Attempt: 1}, StepFailed, "ws-old")

	r.run(RunOptions{Resume: true, Phases: []int{1}})

	calls := r.shared.Calls()
	if closeOld, open := indexOf(calls, "SessionHost.Close ws-old"), indexOf(calls, "SessionHost.Open"); closeOld < 0 || open < closeOld {
		t.Fatalf("calls %v", calls)
	}
	if got := r.calls("SessionHost.Close "); !reflect.DeepEqual(got, []string{"ws-old", "ws-plan", "ws-1"}) {
		t.Fatalf("closed %v", got)
	}
}

func TestAWorkspaceThatWillNotCloseIsAWarningNotAFailure(t *testing.T) {
	r := newLoopRig(t)
	r.loop.Sessions.Host = closeFailHost{r.host}

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	warnings := r.events("warning")
	if len(warnings) != 2 || warnings[0].Fields["reason"] != "close workspace ws-1: workspace_not_found" {
		t.Fatalf("warnings %+v", warnings)
	}
}

func TestALandedPhaseRemovesItsWorktreeAndBranchAfterClosingItsWorkspaces(t *testing.T) {
	r := newLoopRig(t)

	r.run(RunOptions{Phases: []int{2}})

	calls := r.shared.Calls()
	lastClose, remove, del := indexOf(calls, "SessionHost.Close ws-2"), indexOf(calls, "Repo.RemoveWorktree .r-loop/wt/phase-2"), indexOf(calls, "Repo.DeleteBranch r-loop/phase-2")
	if lastClose < 0 || remove < lastClose || del < remove {
		t.Fatalf("calls %v", calls)
	}
	removed := r.events("worktree-removed")
	if len(removed) != 1 || removed[0].Fields["worktree"] != ".r-loop/wt/phase-2" || removed[0].Fields["branch"] != "r-loop/phase-2" {
		t.Fatalf("worktree-removed %+v", removed)
	}
}

func TestABlockedPhaseKeepsItsWorktreeAndBranch(t *testing.T) {
	r := newLoopRig(t)
	r.host.behaviour["rloop-p1-implement"] = "fail"

	r.run(RunOptions{Phases: []int{1}})

	if n := len(r.calls("Repo.RemoveWorktree")) + len(r.calls("Repo.DeleteBranch")); n != 0 {
		t.Fatalf("calls %v", r.shared.Calls())
	}
}

func TestASkippedItemClosesItsWorkspacesButKeepsItsUnmergedBranch(t *testing.T) {
	r := newLoopRig(t)
	r.loop.Sessions.ItemGates = true
	r.host.behaviour["rloop-p2-plan"] = "already-done"

	r.run(RunOptions{Phases: []int{2}})

	if got := r.calls("SessionHost.Close "); !reflect.DeepEqual(got, []string{"ws-1"}) {
		t.Fatalf("closed %v", got)
	}
	if n := len(r.calls("Repo.RemoveWorktree")) + len(r.calls("Repo.DeleteBranch")); n != 0 {
		t.Fatalf("calls %v", r.shared.Calls())
	}
}

type removeFailRepo struct {
	*loopRepo
}

func (r removeFailRepo) RemoveWorktree(dir string) error {
	r.loopRepo.RemoveWorktree(dir)
	return errors.New("worktree is locked")
}

func TestAWorktreeThatWillNotGoIsAWarningAndKeepsTheBranch(t *testing.T) {
	r := newLoopRig(t)
	r.loop.Sessions.Repo = removeFailRepo{r.repo}

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 || len(r.calls("Repo.DeleteBranch")) != 0 {
		t.Fatalf("exit %d, calls %v", code, r.shared.Calls())
	}
	warnings := r.events("warning")
	if len(warnings) != 1 || warnings[0].Fields["reason"] != "remove worktree .r-loop/wt/phase-2: worktree is locked" {
		t.Fatalf("warnings %+v", warnings)
	}
}

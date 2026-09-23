package core

import (
	"strings"
	"testing"
)

func seedSkippedItem(r *loopRig) {
	key := StepKey{Run: "run-1", Phase: "2", Kind: "plan", Attempt: 1}
	r.store.Append("run-1", Record{Kind: RecordStep, Step: &key, State: StepOK})
	r.store.Append("run-1", Record{Kind: RecordEvent, Event: &Event{Kind: "item-skipped", Phase: "2"}})
}

func TestAnItemSkippedBeforeIsNotRerunOnResume(t *testing.T) {
	r := newLoopRig(t)
	seedSkippedItem(r)
	if code := r.run(RunOptions{Phases: []string{"2"}, Resume: true}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	calls := r.shared.Calls()
	for _, c := range calls {
		if strings.HasPrefix(c, "Repo.AddWorktree") || strings.HasPrefix(c, "SessionHost.Prompt") || strings.HasPrefix(c, "Land ") {
			t.Fatalf("unexpected call %s", c)
		}
	}
	remove, del := indexOf(calls, "Repo.RemoveWorktree .r-loop/wt/phase-2"), indexOf(calls, "Repo.DeleteBranch r-loop/phase-2 --force")
	if remove < 0 || del < remove {
		t.Fatalf("calls %v", calls)
	}
}

func TestAnItemSkippedBeforeWhoseCleanupStillFailsStaysBlocked(t *testing.T) {
	r := newLoopRig(t)
	seedSkippedItem(r)
	r.loop.Sessions.Repo = removeFailRepo{r.repo}
	if code := r.run(RunOptions{Phases: []string{"2"}, Resume: true}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	ev := r.events("phase-blocked")
	if len(ev) != 1 || !strings.Contains(ev[0].Fields["reason"], "worktree is locked") || len(r.events("finished")) != 0 {
		t.Fatalf("events %+v", ev)
	}
	if len(r.calls("Repo.AddWorktree")) != 0 {
		t.Fatal("worktree recreated")
	}
}

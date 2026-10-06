package core

import (
	"context"
	"testing"
)

type lateEditHost struct {
	*scriptedHost
	worker string
	turn   func() AgentState
}

func (h *lateEditHost) State(agent string) (AgentState, error) {
	if agent == h.worker {
		return h.turn(), nil
	}
	return h.scriptedHost.State(agent)
}

func (h *lateEditHost) StateSeq(agent string) (AgentState, int64, error) {
	st, err := h.State(agent)
	return st, 0, err
}

type lateEditRepo struct {
	*fakeRepo
	landed *bool
}

func (r lateEditRepo) Snapshot(dir string) (string, error) {
	if *r.landed {
		return "tree-with-late-edit", nil
	}
	return "tree-at-sentinel", nil
}

func (r lateEditRepo) TreeDiff(from, to string) ([]string, error) {
	if from == to {
		return nil, nil
	}
	return []string{".task-plans/phase-3-plan.md"}, nil
}

func TestStepEditAfterItsSentinelIsNotBlamedOnTheReviewer(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.repo.Changed = []string{"a.go"}
	landed, polls := false, 0
	r.sm.Repo = lateEditRepo{fakeRepo: r.repo, landed: &landed}
	r.sm.Host = &lateEditHost{scriptedHost: r.host, worker: r.worker.Agent, turn: func() AgentState {
		polls++
		if polls == 1 {
			r.writeSentinel(t, r.worker, "ok", "")
			return AgentWorking
		}
		landed = true
		return AgentIdle
	}}
	r.behave = func(vars map[string]any) {
		landed = true
		writeReview(t, vars, "ok", 0)
	}

	if out := r.sm.Wait(context.Background(), r.worker, &recObserver{}); out.State != StepOK {
		t.Fatalf("work half = %+v", out)
	}
	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
}

type fixEditRepo struct {
	*fakeRepo
	tree *string
}

func (r fixEditRepo) Snapshot(dir string) (string, error) {
	return *r.tree, nil
}

func (r fixEditRepo) TreeDiff(from, to string) ([]string, error) {
	if from == to {
		return nil, nil
	}
	return []string{"a.go"}, nil
}

func TestStepEditAfterItsFixSentinelIsNotBlamedOnTheReviewer(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	tree, fixing, polls := "tree-r1", false, 0
	r.sm.Repo = fixEditRepo{fakeRepo: r.repo, tree: &tree}
	r.sm.Host = &lateEditHost{scriptedHost: r.host, worker: r.worker.Agent, turn: func() AgentState {
		if !fixing {
			return AgentIdle
		}
		polls++
		if polls == 1 {
			return AgentWorking
		}
		tree = "tree-with-late-edit"
		return AgentIdle
	}}
	r.behave = func(vars map[string]any) {
		n := 0
		if vars["Round"] == 1 {
			n = 1
		} else {
			tree = "tree-with-late-edit"
		}
		writeReview(t, vars, "ok", n)
	}
	r.onFix = func(vars map[string]any) {
		tree, fixing = "tree-fixed", true
		writeVerdict(t, vars, entry("codex-r1-1", "real", "P2", true, ""))
	}

	out := r.run()

	if out.State != StepOK || out.Warning != "" {
		t.Fatalf("outcome = %+v", out)
	}
}

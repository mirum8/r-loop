package core

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func hook(agent, event, tool, target string) HookEvent {
	return HookEvent{At: time.Now(), Agent: agent, Event: event, Tool: tool, Target: target}
}

func TestHookActivityIsShownWhenItChangesAndIsNotRecorded(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.repo.Changed = []string{"internal/core/land.go"}
	r.repo.TreeChanges = []string{"internal/core/land.go"}
	r.repo.Tree = "tree-edited"
	worker := r.worker.Agent
	file := r.worker.Dir + "/internal/core/land.go"
	sentinel := false
	reads := 0
	r.sm.Hooks = &fakeHookFeed{next: func() []HookEvent {
		reads++
		switch {
		case sentinel:
			return []HookEvent{hook(worker, "Stop", "", "")}
		case reads == 1:
			return []HookEvent{hook(worker, "PreToolUse", "Edit", file)}
		case reads == 2:
			return []HookEvent{hook(worker, "PreToolUse", "Edit", file)}
		case reads == 3:
			return []HookEvent{hook(worker, "PostToolUse", "Edit", file)}
		}
		return nil
	}}
	polls := 0
	r.sm.Host = &lateEditHost{scriptedHost: r.host, worker: worker, turn: func() AgentState {
		polls++
		if polls == 4 {
			r.writeSentinel(t, r.worker, "ok", "")
			sentinel = true
		}
		return AgentWorking
	}}
	obs := &showObserver{}

	if out := r.sm.Wait(context.Background(), r.worker, obs); out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}

	var texts []string
	for _, ev := range obs.shown {
		if ev.Kind == "activity" {
			texts = append(texts, ev.Fields["text"])
			if ev.Phase != "3" || ev.Step != "implement" || ev.Fields["agent"] != worker {
				t.Errorf("activity event %+v", ev)
			}
		}
	}
	if want := []string{"editing internal/core/land.go", "editing internal/core/land.go · 1 file"}; !reflect.DeepEqual(texts, want) {
		t.Errorf("activity = %q, want %q", texts, want)
	}
	if got := r.events("activity"); len(got) != 0 {
		t.Errorf("activity recorded: %+v", got)
	}
}

func TestWithHooksAStepTurnEndsAtStopNotWhenHerdrSaysIdle(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.repo.Changed = []string{"a.go"}
	worker := r.worker.Agent
	landed, polls := false, 0
	r.sm.Repo = lateEditRepo{fakeRepo: r.repo, landed: &landed}
	r.sm.Hooks = &fakeHookFeed{next: func() []HookEvent {
		switch {
		case polls == 0:
			return []HookEvent{hook(worker, "UserPromptSubmit", "", "")}
		case polls >= 3 && !landed:
			landed = true
			return []HookEvent{hook(worker, "PostToolUse", "Edit", r.worker.Dir+"/.task-plans/phase-3-plan.md"), hook(worker, "Stop", "", "")}
		}
		return nil
	}}
	r.sm.Host = &lateEditHost{scriptedHost: r.host, worker: worker, turn: func() AgentState {
		polls++
		if polls == 1 {
			r.writeSentinel(t, r.worker, "ok", "")
		}
		return AgentIdle
	}}
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }

	if out := r.sm.Wait(context.Background(), r.worker, &recObserver{}); out.State != StepOK {
		t.Fatalf("work half = %+v", out)
	}
	if !landed {
		t.Fatal("the work half was judged before the turn's Stop")
	}
	if out := r.run(); out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestATreeChangeAReviewerMadeIsBlamedOnThatReviewer(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	var edits []HookEvent
	r.sm.Hooks = &fakeHookFeed{next: func() []HookEvent {
		out := edits
		edits = nil
		return out
	}}
	r.behave = func(vars map[string]any) {
		writeReview(t, vars, "ok", 1)
		r.repo.TreeChanges = []string{"a.go", "b.go"}
		edits = []HookEvent{hook("rloop-2kuxv-p3-implemen-8lgad-r1", "PostToolUse", "Edit", r.worker.Dir+"/a.go")}
	}

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer codex modified the tree: a.go; reviewer modified the tree: b.go" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestATreeChangeTheStepMadeDuringReviewIsBlamedOnTheStep(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	var edits []HookEvent
	r.sm.Hooks = &fakeHookFeed{next: func() []HookEvent {
		out := edits
		edits = nil
		return out
	}}
	r.behave = func(vars map[string]any) {
		writeReview(t, vars, "ok", 1)
		r.repo.TreeChanges = []string{"a.go"}
		edits = []HookEvent{hook(r.worker.Agent, "PostToolUse", "Write", r.worker.Dir+"/a.go")}
	}

	out := r.run()

	if out.State != StepFailed || out.Reason != "step implement edited the tree during review: a.go" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestAnEditFromBeforeTheRoundIsNotBlamed(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	old := hook("rloop-2kuxv-p3-implemen-8lgad-r1", "PostToolUse", "Edit", r.worker.Dir+"/a.go")
	old.At = r.sm.now().Add(-time.Hour)
	edits := []HookEvent{old}
	r.sm.Hooks = &fakeHookFeed{next: func() []HookEvent {
		out := edits
		edits = nil
		return out
	}}
	r.behave = func(vars map[string]any) {
		writeReview(t, vars, "ok", 1)
		r.repo.TreeChanges = []string{"a.go"}
	}

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer modified the tree: a.go" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestADialogCarriesTheHookNoticeToTheWatchdog(t *testing.T) {
	r := newDialogRig(t)
	notice := "Claude needs your permission to use Bash"
	sent := false
	r.loop.Sessions.Hooks = &fakeHookFeed{next: func() []HookEvent {
		if sent {
			return nil
		}
		sent = true
		return []HookEvent{{At: time.Now(), Agent: dialogAgent, Event: "Notification", Notice: notice, Type: "permission_prompt"}}
	}}
	r.serve(t)

	r.raise(t, r.s)

	qs := r.questions()
	if len(qs) != 1 || qs[0].Notice != notice {
		t.Fatalf("questions %+v", qs)
	}
	evs := r.events("dialog")
	if len(evs) != 1 || evs[0].Fields["notice"] != notice {
		t.Errorf("dialog events %+v", evs)
	}
	if got := r.waitDog(t, 1); !reflect.DeepEqual(got, []string{dialogPrompt("d1", "implement", "notice: "+notice+"\n\n"+dialogScreen)}) {
		t.Errorf("watchdog prompts %q", got)
	}
}

func TestAnIdleNotificationIsNotADialogNotice(t *testing.T) {
	r := newDialogRig(t)
	sent := false
	r.loop.Sessions.Hooks = &fakeHookFeed{next: func() []HookEvent {
		if sent {
			return nil
		}
		sent = true
		return []HookEvent{{At: time.Now(), Agent: dialogAgent, Event: "Notification", Notice: "Claude is waiting for your input", Type: "idle_prompt"}}
	}}
	r.serve(t)

	r.raise(t, r.s)

	if qs := r.questions(); len(qs) != 1 || qs[0].Notice != "" {
		t.Fatalf("questions %+v", qs)
	}
}

func TestActivityText(t *testing.T) {
	cases := []struct {
		tool, target string
		files        int
		want         string
	}{
		{"Edit", "/wt/internal/core/land.go", 0, "editing internal/core/land.go"},
		{"Read", "/elsewhere/x.go", 0, "reading /elsewhere/x.go"},
		{"Bash", "  go test ./...\n  && echo ok", 2, "running go test ./... · 2 files"},
		{"Grep", "func main", 0, "searching func main"},
		{"Agent", "explore", 0, "subagent"},
		{"mcp__r-loop__ask_watchdog", "", 0, "asking watchdog"},
		{"WebFetch", "https://x", 0, "webfetch"},
		{"", "", 3, ""},
	}
	for _, c := range cases {
		if got := activityText(c.tool, c.target, "/wt", c.files); got != c.want {
			t.Errorf("activityText(%q, %q, %d) = %q, want %q", c.tool, c.target, c.files, got, c.want)
		}
	}
}

func TestAReviewersActivityReachesTheLiveObserver(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	reviewer := "rloop-2kuxv-p3-implemen-8lgad-r1"
	reads := 0
	r.sm.Hooks = &fakeHookFeed{next: func() []HookEvent {
		reads++
		if reads == 1 {
			return []HookEvent{hook(reviewer, "PreToolUse", "Read", r.worker.Dir+"/calc.go")}
		}
		if reads > 4 {
			return []HookEvent{hook(reviewer, "Stop", "", "")}
		}
		return nil
	}}
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }
	obs := &showObserver{}

	out := ReviewHalf{Sessions: r.sm, Store: r.store}.Run(context.Background(), r.worker.Ref, r.worker, obs)

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	var got []Event
	for _, ev := range obs.shown {
		if ev.Kind == "activity" {
			got = append(got, ev)
		}
	}
	if len(got) != 1 || got[0].Step != "implement-rv-codex" || got[0].Fields["text"] != "reading calc.go" || got[0].Fields["agent"] != reviewer {
		t.Fatalf("activity = %+v", got)
	}
}

func TestAnIdlePromptEndsATurnThatRanToolsAfterItsStop(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.repo.Changed = []string{"a.go"}
	r.repo.TreeChanges = []string{"a.go"}
	r.repo.Tree = "tree-edited"
	worker := r.worker.Agent
	polls := 0
	r.sm.Hooks = &fakeHookFeed{next: func() []HookEvent {
		switch polls {
		case 1:
			return []HookEvent{hook(worker, "Stop", "", ""), hook(worker, "PreToolUse", "Bash", "go test ./...")}
		case 3:
			return []HookEvent{{At: time.Now(), Agent: worker, Event: "Notification", Notice: "Claude is waiting for your input", Type: "idle_prompt"}}
		}
		return nil
	}}
	r.sm.Host = &lateEditHost{scriptedHost: r.host, worker: worker, turn: func() AgentState {
		polls++
		if polls == 1 {
			r.writeSentinel(t, r.worker, "ok", "")
		}
		return AgentIdle
	}}

	out := r.sm.Wait(context.Background(), r.worker, &recObserver{})

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	if got := r.events("turn-overran-sentinel"); len(got) != 0 {
		t.Fatalf("turn overran: %+v", got)
	}
}

package core

import (
	"reflect"
	"testing"
)

func TestBuildStatusListsTheLatestAttemptOfEachStepWithTheRetriesItHasLeft(t *testing.T) {
	st := RunState{
		ID:     "run-1",
		Status: RunRunning,
		Steps: map[StepKey]StepState{
			{Run: "run-1", Phase: "10", Kind: "plan", Attempt: 1}:     StepOK,
			{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}: StepFailed,
			{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 2}: StepRunning,
			{Run: "run-1", Phase: "2", Kind: "plan", Attempt: 1}:      StepOK,
		},
		Landed: []Landing{{Phase: "1"}},
		Events: []Event{
			{Kind: "blocker-resolved", Fields: map[string]string{"action": "retry", "step": "phase-2/implement"}},
			{Kind: "restart", Fields: map[string]string{"step": "phase-2/implement", "remedy": "r1"}},
			{Kind: "restart", Fields: map[string]string{"step": "phase-2/implement", "remedy": "b1"}},
			{Kind: "blocker-resolved", Fields: map[string]string{"action": "block", "step": "phase-2/plan"}},
		},
	}
	open := []Question{{ID: "b3", Kind: QuestionBlocker, Step: StepKey{Phase: "2", Kind: "implement"}, Text: "gate red", Options: []string{"retry", "block"}}}
	fallbacks := map[string]Fallback{"implement": {Provider: "codex", Model: "gpt-5", Effort: "high"}}

	got := BuildStatus(st, open, "phase-2/implement", fallbacks, 3)

	want := RunStatusView{
		Run:    "run-1",
		Status: RunRunning,
		Live:   "phase-2/implement",
		Steps: []StepStatus{
			{Step: "phase-2/implement", Attempt: 2, State: StepRunning, RetriesLeft: 1},
			{Step: "phase-2/plan", Attempt: 1, State: StepOK, RetriesLeft: 3},
			{Step: "phase-10/plan", Attempt: 1, State: StepOK, RetriesLeft: 3},
		},
		Landed:    []string{"1"},
		Open:      open,
		Fallbacks: fallbacks,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("status\n got %+v\nwant %+v", got, want)
	}
}

func TestBuildStatusNeverReportsNegativeRetries(t *testing.T) {
	st := RunState{
		Steps: map[StepKey]StepState{{Phase: "1", Kind: "plan", Attempt: 3}: StepFailed},
		Events: []Event{
			{Kind: "blocker-resolved", Fields: map[string]string{"action": "switch", "step": "phase-1/plan"}},
			{Kind: "blocker-resolved", Fields: map[string]string{"action": "retry", "step": "phase-1/plan"}},
		},
	}

	got := BuildStatus(st, nil, "", nil, 1)

	if len(got.Steps) != 1 || got.Steps[0].RetriesLeft != 0 {
		t.Errorf("steps %+v", got.Steps)
	}
}

func TestStepInfoReturnsWhatStepStartedToldTheWatchdog(t *testing.T) {
	w := newWatch(&fakeStore{})
	ref := implementRef(2, 1)
	ref.Worktree, ref.Base = ".r-loop/wt/phase-2", "main"
	w.StepStarted(ref, &Session{Agent: "rl-2-implement", Dir: "/repo/.r-loop/wt/phase-2", StartSHA: "abc123"})
	defer w.StepEnded(ref, Outcome{State: StepOK})

	got, ok := w.StepInfo("phase-2/implement")

	want := StepInfo{Step: "phase-2/implement", Attempt: 1, Agent: "rl-2-implement", Worktree: "/repo/.r-loop/wt/phase-2", Base: "main", StartSHA: "abc123"}
	if !ok || got != want {
		t.Errorf("info %+v %v, want %+v", got, ok, want)
	}
	if live := w.LiveStep(); live != "phase-2/implement" {
		t.Errorf("live %q", live)
	}
}

func TestStepInfoKeepsAnEndedStepAndFollowsTheLatestAttempt(t *testing.T) {
	w := newWatch(&fakeStore{})
	first := implementRef(2, 1)
	first.Worktree = ".r-loop/wt/phase-2"
	w.StepStarted(first, &Session{Agent: "a1"})
	w.StepEnded(first, Outcome{State: StepFailed})

	got, ok := w.StepInfo("phase-2/implement")
	if !ok || got.Agent != "a1" || got.Worktree != ".r-loop/wt/phase-2" {
		t.Errorf("ended step %+v %v", got, ok)
	}
	if live := w.LiveStep(); live != "" {
		t.Errorf("live after end %q", live)
	}

	second := implementRef(2, 2)
	w.StepStarted(second, &Session{Agent: "a2"})
	defer w.StepEnded(second, Outcome{State: StepOK})

	got, ok = w.StepInfo("phase-2/implement")
	if !ok || got.Agent != "a2" || got.Attempt != 2 {
		t.Errorf("latest attempt %+v %v", got, ok)
	}
}

func TestStepInfoDoesNotKnowAStepThatNeverStarted(t *testing.T) {
	w := newWatch(&fakeStore{})

	if got, ok := w.StepInfo("phase-3/plan"); ok {
		t.Errorf("unknown step %+v", got)
	}
}

func TestOpenAsksListsOpenQuestionsDialogsAndBlockersButNotClaimedOnes(t *testing.T) {
	l := &RunLoop{}
	l.track(Question{ID: "q2", Step: StepKey{Phase: "1", Kind: "plan"}, Text: "which db?"}, nil, "a")
	l.track(Question{ID: "d1", Kind: QuestionDialog, Text: "allow?"}, nil, "a")
	l.track(Question{ID: "b1", Kind: QuestionBlocker, Text: "gate red", Options: []string{"retry"}}, nil, "")
	l.track(Question{ID: "q1", Text: "answered"}, nil, "a")
	l.claim("q1")

	got := l.OpenAsks()

	var ids []string
	for _, q := range got {
		ids = append(ids, q.ID)
	}
	if want := []string{"b1", "d1", "q2"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids %v, want %v", ids, want)
	}
	if got[0].Options[0] != "retry" || got[2].Text != "which db?" {
		t.Errorf("open %+v", got)
	}
}

func TestStepInfoFromEventsReadsTheLatestAttemptsAgentNamedEventAndSkipsReviewers(t *testing.T) {
	events := []Event{
		{Kind: "agent-named", Phase: "2", Step: "implement", Fields: map[string]string{"attempt": "1", "agent": "a1", "worktree": "/r/wt/phase-2", "base": "main", "start_sha": "s1"}},
		{Kind: "agent-named", Phase: "2", Step: "implement", Fields: map[string]string{"attempt": "2", "agent": "a2", "worktree": "/r/wt/phase-2", "base": "main", "start_sha": "s2"}},
		{Kind: "agent-named", Phase: "2", Step: "implement", Fields: map[string]string{"attempt": "2", "agent": "rv", "reviewer": "codex", "round": "1"}},
		{Kind: "agent-named", Phase: "3", Step: "implement", Fields: map[string]string{"attempt": "1", "agent": "a3"}},
	}

	got, ok := StepInfoFromEvents(events, "phase-2/implement")

	want := StepInfo{Step: "phase-2/implement", Attempt: 2, Agent: "a2", Worktree: "/r/wt/phase-2", Base: "main", StartSHA: "s2"}
	if !ok || got != want {
		t.Errorf("info %+v %v, want %+v", got, ok, want)
	}
	if got, ok := StepInfoFromEvents(events, "phase-4/plan"); ok {
		t.Errorf("unknown step %+v", got)
	}
}

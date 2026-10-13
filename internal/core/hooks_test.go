package core

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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
		if reads == 3 {
			return []HookEvent{hook(reviewer, "PreToolUse", "Read", r.worker.Dir+"/calc.go")}
		}
		if reads > 6 {
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

type claudeHost struct {
	SessionHost
	feed        *fakeHookFeed
	onSubmit    map[string][]HookEvent
	onEnter     []HookEvent
	stall       bool
	panics      string
	statePanics bool
	onState     []HookEvent
}

func (h *claudeHost) State(agent string) (AgentState, error) {
	if h.statePanics {
		panic("boom")
	}
	h.feed.push(h.onState...)
	return h.SessionHost.State(agent)
}

func (h *claudeHost) Submit(agent, text string) error {
	if h.panics != "" && text == h.panics {
		panic("boom")
	}
	if err := h.SessionHost.Submit(agent, text); err != nil {
		return err
	}
	h.feed.push(h.onSubmit[text]...)
	if h.stall {
		return ErrPromptStalled
	}
	return nil
}

func (h *claudeHost) SendKeys(agent string, keys ...string) error {
	err := h.SessionHost.SendKeys(agent, keys...)
	h.feed.push(h.onEnter...)
	return err
}

func promptSubmit(agent, prompt string, at time.Time) HookEvent {
	return HookEvent{At: at, Agent: agent, Event: "UserPromptSubmit", Prompt: prompt}
}

func cutPromptSubmit(agent, prompt string, at time.Time) HookEvent {
	return HookEvent{At: at, Agent: agent, Event: "UserPromptSubmit", Prompt: prompt, PromptCut: true}
}

func shortSubmitWait(t *testing.T) {
	old := submitWait
	submitWait = 10 * time.Millisecond
	t.Cleanup(func() { submitWait = old })
}

func slowHookPoll(t *testing.T) {
	old := hookPoll
	hookPoll = time.Hour
	t.Cleanup(func() { hookPoll = old })
}

func idleHookAgentFeed(at time.Time) *fakeHookFeed {
	return &fakeHookFeed{queued: []HookEvent{{At: at.Add(-time.Minute), Agent: askingAgent, Event: "Stop"}}}
}

func hookPromptManager(host SessionHost, feed HookFeed, at time.Time) *SessionManager {
	return &SessionManager{Host: host, Hooks: feed, Now: func() time.Time { return at }}
}

func submitCall(agent, text string) string {
	return "SessionHost.Submit " + agent + " " + strconv.Quote(text)
}

func assertCalls(t *testing.T, actual, expected []string) {
	t.Helper()
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("calls\n got %q\nwant %q", actual, expected)
	}
}

func receiveErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("prompt never returned")
		return nil
	}
}

func TestAPromptToAHookAgentIsDeliveredByAMatchingUserPromptSubmit(t *testing.T) {
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		sent     string
		reported HookEvent
	}{
		{"the typed text", "r-loop: answer to q1 (by watchdog): sqlite", promptSubmit(askingAgent, "r-loop: answer to q1 (by watchdog): sqlite", t0)},
		{"its first 512 bytes", strings.Repeat("ab ", 300), cutPromptSubmit(askingAgent, strings.Repeat("ab ", 300)[:512], t0)},
		{"a rune the cap split", strings.Repeat("é", 300), cutPromptSubmit(askingAgent, strings.Repeat("é", 255), t0)},
		{"line breaks collapsed and trailing space trimmed", "fix round 2\n\nfindings: a.json\n", promptSubmit(askingAgent, "fix round 2\nfindings: a.json", t0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			inner := &fakeSessionHost{}
			feed := idleHookAgentFeed(t0)
			host := &claudeHost{SessionHost: inner, feed: feed, onSubmit: map[string][]HookEvent{tt.sent: {tt.reported}}}
			sm := hookPromptManager(host, feed, t0)

			// when
			actualErr := sm.prompt(askingAgent, tt.sent, nil)

			// then
			if actualErr != nil {
				t.Fatalf("prompt: %v", actualErr)
			}
			assertCalls(t, inner.Calls(), []string{submitCall(askingAgent, tt.sent)})
		})
	}
}

func TestAPromptToAHookAgentIsNotDeliveredWithoutAMatchingUserPromptSubmit(t *testing.T) {
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	answer := "r-loop: answer to q1 (by watchdog): sqlite"
	tests := []struct {
		name     string
		sent     string
		reported []HookEvent
		stall    bool
	}{
		{"herdr reports working, no hook arrives", answer, nil, false},
		{"another text", answer, []HookEvent{promptSubmit(askingAgent, "r-loop: answer to q2 (by watchdog): postgres", t0)}, false},
		{"a shorter prompt the text starts with", answer, []HookEvent{promptSubmit(askingAgent, "r-loop: answer to q1", t0)}, false},
		{"the same words run together", "go test ./...", []HookEvent{promptSubmit(askingAgent, "gotest./...", t0)}, false},
		{"a complete prompt near the cap the text starts with", strings.Repeat("x", 600), []HookEvent{promptSubmit(askingAgent, strings.Repeat("x", 508), t0)}, false},
		{"from before the send", answer, []HookEvent{promptSubmit(askingAgent, answer, t0.Add(-time.Second))}, false},
		{"another agent's", answer, []HookEvent{promptSubmit("rloop-p3-plan", answer, t0)}, false},
		{"stalled and still unsubmitted after Enter", answer, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			shortSubmitWait(t)
			inner := &fakeSessionHost{}
			feed := idleHookAgentFeed(t0)
			host := &claudeHost{SessionHost: inner, feed: feed, onSubmit: map[string][]HookEvent{tt.sent: tt.reported}, stall: tt.stall}
			sm := hookPromptManager(host, feed, t0)

			// when
			actualErr := sm.prompt(askingAgent, tt.sent, nil)

			// then
			if actualErr == nil || actualErr.Error() != "prompt to rloop-p3-implement not submitted: no matching UserPromptSubmit" {
				t.Fatalf("prompt: %v", actualErr)
			}
			assertCalls(t, inner.Calls(), []string{submitCall(askingAgent, tt.sent), "SessionHost.SendKeys rloop-p3-implement enter"})
		})
	}
}

func TestAStalledPromptWhoseUserPromptSubmitWasSeenGetsNoEnter(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	inner := &fakeSessionHost{}
	feed := idleHookAgentFeed(t0)
	host := &claudeHost{SessionHost: inner, feed: feed, stall: true, onSubmit: map[string][]HookEvent{"go": {promptSubmit(askingAgent, "go", t0)}}}
	sm := hookPromptManager(host, feed, t0)

	// when
	actualErr := sm.prompt(askingAgent, "go", nil)

	// then
	if actualErr != nil {
		t.Fatalf("prompt: %v", actualErr)
	}
	assertCalls(t, inner.Calls(), []string{`SessionHost.Submit rloop-p3-implement "go"`})
}

func TestAStalledPromptStaysConfirmedWhenAnotherPromptFollowsItsUserPromptSubmitInTheSameRead(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	inner := &fakeSessionHost{}
	feed := idleHookAgentFeed(t0)
	host := &claudeHost{SessionHost: inner, feed: feed, stall: true, onSubmit: map[string][]HookEvent{"go": {
		promptSubmit(askingAgent, "go", t0),
		promptSubmit(askingAgent, "what are you doing?", t0.Add(time.Second)),
	}}}
	sm := hookPromptManager(host, feed, t0)

	// when
	actualErr := sm.prompt(askingAgent, "go", nil)

	// then
	if actualErr != nil {
		t.Fatalf("prompt: %v", actualErr)
	}
	assertCalls(t, inner.Calls(), []string{`SessionHost.Submit rloop-p3-implement "go"`})
}

func TestAStalledPromptWithoutAUserPromptSubmitIsDeliveredByOneEnter(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	inner := &fakeSessionHost{}
	feed := idleHookAgentFeed(t0)
	host := &claudeHost{SessionHost: inner, feed: feed, stall: true, onEnter: []HookEvent{promptSubmit(askingAgent, "go", t0)}}
	sm := hookPromptManager(host, feed, t0)

	// when
	actualErr := sm.prompt(askingAgent, "go", nil)

	// then
	if actualErr != nil {
		t.Fatalf("prompt: %v", actualErr)
	}
	assertCalls(t, inner.Calls(), []string{`SessionHost.Submit rloop-p3-implement "go"`, "SessionHost.SendKeys rloop-p3-implement enter"})
}

func TestAPromptToAnAgentWithoutHookEventsGoesThroughHerdrPrompt(t *testing.T) {
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		hooks func(feed *fakeHookFeed) HookFeed
	}{
		{"no hook feed", func(*fakeHookFeed) HookFeed { return nil }},
		{"a feed with only another agent's events", func(feed *fakeHookFeed) HookFeed {
			feed.push(HookEvent{At: t0, Agent: "rloop-p3-plan", Event: "Stop"})
			return feed
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			inner := &fakeSessionHost{}
			feed := &fakeHookFeed{}
			host := &claudeHost{SessionHost: inner, feed: feed}
			sm := hookPromptManager(host, tt.hooks(feed), t0)

			// when
			actualErr := sm.prompt(askingAgent, "go", nil)

			// then
			if actualErr != nil {
				t.Fatalf("prompt: %v", actualErr)
			}
			assertCalls(t, inner.Calls(), []string{`SessionHost.Prompt rloop-p3-implement "go" false 0s`})
		})
	}
}

func TestPromptsToOneAgentAreTypedOneAtATime(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	inner := &fakeSessionHost{}
	feed := idleHookAgentFeed(t0)
	host := &claudeHost{SessionHost: inner, feed: feed, onSubmit: map[string][]HookEvent{"nudge": {promptSubmit(askingAgent, "nudge", t0)}}}
	sm := hookPromptManager(host, feed, t0)
	first, second := make(chan error, 1), make(chan error, 1)

	// when
	go func() { first <- sm.prompt(askingAgent, "follow up", nil) }()
	waitFor(t, func() bool { return len(inner.Calls()) == 1 })
	go func() { second <- sm.prompt(askingAgent, "nudge", nil) }()
	reads := feed.readCount()
	waitFor(t, func() bool { return feed.readCount() >= reads+3 })

	// then
	assertCalls(t, inner.Calls(), []string{`SessionHost.Submit rloop-p3-implement "follow up"`})
	feed.push(promptSubmit(askingAgent, "follow up", t0))
	if actualErr := receiveErr(t, first); actualErr != nil {
		t.Fatalf("first prompt: %v", actualErr)
	}
	if actualErr := receiveErr(t, second); actualErr != nil {
		t.Fatalf("second prompt: %v", actualErr)
	}
	assertCalls(t, inner.Calls(), []string{`SessionHost.Submit rloop-p3-implement "follow up"`, `SessionHost.Submit rloop-p3-implement "nudge"`})
}

func TestAHandOffPromptIsNotTypedWhenAnotherPromptMadeTheAgentBusyFirst(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	inner := &fakeSessionHost{}
	feed := idleHookAgentFeed(t0)
	host := &claudeHost{SessionHost: inner, feed: feed, onSubmit: map[string][]HookEvent{"follow up": {promptSubmit(askingAgent, "follow up", t0)}}}
	sm := hookPromptManager(host, feed, t0)
	gate := sm.handOff(askingAgent)
	nudged, followed := make(chan error, 1), make(chan error, 1)

	// when
	go func() { nudged <- sm.prompt(askingAgent, "nudge", nil) }()
	waitFor(t, func() bool { return len(inner.Calls()) == 1 })
	go func() { followed <- sm.prompt(askingAgent, "follow up", gate) }()
	reads := feed.readCount()
	waitFor(t, func() bool { return feed.readCount() >= reads+3 })
	feed.push(promptSubmit(askingAgent, "nudge", t0))

	// then
	if actualErr := receiveErr(t, nudged); actualErr != nil {
		t.Fatalf("nudge: %v", actualErr)
	}
	if actualErr := receiveErr(t, followed); !errors.Is(actualErr, errTurnNotOver) {
		t.Fatalf("follow up: %v", actualErr)
	}
	assertCalls(t, inner.Calls(), []string{`SessionHost.Submit rloop-p3-implement "nudge"`})
}

const (
	handOffAnswer     = "r-loop: answer to q1 (by watchdog, citing docs/x/spec.html:2): sqlite"
	handOffWithdrawal = "r-loop: answer to q1 (by r-loop): withdrawn (panic in question q1: boom); continue without an answer"
)

type handOffRow struct {
	name    string
	text    string
	trigger func(r *routerRig)
}

func handOffRows() []handOffRow {
	return []handOffRow{
		{"answer", handOffAnswer, func(r *routerRig) { r.router.Answer("q1", "sqlite", "docs/x/spec.html:2") }},
		{"withdrawal", handOffWithdrawal, func(r *routerRig) { go r.loop.questionPanicked(routedQuestion(), "boom") }},
	}
}

func hookHandOffRig(t *testing.T, at time.Time, onSubmit map[string][]HookEvent, queued ...HookEvent) (*routerRig, *fakeHookFeed, *stepClock, *claudeHost) {
	t.Helper()
	r := newRouterRig(t)
	clock := &stepClock{t: at}
	feed := &fakeHookFeed{queued: queued}
	host := &claudeHost{SessionHost: r.ehost, feed: feed, onSubmit: onSubmit}
	r.loop.Sessions.Now = clock.Now
	r.loop.Sessions.Poll = time.Hour
	r.loop.Sessions.Hooks = feed
	r.loop.Sessions.Host = host
	return r, feed, clock, host
}

func assertDelivered(t *testing.T, r *routerRig, text string) {
	t.Helper()
	waitFor(t, func() bool { return !r.session.OpenQuestion.Load() })
	expected := []string{askingAgent + " " + strconv.Quote(text)}
	assertCalls(t, r.calls("SessionHost.Submit "), expected)
	assertCalls(t, r.typed(), expected)
}

func assertNothingTyped(t *testing.T, r *routerRig) {
	t.Helper()
	assertCalls(t, r.calls("SessionHost.Submit "), nil)
	assertCalls(t, r.typed(), nil)
	assertCalls(t, r.calls("SessionHost.SendKeys "), nil)
	if !r.session.OpenQuestion.Load() {
		t.Fatal("the question was released")
	}
}

func assertWithdrawnUntyped(t *testing.T, r *routerRig, reason string) {
	t.Helper()
	waitFor(t, func() bool { return !r.session.OpenQuestion.Load() })
	qs := r.questions()
	if len(qs) != 1 || qs[0].AnsweredBy != "withdrawn" || qs[0].Answer != reason {
		t.Fatalf("questions %+v", qs)
	}
	assertCalls(t, r.calls("SessionHost.Submit "), nil)
	assertCalls(t, r.typed(), nil)
	assertCalls(t, r.calls("SessionHost.Prompt "), nil)
	assertCalls(t, r.calls("SessionHost.SendKeys "), nil)
}

func TestHerdrIdleDoesNotHandTextToAHookAgentThatIsStillBusy(t *testing.T) {
	for _, row := range handOffRows() {
		t.Run(row.name, func(t *testing.T) {
			// given
			t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
			r, feed, _, _ := hookHandOffRig(t, t0, map[string][]HookEvent{row.text: {promptSubmit(askingAgent, row.text, t0.Add(time.Hour))}}, promptSubmit(askingAgent, "implement phase 3", t0.Add(-time.Minute)))
			t.Cleanup(func() {
				feed.push(HookEvent{At: t0, Agent: askingAgent, Event: "Stop"})
				waitFor(t, func() bool { return !r.session.OpenQuestion.Load() })
			})
			r.route(t)

			// when
			row.trigger(r)
			waitFor(t, func() bool { return feed.readCount() >= 3 })

			// then
			assertNothingTyped(t, r)
		})
	}
}

func TestTextForAHookAgentIsTypedAtItsStopWithoutAHerdrPollOrASessionWait(t *testing.T) {
	for _, row := range handOffRows() {
		t.Run(row.name, func(t *testing.T) {
			// given
			t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
			r, feed, _, _ := hookHandOffRig(t, t0, map[string][]HookEvent{row.text: {promptSubmit(askingAgent, row.text, t0.Add(time.Hour))}}, promptSubmit(askingAgent, "implement phase 3", t0.Add(-time.Minute)))
			r.route(t)

			// when
			row.trigger(r)
			feed.push(HookEvent{At: t0, Agent: askingAgent, Event: "Stop"})

			// then
			assertDelivered(t, r, row.text)
		})
	}
}

func TestTextForAHookAgentIsTypedAtAStopAnotherReaderTook(t *testing.T) {
	// given
	slowHookPoll(t)
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	r, feed, _, _ := hookHandOffRig(t, t0, map[string][]HookEvent{handOffAnswer: {promptSubmit(askingAgent, handOffAnswer, t0.Add(time.Hour))}}, promptSubmit(askingAgent, "implement phase 3", t0.Add(-time.Minute)))
	r.route(t)

	// when
	r.router.Answer("q1", "sqlite", "docs/x/spec.html:2")
	waitFor(t, func() bool { return feed.readCount() >= 1 })
	feed.push(HookEvent{At: t0, Agent: askingAgent, Event: "Stop"})
	r.loop.Sessions.hookNotice("rloop-p3-plan")

	// then
	assertDelivered(t, r, handOffAnswer)
}

func TestAnAnswerToAHookAgentWhoseTurnNeverEndsIsTypedOnlyOnceTheStallGraceHasPassed(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	r, feed, clock, _ := hookHandOffRig(t, t0, map[string][]HookEvent{handOffAnswer: {promptSubmit(askingAgent, handOffAnswer, t0.Add(time.Hour))}}, promptSubmit(askingAgent, "implement phase 3", t0.Add(-time.Minute)))
	r.loop.Sessions.StallGrace = 2 * time.Minute
	r.route(t)

	// when
	r.router.Answer("q1", "sqlite", "docs/x/spec.html:2")
	waitFor(t, func() bool { return feed.readCount() >= 3 })

	// then
	assertNothingTyped(t, r)
	clock.set(t0.Add(3 * time.Minute))
	assertDelivered(t, r, handOffAnswer)
}

func TestAnAnswerToAHookAgentIsTypedAtAStopThatComesAfterTheStallGrace(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	r, feed, clock, _ := hookHandOffRig(t, t0, map[string][]HookEvent{handOffAnswer: {promptSubmit(askingAgent, handOffAnswer, t0.Add(time.Hour))}}, promptSubmit(askingAgent, "implement phase 3", t0.Add(-time.Minute)))
	r.loop.Sessions.StallGrace = 2 * time.Minute
	r.ehost.mu.Lock()
	r.ehost.idle[askingAgent] = false
	r.ehost.mu.Unlock()
	r.route(t)

	// when
	r.router.Answer("q1", "sqlite", "docs/x/spec.html:2")
	waitFor(t, func() bool { return feed.readCount() >= 1 })
	clock.set(t0.Add(3 * time.Minute))
	reads := feed.readCount()
	waitFor(t, func() bool { return feed.readCount() >= reads+2 })
	feed.push(HookEvent{At: t0, Agent: askingAgent, Event: "Stop"})

	// then
	assertDelivered(t, r, handOffAnswer)
}

func TestAnAnswerRefusedAsBlockedIsTypedAgainForAHookAgent(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	r, _, _, _ := hookHandOffRig(t, t0, map[string][]HookEvent{handOffAnswer: {promptSubmit(askingAgent, handOffAnswer, t0.Add(time.Hour))}}, HookEvent{At: t0.Add(-time.Minute), Agent: askingAgent, Event: "Stop"})
	r.ehost.refuse[askingAgent] = 1
	r.route(t)

	// when
	r.router.Answer("q1", "sqlite", "docs/x/spec.html:2")

	// then
	r.waitTyped(t, 1)
	waitFor(t, func() bool { return !r.session.OpenQuestion.Load() })
	expected := askingAgent + " " + strconv.Quote(handOffAnswer)
	assertCalls(t, r.calls("SessionHost.Submit "), []string{expected, expected})
	assertCalls(t, r.calls("SessionHost.Refused "), []string{askingAgent})
	assertCalls(t, r.typed(), []string{expected})
}

func TestAPanicBeforeTheAskingTurnEndsWithdrawsWithoutTypingIntoTheBusyPane(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	r, feed, _, _ := hookHandOffRig(t, t0, nil, promptSubmit(askingAgent, "implement phase 3", t0.Add(-time.Minute)))
	calls := 0
	feed.next = func() []HookEvent {
		calls++
		if calls == 2 {
			panic("boom")
		}
		return nil
	}
	r.route(t)

	// when
	r.router.Answer("q1", "sqlite", "docs/x/spec.html:2")

	// then
	assertWithdrawnUntyped(t, r, "panic in answer hand-off q1: boom")
}

func TestAPanicAfterAStopTypesNoWithdrawalWhenAToolRanSince(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	r, feed, _, host := hookHandOffRig(t, t0, nil, HookEvent{At: t0.Add(-time.Minute), Agent: askingAgent, Event: "Stop"})
	host.statePanics = true
	calls := 0
	feed.next = func() []HookEvent {
		calls++
		if calls == 2 {
			return []HookEvent{{At: t0, Agent: askingAgent, Event: "PreToolUse", Tool: "Bash", Target: "go test ./..."}}
		}
		return nil
	}
	r.route(t)

	// when
	r.router.Answer("q1", "sqlite", "docs/x/spec.html:2")

	// then
	assertWithdrawnUntyped(t, r, "panic in answer hand-off q1: boom")
}

func TestAPanicAfterTheAskingTurnEndedTypesTheWithdrawalAfterHookConfirmation(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	withdrawal := "r-loop: answer to q1 (by r-loop): withdrawn (panic in answer hand-off q1: boom); continue without an answer"
	r, _, _, host := hookHandOffRig(t, t0, map[string][]HookEvent{withdrawal: {promptSubmit(askingAgent, withdrawal, t0.Add(time.Hour))}}, HookEvent{At: t0.Add(-time.Minute), Agent: askingAgent, Event: "Stop"})
	host.panics = handOffAnswer
	r.route(t)

	// when
	r.router.Answer("q1", "sqlite", "docs/x/spec.html:2")

	// then
	assertDelivered(t, r, withdrawal)
	if qs := r.questions(); len(qs) != 1 || qs[0].AnsweredBy != "withdrawn" || qs[0].Answer != "panic in answer hand-off q1: boom" {
		t.Fatalf("questions %+v", qs)
	}
}

func TestAnAnswerToAnAgentWithNoHookEventsIsTypedOnceHerdrStopsReportingWorking(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	r := newRouterRig(t)
	feed := &fakeHookFeed{queued: []HookEvent{promptSubmit("rloop-p3-plan", "plan phase 3", t0)}}
	r.loop.Sessions.Hooks = feed
	r.ehost.mu.Lock()
	r.ehost.idle[askingAgent] = false
	r.ehost.mu.Unlock()
	r.route(t)

	// when
	r.router.Answer("q1", "sqlite", "docs/x/spec.html:2")
	waitFor(t, func() bool { return feed.readCount() >= 3 })

	// then
	assertCalls(t, r.typed(), nil)
	assertCalls(t, r.calls("SessionHost.Submit "), nil)
	r.ehost.mu.Lock()
	r.ehost.idle[askingAgent] = true
	r.ehost.mu.Unlock()
	assertCalls(t, r.waitTyped(t, 1), []string{askingAgent + ` "r-loop: answer to q1 (by watchdog, citing docs/x/spec.html:2): sqlite"`})
	assertCalls(t, r.calls("SessionHost.Submit "), nil)
}

const declineText = "r-loop: the watchdog declined that request (dialog d1). Do not retry it; finish your task another way, or write a failed sentinel naming what you needed."

func hookDeclineRig(t *testing.T, at time.Time) (*dialogRig, *fakeHookFeed, *stepClock) {
	t.Helper()
	r := newDialogRig(t)
	clock := &stepClock{t: at}
	feed := &fakeHookFeed{queued: []HookEvent{{At: at, Agent: dialogAgent, Event: "PreToolUse", Tool: "Bash", Target: "go test ./..."}}}
	r.loop.Sessions.Now = clock.Now
	r.loop.Sessions.Poll = time.Hour
	r.loop.Sessions.Hooks = feed
	r.loop.Sessions.Host = &claudeHost{SessionHost: r.dhost, feed: feed, onSubmit: map[string][]HookEvent{declineText: {promptSubmit(dialogAgent, declineText, at.Add(time.Hour))}}}
	r.serve(t)
	r.raise(t, r.s)
	r.waitDog(t, 1)
	return r, feed, clock
}

func declineAndSettle(t *testing.T, r *dialogRig, feed *fakeHookFeed) {
	t.Helper()
	reads := feed.readCount()
	if d, reason := r.router.AnswerDialog("d1", []string{"esc"}, "decline", ""); d != decisionAuthorised {
		t.Fatalf("answer %s %q", d, reason)
	}
	r.dhost.set(dialogAgent, AgentIdle, "Interrupted · What should Claude do instead?")
	waitFor(t, func() bool { return feed.readCount() >= reads+3 })
}

func assertFollowUpDelivered(t *testing.T, r *dialogRig) {
	t.Helper()
	waitFor(t, func() bool { return len(r.calls("SessionHost.Submit ")) > 0 })
	assertCalls(t, r.calls("SessionHost.Submit "), []string{dialogAgent + " " + strconv.Quote(declineText)})
	waitFor(t, func() bool { return len(r.events("note")) > 0 })
	if evs := r.events("note"); len(evs) != 1 || evs[0].Fields["reason"] != "dialog d1 declined: told rloop-p2-implement to finish another way" {
		t.Fatalf("notes %+v", evs)
	}
}

func TestADeclineFollowUpToAHookAgentWaitsForItsIdlePromptWhileHerdrSaysIdle(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	r, feed, _ := hookDeclineRig(t, t0)

	// when
	declineAndSettle(t, r, feed)

	// then
	assertCalls(t, r.calls("SessionHost.Submit "), nil)
	assertCalls(t, r.agentPrompts(dialogAgent), nil)
	feed.push(HookEvent{At: t0, Agent: dialogAgent, Event: "Notification", Type: "idle_prompt", Notice: "Claude is waiting for your input"})
	assertFollowUpDelivered(t, r)
}

func TestADeclineFollowUpToAHookAgentWhoseTurnNeverEndsIsTypedOnceTheStallGraceHasPassed(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	r, feed, clock := hookDeclineRig(t, t0)
	r.loop.Sessions.StallGrace = 2 * time.Minute

	// when
	declineAndSettle(t, r, feed)

	// then
	assertCalls(t, r.calls("SessionHost.Submit "), nil)
	assertCalls(t, r.agentPrompts(dialogAgent), nil)
	clock.set(t0.Add(3 * time.Minute))
	assertFollowUpDelivered(t, r)
}

func TestAHandOffPastTheGraceIsNotTypedIntoANewTurnThatStartedInTheSameRead(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	inner := &fakeSessionHost{States: map[string]AgentState{askingAgent: AgentIdle}}
	feed := &fakeHookFeed{queued: []HookEvent{promptSubmit(askingAgent, "implement phase 3", t0.Add(-time.Minute))}}
	clock := &stepClock{t: t0}
	sm := &SessionManager{Host: &claudeHost{SessionHost: inner, feed: feed}, Hooks: feed, Now: clock.Now, StallGrace: 2 * time.Minute}
	gate := sm.handOff(askingAgent)
	gate.check()
	clock.set(t0.Add(3 * time.Minute))
	gate.check()
	feed.push(HookEvent{At: t0.Add(3 * time.Minute), Agent: askingAgent, Event: "Stop"}, promptSubmit(askingAgent, "nudge", t0.Add(3*time.Minute)))

	// when
	actualErr := sm.prompt(askingAgent, "follow up", gate)

	// then
	if !errors.Is(actualErr, errTurnNotOver) {
		t.Fatalf("prompt: %v", actualErr)
	}
	assertCalls(t, inner.Calls(), []string{"SessionHost.State rloop-p3-implement"})
}

func assertErrorIs(t *testing.T, actual, expected error) {
	t.Helper()
	if !errors.Is(actual, expected) {
		t.Fatalf("error = %v, want %v", actual, expected)
	}
}

func TestAHandOffIsNotTypedWhenAToolStartsWhileHerdrIsAsked(t *testing.T) {
	// given
	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	inner := &fakeSessionHost{States: map[string]AgentState{askingAgent: AgentIdle}}
	feed := idleHookAgentFeed(t0)
	host := &claudeHost{SessionHost: inner, feed: feed, onState: []HookEvent{{At: t0, Agent: askingAgent, Event: "PreToolUse", Tool: "Bash", Target: "go test ./..."}}}
	sm := hookPromptManager(host, feed, t0)

	// when
	actualErr := sm.prompt(askingAgent, "follow up", sm.handOff(askingAgent))

	// then
	assertErrorIs(t, actualErr, errTurnNotOver)
	assertCalls(t, inner.Calls(), []string{"SessionHost.State rloop-p3-implement"})
}

var (
	t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	t1 = time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
)

const (
	stepAgent      = "rloop-2kuxv-p3-implement"
	codexReviewer  = "rloop-2kuxv-p3-implemen-8lgad-r1"
	hookNudgeText  = "r-loop: your turn ended with no sentinel. If your work is done, write the sentinel now. If you are blocked, write a failed sentinel with the reason."
	graceNudgeText = "r-loop: no sentinel and no activity for 2m0s. If your work is done, write the sentinel now. If you are blocked, write a failed sentinel with the reason."
)

type startingHost struct {
	SessionHost
	feed   *fakeHookFeed
	pushes [][]HookEvent
	errs   []error
	n      int
}

func (h *startingHost) Start(pane, name, kind string, args []string) (Agent, error) {
	n := h.n
	h.n++
	if n < len(h.errs) && h.errs[n] != nil {
		if n < len(h.pushes) {
			h.feed.push(h.pushes[n]...)
		}
		return Agent{}, h.errs[n]
	}
	a, err := h.SessionHost.Start(pane, name, kind, args)
	if n < len(h.pushes) {
		h.feed.push(h.pushes[n]...)
	}
	return a, err
}

func hookStepRig(t *testing.T, onStart ...HookEvent) (*rig, *fakeHookFeed, *fakeFace) {
	t.Helper()
	r := newRig(t)
	feed := &fakeHookFeed{}
	face := &fakeFace{}
	r.sm.Hooks = feed
	r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, pushes: [][]HookEvent{onStart}}
	r.sm.Face = face
	r.sm.HookTap = "/usr/local/bin/r-loop"
	r.sm.Now = (&stepClock{t: t0}).Now
	r.sm.Resolve = func(provider, model, effort, askURL, mcpConfigPath, dir string) (ProviderArgs, error) {
		return ProviderArgs{Kind: "claude", Hooks: true}, nil
	}
	r.host.onSubmit = func(agent, text string) { feed.push(promptSubmit(agent, text, t1)) }
	return r, feed, face
}

func hookedResolve(r *reviewRig) {
	inner := r.sm.Resolve
	r.sm.Resolve = func(provider, model, effort, askURL, mcpConfigPath, dir string) (ProviderArgs, error) {
		args, err := inner(provider, model, effort, askURL, mcpConfigPath, dir)
		args.Hooks = true
		return args, err
	}
}

func rolloutResolve(r *reviewRig) {
	inner := r.sm.Resolve
	r.sm.Resolve = func(provider, model, effort, askURL, mcpConfigPath, dir string) (ProviderArgs, error) {
		args, err := inner(provider, model, effort, askURL, mcpConfigPath, dir)
		args.Rollout = provider == "codex"
		return args, err
	}
}

func sessionStart(agent string) HookEvent {
	return HookEvent{At: t0, Agent: agent, Event: "SessionStart"}
}

func sessionEnd(agent, reason string) HookEvent {
	return HookEvent{At: t0, Agent: agent, Event: "SessionEnd", Reason: reason}
}

func stop(agent string) HookEvent {
	return HookEvent{At: t0, Agent: agent, Event: "Stop"}
}

func idlePrompt(agent string) HookEvent {
	return HookEvent{At: t0, Agent: agent, Event: "Notification", Type: "idle_prompt", Notice: "Claude is waiting for your input"}
}

func shortHookStartWait(sm *SessionManager) {
	sm.HookStartWait = 10 * time.Millisecond
}

func fastHookPoll(t *testing.T) {
	old := hookPoll
	hookPoll = time.Millisecond
	t.Cleanup(func() { hookPoll = old })
}

func waitCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func warnings(face *fakeFace) []string {
	var out []string
	for _, ev := range face.events() {
		if ev.Kind == "warning" {
			out = append(out, ev.Fields["reason"])
		}
	}
	return out
}

func pinnedWarning(agent string) string {
	return "hook events: no SessionStart from " + agent + " within 10ms (hook tap /usr/local/bin/r-loop hook-tap); its turn ends follow herdr"
}

func prefixed(calls []string, prefix string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func statesBeforeEach(calls []string, call string) []int {
	var out []int
	states := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "SessionHost.State ") {
			states++
		}
		if c == call {
			out = append(out, states)
		}
	}
	return out
}

func assertState(t *testing.T, actual Outcome, expectedState StepState, expectedReason string) {
	t.Helper()
	if actual.State != expectedState || actual.Reason != expectedReason {
		t.Fatalf("outcome = %s %q, want %s %q", actual.State, actual.Reason, expectedState, expectedReason)
	}
}

func assertEqual[T any](t *testing.T, what string, actual, expected T) {
	t.Helper()
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("%s = %v, want %v", what, actual, expected)
	}
}

func stepPrompt(text string) string {
	return "SessionHost.Prompt " + stepAgent + " " + strconv.Quote(text) + " false 0s"
}

func TestAClaudeAgentsFirstPromptIsTypedAfterItsSessionStartIsRead(t *testing.T) {
	// given
	fastHookPoll(t)
	r, feed, _ := hookStepRig(t)
	reads := 0
	feed.next = func() []HookEvent {
		reads++
		if reads == 4 {
			return []HookEvent{sessionStart(stepAgent)}
		}
		return nil
	}
	actualReadsAtSubmit := 0
	r.host.onSubmit = func(agent, text string) {
		actualReadsAtSubmit = feed.readCount()
		feed.push(promptSubmit(agent, text, t1))
	}

	// when
	r.spawn(t, 1)

	// then
	assertEqual(t, "reads at submit", actualReadsAtSubmit, 5)
	assertCalls(t, prefixed(r.shared.Calls(), "SessionHost.Submit"), []string{submitCall(stepAgent, "do phase 3")})
	assertCalls(t, prefixed(r.shared.Calls(), "SessionHost.Prompt"), nil)
}

func TestAClaudeAgentWithoutASessionStartWarnsOnceNamingItAndTheHookTap(t *testing.T) {
	tests := []struct {
		name    string
		onStart []HookEvent
	}{
		{"no hook events", nil},
		{"other hook events only", []HookEvent{{At: t0, Agent: stepAgent, Event: "PreToolUse", Tool: "Read", Target: "/repo/a.go"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			fastHookPoll(t)
			r, _, face := hookStepRig(t, tt.onStart...)
			shortHookStartWait(r.sm)

			// when
			s := r.spawn(t, 1)
			r.repo.TreeChanges = []string{"a.go"}
			r.writeSentinel(t, s, "ok", "")
			r.host.script = func(int) AgentState { return AgentIdle }
			r.sm.Wait(waitCtx(t), s, &recObserver{})
			r.sm.Wait(waitCtx(t), s, &recObserver{})

			// then
			assertCalls(t, warnings(face), []string{pinnedWarning(stepAgent)})
		})
	}
}

func TestAWorkerWithoutASessionStartIsWarnedAboutEvenWhenItsStartFails(t *testing.T) {
	// given
	fastHookPoll(t)
	r, _, face := hookStepRig(t)
	shortHookStartWait(r.sm)
	r.prompts.Err = errors.New("prompt template missing")

	// when
	_, actualErr := r.sm.Spawn(context.Background(), r.ref(1))

	// then
	if actualErr == nil || actualErr.Error() != "spawn: prompt template missing" {
		t.Fatalf("spawn: %v", actualErr)
	}
	assertCalls(t, warnings(face), []string{pinnedWarning(stepAgent)})
}

func TestAReviewerWithoutASessionStartIsWarnedAboutWhenAnotherReviewersStartFails(t *testing.T) {
	// given
	fastHookPoll(t)
	r := newReviewRig(t, Reviewer{Provider: "codex"}, Reviewer{Provider: "claude"})
	shortHookStartWait(r.sm)
	feed := &fakeHookFeed{}
	face := &fakeFace{}
	r.sm.Hooks, r.sm.Face, r.sm.HookTap = feed, face, "/usr/local/bin/r-loop"
	hookedResolve(r)
	r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, errs: []error{nil, errors.New("start refused")}}

	// when
	out := r.run()

	// then
	assertEqual(t, "reason", out.Reason, "reviewer claude: start refused")
	assertCalls(t, warnings(face), []string{pinnedWarning(codexReviewer)})
}

func TestAReopenedReviewerIsNotFailedByItsPredecessorsSessionEnd(t *testing.T) {
	// given
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	feed := &fakeHookFeed{}
	r.sm.Hooks = feed
	hookedResolve(r)
	started := HookEvent{At: t1, Agent: codexReviewer, Event: "SessionStart"}
	r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, pushes: [][]HookEvent{
		{started},
		{{At: time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC), Agent: codexReviewer, Event: "SessionEnd", Reason: "other"}, started},
	}}
	reviews := 0
	r.behave = func(vars map[string]any) {
		reviews++
		if reviews == 1 {
			feed.push(HookEvent{At: t1, Agent: codexReviewer, Event: "SessionEnd", Reason: "logout"})
			return
		}
		writeReview(t, vars, "ok", 0)
	}

	// when
	out, obs := r.runRaising(then(Resolution{Action: "retry", By: "watchdog"}, Resolution{Action: "block", By: "watchdog"}))

	// then
	assertEqual(t, "state", out.State, StepOK)
	if len(obs.blockers) != 1 || obs.blockers[0].Reason != "reviewer codex: agent exited: logout" {
		t.Fatalf("blockers = %+v", obs.blockers)
	}
}

func TestAReopenedReviewerIsHookEnabledThoughItsPredecessorHadNoSessionStart(t *testing.T) {
	// given
	fastHookPoll(t)
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	shortHookStartWait(r.sm)
	failCodexOnce(t, r)
	feed := &fakeHookFeed{}
	face := &fakeFace{}
	r.sm.Hooks, r.sm.Face, r.sm.HookTap = feed, face, "/usr/local/bin/r-loop"
	hookedResolve(r)
	r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, pushes: [][]HookEvent{{}, {{At: t1, Agent: codexReviewer, Event: "SessionStart"}}}}

	// when
	out, _ := r.runRaising(then(Resolution{Action: "retry", By: "watchdog"}, Resolution{Action: "block", By: "watchdog"}))

	// then
	assertEqual(t, "state", out.State, StepOK)
	assertCalls(t, prefixed(r.shared.Calls(), "SessionHost.Prompt "+codexReviewer), []string{"SessionHost.Prompt " + codexReviewer + ` "review r1" false 0s`})
	assertCalls(t, prefixed(r.shared.Calls(), "SessionHost.Submit "+codexReviewer), []string{submitCall(codexReviewer, "review r1")})
	assertCalls(t, warnings(face), []string{pinnedWarning(codexReviewer)})
}

func TestAnAgentWithoutASessionStartIsJudgedAtHerdrIdleWhenHookEventsArriveLater(t *testing.T) {
	// given
	fastHookPoll(t)
	r, feed, _ := hookStepRig(t)
	shortHookStartWait(r.sm)
	s := r.spawn(t, 1)
	r.sm.Now = (&stepClock{t: t0, step: time.Minute}).Now
	r.repo.TreeChanges = []string{"a.go"}
	r.host.script = func(n int) AgentState {
		if n == 1 {
			r.writeSentinel(t, s, "ok", "")
			feed.push(promptSubmit(stepAgent, "do phase 3", t1))
		}
		return AgentIdle
	}

	// when
	out := r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertEqual(t, "state", out.State, StepOK)
	assertEqual(t, "turn-overran-sentinel events", len(r.events("turn-overran-sentinel")), 0)
}

func TestAnAgentWithoutASessionStartIsNudgedOnlyOnHerdrQuietThroughHerdrPrompt(t *testing.T) {
	// given
	fastHookPoll(t)
	r, feed, _ := hookStepRig(t)
	shortHookStartWait(r.sm)
	s := r.spawn(t, 1)
	r.sm.Now = (&stepClock{t: t0, step: time.Minute}).Now
	r.host.script = func(n int) AgentState {
		if n == 1 {
			feed.push(promptSubmit(stepAgent, "do phase 3", t1), stop(stepAgent))
		}
		return AgentIdle
	}

	// when
	out := r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertEqual(t, "reason", out.Reason, "stalled: no response to nudge")
	assertCalls(t, prefixed(r.shared.Calls(), "SessionHost.Prompt"), []string{stepPrompt("do phase 3"), stepPrompt(graceNudgeText)})
	assertCalls(t, prefixed(r.shared.Calls(), "SessionHost.Submit"), nil)
	assertEqual(t, "nudges", len(r.events("nudge")), 1)
}

func TestAHandOffToAnAgentWithoutASessionStartFollowsHerdrThoughItsHooksSayBusy(t *testing.T) {
	// given
	fastHookPoll(t)
	feed := &fakeHookFeed{}
	sm := hookPromptManager(&fakeSessionHost{States: map[string]AgentState{askingAgent: AgentIdle}}, feed, t0)
	shortHookStartWait(sm)

	// when
	sm.hookStart(&Session{Agent: askingAgent}, true)
	feed.push(promptSubmit(askingAgent, "implement phase 3", t0))
	actualState, actualReady, actualErr := sm.handOff(askingAgent).check()

	// then
	assertEqual(t, "state", actualState, AgentIdle)
	assertEqual(t, "ready", actualReady, true)
	assertEqual(t, "err", actualErr, nil)
}

func TestAPromptToAnAgentWithoutASessionStartGoesThroughHerdrPromptThoughItHasHookEvents(t *testing.T) {
	// given
	fastHookPoll(t)
	feed := &fakeHookFeed{}
	inner := &fakeSessionHost{}
	sm := hookPromptManager(inner, feed, t0)
	shortHookStartWait(sm)
	sm.hookStart(&Session{Agent: askingAgent}, true)
	feed.push(stop(askingAgent))

	// when
	actualErr := sm.prompt(askingAgent, "go", nil)

	// then
	assertEqual(t, "err", actualErr, nil)
	assertCalls(t, inner.Calls(), []string{`SessionHost.Prompt rloop-p3-implement "go" false 0s`})
}

func TestNoSessionStartWarningForAProviderWithoutHooksOrARunWithoutAHookFeed(t *testing.T) {
	tests := []struct {
		name  string
		hooks bool
		feed  bool
	}{
		{"provider without hooks", false, true},
		{"run without a hook feed", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			fastHookPoll(t)
			r, _, face := hookStepRig(t)
			shortHookStartWait(r.sm)
			r.sm.Resolve = func(provider, model, effort, askURL, mcpConfigPath, dir string) (ProviderArgs, error) {
				return ProviderArgs{Kind: "codex", Hooks: tt.hooks}, nil
			}
			if !tt.feed {
				r.sm.Hooks = nil
			}

			// when
			s := r.spawn(t, 1)
			r.writeSentinel(t, s, "ok", "")
			r.repo.TreeChanges = []string{"a.go"}
			r.host.script = func(int) AgentState { return AgentIdle }
			r.sm.Wait(waitCtx(t), s, &recObserver{})

			// then
			assertCalls(t, warnings(face), nil)
		})
	}
}

func TestASentinelWrittenBeforeATypedPromptsUserPromptSubmitIsReadIsNotJudged(t *testing.T) {
	// given
	r, feed, _ := hookStepRig(t, sessionStart(stepAgent))
	s := r.spawn(t, 1)
	feed.push(stop(stepAgent))
	r.host.onSubmit = nil
	r.repo.TreeChanges = []string{"a.go"}
	answer := "r-loop: answer to q1 (by watchdog): sqlite"
	prompted := make(chan error, 1)
	waited := make(chan Outcome, 1)

	// when
	go func() { prompted <- r.sm.prompt(stepAgent, answer, nil) }()
	waitFor(t, func() bool { return len(prefixed(r.shared.Calls(), submitCall(stepAgent, answer))) == 1 })
	r.writeSentinel(t, s, "ok", "")
	go func() { waited <- r.sm.Wait(waitCtx(t), s, &recObserver{}) }()
	reads := feed.readCount()
	waitFor(t, func() bool { return feed.readCount() >= reads+3 })

	// then
	select {
	case out := <-waited:
		t.Fatalf("judged before the UserPromptSubmit was read: %+v", out)
	default:
	}
	feed.push(promptSubmit(stepAgent, answer, t1), stop(stepAgent))
	select {
	case out := <-waited:
		assertEqual(t, "state", out.State, StepOK)
	case <-time.After(5 * time.Second):
		t.Fatal("the step was never judged")
	}
	assertEqual(t, "prompt err", receiveErr(t, prompted), nil)
}

func TestASessionStartMidTurnDoesNotEndTheTurn(t *testing.T) {
	// given
	r, feed, _ := hookStepRig(t, sessionStart(stepAgent))
	s := r.spawn(t, 1)
	r.repo.TreeChanges = []string{"a.go"}
	r.host.script = func(n int) AgentState {
		switch n {
		case 1:
			r.writeSentinel(t, s, "ok", "")
			feed.push(sessionStart(stepAgent))
		case 3:
			feed.push(stop(stepAgent))
		}
		return AgentIdle
	}

	// when
	out := r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertEqual(t, "state", out.State, StepOK)
	assertEqual(t, "polls", r.host.polls, 3)
}

func hookStallRig(t *testing.T) (*rig, *fakeHookFeed, *Session) {
	t.Helper()
	r, feed, _ := hookStepRig(t, sessionStart(stepAgent))
	s := r.spawn(t, 1)
	r.repo.TreeChanges = []string{"a.go"}
	return r, feed, s
}

func recordNudgePoll(r *rig, feed *fakeHookFeed, poll *int) {
	r.host.onSubmit = func(agent, text string) {
		feed.push(promptSubmit(agent, text, t1))
		if text == hookNudgeText {
			*poll = r.host.polls
		}
	}
}

func TestAHookAgentWhoseTurnEndsWithoutASentinelIsNudgedAtTheNextTick(t *testing.T) {
	tests := []struct {
		name    string
		turnEnd HookEvent
	}{
		{"Stop", stop(stepAgent)},
		{"idle_prompt", idlePrompt(stepAgent)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			r, feed, s := hookStallRig(t)
			r.host.script = func(n int) AgentState {
				switch n {
				case 1:
					feed.push(tt.turnEnd)
				case 5:
					r.writeSentinel(t, s, "ok", "")
					feed.push(stop(stepAgent))
				}
				return AgentWorking
			}
			actualNudgePoll := 0
			recordNudgePoll(r, feed, &actualNudgePoll)
			obs := &recObserver{}

			// when
			r.sm.Wait(waitCtx(t), s, obs)

			// then
			assertEqual(t, "nudge poll", actualNudgePoll, 3)
			assertCalls(t, prefixed(r.shared.Calls(), "SessionHost.Submit"), []string{submitCall(stepAgent, "do phase 3"), submitCall(stepAgent, hookNudgeText)})
			assertEqual(t, "nudges", len(r.events("nudge")), 1)
			assertEqual(t, "stalled", obs.stalled, 1)
		})
	}
}

func TestAHookAgentThatEndsASecondTurnWithoutASentinelFailsAsStalled(t *testing.T) {
	// given
	r, feed, s := hookStallRig(t)
	r.host.script = func(n int) AgentState {
		if n == 1 || n == 5 {
			feed.push(stop(stepAgent))
		}
		return AgentWorking
	}

	// when
	out := r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertState(t, out, StepFailed, "stalled: no response to nudge")
	assertEqual(t, "stalled", out.Stalled, true)
	assertEqual(t, "nudges", len(r.events("nudge")), 1)
}

func TestAHookAgentQuietForAGraceAfterItsTurnEndNudgeFailsWithoutASecondNudge(t *testing.T) {
	// given
	r, feed, s := hookStallRig(t)
	r.sm.Now = (&stepClock{t: t0, step: time.Minute}).Now
	r.host.script = func(n int) AgentState {
		if n == 1 {
			feed.push(stop(stepAgent))
		}
		if n <= 4 {
			return AgentWorking
		}
		return AgentIdle
	}
	obs := &recObserver{}

	// when
	out := r.sm.Wait(waitCtx(t), s, obs)

	// then
	assertEqual(t, "reason", out.Reason, "stalled: no response to nudge")
	assertEqual(t, "polls", r.host.polls, 6)
	assertEqual(t, "nudges", len(r.events("nudge")), 1)
	assertEqual(t, "resumed", obs.resumed, 1)
}

func TestATurnEndNudgeIsNotTypedWhenAToolStartsBeforeIt(t *testing.T) {
	// given
	r, feed, s := hookStallRig(t)
	r.host.script = func(n int) AgentState {
		switch n {
		case 1:
			feed.push(stop(stepAgent))
		case 2:
			feed.push(HookEvent{At: t0, Agent: stepAgent, Event: "PreToolUse", Tool: "Bash", Target: "go test ./..."})
		case 3:
			r.writeSentinel(t, s, "ok", "")
			feed.push(stop(stepAgent))
		}
		return AgentWorking
	}
	obs := &recObserver{}

	// when
	out := r.sm.Wait(waitCtx(t), s, obs)

	// then
	assertEqual(t, "state", out.State, StepOK)
	assertCalls(t, prefixed(r.shared.Calls(), "SessionHost.Submit"), []string{submitCall(stepAgent, "do phase 3")})
	assertEqual(t, "nudges", len(r.events("nudge")), 0)
	assertEqual(t, "steps", r.steps(), []StepState{StepSpawned, StepRunning, StepStalled, StepRunning})
	assertEqual(t, "stalled", obs.stalled, 1)
	assertEqual(t, "resumed", obs.resumed, 1)
}

func TestAHookAgentsTurnEndDuringAnOpenQuestionIsNotNudged(t *testing.T) {
	// given
	r, feed, s := hookStallRig(t)
	s.OpenQuestion.Store(true)
	r.host.script = func(n int) AgentState {
		switch n {
		case 1:
			feed.push(stop(stepAgent))
		case 5:
			r.writeSentinel(t, s, "ok", "")
		}
		return AgentWorking
	}

	// when
	out := r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertEqual(t, "state", out.State, StepOK)
	assertEqual(t, "nudges", len(r.events("nudge")), 0)
	assertCalls(t, prefixed(r.shared.Calls(), "SessionHost.Submit"), []string{submitCall(stepAgent, "do phase 3")})
}

func declineWatchRig(t *testing.T, agents ...string) (*dialogRig, *fakeHookFeed) {
	t.Helper()
	r := newDialogRig(t)
	slowHookPoll(t)
	r.loop.Sessions.Now = (&stepClock{t: t0}).Now
	r.loop.Sessions.Poll = time.Millisecond
	feed := &fakeHookFeed{}
	onSubmit := map[string][]HookEvent{}
	for i, agent := range agents {
		feed.push(HookEvent{At: t0, Agent: agent, Event: "PreToolUse", Tool: "Bash", Target: "go test ./..."})
		text := declinedMessage("d" + strconv.Itoa(i+1))
		onSubmit[text] = []HookEvent{promptSubmit(agent, text, t1)}
	}
	onSubmit[hookNudgeText] = []HookEvent{promptSubmit(agents[len(agents)-1], hookNudgeText, t1)}
	r.loop.Sessions.Hooks = feed
	r.loop.Sessions.Host = &claudeHost{SessionHost: r.dhost, feed: feed, onSubmit: onSubmit}
	r.serve(t)
	return r, feed
}

func watchSession(t *testing.T, r *dialogRig, s *Session) (<-chan Outcome, *recObserver, context.CancelFunc) {
	t.Helper()
	s.Sentinel = filepath.Join(t.TempDir(), "implement-a1.sentinel")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	obs := &recObserver{}
	waited := make(chan Outcome, 1)
	go func() { waited <- r.loop.Sessions.Wait(ctx, s, obs) }()
	return waited, obs, cancel
}

func decline(t *testing.T, r *dialogRig, id, agent string) {
	t.Helper()
	if d, reason := r.router.AnswerDialog(id, []string{"esc"}, "decline", ""); d != decisionAuthorised {
		t.Fatalf("answer %s %q", d, reason)
	}
	r.dhost.set(agent, AgentIdle, "Interrupted · What should Claude do instead?")
}

func waitReads(t *testing.T, feed *fakeHookFeed, n int) {
	t.Helper()
	reads := feed.readCount()
	waitFor(t, func() bool { return feed.readCount() >= reads+n })
}

func TestADeclineFollowUpIsTypedBeforeAnyTurnEndNudge(t *testing.T) {
	// given
	r, feed := declineWatchRig(t, dialogAgent)
	r.raise(t, r.s)
	r.waitDog(t, 1)

	// when
	decline(t, r, "d1", dialogAgent)
	waited, obs, cancel := watchSession(t, r, r.s)
	waitReads(t, feed, 3)
	feed.push(idlePrompt(dialogAgent))
	waitFor(t, func() bool { return len(r.calls("SessionHost.Submit ")) == 1 })
	waitReads(t, feed, 3)
	cancel()
	<-waited

	// then
	assertCalls(t, r.calls("SessionHost.Submit "), []string{dialogAgent + " " + strconv.Quote(declinedMessage("d1"))})
	assertEqual(t, "nudges", len(r.events("nudge")), 0)
	assertEqual(t, "stalled", obs.stalled, 0)
}

func TestASecondReviewersDeclineFollowUpStillComesBeforeAnyNudgeWhenTheFirstFinishes(t *testing.T) {
	// given
	rvA := &Session{Agent: "rloop-p2-implement-rv-codex", Reviewer: "codex"}
	rvB := &Session{Agent: "rloop-p2-implement-rv-claude", Reviewer: "claude"}
	r, feed := declineWatchRig(t, rvA.Agent, rvB.Agent)
	rvA.Ref, rvA.owner = r.s.Ref, r.s
	rvB.Ref, rvB.owner = r.s.Ref, r.s
	r.dhost.set(dialogAgent, AgentWorking, "")
	r.dhost.set(rvA.Agent, AgentBlocked, "Run tests?\n> yes")
	r.raise(t, rvA)
	r.waitDog(t, 1)
	decline(t, r, "d1", rvA.Agent)
	r.dhost.set(rvB.Agent, AgentBlocked, "Edit a.go?\n> yes")
	r.raise(t, rvB)
	r.waitDog(t, 2)
	decline(t, r, "d2", rvB.Agent)
	waited, obs, cancel := watchSession(t, r, rvB)

	// when
	feed.push(idlePrompt(rvA.Agent))
	waitFor(t, func() bool { return len(r.calls("SessionHost.Submit ")) == 1 })
	waitReads(t, feed, 3)
	feed.push(idlePrompt(rvB.Agent))
	waitFor(t, func() bool { return len(r.calls("SessionHost.Submit ")) == 2 })
	waitReads(t, feed, 3)
	cancel()
	<-waited

	// then
	assertCalls(t, r.calls("SessionHost.Submit "), []string{rvA.Agent + " " + strconv.Quote(declinedMessage("d1")), rvB.Agent + " " + strconv.Quote(declinedMessage("d2"))})
	assertEqual(t, "nudges", len(r.events("nudge")), 0)
	assertEqual(t, "stalled", obs.stalled, 0)
}

func TestASessionEndWithAnExitReasonFailsTheStepWhileHerdrNeverReportsGone(t *testing.T) {
	for _, reason := range []string{"exit", "logout", "prompt_input_exit", "other"} {
		t.Run(reason, func(t *testing.T) {
			// given
			r, feed, s := hookStallRig(t)
			r.host.script = func(n int) AgentState {
				if n == 1 {
					feed.push(sessionEnd(stepAgent, reason))
				}
				return AgentWorking
			}

			// when
			out := r.sm.Wait(waitCtx(t), s, &recObserver{})

			// then
			assertState(t, out, StepFailed, "agent exited: "+reason)
			assertEqual(t, "polls", r.host.polls, 1)
			assertEqual(t, "nudges", len(r.events("nudge")), 0)
		})
	}
}

func TestASessionEndWithASentinelPresentJudgesTheSentinel(t *testing.T) {
	tests := []struct {
		name           string
		write          func(t *testing.T, r *rig, s *Session)
		expectedState  StepState
		expectedPrefix string
	}{
		{"ok sentinel", func(t *testing.T, r *rig, s *Session) { r.writeSentinel(t, s, "ok", "") }, StepOK, ""},
		{"half-written sentinel", func(t *testing.T, r *rig, s *Session) { r.writeRawSentinel(t, s, `{"outcome":`) }, StepFailed, "sentinel unreadable: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			r, feed, s := hookStallRig(t)
			r.host.script = func(n int) AgentState {
				if n == 1 {
					tt.write(t, r, s)
					feed.push(sessionEnd(stepAgent, "logout"))
				}
				return AgentWorking
			}

			// when
			out := r.sm.Wait(waitCtx(t), s, &recObserver{})

			// then
			assertEqual(t, "state", out.State, tt.expectedState)
			if actualReason := out.Reason; !strings.HasPrefix(actualReason, tt.expectedPrefix) {
				t.Fatalf("reason = %q, want prefix %q", actualReason, tt.expectedPrefix)
			}
		})
	}
}

func pinnedStallRig(t *testing.T) (*rig, *fakeHookFeed, *Session) {
	t.Helper()
	fastHookPoll(t)
	r, feed, _ := hookStepRig(t)
	shortHookStartWait(r.sm)
	s := r.spawn(t, 1)
	r.repo.TreeChanges = []string{"a.go"}
	return r, feed, s
}

func TestASessionEndFromAnAgentPinnedToHerdrFailsTheStepAtTheNextTick(t *testing.T) {
	for _, reason := range []string{"exit", "logout", "prompt_input_exit", "other"} {
		t.Run(reason, func(t *testing.T) {
			// given
			r, feed, s := pinnedStallRig(t)
			r.host.script = func(n int) AgentState {
				if n == 1 {
					feed.push(sessionEnd(stepAgent, reason))
				}
				return AgentWorking
			}

			// when
			out := r.sm.Wait(waitCtx(t), s, &recObserver{})

			// then
			assertEqual(t, "reason", out.Reason, "agent exited: "+reason)
			assertEqual(t, "polls", r.host.polls, 1)
		})
	}
}

func TestASessionEndFromAnAgentPinnedToHerdrJudgesItsSentinel(t *testing.T) {
	// given
	r, feed, s := pinnedStallRig(t)
	r.host.script = func(n int) AgentState {
		if n == 1 {
			r.writeSentinel(t, s, "ok", "")
			feed.push(sessionEnd(stepAgent, "logout"))
		}
		return AgentWorking
	}

	// when
	out := r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertEqual(t, "state", out.State, StepOK)
	assertEqual(t, "polls", r.host.polls, 1)
}

func TestASessionEndForClearOrResumeDoesNotEndTheStep(t *testing.T) {
	for _, reason := range []string{"clear", "resume"} {
		t.Run(reason, func(t *testing.T) {
			// given
			r, feed, s := hookStallRig(t)
			r.host.script = func(n int) AgentState {
				switch n {
				case 1:
					feed.push(sessionEnd(stepAgent, reason))
				case 3:
					r.writeSentinel(t, s, "ok", "")
					feed.push(stop(stepAgent))
				}
				return AgentWorking
			}

			// when
			out := r.sm.Wait(waitCtx(t), s, &recObserver{})

			// then
			assertEqual(t, "state", out.State, StepOK)
		})
	}
}

func TestARefusedPromptLeavesTheAgentNotBusy(t *testing.T) {
	// given
	inner := &fakeSessionHost{Err: errors.New("herdr agent prompt: agent_blocked")}
	sm := hookPromptManager(inner, idleHookAgentFeed(t0), t0)

	// when
	actualErr := sm.prompt(askingAgent, "go", nil)

	// then
	assertEqual(t, "err", actualErr.Error(), "herdr agent prompt: agent_blocked")
	assertCalls(t, inner.Calls(), []string{`SessionHost.Submit rloop-p3-implement "go"`})
	_, actualBusy, _ := sm.hookTurn(askingAgent)
	assertEqual(t, "busy", actualBusy, false)
}

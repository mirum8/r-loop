package core

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	dialogAgent  = "rloop-p2-implement"
	dialogScreen = "Allow write to .r-loop/runs/run-1?\n1. Yes\n2. No"
	dialogRule   = "approve writes under the run folder"
)

type dialogHost struct {
	fakeSessionHost
	mu      sync.Mutex
	states  map[string]AgentState
	screens map[string]string
	script  []AgentState
}

func (h *dialogHost) State(agent string) (AgentState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.script) > 0 {
		st := h.script[0]
		h.script = h.script[1:]
		return st, nil
	}
	if st, ok := h.states[agent]; ok {
		return st, nil
	}
	return AgentWorking, nil
}

func (h *dialogHost) Screen(agent string) (string, error) {
	h.record("SessionHost.Screen %s", agent)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.screens[agent], nil
}

func (h *dialogHost) set(agent string, state AgentState, screen string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.states[agent], h.screens[agent] = state, screen
}

type dialogRig struct {
	*eventsRig
	dhost  *dialogHost
	dog    *fakeSessionHost
	router *QuestionRouter
	watch  *Watch
	s      *Session
}

func newDialogRig(t *testing.T) *dialogRig {
	r := &dialogRig{eventsRig: newEventsRig(t)}
	r.dhost = &dialogHost{fakeSessionHost: fakeSessionHost{callLog: callLog{Shared: r.shared}}, states: map[string]AgentState{}, screens: map[string]string{}}
	r.loop.Sessions.Host = r.dhost
	r.dog = &fakeSessionHost{}
	r.router = &QuestionRouter{
		Dog:     newWatchdog(r.dog, r.store, ProviderArgs{Kind: "claude"}),
		Deliver: r.loop.Deliver,
		Keys:    r.loop.DeliverKeys,
		Repo:    r.repo,
		Rules:   []string{"  " + dialogRule + " "},
	}
	r.watch = &Watch{Store: r.store, Router: r.router}
	r.loop.Watcher = r.watch
	r.s = &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}}, Agent: dialogAgent}
	r.watch.StepStarted(r.s.Ref, r.s)
	r.loop.setLive(r.s)
	r.dhost.set(dialogAgent, AgentBlocked, dialogScreen+"   \n\n\n")
	return r
}

func (r *dialogRig) serve(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.loop.ServeQuestions(ctx)
}

func (r *dialogRig) raise(t *testing.T, s *Session) {
	t.Helper()
	if !r.loop.Blocked(s) {
		t.Fatal("the blocked agent raised no dialog")
	}
}

func (r *dialogRig) dogPrompts() []string {
	var out []string
	for _, c := range r.dog.Calls() {
		if strings.HasPrefix(c, "SessionHost.Prompt ") {
			out = append(out, c)
		}
	}
	return out
}

func (r *dialogRig) waitDog(t *testing.T, n int) []string {
	t.Helper()
	waitFor(t, func() bool { return len(r.dogPrompts()) >= n })
	return r.dogPrompts()
}

func dialogPrompt(id, step, screen string) string {
	return "SessionHost.Prompt rloop-wd-run-1 " + strconv.Quote("dialog "+id+" from phase-2/"+step+": answer with answer_dialog\n\n"+screen) + " false 0s"
}

func (r *dialogRig) questions() []Question {
	st, _ := r.store.Load("run-1")
	return st.Questions
}

func (r *dialogRig) sent() []string {
	return r.calls("SessionHost.SendKeys ")
}

func (r *dialogRig) screenReads() int {
	return len(r.calls("SessionHost.Screen "))
}

func TestABlockedAgentRaisesOneDialogAndPausesItsStep(t *testing.T) {
	r := newDialogRig(t)
	r.serve(t)

	r.raise(t, r.s)
	r.raise(t, r.s)

	qs := r.questions()
	want := Question{ID: "d1", Kind: QuestionDialog, Step: r.s.Ref.Key, Text: dialogScreen, AskedAt: qs[0].AskedAt}
	if len(qs) != 1 || !reflect.DeepEqual(qs[0], want) {
		t.Fatalf("questions %+v", qs)
	}
	if r.screenReads() != 1 {
		t.Errorf("screen read %d times", r.screenReads())
	}
	if !r.s.OpenQuestion.Load() {
		t.Error("the step's stall clock was not paused")
	}
	if got := r.stepStates("implement"); !reflect.DeepEqual(got, []string{"waiting-input"}) {
		t.Errorf("states %v", got)
	}
	evs := r.events("dialog")
	if len(evs) != 1 || evs[0].Phase != "2" || evs[0].Step != "implement" || !reflect.DeepEqual(evs[0].Fields, map[string]string{"id": "d1", "agent": dialogAgent, "text": dialogScreen}) {
		t.Errorf("dialog events %+v", evs)
	}
	if got := r.waitDog(t, 1); !reflect.DeepEqual(got, []string{dialogPrompt("d1", "implement", dialogScreen)}) {
		t.Errorf("watchdog prompts %q", got)
	}
	time.Sleep(20 * time.Millisecond)
	if got := r.dogPrompts(); len(got) != 1 {
		t.Errorf("watchdog prompted %d times", len(got))
	}
}

func TestAReviewerDialogIsKeyedToTheReviewerAndPausesItsOwner(t *testing.T) {
	r := newDialogRig(t)
	r.serve(t)
	rv := &Session{Ref: r.s.Ref, Agent: "rloop-p2-implement-rv-codex", Reviewer: "codex", owner: r.s}
	r.dhost.set(rv.Agent, AgentBlocked, "Run tests?\n> yes")
	r.dhost.set(dialogAgent, AgentWorking, "")

	r.raise(t, rv)

	qs := r.questions()
	if len(qs) != 1 || qs[0].Step != reviewerKey(r.s.Ref.Key, "codex") || qs[0].Text != "Run tests?\n> yes" {
		t.Fatalf("questions %+v", qs)
	}
	if !r.s.OpenQuestion.Load() {
		t.Error("the owner step was not paused")
	}
	if got := r.waitDog(t, 1); !reflect.DeepEqual(got, []string{dialogPrompt("d1", "implement-rv-codex", "Run tests?\n> yes")}) {
		t.Errorf("watchdog prompts %q", got)
	}
	if d, reason := r.router.AnswerDialog("d1", []string{"esc"}, "decline", ""); d != decisionAuthorised {
		t.Fatalf("answer %s %q", d, reason)
	}
	if got := r.sent(); !reflect.DeepEqual(got, []string{"rloop-p2-implement-rv-codex esc"}) {
		t.Errorf("keys %q", got)
	}
	if r.s.OpenQuestion.Load() {
		t.Error("the owner step stayed paused")
	}
}

func TestLandStageSessionsRaiseNoDialog(t *testing.T) {
	for _, kind := range []string{"gate", "milestone", "gatefix"} {
		t.Run(kind, func(t *testing.T) {
			r := newDialogRig(t)
			r.serve(t)
			s := &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: "2", Kind: kind, Attempt: 1}}, Agent: dialogAgent}
			r.loop.setLive(s)
			rv := &Session{Ref: s.Ref, Agent: dialogAgent + "-rv", Reviewer: "codex", owner: s}
			r.dhost.set(rv.Agent, AgentBlocked, dialogScreen)

			if r.loop.Blocked(s) || r.loop.Blocked(rv) {
				t.Fatal("a land-stage session raised a dialog")
			}
			if r.screenReads() != 0 || len(r.questions()) != 0 || s.OpenQuestion.Load() {
				t.Errorf("screen reads %d, questions %+v", r.screenReads(), r.questions())
			}
		})
	}
}

func TestNoDialogBeforeTheQuestionServerOrForASessionThatIsNotLive(t *testing.T) {
	r := newDialogRig(t)
	if r.loop.Blocked(r.s) {
		t.Fatal("a dialog raised before ServeQuestions")
	}
	r.serve(t)
	other := &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: "2", Kind: "plan", Attempt: 1}}, Agent: dialogAgent}
	if r.loop.Blocked(other) {
		t.Fatal("a dialog raised for a session that is not live")
	}
	r.s.end()
	if r.loop.Blocked(r.s) {
		t.Fatal("a dialog raised for an ended session")
	}
	if len(r.questions()) != 0 || r.s.OpenQuestion.Load() {
		t.Errorf("questions %+v", r.questions())
	}
}

func TestAnEmptyScreenRaisesNoDialog(t *testing.T) {
	r := newDialogRig(t)
	r.serve(t)
	r.dhost.set(dialogAgent, AgentBlocked, "   \n\n  \n")

	if r.loop.Blocked(r.s) {
		t.Fatal("an empty screen raised a dialog")
	}
	if len(r.questions()) != 0 {
		t.Errorf("questions %+v", r.questions())
	}
}

func TestTheDialogScreenKeepsTheLast40LinesAndAtMost4096Bytes(t *testing.T) {
	var lines []string
	for i := 1; i <= 50; i++ {
		lines = append(lines, "line "+strconv.Itoa(i)+"  ")
	}
	got := normaliseScreen(strings.Join(lines, "\n") + "\n\n  \n")
	var want []string
	for i := 11; i <= 50; i++ {
		want = append(want, "line "+strconv.Itoa(i))
	}
	if got != strings.Join(want, "\n") {
		t.Errorf("40 lines:\n%s", got)
	}

	var wide []string
	for i := 0; i < 30; i++ {
		wide = append(wide, strconv.Itoa(i%10)+strings.Repeat("x", 199))
	}
	got = normaliseScreen(strings.Join(wide, "\n"))
	if len(got) > 4096 || !strings.HasSuffix(got, wide[29]) || !strings.HasPrefix(got, wide[10]) {
		t.Errorf("capped screen is %d bytes and starts %q", len(got), got[:10])
	}
	if strings.Count(got, "\n") != 19 {
		t.Errorf("cut inside a line: %d lines", strings.Count(got, "\n")+1)
	}
}

func TestAnAuthorisedAnswerIsRecordedBeforeItsKeysArePressed(t *testing.T) {
	for _, tc := range []struct {
		name, rule, said, by, citation string
		keys                           []string
	}{
		{"decline", "decline", "", "watchdog", "decline", []string{"esc"}},
		{"rule", dialogRule, "", "watchdog", dialogRule, []string{"2", "enter"}},
		{"maintainer", "", "yes, let it write", "maintainer", "", []string{"2", "enter"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newDialogRig(t)
			r.serve(t)
			r.raise(t, r.s)
			r.waitDog(t, 1)

			d, reason := r.router.AnswerDialog("d1", tc.keys, tc.rule, tc.said)

			if d != decisionAuthorised || reason != "" {
				t.Fatalf("answer %s %q", d, reason)
			}
			var want []string
			for _, k := range tc.keys {
				want = append(want, dialogAgent+" "+k)
			}
			if got := r.sent(); !reflect.DeepEqual(got, want) {
				t.Errorf("keys %q", got)
			}
			answer := strings.Join(tc.keys, " ")
			calls := r.shared.Calls()
			keys := indexOf(calls, "SessionHost.SendKeys ")
			answered := indexOf(calls, "Face.Emit dialog-answered")
			appends := 0
			for _, c := range calls[:keys] {
				if c == "Store.Append run-1 question" {
					appends++
				}
			}
			if answered < 0 || answered > keys || appends != 2 {
				t.Errorf("keys at %d, dialog-answered at %d, %d question records before the keys: %q", keys, answered, appends, calls)
			}
			qs := r.questions()
			if len(qs) != 1 || qs[0].Answer != answer || qs[0].AnsweredBy != tc.by || qs[0].Citation != tc.citation || qs[0].Kind != QuestionDialog {
				t.Errorf("questions %+v", qs)
			}
			evs := r.events("dialog-answered")
			if len(evs) != 1 || !reflect.DeepEqual(evs[0].Fields, map[string]string{"id": "d1", "keys": answer, "by": tc.by, "rule": tc.citation}) {
				t.Errorf("dialog-answered %+v", evs)
			}
			human := r.events("human")
			if tc.by == "maintainer" && (len(human) != 1 || human[0].Fields["what"] != "dialog" || human[0].Fields["id"] != "d1") {
				t.Errorf("human %+v", human)
			}
			if tc.by != "maintainer" && len(human) != 0 {
				t.Errorf("human %+v", human)
			}
			if r.s.OpenQuestion.Load() {
				t.Error("the step stayed in waiting-input")
			}
			if got := r.stepStates("implement"); !reflect.DeepEqual(got, []string{"waiting-input", "running"}) {
				t.Errorf("states %v", got)
			}
			if d, _ := r.router.AnswerDialog("d1", []string{"esc"}, "decline", ""); d != decisionRefused {
				t.Errorf("a second answer was %s", d)
			}
		})
	}
}

func TestADeclineWithKeysOtherThanEscIsRefused(t *testing.T) {
	r := newDialogRig(t)
	r.serve(t)
	r.raise(t, r.s)
	r.waitDog(t, 1)

	for _, keys := range [][]string{{"1"}, {"enter"}, {"esc", "enter"}, {"down", "enter"}} {
		if d, reason := r.router.AnswerDialog("d1", keys, "decline", ""); d != decisionRefused || !strings.Contains(reason, "decline presses esc only") {
			t.Errorf("keys %q: %s %q", keys, d, reason)
		}
	}
	if len(r.sent()) != 0 || !routerOpen(r.router, "d1") || !r.s.OpenQuestion.Load() {
		t.Errorf("keys %q, open %t", r.sent(), routerOpen(r.router, "d1"))
	}
	if len(r.events("dialog-answered")) != 0 {
		t.Errorf("answered %+v", r.events("dialog-answered"))
	}
	if d, reason := r.router.AnswerDialog("d1", []string{"esc"}, "decline", ""); d != decisionAuthorised {
		t.Errorf("esc was %s %q", d, reason)
	}
}

func TestKeysStopWhenTheDialogClosesBetweenPresses(t *testing.T) {
	r := newDialogRig(t)
	r.serve(t)
	r.raise(t, r.s)
	r.waitDog(t, 1)
	r.dhost.mu.Lock()
	r.dhost.script = []AgentState{AgentBlocked, AgentBlocked, AgentWorking}
	r.dhost.mu.Unlock()

	d, reason := r.router.AnswerDialog("d1", []string{"1", "down", "enter"}, dialogRule, "")

	if d != decisionRefused || !strings.Contains(reason, "d1") || !strings.Contains(reason, "before enter was pressed") {
		t.Fatalf("answer %s %q", d, reason)
	}
	if got := r.sent(); !reflect.DeepEqual(got, []string{dialogAgent + " 1", dialogAgent + " down"}) {
		t.Errorf("keys %q", got)
	}
	warnings := r.events("warning")
	if len(warnings) != 1 || !strings.Contains(warnings[0].Fields["reason"], "d1") || !strings.Contains(warnings[0].Fields["reason"], "enter") || strings.Contains(warnings[0].Fields["reason"], "down") {
		t.Errorf("warnings %+v", warnings)
	}
	qs := r.questions()
	if len(qs) != 1 || qs[0].Answer != "1 down enter" || qs[0].AnsweredBy != "watchdog" {
		t.Errorf("questions %+v", qs)
	}
	if routerOpen(r.router, "d1") || r.s.OpenQuestion.Load() {
		t.Error("the dialog stayed open")
	}
}

func TestADialogWithNoRuleAndNoMaintainerReplyAsksTheMaintainer(t *testing.T) {
	r := newDialogRig(t)
	r.serve(t)
	r.raise(t, r.s)
	r.waitDog(t, 1)

	d, reason := r.router.AnswerDialog("d1", []string{"1", "enter"}, "", "")

	if d != decisionAsk || !strings.Contains(reason, "maintainer_said") {
		t.Fatalf("answer %s %q", d, reason)
	}
	st, _ := r.store.Load("run-1")
	waiting := 0
	for _, ev := range st.Events {
		if ev.Kind == "watchdog-waiting" {
			waiting++
		}
	}
	if waiting != 1 {
		t.Errorf("watchdog-waiting %d", waiting)
	}
	if len(r.sent()) != 0 || !routerOpen(r.router, "d1") || !r.s.OpenQuestion.Load() {
		t.Errorf("keys %q, open %t", r.sent(), routerOpen(r.router, "d1"))
	}
	if d, reason := r.router.AnswerDialog("d1", []string{"1", "enter"}, "", "yes"); d != decisionAuthorised {
		t.Errorf("the maintainer's reply was %s %q", d, reason)
	}
}

func TestAnUnknownRuleIsRefusedAndTheDialogStaysOpen(t *testing.T) {
	r := newDialogRig(t)
	r.serve(t)
	r.raise(t, r.s)
	r.waitDog(t, 1)

	d, reason := r.router.AnswerDialog("d1", []string{"1"}, "approve anything", "yes")

	if d != decisionRefused || !strings.Contains(reason, "approve anything") {
		t.Fatalf("answer %s %q", d, reason)
	}
	if len(r.sent()) != 0 || !routerOpen(r.router, "d1") {
		t.Errorf("keys %q, open %t", r.sent(), routerOpen(r.router, "d1"))
	}
	if d, reason := r.router.AnswerDialog("d1", []string{"esc"}, "decline", ""); d != decisionAuthorised {
		t.Errorf("decline was %s %q", d, reason)
	}
}

func TestUnattendedRefusesAnAnswerOffTheRules(t *testing.T) {
	r := newDialogRig(t)
	r.router.Dog.Unattended = true
	r.serve(t)
	r.raise(t, r.s)
	r.waitDog(t, 1)

	for _, said := range []string{"", "yes"} {
		d, reason := r.router.AnswerDialog("d1", []string{"1"}, "", said)
		if d != decisionRefused || !strings.Contains(reason, "unattended") || !strings.Contains(reason, "decline") {
			t.Errorf("maintainer_said %q: %s %q", said, d, reason)
		}
	}
	st, _ := r.store.Load("run-1")
	for _, ev := range st.Events {
		if ev.Kind == "watchdog-waiting" {
			t.Errorf("the unattended watchdog was marked waiting")
		}
	}
	if len(r.sent()) != 0 || !routerOpen(r.router, "d1") {
		t.Errorf("keys %q", r.sent())
	}
	if d, reason := r.router.AnswerDialog("d1", []string{"1"}, dialogRule, ""); d != decisionAuthorised {
		t.Errorf("a rule was %s %q", d, reason)
	}
}

func TestEmptyOrBlankKeysAreRefused(t *testing.T) {
	r := newDialogRig(t)
	r.serve(t)
	r.raise(t, r.s)
	r.waitDog(t, 1)

	for _, keys := range [][]string{nil, {" "}, {"enter", ""}} {
		if d, reason := r.router.AnswerDialog("d1", keys, "decline", ""); d != decisionRefused || reason == "" {
			t.Errorf("keys %q: %s %q", keys, d, reason)
		}
	}
	if len(r.sent()) != 0 || !routerOpen(r.router, "d1") {
		t.Errorf("keys %q", r.sent())
	}
}

func TestQuestionAndDialogIDsCannotBeSwapped(t *testing.T) {
	r := newDialogRig(t)
	r.serve(t)
	q := Question{ID: "q1", Step: r.s.Ref.Key, Text: "which db?"}
	r.loop.track(q, r.s, dialogAgent)
	r.loop.openQuestion(r.s, 1)
	if !r.router.Route(context.Background(), q) {
		t.Fatal("the question was not routed")
	}
	r.raise(t, r.s)
	r.waitDog(t, 2)

	if ok, reason := r.router.Answer("d1", "yes", "maintainer"); ok || reason != "d1 is a dialog: answer it with answer_dialog" {
		t.Errorf("answer to d1: %t %q", ok, reason)
	}
	if d, reason := r.router.AnswerDialog("q1", []string{"esc"}, "decline", ""); d != decisionRefused || reason != "dialog q1 is not open" {
		t.Errorf("answer_dialog to q1: %s %q", d, reason)
	}
	if !routerOpen(r.router, "d1") || !routerOpen(r.router, "q1") {
		t.Error("a refused answer closed an entry")
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 0 || len(r.sent()) != 0 {
		t.Errorf("answered %q, keys %q", got, r.sent())
	}
}

func TestNoKeysAreSentWhenThePaneMovedOn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  AgentState
		screen string
		reason string
	}{
		{"screen changed", AgentBlocked, "Allow network access?\n1. Yes", "the screen changed"},
		{"left blocked", AgentWorking, dialogScreen, "not blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newDialogRig(t)
			r.serve(t)
			r.raise(t, r.s)
			r.waitDog(t, 1)
			r.dhost.set(dialogAgent, tc.state, tc.screen)

			d, reason := r.router.AnswerDialog("d1", []string{"esc"}, "decline", "")

			if d != decisionRefused || !strings.Contains(reason, tc.reason) || !strings.Contains(reason, "d1") {
				t.Fatalf("answer %s %q", d, reason)
			}
			if len(r.sent()) != 0 {
				t.Errorf("keys %q", r.sent())
			}
			qs := r.questions()
			if len(qs) != 1 || qs[0].AnsweredBy != "withdrawn" || !strings.Contains(qs[0].Answer, tc.reason) {
				t.Errorf("questions %+v", qs)
			}
			if evs := r.events("dialog-closed"); len(evs) != 1 || evs[0].Fields["id"] != "d1" || !strings.Contains(evs[0].Fields["reason"], tc.reason) {
				t.Errorf("dialog-closed %+v", evs)
			}
			if len(r.events("dialog-answered")) != 0 || r.s.OpenQuestion.Load() {
				t.Errorf("answered %+v, open %t", r.events("dialog-answered"), r.s.OpenQuestion.Load())
			}
		})
	}
}

func TestADialogClosedInThePaneIsWithdrawn(t *testing.T) {
	r := newDialogRig(t)
	r.serve(t)
	r.raise(t, r.s)
	r.waitDog(t, 1)

	r.dhost.set(dialogAgent, AgentWorking, "")
	r.loop.Unblocked(r.s)

	qs := r.questions()
	if len(qs) != 1 || qs[0].AnsweredBy != "withdrawn" || qs[0].Answer != "dialog closed in the pane" {
		t.Fatalf("questions %+v", qs)
	}
	if evs := r.events("dialog-closed"); len(evs) != 1 || !reflect.DeepEqual(evs[0].Fields, map[string]string{"id": "d1", "reason": "dialog closed in the pane"}) {
		t.Errorf("dialog-closed %+v", evs)
	}
	if routerOpen(r.router, "d1") || r.s.OpenQuestion.Load() {
		t.Error("the dialog stayed open")
	}
	if got := r.stepStates("implement"); !reflect.DeepEqual(got, []string{"waiting-input", "running"}) {
		t.Errorf("states %v", got)
	}
	if d, _ := r.router.AnswerDialog("d1", []string{"esc"}, "decline", ""); d != decisionRefused || len(r.sent()) != 0 {
		t.Errorf("a closed dialog was answered: %s", d)
	}
}

func TestAnAnsweredScreenIsNotRaisedAgainAndANewScreenIsD2(t *testing.T) {
	r := newDialogRig(t)
	r.serve(t)
	r.raise(t, r.s)
	r.waitDog(t, 1)
	if d, reason := r.router.AnswerDialog("d1", []string{"esc"}, "decline", ""); d != decisionAuthorised {
		t.Fatalf("answer %s %q", d, reason)
	}

	if r.loop.Blocked(r.s) {
		t.Fatal("the answered screen was raised again")
	}
	r.dhost.set(dialogAgent, AgentBlocked, "Allow network access?\n1. Yes")
	r.raise(t, r.s)

	qs := r.questions()
	if len(qs) != 2 || qs[1].ID != "d2" || qs[1].Text != "Allow network access?\n1. Yes" {
		t.Fatalf("questions %+v", qs)
	}
	if got := r.waitDog(t, 2); got[1] != dialogPrompt("d2", "implement", "Allow network access?\n1. Yes") {
		t.Errorf("watchdog prompts %q", got)
	}
}

func TestAStepThatEndsWithdrawsItsDialogWithoutAnsweringTheAskChannel(t *testing.T) {
	r := newDialogRig(t)
	r.serve(t)
	r.raise(t, r.s)
	r.waitDog(t, 1)

	r.s.end()
	r.loop.withdrawStep(r.s.Ref.Key, StepFailed)

	qs := r.questions()
	if len(qs) != 1 || qs[0].AnsweredBy != "withdrawn" || qs[0].Answer != "step failed" {
		t.Fatalf("questions %+v", qs)
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 0 {
		t.Errorf("answered the ask channel %q", got)
	}
	if routerOpen(r.router, "d1") {
		t.Error("the router kept the dialog open")
	}
	if d, _ := r.router.AnswerDialog("d1", []string{"esc"}, "decline", ""); d != decisionRefused || len(r.sent()) != 0 {
		t.Errorf("an ended step's dialog was answered: %s", d)
	}
}

func TestADialogForAGoneWatchdogHaltsTheRunOnce(t *testing.T) {
	r := newDialogRig(t)
	r.router.Dog.gone = true
	r.serve(t)

	r.raise(t, r.s)
	sig := goneHalt(t, r.eventsRig)

	assertOneHaltAndNeverAnswered(t, r.eventsRig, r.s, sig)
	if got := r.dogPrompts(); len(got) != 0 {
		t.Errorf("prompted a gone watchdog %q", got)
	}
}

func TestDialogIDsContinueAfterAResume(t *testing.T) {
	r := newDialogRig(t)
	key := r.s.Ref.Key
	for _, q := range []Question{
		{ID: "d1", Kind: QuestionDialog, Step: key, Text: "a", Answer: "1", AnsweredBy: "watchdog", Citation: "decline"},
		{ID: "q1", Step: key, Text: "which db?"},
		{ID: "d2", Kind: QuestionDialog, Step: key, Text: "b", Answer: "step failed", AnsweredBy: "withdrawn"},
	} {
		r.store.Append("run-1", Record{Kind: RecordQuestion, Question: &q})
	}
	r.serve(t)

	r.raise(t, r.s)

	var ids []string
	for _, q := range r.questions() {
		ids = append(ids, q.ID)
	}
	if !reflect.DeepEqual(ids, []string{"d1", "q1", "d2", "d3"}) {
		t.Errorf("ids %v", ids)
	}
}

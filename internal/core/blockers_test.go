package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	reviewerAgent  = "rloop-p2-implement-rv-codex"
	reviewerScreen = "Review was interrupted"
)

type blockerRig struct {
	*dialogRig
	rem *Remedies
	rv  *Session
}

func newBlockerRig(t *testing.T) *blockerRig {
	r := &blockerRig{dialogRig: newDialogRig(t)}
	r.loop.BlockerTimeout = time.Hour
	r.watch.Dog = r.router.Dog
	r.router.Dog.Face = r.face
	r.rem = &Remedies{
		Allow: []string{"restart"}, Store: r.store, Face: r.face, Watch: r.watch, MaxRestarts: 2, Dog: r.router.Dog,
		Fallbacks: map[string]Fallback{"implement": {Provider: "claude", Model: "opus", Effort: "high"}},
		Asks:      func(string) bool { return true },
	}
	r.router.Remedies = r.rem
	r.router.Blocker = r.loop.OpenBlocker
	r.router.Settle = r.loop.SettleBlocker
	r.rv = &Session{Ref: r.s.Ref, Agent: reviewerAgent, Reviewer: "codex", owner: r.s}
	r.s.setReviewers([]*Session{r.rv})
	r.dhost.set(reviewerAgent, AgentIdle, reviewerScreen)
	r.dhost.set(dialogAgent, AgentWorking, "")
	return r
}

func reviewerBlocker() Blocker {
	return Blocker{Source: "reviewer", Phase: "2", Step: "implement-rv-codex", Reason: "review in pane: Review was interrupted", Excerpt: reviewerScreen, Actions: []string{"retry", "keys", "switch", "skip", "block", "stop"}}
}

func landBlocker() Blocker {
	return Blocker{Source: "land", Phase: "2", Step: "land", Reason: "merge conflict in a.go", Excerpt: "CONFLICT (content): a.go", Actions: []string{"retry", "block", "stop"}}
}

func (r *blockerRig) hold(t *testing.T, ctx context.Context, b Blocker) <-chan Resolution {
	t.Helper()
	n := len(r.events("blocked-on"))
	ch := make(chan Resolution, 1)
	go func() { ch <- r.loop.Raise(ctx, b) }()
	waitFor(t, func() bool { return len(r.events("blocked-on")) > n })
	return ch
}

func (r *blockerRig) open(t *testing.T, b Blocker) <-chan Resolution {
	t.Helper()
	ch := r.hold(t, context.Background(), b)
	evs := r.events("blocked-on")
	id := evs[len(evs)-1].Fields["id"]
	waitFor(t, func() bool { return routerOpen(r.router, id) })
	return ch
}

func resolved(t *testing.T, ch <-chan Resolution) Resolution {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("the blocker was never resolved")
		return Resolution{}
	}
}

func stillHeld(t *testing.T, ch <-chan Resolution) {
	t.Helper()
	select {
	case res := <-ch:
		t.Fatalf("the blocker resolved %+v", res)
	case <-time.After(30 * time.Millisecond):
	}
}

func (r *blockerRig) resolve(id, action string, opts ...func(*resolveArgs)) (string, string) {
	a := resolveArgs{}
	for _, o := range opts {
		o(&a)
	}
	return r.router.ResolveBlocker(id, action, a.rule, a.addendum, a.keys, a.provider, a.model, a.effort, a.said)
}

type resolveArgs struct {
	rule, addendum, provider, model, effort, said string
	keys                                          []string
}

func said(s string) func(*resolveArgs)     { return func(a *resolveArgs) { a.said = s } }
func addendum(s string) func(*resolveArgs) { return func(a *resolveArgs) { a.addendum = s } }
func keysUnder(rule string, keys ...string) func(*resolveArgs) {
	return func(a *resolveArgs) { a.rule, a.keys = rule, keys }
}
func onProvider(p, m, e string) func(*resolveArgs) {
	return func(a *resolveArgs) { a.provider, a.model, a.effort = p, m, e }
}

func (r *blockerRig) waitingEvents() int {
	return len(r.events("watchdog-waiting"))
}

func TestRaiseRecordsRoutesAndPausesTheReviewersOwner(t *testing.T) {
	r := newBlockerRig(t)

	ch := r.open(t, reviewerBlocker())

	text := "blocker b1 from phase-2/implement-rv-codex (reviewer): review in pane: Review was interrupted\nactions: retry, keys, switch, skip, block, stop; resolve it with resolve_blocker\n\n" + reviewerScreen
	qs := r.questions()
	want := Question{ID: "b1", Kind: QuestionBlocker, Step: reviewerKey(r.s.Ref.Key, "codex"), Text: text, Options: reviewerBlocker().Actions, AskedAt: qs[0].AskedAt}
	if len(qs) != 1 || !reflect.DeepEqual(qs[0], want) {
		t.Fatalf("questions %+v", qs)
	}
	evs := r.events("blocked-on")
	if len(evs) != 1 || evs[0].Phase != "2" || evs[0].Step != "implement-rv-codex" || !reflect.DeepEqual(evs[0].Fields, map[string]string{"id": "b1", "source": "reviewer", "phase": "2", "step": "implement-rv-codex", "reason": "review in pane: Review was interrupted"}) {
		t.Errorf("blocked-on %+v", evs)
	}
	if !r.s.OpenQuestion.Load() {
		t.Error("the owner step was not paused")
	}
	if got := r.waitDog(t, 1); got[0] != "SessionHost.Prompt rloop-wd-run-1 "+strconv.Quote(text)+" false 0s" {
		t.Errorf("watchdog prompts %q", got)
	}

	if d, reason := r.resolve("b1", "block"); d != decisionAuthorised {
		t.Fatalf("block %s %q", d, reason)
	}

	if res := resolved(t, ch); !reflect.DeepEqual(res, Resolution{ID: "b1", Action: "block", By: "watchdog", Citation: "always"}) {
		t.Errorf("resolution %+v", res)
	}
	if q := r.questions()[0]; q.Answer != "block" || q.AnsweredBy != "watchdog" || q.Citation != "always" {
		t.Errorf("question %+v", q)
	}
	if evs := r.events("blocker-resolved"); len(evs) != 1 || !reflect.DeepEqual(evs[0].Fields, map[string]string{"id": "b1", "action": "block", "by": "watchdog", "source": "reviewer", "step": "phase-2/implement-rv-codex"}) {
		t.Errorf("blocker-resolved %+v", evs)
	}
	if got := r.stepStates("implement"); !reflect.DeepEqual(got, []string{"waiting-input", "running"}) {
		t.Errorf("states %v", got)
	}
	if r.s.OpenQuestion.Load() || routerOpen(r.router, "b1") {
		t.Error("the blocker stayed open")
	}
	if d, _ := r.resolve("b1", "stop"); d != decisionRefused {
		t.Errorf("a resolved blocker was resolved again: %s", d)
	}
}

func TestABlockerWithNoLiveStepIsHeldWithoutAPane(t *testing.T) {
	r := newBlockerRig(t)

	ch := r.open(t, landBlocker())

	if q := r.questions()[0]; q.Step != (StepKey{Run: "run-1", Phase: "2", Kind: "land"}) {
		t.Errorf("question %+v", q)
	}
	if r.s.OpenQuestion.Load() {
		t.Error("a land blocker paused the live step")
	}
	if d, reason := r.resolve("b1", "stop"); d != decisionAuthorised {
		t.Fatalf("stop %s %q", d, reason)
	}
	if res := resolved(t, ch); res.Action != "stop" || res.By != "watchdog" {
		t.Errorf("resolution %+v", res)
	}
}

func TestAnActionTheSourceLacksOrAnUnknownIDIsRefused(t *testing.T) {
	r := newBlockerRig(t)
	ch := r.open(t, landBlocker())

	for _, c := range []struct{ id, action, want string }{
		{"b9", "block", "blocker b9 is not open"},
		{"d1", "block", "blocker d1 is not open"},
		{"b1", "skip", "skip is not an action for blocker b1 (land): retry, block or stop"},
		{"b1", "keys", "keys is not an action for blocker b1 (land): retry, block or stop"},
		{"b1", "switch", "switch is not an action for blocker b1 (land): retry, block or stop"},
	} {
		if d, reason := r.resolve(c.id, c.action, said("yes")); d != decisionRefused || reason != c.want {
			t.Errorf("%s %s: %s %q", c.id, c.action, d, reason)
		}
	}
	stillHeld(t, ch)
}

func TestRetryUnderTheAllowListAndBudgetIsAuthorised(t *testing.T) {
	r := newBlockerRig(t)
	ch := r.open(t, reviewerBlocker())

	if d, reason := r.resolve("b1", "retry", addendum("run /review again")); d != decisionAuthorised {
		t.Fatalf("retry %s %q", d, reason)
	}

	if res := resolved(t, ch); !reflect.DeepEqual(res, Resolution{ID: "b1", Action: "retry", By: "watchdog", Citation: "allow-list", Addendum: "run /review again"}) {
		t.Errorf("resolution %+v", res)
	}
	if r.waitingEvents() != 0 {
		t.Error("an allow-listed retry asked the maintainer")
	}
}

func TestRetryOffTheAllowListAsksThenTakesTheMaintainersReply(t *testing.T) {
	r := newBlockerRig(t)
	r.rem.Allow = nil
	ch := r.open(t, reviewerBlocker())

	d, reason := r.resolve("b1", "retry")

	if d != decisionAsk || !strings.Contains(reason, "maintainer_said") {
		t.Fatalf("retry %s %q", d, reason)
	}
	evs := r.events("watchdog-waiting")
	if len(evs) != 1 || evs[0].Fields["question"] != "blocker b1 from phase-2/implement-rv-codex (reviewer): review in pane: Review was interrupted — what now?" || evs[0].Fields["options"] != "retry; skip; switch provider; block this phase; stop the run" {
		t.Fatalf("waiting %+v", evs)
	}
	stillHeld(t, ch)

	if d, reason := r.resolve("b1", "retry", said("yes, retry it")); d != decisionAuthorised {
		t.Fatalf("retry %s %q", d, reason)
	}
	if res := resolved(t, ch); res.Action != "retry" || res.By != "maintainer" || res.Citation != "maintainer" {
		t.Errorf("resolution %+v", res)
	}
	if evs := r.events("human"); len(evs) != 1 || !reflect.DeepEqual(evs[0].Fields, map[string]string{"what": "blocker", "id": "b1"}) {
		t.Errorf("human %+v", evs)
	}
}

func TestRetryAtTheBudgetIsRefusedEvenWithTheMaintainersReply(t *testing.T) {
	r := newBlockerRig(t)
	r.rem.MaxRestarts = 1
	ev := Event{Kind: "blocker-resolved", Fields: map[string]string{"id": "b0", "action": "retry", "by": "watchdog", "source": "reviewer", "step": "phase-2/implement-rv-codex"}}
	r.store.Append("run-1", Record{Kind: RecordEvent, Event: &ev})
	ch := r.open(t, reviewerBlocker())

	for _, s := range []string{"", "retry anyway"} {
		if d, reason := r.resolve("b1", "retry", said(s)); d != decisionRefused || reason != "retry limit 1 reached for phase-2/implement-rv-codex" {
			t.Errorf("retry said %q: %s %q", s, d, reason)
		}
	}
	stillHeld(t, ch)
}

func TestAStepsRestartsCountTowardsItsRetryBudget(t *testing.T) {
	r := newBlockerRig(t)
	r.rem.MaxRestarts = 1
	ev := Event{Kind: "restart", Fields: map[string]string{"step": "phase-2/land", "remedy": "remedy-1"}}
	r.store.Append("run-1", Record{Kind: RecordEvent, Event: &ev})
	r.open(t, landBlocker())

	if d, reason := r.resolve("b1", "retry"); d != decisionRefused || reason != "retry limit 1 reached for phase-2/land" {
		t.Errorf("retry %s %q", d, reason)
	}
}

func TestKeysUnderARuleArePressedInTheBlockersPane(t *testing.T) {
	r := newBlockerRig(t)
	ch := r.open(t, reviewerBlocker())

	if d, reason := r.resolve("b1", "keys", keysUnder(" "+dialogRule, "down", "enter")); d != decisionAuthorised {
		t.Fatalf("keys %s %q", d, reason)
	}

	if res := resolved(t, ch); !reflect.DeepEqual(res, Resolution{ID: "b1", Action: "keys", By: "watchdog", Citation: dialogRule, Keys: []string{"down", "enter"}}) {
		t.Errorf("resolution %+v", res)
	}
	if got := r.sent(); !reflect.DeepEqual(got, []string{reviewerAgent + " down enter"}) {
		t.Errorf("keys %q", got)
	}
	if q := r.questions()[0]; q.Answer != "keys" || q.Citation != dialogRule {
		t.Errorf("question %+v", q)
	}
}

func TestKeysWithNoRuleAskThenTakeTheMaintainersReply(t *testing.T) {
	r := newBlockerRig(t)
	ch := r.open(t, reviewerBlocker())

	if d, _ := r.resolve("b1", "keys", keysUnder("", "enter")); d != decisionAsk {
		t.Fatalf("keys %s", d)
	}
	if d, _ := r.resolve("b1", "keys", keysUnder("an unknown rule", "enter")); d != decisionAsk {
		t.Fatalf("keys under an unknown rule %s", d)
	}
	if len(r.sent()) != 0 {
		t.Fatalf("keys pressed before consent %q", r.sent())
	}
	if d, reason := r.resolve("b1", "keys", keysUnder("", "enter"), said("press enter")); d != decisionAuthorised {
		t.Fatalf("keys %s %q", d, reason)
	}
	if res := resolved(t, ch); res.By != "maintainer" || !reflect.DeepEqual(res.Keys, []string{"enter"}) {
		t.Errorf("resolution %+v", res)
	}
}

func TestKeysAreRefusedWhenThePaneMovedOnOrNoKeysAreGiven(t *testing.T) {
	r := newBlockerRig(t)
	ch := r.open(t, reviewerBlocker())

	if d, _ := r.resolve("b1", "keys", keysUnder(dialogRule)); d != decisionRefused {
		t.Errorf("no keys: %s", d)
	}
	r.dhost.set(reviewerAgent, AgentIdle, "something else")
	if d, reason := r.resolve("b1", "keys", keysUnder(dialogRule, "enter")); d != decisionRefused || reason != "the pane of blocker b1 changed since it was raised; read it again" {
		t.Errorf("moved pane: %s %q", d, reason)
	}
	if len(r.sent()) != 0 {
		t.Errorf("keys pressed %q", r.sent())
	}
	stillHeld(t, ch)
	if d, _ := r.resolve("b1", "block"); d != decisionAuthorised {
		t.Errorf("the refused keys closed the blocker: %s", d)
	}
}

func TestSwitchToTheFallbackIsAuthorisedWithItsModelAndEffort(t *testing.T) {
	r := newBlockerRig(t)
	ch := r.open(t, reviewerBlocker())

	if d, reason := r.resolve("b1", "switch", onProvider("claude", "", "")); d != decisionAuthorised {
		t.Fatalf("switch %s %q", d, reason)
	}

	if res := resolved(t, ch); !reflect.DeepEqual(res, Resolution{ID: "b1", Action: "switch", By: "watchdog", Citation: "fallback", Provider: "claude", Model: "opus", Effort: "high"}) {
		t.Errorf("resolution %+v", res)
	}
}

func TestSwitchElsewhereNeedsTheMaintainerAModelAndAnEffort(t *testing.T) {
	r := newBlockerRig(t)
	ch := r.open(t, reviewerBlocker())

	if d, _ := r.resolve("b1", "switch", onProvider("gemini", "pro", "high")); d != decisionAsk {
		t.Errorf("switch without consent: %s", d)
	}
	if d, reason := r.resolve("b1", "switch", onProvider("gemini", "", ""), said("use gemini")); d != decisionRefused || reason != "switch to gemini needs a model and an effort: it is not the row's fallback" {
		t.Errorf("switch without a model: %s %q", d, reason)
	}
	if d, reason := r.resolve("b1", "switch"); d != decisionRefused || reason != "switch needs a provider" {
		t.Errorf("switch without a provider: %s %q", d, reason)
	}
	r.rem.Asks = func(p string) bool { return p != "plain" }
	if d, reason := r.resolve("b1", "switch", onProvider("plain", "m", "e"), said("yes")); d != decisionRefused || reason != "provider plain has no MCP ask channel" {
		t.Errorf("switch to a provider without ask: %s %q", d, reason)
	}
	stillHeld(t, ch)

	if d, reason := r.resolve("b1", "switch", onProvider("gemini", "pro", "high"), said("use gemini pro")); d != decisionAuthorised {
		t.Fatalf("switch %s %q", d, reason)
	}
	if res := resolved(t, ch); !reflect.DeepEqual(res, Resolution{ID: "b1", Action: "switch", By: "maintainer", Citation: "maintainer", Provider: "gemini", Model: "pro", Effort: "high"}) {
		t.Errorf("resolution %+v", res)
	}
}

func TestSkipNeedsTheMaintainer(t *testing.T) {
	r := newBlockerRig(t)
	ch := r.open(t, reviewerBlocker())

	if d, _ := r.resolve("b1", "skip"); d != decisionAsk {
		t.Errorf("skip without consent: %s", d)
	}
	if d, reason := r.resolve("b1", "skip", said("skip codex this round")); d != decisionAuthorised {
		t.Fatalf("skip %s %q", d, reason)
	}
	if res := resolved(t, ch); res.Action != "skip" || res.By != "maintainer" {
		t.Errorf("resolution %+v", res)
	}
}

func TestUnattendedNeverAsksAndStillBlocksOrStops(t *testing.T) {
	r := newBlockerRig(t)
	r.router.Dog.Unattended = true
	r.rem.Allow = nil
	ch := r.open(t, reviewerBlocker())

	for _, c := range []struct {
		action string
		opts   []func(*resolveArgs)
	}{
		{"retry", nil},
		{"skip", nil},
		{"skip", []func(*resolveArgs){said("skip it")}},
		{"keys", []func(*resolveArgs){keysUnder("", "enter")}},
		{"switch", []func(*resolveArgs){onProvider("gemini", "pro", "high")}},
	} {
		if d, reason := r.resolve("b1", c.action, c.opts...); d != decisionRefused || reason != "unattended: block or stop" {
			t.Errorf("%s: %s %q", c.action, d, reason)
		}
	}
	if r.waitingEvents() != 0 {
		t.Error("an unattended run asked the maintainer")
	}
	if d, _ := r.resolve("b1", "keys", keysUnder(dialogRule, "enter")); d != decisionAuthorised {
		t.Errorf("keys under a rule: %s", d)
	}
	if res := resolved(t, ch); res.Action != "keys" {
		t.Errorf("resolution %+v", res)
	}
}

func TestTheHoldExpiresAsABlock(t *testing.T) {
	r := newBlockerRig(t)
	r.loop.BlockerTimeout = 20 * time.Millisecond

	res := resolved(t, r.hold(t, context.Background(), landBlocker()))

	if !reflect.DeepEqual(res, Resolution{ID: "b1", Action: "block", By: "timeout", Citation: "timeout"}) {
		t.Errorf("resolution %+v", res)
	}
	if q := r.questions()[0]; q.Answer != "block" || q.AnsweredBy != "timeout" {
		t.Errorf("question %+v", q)
	}
	if routerOpen(r.router, "b1") {
		t.Error("the router kept an expired blocker open")
	}
}

func TestAMilestoneBlockerExpiresAsASkip(t *testing.T) {
	r := newBlockerRig(t)
	r.loop.BlockerTimeout = 20 * time.Millisecond

	res := resolved(t, r.hold(t, context.Background(), Blocker{Source: "milestone", Phase: "2", Step: "milestone", Reason: "report failed", Actions: []string{"retry", "skip", "stop"}}))

	if res.Action != "skip" || res.By != "timeout" {
		t.Errorf("resolution %+v", res)
	}
}

func TestTheHoldStopsWhileTheWatchdogAsksTheMaintainer(t *testing.T) {
	r := newBlockerRig(t)
	r.rem.Allow = nil
	r.loop.BlockerTimeout = 250 * time.Millisecond
	ch := r.open(t, landBlocker())

	if d, _ := r.resolve("b1", "retry"); d != decisionAsk {
		t.Fatalf("retry %s", d)
	}
	if !r.router.Dog.Waiting() {
		t.Fatal("asking did not mark the watchdog waiting")
	}
	time.Sleep(500 * time.Millisecond)
	stillHeld(t, ch)

	if err := r.router.Dog.Resume(); err != nil {
		t.Fatal(err)
	}

	if res := resolved(t, ch); res.Action != "block" || res.By != "timeout" {
		t.Errorf("resolution %+v", res)
	}
}

func TestAReviewersBlockerIsWithdrawnWhenItsOwnerEnds(t *testing.T) {
	r := newBlockerRig(t)
	ch := r.open(t, reviewerBlocker())

	r.s.end()
	r.loop.withdrawStep(r.s.Ref.Key, StepFailed)

	if res := resolved(t, ch); !reflect.DeepEqual(res, Resolution{ID: "b1", Action: "block", By: "withdrawn"}) {
		t.Errorf("resolution %+v", res)
	}
	if q := r.questions()[0]; q.Answer != "step failed" || q.AnsweredBy != "withdrawn" {
		t.Errorf("question %+v", q)
	}
	if evs := r.events("blocker-resolved"); len(evs) != 1 || evs[0].Fields["by"] != "withdrawn" || evs[0].Fields["action"] != "block" {
		t.Errorf("blocker-resolved %+v", evs)
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 0 {
		t.Errorf("answered the ask channel %q", got)
	}
	if routerOpen(r.router, "b1") {
		t.Error("the router kept the blocker open")
	}
	if d, _ := r.resolve("b1", "block"); d != decisionRefused {
		t.Errorf("a withdrawn blocker was resolved: %s", d)
	}
}

func TestAnInterruptOrAnAbortWithdrawsTheBlocker(t *testing.T) {
	t.Run("interrupt", func(t *testing.T) {
		r := newBlockerRig(t)
		ctx, cancel := context.WithCancel(context.Background())
		ch := r.hold(t, ctx, landBlocker())

		cancel()

		if res := resolved(t, ch); res.Action != "block" || res.By != "withdrawn" {
			t.Errorf("resolution %+v", res)
		}
		if q := r.questions()[0]; q.Answer != "run stopped" || q.AnsweredBy != "withdrawn" {
			t.Errorf("question %+v", q)
		}
	})
	t.Run("abort", func(t *testing.T) {
		r := newBlockerRig(t)
		ch := r.open(t, landBlocker())

		r.store.MarkAbort("run-1")

		if res := resolved(t, ch); res.Action != "block" || res.By != "withdrawn" {
			t.Errorf("resolution %+v", res)
		}
	})
}

func TestABlockerForAGoneWatchdogHaltsTheRunOnce(t *testing.T) {
	r := newBlockerRig(t)
	r.router.Dog.gone = true
	r.loop.signalsMu.Lock()
	defer r.loop.signalsMu.Unlock()

	res := resolved(t, r.hold(t, context.Background(), reviewerBlocker()))
	sig := goneHalt(t, r.eventsRig)

	if !reflect.DeepEqual(res, Resolution{ID: "b1", Action: "block", By: "withdrawn"}) {
		t.Errorf("resolution %+v", res)
	}
	if q := r.questions()[0]; q.Answer != watchdogGone || q.AnsweredBy != "withdrawn" {
		t.Errorf("question %+v", q)
	}
	if want := (Signal{Seq: 1, Kind: SignalHalt, Source: SourceDriver, Step: r.s.Ref.Key, Reason: watchdogGone, At: sig.At}); sig != want {
		t.Errorf("signal %+v, want %+v", sig, want)
	}
	select {
	case more := <-r.loop.Watcher.Signals():
		t.Errorf("a second signal %+v", more)
	case <-time.After(30 * time.Millisecond):
	}
	if r.s.OpenQuestion.Load() {
		t.Error("the owner stayed paused")
	}
	if got := r.dogPrompts(); len(got) != 0 {
		t.Errorf("prompted a gone watchdog %q", got)
	}
}

func TestBlockerIDsContinueAfterAResume(t *testing.T) {
	r := newBlockerRig(t)
	key := StepKey{Run: "run-1", Phase: "1", Kind: "land"}
	for _, q := range []Question{
		{ID: "b1", Kind: QuestionBlocker, Step: key, Text: "blocker b1", Answer: "block", AnsweredBy: "timeout"},
		{ID: "d1", Kind: QuestionDialog, Step: key, Text: "a"},
		{ID: "b2", Kind: QuestionBlocker, Step: key, Text: "blocker b2", Answer: "run stopped", AnsweredBy: "withdrawn"},
	} {
		r.store.Append("run-1", Record{Kind: RecordQuestion, Question: &q})
	}

	r.open(t, landBlocker())

	var ids []string
	for _, q := range r.questions() {
		ids = append(ids, q.ID)
	}
	if !reflect.DeepEqual(ids, []string{"b1", "d1", "b2", "b3"}) {
		t.Errorf("ids %v", ids)
	}
}

func TestAnsweringABlockerAsAQuestionOrADialogIsRefused(t *testing.T) {
	r := newBlockerRig(t)
	ch := r.open(t, landBlocker())

	if ok, reason := r.router.Answer("b1", "retry", "maintainer"); ok || reason != "b1 is a blocker: resolve it with resolve_blocker" {
		t.Errorf("answer %v %q", ok, reason)
	}
	if d, _ := r.router.AnswerDialog("b1", []string{"enter"}, "decline", ""); d != decisionRefused {
		t.Errorf("answer_dialog %s", d)
	}
	stillHeld(t, ch)
}

func TestRestartStepOnAStepsOpenBlockerResolvesItAsRetry(t *testing.T) {
	r := newBlockerRig(t)
	r.rem.Blockers = r.loop
	key := StepKey{Run: "run-1", Phase: "2", Kind: "plan", Attempt: 1}
	r.watch.StepStarted(StepRef{Key: key}, nil)
	r.watch.StepEnded(StepRef{Key: key}, Outcome{State: StepFailed})
	r.watch.Hold(key)
	ch := r.open(t, Blocker{Source: "step", Phase: "2", Step: "plan", Reason: "failed: no plan file", Actions: []string{"retry", "switch", "block", "stop"}})

	ok, reason := r.rem.Restart("phase-2/plan", "write the plan file", "", "", "", "")

	if !ok {
		t.Fatalf("restart refused: %s", reason)
	}
	if res := resolved(t, ch); !reflect.DeepEqual(res, Resolution{ID: "b1", Action: "retry", By: "watchdog", Citation: "allow-list", Addendum: "write the plan file"}) {
		t.Errorf("resolution %+v", res)
	}
}

func TestBlockerLineIsTheFirstLineAndItsOutcome(t *testing.T) {
	q := Question{ID: "b4", Kind: QuestionBlocker, Text: "blocker b4 from phase-3/gatefix (gatefix): gate still red\nactions: retry, block, stop; resolve it with resolve_blocker\n\nFAIL x"}
	if got := BlockerLine(q); got != "b4 phase-3/gatefix (gatefix): gate still red (open)" {
		t.Errorf("open line %q", got)
	}
	q.Answer, q.AnsweredBy = "switch", "maintainer"
	if got := BlockerLine(q); got != "b4 phase-3/gatefix (gatefix): gate still red → switch (maintainer)" {
		t.Errorf("resolved line %q", got)
	}
}

func TestAMultiLineReasonIsFoldedIntoTheFirstLine(t *testing.T) {
	r := newBlockerRig(t)
	b := landBlocker()
	b.Reason = "merge conflict:\n  a.go\n  b.go"

	r.open(t, b)

	if got := BlockerLine(r.questions()[0]); got != "b1 phase-2/land (land): merge conflict: a.go b.go (open)" {
		t.Errorf("line %q", got)
	}
}

type raised struct {
	b Blocker
	q Question
}

func newSourceRig(t *testing.T, resolve func(b Blocker) Resolution) (*eventsRig, *[]raised) {
	r := newEventsRig(t)
	r.loop.BlockerTimeout = time.Hour
	var mu sync.Mutex
	var seen []raised
	r.watcher.route = func(ctx context.Context, q Question) bool {
		if q.Kind != QuestionBlocker {
			return false
		}
		b, _ := r.loop.OpenBlocker(q.ID)
		mu.Lock()
		seen = append(seen, raised{b, q})
		mu.Unlock()
		res := resolve(b)
		res.ID = q.ID
		if res.By == "" {
			res.By = "watchdog"
		}
		if err := r.loop.SettleBlocker(res); err != nil {
			t.Errorf("settle %s: %v", q.ID, err)
		}
		return true
	}
	return r, &seen
}

func resolveAs(action string) func(Blocker) Resolution {
	return func(Blocker) Resolution { return Resolution{Action: action, Citation: "always"} }
}

func TestAFailedStepRaisesAStepBlockerAndRetryRerunsItWithTheAddendum(t *testing.T) {
	r, seen := newSourceRig(t, func(Blocker) Resolution {
		return Resolution{Action: "retry", Citation: "allow-list", Addendum: "use the fake"}
	})
	r.host.behaviour["rloop-p2-implement"] = "fail"

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if len(*seen) != 1 || !reflect.DeepEqual((*seen)[0].b, Blocker{Source: "step", Phase: "2", Step: "implement", Reason: "tests red", Actions: []string{"retry", "switch", "block", "stop"}}) {
		t.Fatalf("blockers %+v", *seen)
	}
	if got := r.agents(); !reflect.DeepEqual(got, []string{"rloop-p2-plan", "rloop-p2-implement", "rloop-p2-implement-a2"}) {
		t.Errorf("agents %v", got)
	}
	if got := r.prompts.addendums[r.host.Opened[2].Env["R_LOOP_SENTINEL"]]; got != "use the fake" {
		t.Errorf("attempt 2 addendum %q", got)
	}
	restarts := r.events("restart")
	want := map[string]string{"step": "phase-2/implement", "attempt": "2", "addendum": "use the fake", "provider": "", "remedy": "b1", "citation": "allow-list"}
	if len(restarts) != 1 || !reflect.DeepEqual(restarts[0].Fields, want) {
		t.Errorf("restarts %+v", restarts)
	}
	if evs := r.events("blocker-resolved"); len(evs) != 1 || evs[0].Fields["action"] != "retry" || evs[0].Fields["step"] != "phase-2/implement" {
		t.Errorf("blocker-resolved %+v", evs)
	}
}

func TestAStepBlockerSwitchRestartsOnTheGivenProvider(t *testing.T) {
	r, _ := newSourceRig(t, func(Blocker) Resolution {
		return Resolution{Action: "switch", Citation: "fallback", Provider: "claude", Model: "sonnet", Effort: "low"}
	})
	r.host.behaviour["rloop-p2-implement"] = "fail"

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if want := []string{"codex//", "codex//", "claude/sonnet/low"}; !reflect.DeepEqual(r.resolved, want) {
		t.Errorf("resolved %v, want %v", r.resolved, want)
	}
	if restarts := r.events("restart"); len(restarts) != 1 || restarts[0].Fields["provider"] != "claude" || restarts[0].Fields["model"] != "sonnet" {
		t.Errorf("restarts %+v", restarts)
	}
}

func TestAStepBlockerResolvedBlockBlocksThePhaseWithTodaysExitCode(t *testing.T) {
	for _, c := range []struct {
		behaviour string
		code      int
	}{{"fail", 1}, {"stall", 3}} {
		t.Run(c.behaviour, func(t *testing.T) {
			r, seen := newSourceRig(t, resolveAs("block"))
			r.host.behaviour["rloop-p2-implement"] = c.behaviour

			code := r.run(RunOptions{Phases: []string{"2"}})

			if code != c.code {
				t.Fatalf("exit %d, want %d", code, c.code)
			}
			if len(*seen) != 1 || (*seen)[0].b.Source != "step" {
				t.Errorf("blockers %+v", *seen)
			}
			if blocked := r.events("phase-blocked"); len(blocked) != 1 || blocked[0].Fields["phase"] != "2" {
				t.Errorf("blocked %+v", blocked)
			}
			if len(r.events("restart")) != 0 {
				t.Error("a blocked step restarted")
			}
		})
	}
}

func TestAStepBlockerResolvedStopHaltsTheRunWithExit5(t *testing.T) {
	r, _ := newSourceRig(t, resolveAs("stop"))
	r.host.behaviour["rloop-p1-implement"] = "fail"

	code := r.run(RunOptions{})

	if code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	reason := "stopped by the watchdog at b1: tests red"
	runs := r.runRecords()
	if last := runs[len(runs)-1]; last.Run != RunHalted || last.Reason != reason {
		t.Errorf("last run record %+v", last)
	}
	if got := r.calls("Land "); len(got) != 0 {
		t.Errorf("landed %v after a stop", got)
	}
	if blocked := r.events("phase-blocked"); len(blocked) == 0 || blocked[0].Fields["reason"] != reason {
		t.Errorf("blocked %+v", blocked)
	}
	fired := r.notifier.Fired[len(r.notifier.Fired)-1]
	if fired["R_LOOP_STATUS"] != "halted" || fired["R_LOOP_REASON"] != reason {
		t.Errorf("halt hook %v", fired)
	}
}

func TestAStepBlockerExpiresAsABlock(t *testing.T) {
	r, _ := newSourceRig(t, nil)
	r.watcher.route = nil
	r.loop.BlockerTimeout = 20 * time.Millisecond
	r.host.behaviour["rloop-p2-implement"] = "fail"

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	if evs := r.events("blocker-resolved"); len(evs) != 1 || evs[0].Fields["by"] != "timeout" || evs[0].Fields["action"] != "block" {
		t.Errorf("blocker-resolved %+v", evs)
	}
}

func landingOnce(r *eventsRig, errs ...error) *int {
	calls := 0
	r.loop.Lander = landerFunc(func(ctx context.Context, ph Phase) (Landing, error) {
		r.shared.record("Land %s", ph.ID)
		calls++
		if calls <= len(errs) {
			return Landing{}, errs[calls-1]
		}
		l := Landing{Phase: ph.ID, MergeSHA: "merge-" + ph.Title}
		r.store.Append("run-1", Record{Kind: RecordLanding, Landing: &l})
		return l, nil
	})
	return &calls
}

func TestAMergeConflictRaisesALandBlockerAndRetryLandsAfterTheFix(t *testing.T) {
	r, seen := newSourceRig(t, resolveAs("retry"))
	calls := landingOnce(r, fmt.Errorf("%w: a.go", ErrMergeConflict))

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if *calls != 2 {
		t.Errorf("Land called %d times", *calls)
	}
	want := Blocker{Source: "land", Phase: "2", Step: "land", Reason: ErrMergeConflict.Error() + ": a.go", Actions: []string{"retry", "block", "stop"}}
	if len(*seen) != 1 || !reflect.DeepEqual((*seen)[0].b, want) {
		t.Fatalf("blockers %+v", *seen)
	}
	if (*seen)[0].q.Step != (StepKey{Run: "run-1", Phase: "2", Kind: "land"}) {
		t.Errorf("question step %+v", (*seen)[0].q.Step)
	}
	if landed := r.events("landed"); len(landed) != 1 {
		t.Errorf("landed %+v", landed)
	}
}

func TestARedGateRaisesALandBlockerWithItsOutputAndBlockGivesExit1(t *testing.T) {
	r, seen := newSourceRig(t, resolveAs("block"))
	landingOnce(r, fmt.Errorf("%w: go test ./... exited 1\nFAIL x_test.go", ErrGate), fmt.Errorf("%w: again", ErrGate))

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	if len(*seen) != 1 || (*seen)[0].b.Reason != "gate failed: go test ./... exited 1" || (*seen)[0].b.Excerpt != "FAIL x_test.go" {
		t.Fatalf("blockers %+v", *seen)
	}
	if !strings.HasSuffix((*seen)[0].q.Text, "\n\nFAIL x_test.go") {
		t.Errorf("routed text %q", (*seen)[0].q.Text)
	}
	if blocked := r.events("phase-blocked"); len(blocked) != 1 || blocked[0].Fields["reason"] != "land: gate failed: go test ./... exited 1\nFAIL x_test.go" {
		t.Errorf("blocked %+v", blocked)
	}
}

func TestALandBlockerStopHaltsTheRunWithExit5(t *testing.T) {
	r, _ := newSourceRig(t, resolveAs("stop"))
	landingOnce(r, errors.New("primary tree is not clean: a.go"))

	code := r.run(RunOptions{})

	if code != 5 {
		t.Fatalf("exit %d", code)
	}
	runs := r.runRecords()
	if last := runs[len(runs)-1]; last.Reason != "stopped by the watchdog at b1: primary tree is not clean: a.go" {
		t.Errorf("last run record %+v", last)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("Land calls %v, want only phase 1", got)
	}
}

func TestAGateProbeErrorRaisesAGateProbeBlockerAndRetryProbesAgain(t *testing.T) {
	r, seen := newSourceRig(t, resolveAs("retry"))
	calls := landingOnce(r, &probeError{fmt.Errorf("%w: gate.md names no command in backticks", ErrNoGate)})

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 0 || *calls != 2 {
		t.Fatalf("exit %d, Land calls %d", code, *calls)
	}
	if len(*seen) != 1 || (*seen)[0].b.Source != "gate-probe" || (*seen)[0].b.Step != "gate" || !reflect.DeepEqual((*seen)[0].b.Actions, []string{"retry", "block", "stop"}) {
		t.Fatalf("blockers %+v", *seen)
	}
}

func TestALandErrorBlocksAtOnceWithNoBlockerTimeout(t *testing.T) {
	r, seen := newSourceRig(t, resolveAs("retry"))
	r.loop.BlockerTimeout = 0
	landingOnce(r, errors.New("gate red"))

	if code := r.run(RunOptions{Phases: []string{"2"}}); code != 1 || len(*seen) != 0 {
		t.Fatalf("exit %d, blockers %+v", code, *seen)
	}
}

func TestAResolvedGatefixBlockerIsNotRaisedAgainAsALandBlocker(t *testing.T) {
	r, seen := newSourceRig(t, resolveAs("retry"))
	landingOnce(r, &resolvedError{res: Resolution{ID: "b1", Action: "block"}, err: fmt.Errorf("%w: gate-fix round 1 ended failed: x", ErrGate)})

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 1 || len(*seen) != 0 {
		t.Fatalf("exit %d, blockers %+v", code, *seen)
	}
}

func TestAHaltForTheHeldPhaseWithdrawsItsLandBlockerWithExit5(t *testing.T) {
	r, _ := newSourceRig(t, nil)
	r.watcher.route = func(ctx context.Context, q Question) bool {
		go func() {
			r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: "2", Kind: "land"}, Reason: "wrong turn"}
		}()
		return true
	}
	landingOnce(r, errors.New("gate red"))

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 5 {
		t.Fatalf("exit %d", code)
	}
	if blocked := r.events("phase-blocked"); len(blocked) != 1 || blocked[0].Fields["reason"] != "watchdog: wrong turn" {
		t.Errorf("blocked %+v", blocked)
	}
	if q := r.questionsOf(t); len(q) != 1 || q[0].AnsweredBy != "withdrawn" || q[0].Answer != "watchdog: wrong turn" {
		t.Errorf("questions %+v", q)
	}
}

func (r *eventsRig) questionsOf(t *testing.T) []Question {
	st, err := r.store.Load("run-1")
	if err != nil {
		t.Fatal(err)
	}
	return st.Questions
}

func TestTheQuestionInvariantHaltIsPostedToTheWatchdog(t *testing.T) {
	r := newEventsRig(t)
	r.loop.BlockerTimeout = time.Minute
	r.loop.Runners = map[string]StepRunner{"plan-file": backstopRunner{}}

	r.run(RunOptions{})

	if got := r.calls("Watcher.Post "); !reflect.DeepEqual(got, []string{"run halting: " + invariantQuestion}) {
		t.Errorf("posts %q", got)
	}
}

func TestARecordHaltIsPostedToTheWatchdogBeforeTheRunHalts(t *testing.T) {
	r := newEventsRig(t)
	guard := &RecordGuard{Store: failingStore{Store: r.store, fail: func(Record) bool { return true }}}
	r.loop.Store, r.loop.Sessions.Store = guard, guard

	if code := r.run(RunOptions{}); code != 2 {
		t.Fatalf("exit %d", code)
	}
	calls := r.shared.Calls()
	post := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "Watcher.Post run halting: record: ") })
	hook := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "Notifier.Fire halt-hook") })
	if post < 0 || hook < 0 || post > hook {
		t.Errorf("post %d, halt hook %d in %q", post, hook, calls)
	}
}

func TestALandPanicIsPostedToTheWatchdog(t *testing.T) {
	r := newEventsRig(t)
	r.loop.Lander = landerFunc(func(context.Context, Phase) (Landing, error) { panic("boom") })

	r.run(RunOptions{Phases: []string{"2"}})

	if got := r.calls("Watcher.Post "); len(got) != 1 || !strings.HasPrefix(got[0], "run halting: panic in land: boom") {
		t.Errorf("posts %q", got)
	}
}

func stepBlocker() Blocker {
	return Blocker{Source: "step", Phase: "2", Step: "implement", Reason: "tests red", Actions: []string{"retry", "switch", "block", "stop"}}
}

func gatefixBlocker() Blocker {
	return Blocker{Source: "gatefix", Phase: "2", Step: "gatefix", Reason: "gate failed: gate-fix round 1 ended failed: x", Actions: []string{"retry", "switch", "block", "stop"}}
}

func TestSwitchAtTheBudgetIsRefusedEvenWithTheMaintainersReply(t *testing.T) {
	for _, b := range []Blocker{stepBlocker(), reviewerBlocker(), gatefixBlocker()} {
		t.Run(b.Source, func(t *testing.T) {
			r := newBlockerRig(t)
			r.rem.MaxRestarts = 1
			target := "phase-2/" + b.Step
			ev := Event{Kind: "blocker-resolved", Fields: map[string]string{"id": "b0", "action": "switch", "by": "watchdog", "source": b.Source, "step": target}}
			r.store.Append("run-1", Record{Kind: RecordEvent, Event: &ev})
			ch := r.open(t, b)

			want := "switch limit 1 reached for " + target
			if d, reason := r.resolve("b1", "switch", onProvider("claude", "", "")); d != decisionRefused || reason != want {
				t.Errorf("switch to the fallback: %s %q", d, reason)
			}
			if d, reason := r.resolve("b1", "switch", onProvider("gemini", "pro", "high"), said("use gemini pro")); d != decisionRefused || reason != want {
				t.Errorf("switch with the maintainer: %s %q", d, reason)
			}
			stillHeld(t, ch)
		})
	}
}

func TestSwitchesCountTowardsTheRetryBudget(t *testing.T) {
	r := newBlockerRig(t)
	r.rem.MaxRestarts = 2
	ch := r.open(t, stepBlocker())
	if d, reason := r.resolve("b1", "switch", onProvider("claude", "", "")); d != decisionAuthorised {
		t.Fatalf("switch %s %q", d, reason)
	}
	resolved(t, ch)
	ch = r.open(t, stepBlocker())
	if d, reason := r.resolve("b2", "retry"); d != decisionAuthorised {
		t.Fatalf("retry %s %q", d, reason)
	}
	resolved(t, ch)
	ch = r.open(t, stepBlocker())

	if d, reason := r.resolve("b3", "retry"); d != decisionRefused || reason != "retry limit 2 reached for phase-2/implement" {
		t.Errorf("retry past the budget: %s %q", d, reason)
	}
	stillHeld(t, ch)
}

type waitingWatcher struct{ *fakeWatcher }

func (waitingWatcher) Waiting() bool { return true }

func haltOnBlocker(r *eventsRig) {
	r.watcher.route = func(ctx context.Context, q Question) bool {
		go func() {
			r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: q.Step.Phase, Kind: q.Step.Kind}, Reason: "wrong turn"}
		}()
		return true
	}
}

func runWithin(t *testing.T, r *eventsRig, opts RunOptions) int {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- r.run(opts) }()
	select {
	case code := <-done:
		return code
	case <-time.After(5 * time.Second):
		t.Fatal("the run hung behind an open blocker")
		return 0
	}
}

func eachWatchdogState(t *testing.T, test func(t *testing.T, r *eventsRig)) {
	for _, waiting := range []bool{false, true} {
		t.Run(fmt.Sprintf("waiting=%v", waiting), func(t *testing.T) {
			r, _ := newSourceRig(t, nil)
			haltOnBlocker(r)
			if waiting {
				r.loop.Watcher = waitingWatcher{r.watcher}
			}
			test(t, r)
		})
	}
}

func assertHaltWithdrew(t *testing.T, r *eventsRig, source string) {
	t.Helper()
	q := r.questionsOf(t)
	if len(q) != 1 || q[0].AnsweredBy != "withdrawn" {
		t.Fatalf("questions %+v", q)
	}
	if evs := r.events("blocker-resolved"); len(evs) != 1 || evs[0].Fields["source"] != source || evs[0].Fields["by"] != "withdrawn" {
		t.Errorf("blocker-resolved %+v", evs)
	}
}

func TestAHaltForThePhaseWithdrawsItsGatefixBlockerWithExit5(t *testing.T) {
	eachWatchdogState(t, func(t *testing.T, r *eventsRig) {
		r.loop.Lander = landerFunc(func(ctx context.Context, ph Phase) (Landing, error) {
			ferr := fmt.Errorf("%w: gate-fix round 1 ended failed: x", ErrGate)
			res, _ := r.loop.TryRaise(ctx, Blocker{Source: "gatefix", Phase: ph.ID, Step: "gatefix", Reason: ferr.Error(), Actions: stepActions})
			return Landing{}, &resolvedError{res: res, err: ferr}
		})

		code := runWithin(t, r, RunOptions{Phases: []string{"2"}})

		if code != 5 {
			t.Fatalf("exit %d, want 5", code)
		}
		if blocked := r.events("phase-blocked"); len(blocked) != 1 || blocked[0].Fields["phase"] != "2" || blocked[0].Fields["reason"] != "watchdog: wrong turn" {
			t.Errorf("blocked %+v", blocked)
		}
		assertHaltWithdrew(t, r, "gatefix")
		if q := r.questionsOf(t); q[0].Answer != "watchdog: wrong turn" {
			t.Errorf("answer %q", q[0].Answer)
		}
	})
}

func TestAHaltForThePhaseWithdrawsItsMilestoneBlockerAndStopsTheRun(t *testing.T) {
	eachWatchdogState(t, func(t *testing.T, r *eventsRig) {
		r.loop.Lander = landerFunc(func(ctx context.Context, ph Phase) (Landing, error) {
			r.shared.record("Land %s", ph.ID)
			l := Landing{Phase: ph.ID, MergeSHA: "merge-" + ph.Title}
			r.loop.Store.Append("run-1", Record{Kind: RecordLanding, Landing: &l})
			if ph.ID == "2" {
				r.loop.TryRaise(ctx, Blocker{Source: "milestone", Phase: ph.ID, Step: "milestone", Reason: "report failed", Actions: milestoneActions})
			}
			return l, nil
		})

		code := runWithin(t, r, RunOptions{})

		if code != 5 {
			t.Fatalf("exit %d, want 5", code)
		}
		if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"1", "2"}) {
			t.Errorf("Land calls %v, want the run to stop after phase 2", got)
		}
		runs := r.runRecords()
		if last := runs[len(runs)-1]; last.Run != RunHalted || last.Reason != "watchdog: wrong turn" {
			t.Errorf("last run record %+v", last)
		}
		assertHaltWithdrew(t, r, "milestone")
	})
}

type reviewerBlockerRunner struct{}

func (reviewerBlockerRunner) Run(ctx context.Context, ref StepRef, obs Observer) Outcome {
	b := Blocker{Source: "reviewer", Phase: ref.Key.Phase, Step: ref.Key.Kind + "-rv-codex", Reason: "review in pane: Review was interrupted", Actions: reviewerActions}
	res := obs.(blockerRaiser).raise(ctx, b)
	return Outcome{State: StepFailed, Reason: "reviewer " + res.Action}
}

func TestAHaltForThePhaseWithdrawsAReviewersBlockerThroughItsStep(t *testing.T) {
	eachWatchdogState(t, func(t *testing.T, r *eventsRig) {
		r.loop.Runners["diff"] = reviewerBlockerRunner{}

		code := runWithin(t, r, RunOptions{Phases: []string{"2"}})

		if code != 5 {
			t.Fatalf("exit %d, want 5", code)
		}
		if blocked := r.events("phase-blocked"); len(blocked) != 1 || blocked[0].Fields["phase"] != "2" || blocked[0].Fields["reason"] != "watchdog: wrong turn" {
			t.Errorf("blocked %+v", blocked)
		}
		assertHaltWithdrew(t, r, "reviewer")
		if got := r.calls("Land "); len(got) != 0 {
			t.Errorf("landed %v after a halt", got)
		}
	})
}

func failReviewerInLoop(r *eventsRig) {
	kind := r.loop.Kinds[1]
	kind.Row.Reviewers, kind.Row.Rounds = []Reviewer{{Provider: "codex"}}, 1
	r.loop.Kinds[1] = kind
	r.loop.Runners = map[string]StepRunner{kind.Check: singleRunner{sm: r.loop.Sessions, review: func(ctx context.Context, ref StepRef, s *Session, obs Observer) Outcome {
		h := ReviewHalf{Sessions: r.loop.Sessions, Store: r.loop.Sessions.Store, obs: obs}
		run := &reviewerRun{rv: Reviewer{Provider: "codex"}, s: &Session{Reviewer: "codex"}, fail: &reviewerFail{reason: "reviewer codex: review did not start within 2m0s", pane: true}}
		return h.recover(ctx, s, []*reviewerRun{run}, 0, reviewRound{n: 1})
	}}}
}

func TestAReviewerBlockerResolvedBlockBlocksThePhaseWithoutAStepBlocker(t *testing.T) {
	r, seen := newSourceRig(t, resolveAs("block"))
	failReviewerInLoop(r)

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if len(*seen) != 1 || (*seen)[0].b.Source != "reviewer" {
		t.Fatalf("blockers %+v", *seen)
	}
	if evs := r.events("blocked-on"); len(evs) != 1 {
		t.Errorf("blocked-on %+v", evs)
	}
	if blocked := r.events("phase-blocked"); len(blocked) != 1 || blocked[0].Fields["phase"] != "2" {
		t.Errorf("blocked %+v", blocked)
	}
	if len(r.events("restart")) != 0 {
		t.Error("a blocked step restarted")
	}
}

func TestAReviewerBlockerResolvedStopHaltsWithoutAStepBlocker(t *testing.T) {
	r, seen := newSourceRig(t, resolveAs("stop"))
	failReviewerInLoop(r)

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	if len(*seen) != 1 || (*seen)[0].b.Source != "reviewer" {
		t.Fatalf("blockers %+v", *seen)
	}
	if evs := r.events("blocked-on"); len(evs) != 1 {
		t.Errorf("blocked-on %+v", evs)
	}
}

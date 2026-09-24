package core

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

var remedyT0 = time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

func newRemedies(w *Watch, store Store, allow ...string) *Remedies {
	asks := func(provider string) bool { return provider != "plainbot" }
	return &Remedies{Allow: allow, Store: store, Now: func() time.Time { return remedyT0 }, Watch: w, MaxRestarts: 2, Asks: asks}
}

func failedImplement(t *testing.T, store Store) (*Watch, StepKey) {
	t.Helper()
	key := StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}
	w := &Watch{Store: store, Now: func() time.Time { return remedyT0 }}
	w.StepStarted(StepRef{Key: key}, nil)
	w.StepEnded(StepRef{Key: key}, Outcome{State: StepFailed, Reason: "backstop"})
	w.Hold(key)
	return w, key
}

func takeRestart(w *Watch) <-chan Restart {
	got := make(chan Restart, 1)
	go func() {
		rs := <-w.Restarts()
		if rs.Reply != nil {
			rs.Reply <- ""
			rs.Reply = nil
		}
		got <- rs
	}()
	return got
}

func remedyRecords(store *fakeStore) []Remedy {
	var out []Remedy
	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordRemedy {
			out = append(out, *rec.Remedy)
		}
	}
	return out
}

func TestAnAllowListedClassIsAuthorisedWithoutTheMaintainer(t *testing.T) {
	store := &fakeStore{}
	w, key := failedImplement(t, store)
	rem := newRemedies(w, store, "locks")

	got, _ := rem.Propose("locks", "rm -f .git/index.lock", "a stale git lock", "")

	if got != "authorised" {
		t.Fatalf("decision %q", got)
	}
	want := []Remedy{{ID: "remedy-1", Step: key, Class: "locks", Command: "rm -f .git/index.lock", Why: "a stale git lock", Consent: "allow-list", ProposedAt: remedyT0, DecidedAt: remedyT0}}
	if got := remedyRecords(store); !reflect.DeepEqual(got, want) {
		t.Errorf("records\n got %+v\nwant %+v", got, want)
	}
}

func TestAnUnknownClassIsRefusedNamingItAndNothingIsRecorded(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, "locks")

	got, reason := rem.Propose("git", "git reset --hard", "tree is dirty", "")

	if got != "refused" || !strings.Contains(reason, `"git"`) {
		t.Errorf("decision %q reason %q", got, reason)
	}
	if recs := remedyRecords(store); len(recs) != 0 {
		t.Errorf("records %+v", recs)
	}
}

func TestAProposalWithNoHeldOrLiveStepIsRefused(t *testing.T) {
	store := &fakeStore{}
	rem := newRemedies(&Watch{Store: store}, store, "locks")

	if got, reason := rem.Propose("locks", "rm .lock", "stale", ""); got != "refused" || reason != "no step to remedy" {
		t.Errorf("decision %q reason %q", got, reason)
	}
}

func TestAProposalAttachesToTheLiveStepWhenNothingIsHeld(t *testing.T) {
	store := &fakeStore{}
	key := StepKey{Run: "run-1", Phase: "3", Kind: "plan", Attempt: 1}
	w := &Watch{Store: store}
	w.StepStarted(StepRef{Key: key}, nil)
	defer w.StepEnded(StepRef{Key: key}, Outcome{State: StepOK})
	rem := newRemedies(w, store, "ports")

	rem.Propose("ports", "fuser -k 5432/tcp", "port held", "")

	if recs := remedyRecords(store); len(recs) != 1 || recs[0].Step != key {
		t.Errorf("records %+v", recs)
	}
}

func TestTheRecordHoldsTheCommandVerbatimAndIsAppendedBeforeTheDecisionReturns(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store)
	cmd := "  pkill -f 'vite --port 5173' && rm -rf node_modules/.vite  "

	rem.Propose("ports", cmd, "vite still holds the port", "yes, go ahead")
	seen := remedyRecords(store)

	if len(seen) != 1 || seen[0].Command != cmd || seen[0].Why != "vite still holds the port" || seen[0].Class != "ports" {
		t.Errorf("record %+v", seen)
	}
	if seen[0].ProposedAt.IsZero() || seen[0].DecidedAt.IsZero() {
		t.Errorf("times %+v", seen[0])
	}
}

func TestARestartAfterAnAuthorisedRestartRemedyIsQueuedForTheHeldStep(t *testing.T) {
	store := &fakeStore{}
	w, key := failedImplement(t, store)
	rem := newRemedies(w, store)
	rem.Propose("restart", "herdr pane close stuck", "the agent froze", "yes, go ahead")

	got := takeRestart(w)

	ok, reason := rem.Restart("phase-2/implement", "use the fake", "", "", "", "")

	if !ok || reason != "" {
		t.Fatalf("restart %v %q", ok, reason)
	}
	if want := (Restart{Step: key, Addendum: "use the fake", Remedy: "remedy-1"}); <-got != want {
		t.Errorf("restart, want %+v", want)
	}
}

func TestARestartTheLoopRefusesIsNotAcceptedWithTheLoopsReason(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, "restart")
	go func() {
		rs := <-w.Restarts()
		rs.Reply <- "run halted: watchdog: wrong turn"
	}()
	ok, reason := rem.Restart("phase-2/implement", "", "", "", "", "")
	if ok || reason != "run halted: watchdog: wrong turn" {
		t.Errorf("restart %t %q", ok, reason)
	}
}

func TestARestartTheLoopRefusesAtItsRestartLimitIsNotAccepted(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	r.loop.MaxRestarts = 0
	r.host.behaviour["rloop-p2-implement"] = "fail"
	w := &Watch{Store: r.store}
	r.loop.Watcher = w
	rem := newRemedies(w, r.store, "restart")
	decided := make(chan struct {
		ok     bool
		reason string
	}, 1)
	go func() {
		for {
			if key, ok := w.holding(); ok && key.Kind == "implement" {
				break
			}
			time.Sleep(time.Millisecond)
		}
		ok, reason := rem.Restart("phase-2/implement", "again", "", "", "", "")
		decided <- struct {
			ok     bool
			reason string
		}{ok, reason}
	}()
	code := r.run(RunOptions{Phases: []string{"2"}})
	got := <-decided
	if got.ok || got.reason != "restart limit 0 reached" {
		t.Errorf("restart %+v", got)
	}
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	if agents := r.agents(); !reflect.DeepEqual(agents, []string{"rloop-p2-plan", "rloop-p2-implement"}) {
		t.Errorf("agents %v", agents)
	}
	if len(r.events("restart")) != 0 || len(r.events("restart-refused")) != 1 {
		t.Errorf("restart events %v, refused %v", r.events("restart"), r.events("restart-refused"))
	}
}

func TestARestartTheLoopNeverTakesBeforeTheWindowClosesIsNotAccepted(t *testing.T) {
	store := &fakeStore{}
	w, key := failedImplement(t, store)
	rem := newRemedies(w, store, "restart")
	go func() {
		time.Sleep(20 * time.Millisecond)
		w.Release(key)
	}()

	ok, reason := rem.Restart("phase-2/implement", "", "", "", "", "")

	if ok || reason != "run halted" {
		t.Errorf("restart %v %q", ok, reason)
	}
	select {
	case rs := <-w.Restarts():
		t.Errorf("stale restart left queued %+v", rs)
	default:
	}
}

type probeHost struct {
	fakeSessionHost
	watch *Watch
	held  chan bool
}

func (h *probeHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	if strings.HasPrefix(text, "step ended phase-2/implement failed") {
		_, ok := h.watch.holding()
		h.held <- ok
	}
	return nil
}

func TestTheRemedyWindowIsOpenWhenTheWatchdogHearsStepEnded(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = 20 * time.Millisecond
	r.host.behaviour["rloop-p2-implement"] = "fail"
	w := &Watch{Store: r.store}
	host := &probeHost{watch: w, held: make(chan bool, 4)}
	w.Dog = &Watchdog{Host: host, Store: r.store, RunID: "run-1"}
	r.loop.Watcher = w

	r.run(RunOptions{Phases: []string{"2"}})

	select {
	case held := <-host.held:
		if !held {
			t.Error("the watchdog heard step ended before the remedy window opened")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the watchdog never heard step ended")
	}
	if _, ok := w.holding(); ok {
		t.Error("the window is still held after the run")
	}
}

func TestARetryNeedsAnAddendumAndAProviderRemedyNeedsAProvider(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, "retry", "provider")
	rem.Fallbacks = map[string]Fallback{"implement": {Provider: "claude"}}
	rem.Propose("retry", "none", "flaky", "")
	rem.Propose("provider", "none", "codex is down", "")

	if ok, reason := rem.Restart("phase-2/implement", "", "", "", "", ""); ok || reason != "no authorised remedy" {
		t.Errorf("bare restart %v %q", ok, reason)
	}
	takeRestart(w)
	if ok, reason := rem.Restart("phase-2/implement", "", "claude", "", "", ""); !ok {
		t.Errorf("provider restart %v %q", ok, reason)
	}
}

func TestARestartOfAnOkStepIsRefused(t *testing.T) {
	store := &fakeStore{}
	key := StepKey{Run: "run-1", Phase: "1", Kind: "plan", Attempt: 1}
	w := &Watch{Store: store}
	w.StepStarted(StepRef{Key: key}, nil)
	w.StepEnded(StepRef{Key: key}, Outcome{State: StepOK})
	rem := newRemedies(w, store, "restart")

	ok, reason := rem.Restart("phase-1/plan", "", "", "", "", "")

	if ok || reason != "step is ok" {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestARestartWithNoOpenRemedyWindowIsRefusedAsRunHalted(t *testing.T) {
	store := &fakeStore{}
	w, key := failedImplement(t, store)
	w.Release(key)
	rem := newRemedies(w, store, "restart")

	ok, reason := rem.Restart("phase-2/implement", "", "", "", "", "")

	if ok || reason != "run halted" {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestARefusedRemedyLeadsToNoRestart(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store)
	rem.Propose("restart", "herdr pane close stuck", "the agent froze", "")

	ok, reason := rem.Restart("phase-2/implement", "", "", "", "", "")

	if ok || reason != "no authorised remedy" {
		t.Errorf("restart %v %q", ok, reason)
	}
	select {
	case rs := <-w.Restarts():
		t.Errorf("restart queued %+v", rs)
	default:
	}
}

func TestARestartPastMaxRestartsIsRefused(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	for range 2 {
		store.Append("run-1", Record{Kind: RecordEvent, Event: &Event{Kind: "restart", Fields: map[string]string{"step": "phase-2/implement"}}})
	}
	rem := newRemedies(w, store, "restart")

	ok, reason := rem.Restart("phase-2/implement", "", "", "", "", "")

	if ok || reason != "restart limit 2 reached" {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestAnAuthorisedRemedyIsSpentByOneRestart(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store)
	rem.Propose("restart", "herdr pane close stuck", "froze", "yes, go ahead")
	store.Append("run-1", Record{Kind: RecordEvent, Event: &Event{Kind: "restart", Fields: map[string]string{"step": "phase-2/implement", "remedy": "remedy-1"}}})

	if ok, reason := rem.Restart("phase-2/implement", "", "", "", "", ""); ok || reason != "no authorised remedy" {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestAnAuthorisedRestartRerunsTheStepAsANewAttemptWithTheAddendum(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	r.host.behaviour["rloop-p2-implement"] = "fail"
	w := &Watch{Store: r.store}
	r.loop.Watcher = w
	rem := newRemedies(w, r.store)
	decided := make(chan string, 2)
	go func() {
		for {
			if key, ok := w.holding(); ok && key.Kind == "implement" {
				break
			}
			time.Sleep(time.Millisecond)
		}
		decided <- decisionOf(rem.Propose("restart", "herdr pane close stuck", "froze", "yes, go ahead"))
		_, reason := rem.Restart("phase-2/implement", "use the fake", "", "", "", "")
		decided <- reason
	}()

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if d, reason := <-decided, <-decided; d != "authorised" || reason != "" {
		t.Fatalf("decision %q restart %q", d, reason)
	}
	want := []string{"rloop-p2-plan", "rloop-p2-implement", "rloop-p2-implement-a2"}
	if got := r.agents(); !reflect.DeepEqual(got, want) {
		t.Errorf("spawned %v, want %v", got, want)
	}
	if got := r.prompts.addendums[r.host.Opened[2].Env["R_LOOP_SENTINEL"]]; got != "use the fake" {
		t.Errorf("attempt 2 addendum %q", got)
	}
	restarts := r.events("restart")
	wantFields := map[string]string{"step": "phase-2/implement", "attempt": "2", "addendum": "use the fake", "provider": "", "remedy": "remedy-1"}
	if len(restarts) != 1 || !reflect.DeepEqual(restarts[0].Fields, wantFields) {
		t.Errorf("restart events %+v", restarts)
	}
	rep := r.report(t)
	for _, line := range []string{
		"- phase 2 implement: restart as attempt 2 — use the fake (remedy: herdr pane close stuck)\n",
		"- phase 2 implement: restart `herdr pane close stuck` — maintainer; restart followed\n",
	} {
		if !strings.Contains(rep, line) {
			t.Errorf("report missing %q:\n%s", line, rep)
		}
	}
}

func decisionOf(decision, _ string) string { return decision }

type gateLander struct {
	store  *loopStore
	calls  int
	always bool
}

func (g *gateLander) Land(ctx context.Context, ph Phase) (Landing, error) {
	g.calls++
	if g.calls == 1 || g.always {
		ref := StepRef{Key: StepKey{Run: "run-1", Phase: "2", Kind: "gate", Attempt: 1}, Kind: StepKind{Name: "gate"}}
		return Landing{}, fmt.Errorf("%w: %w", ErrNoGate, &FailedStep{Ref: ref, Outcome: Outcome{State: StepFailed, Reason: "HEAD moved from outside the step: abc1234 maintainer work"}})
	}
	landing := Landing{Phase: ph.ID, MergeSHA: "merge-" + ph.Title}
	g.store.Append("run-1", Record{Kind: RecordLanding, Landing: &landing})
	return landing, nil
}

func TestAFailedGateStepIsRestartedInTheRemedyWindowWithoutBlockingThePhase(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	w := &Watch{Store: r.store}
	r.loop.Watcher = w
	rem := newRemedies(w, r.store, "restart")
	g := &gateLander{store: r.store}
	r.loop.Lander = g
	decided := make(chan string, 1)
	go func() {
		for {
			if key, ok := w.holding(); ok && key.Kind == "gate" {
				break
			}
			time.Sleep(time.Millisecond)
		}
		_, reason := rem.Restart("phase-2/gate", "retry", "", "", "", "")
		decided <- reason
	}()
	code := r.run(RunOptions{Phases: []string{"2"}})
	reason := <-decided
	if code != 0 || reason != "" || g.calls != 2 || len(r.events("phase-blocked")) != 0 {
		t.Fatalf("exit %d; restart reason %q; Land calls %d; blocked %+v", code, reason, g.calls, r.events("phase-blocked"))
	}
	restarts := r.events("restart")
	if len(restarts) != 1 || restarts[0].Fields["step"] != "phase-2/gate" || restarts[0].Fields["attempt"] != "2" {
		t.Errorf("restarts = %+v", restarts)
	}
}

func TestAFailedGateStepWithNoRestartBlocksThePhaseAfterTheWindow(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = 20 * time.Millisecond
	r.loop.Watcher = &Watch{Store: r.store}
	r.loop.Lander = &gateLander{store: r.store, always: true}
	code := r.run(RunOptions{Phases: []string{"2"}})
	blocked := r.events("phase-blocked")
	if code != 1 || len(blocked) != 1 || !strings.HasPrefix(blocked[0].Fields["reason"], "land: no gate: gate step failed: HEAD moved from outside the step") {
		t.Fatalf("exit %d; blocked %+v", code, blocked)
	}
}

func TestAHaltInTheGateRemedyWindowBlocksThePhaseWithExit5(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	w := &Watch{Store: r.store}
	r.loop.Watcher = w
	r.loop.Lander = &gateLander{store: r.store, always: true}
	go func() {
		for {
			if key, ok := w.holding(); ok && key.Kind == "gate" {
				w.Handle(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: key, Reason: "stop"})
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	code := r.run(RunOptions{Phases: []string{"2"}})
	blocked := r.events("phase-blocked")
	if code != 5 || len(blocked) != 1 || blocked[0].Fields["reason"] != "watchdog: stop" {
		t.Fatalf("exit %d; blocked %+v", code, blocked)
	}
}

func TestAnUnlistedClassWithoutConsentAsksTheWatchdogToAskAndRecordsNothing(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, "locks")

	got, reason := rem.Propose("deps", "go mod download", "module cache is empty", "")

	if got != "ask" || !strings.Contains(reason, "ask the maintainer in your session") || !strings.Contains(reason, "maintainer_said") {
		t.Fatalf("decision %q reason %q", got, reason)
	}
	if recs := remedyRecords(store); len(recs) != 0 {
		t.Errorf("records %+v", recs)
	}
}

func TestAnUnlistedClassIsAuthorisedByWhatTheMaintainerSaid(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, "locks")

	got, _ := rem.Propose("deps", "go mod download", "module cache is empty", "yes, go ahead")

	if got != "authorised" {
		t.Fatalf("decision %q", got)
	}
	if recs := remedyRecords(store); len(recs) != 1 || recs[0].Consent != "maintainer" {
		t.Errorf("records %+v", recs)
	}
}

func TestABlankMaintainerQuoteIsNoConsent(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store)

	got, reason := rem.Propose("ports", "kill $(lsof -ti :8080)", "port 8080 is taken", "  ")

	if got != "ask" || !strings.Contains(reason, "maintainer_said") {
		t.Fatalf("decision %q reason %q", got, reason)
	}
	if recs := remedyRecords(store); len(recs) != 0 {
		t.Errorf("records %+v", recs)
	}
}

func TestARestartOnAProviderWithoutAnAskChannelIsRefused(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, "provider")
	rem.Fallbacks = map[string]Fallback{"implement": {Provider: "plainbot"}}

	for _, said := range []string{"", "yes, use plainbot"} {
		if ok, reason := rem.Restart("phase-2/implement", "", "plainbot", "", "", said); ok || reason != "provider plainbot has no MCP ask channel" {
			t.Errorf("maintainer_said %q: %v %q", said, ok, reason)
		}
	}
	if recs := remedyRecords(store); len(recs) != 0 {
		t.Errorf("records %+v", recs)
	}
}

func TestARemedyThatNeedsTheMaintainerShowsTheWatchdogWaitingForThem(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	face := &fakeFace{}
	rem := newRemedies(w, store)
	rem.Dog = &Watchdog{RunID: "run-1", Store: store, Face: face}

	got, _ := rem.Propose("retry", "true", "the plan named a helper Go does not have", "")

	if got != "ask" {
		t.Fatalf("decision %q", got)
	}
	var kinds []string
	for _, ev := range face.Events {
		kinds = append(kinds, ev.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{"watchdog-waiting"}) {
		t.Errorf("emitted %v, want the watchdog shown waiting for the maintainer", kinds)
	}
}

func TestAnAuthorisedRemedyDoesNotShowTheWatchdogWaiting(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	face := &fakeFace{}
	rem := newRemedies(w, store, "locks")
	rem.Dog = &Watchdog{RunID: "run-1", Store: store, Face: face}

	got, _ := rem.Propose("locks", "rm -f .git/index.lock", "a stale git lock", "")

	if got != "authorised" || len(face.Events) != 0 {
		t.Errorf("decision %q, emitted %+v", got, face.Events)
	}
}

func TestARemedyTheMaintainerConsentedToIsAHumanTouch(t *testing.T) {
	store := &fakeStore{}
	w, key := failedImplement(t, store)
	rem := newRemedies(w, store)

	got, _ := rem.Propose("retry", "true", "the plan named a helper Go does not have", "Yes — retry with the test-local writer.")

	if got != "authorised" {
		t.Fatalf("decision %q", got)
	}
	var human []Event
	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == "human" {
			human = append(human, *rec.Event)
		}
	}
	if len(human) != 1 || human[0].Fields["what"] != "consent" || human[0].Phase != key.Phase || human[0].Step != key.Kind || human[0].Fields["id"] != "remedy-1" {
		t.Errorf("human events %+v, want one consent for remedy-1", human)
	}
}

func TestAnAllowListedRemedyIsNoHumanTouch(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, "locks")

	rem.Propose("locks", "rm -f .git/index.lock", "a stale git lock", "")

	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == "human" {
			t.Errorf("human event %+v", rec.Event)
		}
	}
}

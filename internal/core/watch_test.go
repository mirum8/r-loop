package core

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeCheck struct {
	name string
	sig  Signal
	seen chan CheckContext
}

func (c *fakeCheck) Name() string { return c.name }

func (c *fakeCheck) Run(ctx CheckContext) []Signal {
	select {
	case c.seen <- ctx:
	default:
	}
	sig := c.sig
	sig.Step = ctx.Step.Key
	return []Signal{sig}
}

var watchT0 = time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

func newWatch(store *fakeStore) *Watch {
	now := watchT0
	return &Watch{Store: store, Face: &fakeFace{}, Now: func() time.Time { return now }, Poll: time.Hour}
}

func implementRef(phase, attempt int) StepRef {
	return StepRef{Key: StepKey{Run: "run-1", Phase: strconv.Itoa(phase), Kind: "implement", Attempt: attempt}, Phase: Phase{ID: strconv.Itoa(phase)}}
}

func receive(t *testing.T, w *Watch) Signal {
	t.Helper()
	select {
	case sig := <-w.Signals():
		return sig
	case <-time.After(2 * time.Second):
		t.Fatal("no signal forwarded")
		return Signal{}
	}
}

func noSignal(t *testing.T, w *Watch) {
	t.Helper()
	select {
	case sig := <-w.Signals():
		t.Fatalf("unexpected signal forwarded %+v", sig)
	default:
	}
}

func recordedSignals(store *fakeStore) []Signal {
	var out []Signal
	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordSignal {
			out = append(out, *rec.Signal)
		}
	}
	return out
}

func TestAcceptedWarnIsRecordedAndForwarded(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	w.StepStarted(implementRef(2, 1), &Session{})
	defer w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})

	got, err := w.Accept(Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: StepKey{Phase: "2", Kind: "implement"}, Reason: "drifting"})

	if err != nil || got.Rejected {
		t.Fatalf("accept %+v, %v", got, err)
	}
	fwd := receive(t, w)
	if fwd.Kind != SignalWarn || fwd.Reason != "drifting" || fwd.Step != implementRef(2, 1).Key {
		t.Errorf("forwarded %+v", fwd)
	}
	recs := recordedSignals(store)
	if len(recs) != 1 || recs[0].Rejected || recs[0].Reason != "drifting" || !recs[0].At.Equal(watchT0) {
		t.Errorf("recorded %+v", recs)
	}
}

func TestAcceptedHaltIsRecordedAndForwarded(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	w.StepStarted(implementRef(2, 1), &Session{})
	defer w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})

	got, _ := w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Phase: "2", Kind: "implement"}, Reason: "rewriting the spec"})

	if got.Rejected {
		t.Fatalf("rejected %+v", got)
	}
	fwd := receive(t, w)
	if fwd.Kind != SignalHalt || fwd.Reason != "rewriting the spec" || fwd.Step != implementRef(2, 1).Key {
		t.Errorf("forwarded %+v", fwd)
	}
	if recs := recordedSignals(store); len(recs) != 1 || recs[0].Kind != SignalHalt || recs[0].Rejected {
		t.Errorf("recorded %+v", recs)
	}
}

func TestSignalForAStepThatEndedWithinAPollIsAccepted(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	w.StepStarted(implementRef(2, 1), &Session{})
	w.StepEnded(implementRef(2, 1), Outcome{State: StepFailed})

	got, _ := w.Accept(Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: StepKey{Phase: "2", Kind: "implement"}, Reason: "late"})

	if got.Rejected {
		t.Fatalf("rejected %+v", got)
	}
	receive(t, w)
}

func TestOkAndApproveKindsAreRejectedAndRecorded(t *testing.T) {
	for _, kind := range []SignalKind{"ok", "approve"} {
		t.Run(string(kind), func(t *testing.T) {
			store := &fakeStore{}
			w := newWatch(store)
			w.StepStarted(implementRef(2, 1), &Session{})
			defer w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})

			got, err := w.Accept(Signal{Kind: kind, Source: SourceDriver, Step: StepKey{Phase: "2", Kind: "implement"}, Reason: "looks fine"})

			if err != nil || !got.Rejected || !strings.Contains(got.RejectReason, string(kind)) {
				t.Fatalf("accept %+v, %v", got, err)
			}
			recs := recordedSignals(store)
			if len(recs) != 1 || !recs[0].Rejected || recs[0].RejectReason != got.RejectReason || recs[0].Kind != kind {
				t.Errorf("recorded %+v", recs)
			}
		})
	}
}

func TestARejectedDriverSignalBecomesAWarnNamingTheCheck(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	w.Poll = 5 * time.Millisecond
	w.Checks = []Check{&fakeCheck{name: "odd-check", sig: Signal{Kind: "approve", Source: SourceDriver, Reason: "fine"}, seen: make(chan CheckContext, 1)}}
	w.StepStarted(implementRef(2, 1), &Session{})
	defer w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})

	fwd := receive(t, w)

	if fwd.Kind != SignalWarn || fwd.Source != SourceDriver || !strings.Contains(fwd.Reason, "odd-check") || fwd.Step != implementRef(2, 1).Key {
		t.Errorf("forwarded %+v", fwd)
	}
}

func TestARejectedWatchdogSignalTurnsIntoAHalt(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	w.StepStarted(implementRef(2, 1), &Session{})
	defer w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})

	got, _ := w.Accept(Signal{Kind: "ok", Source: SourceWatchdog, Step: StepKey{Phase: "2", Kind: "implement"}, Reason: "done"})

	fwd := receive(t, w)
	if fwd.Kind != SignalHalt || fwd.Reason != "watchdog signal rejected: "+got.RejectReason || fwd.Step != implementRef(2, 1).Key {
		t.Errorf("forwarded %+v", fwd)
	}
	if recs := recordedSignals(store); len(recs) != 1 || !recs[0].Rejected {
		t.Errorf("recorded %+v", recs)
	}
}

func TestASignalForALandedPhaseIsRejected(t *testing.T) {
	store := &fakeStore{}
	store.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: "1", MergeSHA: "abc"}})
	w := newWatch(store)
	w.StepStarted(implementRef(2, 1), &Session{})
	defer w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})

	got, _ := w.Accept(Signal{Kind: SignalHalt, Source: SourceDriver, Step: StepKey{Phase: "1", Kind: "implement"}, Reason: "stale"})

	if !got.Rejected || got.RejectReason != "phase 1 has landed" {
		t.Fatalf("accept %+v", got)
	}
	if recs := recordedSignals(store); len(recs) != 1 || !recs[0].Rejected {
		t.Errorf("recorded %+v", recs)
	}
	fwd := receive(t, w)
	if fwd.Kind != SignalWarn {
		t.Errorf("forwarded %+v", fwd)
	}
}

func TestASignalForAnOkStepIsRejected(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	w.StepStarted(implementRef(2, 1), &Session{})
	w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})

	got, _ := w.Accept(Signal{Kind: SignalWarn, Source: SourceDriver, Step: StepKey{Phase: "2", Kind: "implement"}, Reason: "late"})

	if !got.Rejected || got.RejectReason != "phase-2/implement is ok" {
		t.Fatalf("accept %+v", got)
	}
}

func TestASignalForAnUnknownStepIsRejected(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	w.StepStarted(implementRef(2, 1), &Session{})
	defer w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})

	got, _ := w.Accept(Signal{Kind: SignalWarn, Source: SourceDriver, Step: StepKey{Phase: "3", Kind: "plan"}, Reason: "?"})

	if !got.Rejected || got.RejectReason != "phase-3/plan is not running" {
		t.Fatalf("accept %+v", got)
	}
}

func TestASignalForAStepThatEndedLongerAgoThanAPollIsRejected(t *testing.T) {
	store := &fakeStore{}
	now := watchT0
	w := &Watch{Store: store, Face: &fakeFace{}, Now: func() time.Time { return now }, Poll: time.Minute}
	w.StepStarted(implementRef(2, 1), &Session{})
	w.StepEnded(implementRef(2, 1), Outcome{State: StepFailed})
	now = now.Add(2 * time.Minute)

	got, _ := w.Accept(Signal{Kind: SignalWarn, Source: SourceDriver, Step: StepKey{Phase: "2", Kind: "implement"}, Reason: "late"})

	if !got.Rejected || got.RejectReason != "phase-2/implement is not running" {
		t.Fatalf("accept %+v", got)
	}
}

func TestAFakeCheckWarningArrivesOnATick(t *testing.T) {
	store := &fakeStore{}
	repo := &fakeRepo{}
	plan := Plan{Path: "todo.md"}
	check := &fakeCheck{name: "fake", sig: Signal{Kind: SignalWarn, Source: SourceDriver, Reason: "files outside plan"}, seen: make(chan CheckContext, 1)}
	w := &Watch{Store: store, Face: &fakeFace{}, Checks: []Check{check}, Now: func() time.Time { return watchT0 }, Poll: 5 * time.Millisecond, Repo: repo, Plan: plan}
	s := &Session{Agent: "rloop-p2-implement"}
	w.StepStarted(implementRef(2, 1), s)

	fwd := receive(t, w)
	w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})

	if fwd.Kind != SignalWarn || fwd.Source != SourceDriver || fwd.Reason != "files outside plan" || fwd.Step != implementRef(2, 1).Key {
		t.Errorf("forwarded %+v", fwd)
	}
	ctx := <-check.seen
	if ctx.Session != s || ctx.Step.Key != implementRef(2, 1).Key || ctx.Repo != repo || ctx.Store != store || ctx.Plan.Path != "todo.md" || !ctx.Started.Equal(watchT0) || !ctx.Now.Equal(watchT0) {
		t.Errorf("check context %+v", ctx)
	}
	if recs := recordedSignals(store); len(recs) == 0 || recs[0].Rejected {
		t.Errorf("recorded %+v", recs)
	}
}

func TestChecksStopAfterStepEnded(t *testing.T) {
	store := &fakeStore{}
	check := &fakeCheck{name: "fake", sig: Signal{Kind: SignalWarn, Source: SourceDriver, Reason: "x"}, seen: make(chan CheckContext, 1)}
	w := &Watch{Store: store, Face: &fakeFace{}, Checks: []Check{check}, Now: time.Now, Poll: 5 * time.Millisecond}
	w.StepStarted(implementRef(2, 1), &Session{})
	receive(t, w)
	w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})
	for len(w.Signals()) > 0 {
		<-w.Signals()
	}

	time.Sleep(30 * time.Millisecond)

	noSignal(t, w)
}

func TestWatchIsAWatcher(t *testing.T) {
	var _ Watcher = (*Watch)(nil)
}

type gatedCheck struct {
	entered, release chan struct{}
}

func (c *gatedCheck) Name() string { return "gated" }

func (c *gatedCheck) Run(ctx CheckContext) []Signal {
	select {
	case c.entered <- struct{}{}:
		<-c.release
		return []Signal{{Kind: SignalWarn, Source: SourceDriver, Step: ctx.Step.Key, Reason: "late check"}}
	default:
		return nil
	}
}

func TestACheckFinishingWhileTheStepEndsIsStillForwarded(t *testing.T) {
	for range 50 {
		store := &fakeStore{}
		check := &gatedCheck{entered: make(chan struct{}), release: make(chan struct{})}
		w := &Watch{Store: store, Face: &fakeFace{}, Checks: []Check{check}, Now: time.Now, Poll: time.Millisecond}
		w.StepStarted(implementRef(2, 1), &Session{})
		<-check.entered
		ended := make(chan struct{})
		go func() { w.StepEnded(implementRef(2, 1), Outcome{State: StepOK}); close(ended) }()
		time.Sleep(time.Millisecond)
		close(check.release)
		<-ended

		if len(w.Signals()) != 1 {
			t.Fatalf("forwarded %d signals, want 1", len(w.Signals()))
		}
	}
}

func TestARejectedWatchdogSignalBetweenStepsHaltsTheNextStep(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	w.StepStarted(implementRef(2, 1), &Session{})
	w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})

	w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Phase: "2", Kind: "implement"}, Reason: "too late"})
	noSignal(t, w)
	next := StepRef{Key: StepKey{Run: "run-1", Phase: "3", Kind: "plan", Attempt: 1}}
	w.StepStarted(next, &Session{})
	defer w.StepEnded(next, Outcome{State: StepOK})

	fwd := receive(t, w)
	if fwd.Kind != SignalHalt || fwd.Step != next.Key || fwd.Reason != "watchdog signal rejected: phase-2/implement is ok" {
		t.Errorf("forwarded %+v", fwd)
	}
}

func TestASignalForAMalformedStepIsRejectedAndHaltsTheRun(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	w.StepStarted(implementRef(2, 1), &Session{})
	defer w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})

	got, err := w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Kind: "phase3/implement"}, Reason: "off the plan"})

	want := `step "phase3/implement" is not phase-<N>/<kind>`
	if err != nil || !got.Rejected || got.RejectReason != want {
		t.Fatalf("accept %+v, %v", got, err)
	}
	if recs := recordedSignals(store); len(recs) != 1 || !recs[0].Rejected || recs[0].RejectReason != want {
		t.Errorf("recorded %+v", recs)
	}
	fwd := receive(t, w)
	if fwd.Kind != SignalHalt || fwd.Step != implementRef(2, 1).Key || fwd.Reason != "watchdog signal rejected: "+want {
		t.Errorf("forwarded %+v", fwd)
	}
}

func TestAnAcceptedHaltForTheHeldStepClosesItsRemedyWindow(t *testing.T) {
	store := &fakeStore{}
	w, key := failedImplement(t, store)
	w.Poll = time.Hour
	rem := newRemedies(w, store, "restart")
	go func() { <-w.Restarts() }()

	if ok, reason := w.Handle(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: "2", Kind: "implement"}, Reason: "wrong turn"}); !ok {
		t.Fatalf("halt rejected: %s", reason)
	}
	if fwd := receive(t, w); fwd.Kind != SignalHalt || fwd.Step != key {
		t.Errorf("forwarded %+v", fwd)
	}

	if ok, reason := rem.Restart("phase-2/implement", "", "", ""); ok || reason != "run halted" {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestARejectedWatchdogSignalDuringTheRemedyWindowHaltsTheHeldStep(t *testing.T) {
	store := &fakeStore{}
	w, key := failedImplement(t, store)
	rem := newRemedies(w, store, "restart")
	go func() { <-w.Restarts() }()

	w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Kind: "phase3/implement"}, Reason: "off the plan"})

	fwd := receive(t, w)
	if fwd.Kind != SignalHalt || fwd.Step != key || !strings.HasPrefix(fwd.Reason, "watchdog signal rejected: ") {
		t.Errorf("forwarded %+v", fwd)
	}
	if ok, reason := rem.Restart("phase-2/implement", "", "", ""); ok || reason != "run halted" {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestStepNoticesDoNotWaitForAWatchdogAskingTheMaintainer(t *testing.T) {
	host := &askingHost{blockedFor: 1}
	host.States = map[string]AgentState{"rloop-wd-run-1": AgentBlocked}
	dog := newWatchdog(host, &fakeStore{}, ProviderArgs{Kind: "claude"})
	answered := make(chan struct{})
	dog.Sleep = func(time.Duration) { <-answered }
	defer close(answered)
	w := newWatch(&fakeStore{})
	w.Dog = dog

	done := make(chan struct{})
	go func() {
		w.StepStarted(implementRef(2, 1), nil)
		w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})
		dog.live()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("step notices and live() waited for the maintainer to answer the watchdog")
	}
}

package core

import (
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type panicWatchCheck struct {
	name     string
	nameBoom bool
	calls    atomic.Int32
}

func (c *panicWatchCheck) Name() string {
	if c.nameBoom {
		panic("boom")
	}
	return c.name
}

func (c *panicWatchCheck) Run(CheckContext) []Signal {
	c.calls.Add(1)
	if !c.nameBoom {
		panic("boom")
	}
	return []Signal{{Kind: SignalWarn, Source: SourceDriver, Reason: "warning"}}
}

func receiveCheck(t *testing.T, seen <-chan CheckContext) {
	t.Helper()
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("good check did not tick")
	}
}

func endWatchStep(t *testing.T, w *Watch, ref StepRef) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		w.StepEnded(ref, Outcome{State: StepOK})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("StepEnded waited for a panicking check")
	}
}

func watchErrorEvents(store *fakeStore) []Event {
	store.mu.Lock()
	defer store.mu.Unlock()
	var out []Event
	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == "error" {
			out = append(out, *rec.Event)
		}
	}
	return out
}

func TestAPanickingWatchCheckIsRecordedAndOtherChecksKeepTicking(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	w.Poll = time.Millisecond
	bad := &panicWatchCheck{name: "bad"}
	good := &fakeCheck{name: "good", seen: make(chan CheckContext, 1)}
	w.Checks = []Check{bad, good}
	ref := implementRef(1, 1)
	w.StepStarted(ref, nil)
	for range 3 {
		receiveCheck(t, good.seen)
	}
	endWatchStep(t, w, ref)
	if got := bad.calls.Load(); got != 1 {
		t.Errorf("bad check calls %d, want 1", got)
	}
	events := watchErrorEvents(store)
	if len(events) != 1 || events[0].Fields["reason"] != "panic in watch check bad: boom" || events[0].Fields["stack"] == "" {
		t.Errorf("error events %+v", events)
	}
}

func TestAPanickingWatchCheckNameIsRecordedWithoutACrash(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	w.Poll = time.Millisecond
	bad := &panicWatchCheck{nameBoom: true}
	good := &fakeCheck{name: "good", seen: make(chan CheckContext, 1)}
	w.Checks = []Check{bad, good}
	ref := implementRef(1, 1)
	w.StepStarted(ref, nil)
	for range 3 {
		receiveCheck(t, good.seen)
	}
	endWatchStep(t, w, ref)
	events := watchErrorEvents(store)
	if len(events) != 1 || events[0].Fields["reason"] != "panic in watch check unnamed: boom" {
		t.Errorf("error events %+v", events)
	}
}

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

func TestASignalOnAFullQueueReturnsAtOnceAndTheWarnIsReportedDropped(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	key := implementRef(2, 1).Key
	w.StepStarted(implementRef(2, 1), nil)
	for i := 0; i < 64; i++ {
		if ok, reason := w.Handle(Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: key, Reason: "w"}); !ok {
			t.Fatalf("warn %d: %s", i, reason)
		}
	}
	type result struct {
		ok     bool
		reason string
	}
	done := make(chan result, 1)
	go func() {
		ok, reason := w.Handle(Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: key, Reason: "w"})
		done <- result{ok, reason}
	}()
	select {
	case got := <-done:
		if got.ok || !strings.Contains(got.reason, "the signal queue is full") {
			t.Errorf("result %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Handle blocked on a full queue")
	}
	var dropped []Event
	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == "signal-dropped" {
			dropped = append(dropped, *rec.Event)
		}
	}
	if len(dropped) != 1 || dropped[0].Fields["seq"] != "65" {
		t.Errorf("dropped %+v", dropped)
	}
}

func TestAHaltOnAFullQueueIsDeliveredAfterTheQueuedSignals(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	key := implementRef(2, 1).Key
	w.StepStarted(implementRef(2, 1), nil)
	for i := 0; i < 64; i++ {
		if ok, reason := w.Handle(Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: key, Reason: "w"}); !ok {
			t.Fatalf("warn %d: %s", i, reason)
		}
	}
	type result struct {
		ok     bool
		reason string
	}
	done := make(chan result, 1)
	go func() {
		ok, reason := w.Handle(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: key, Reason: "wrong turn"})
		done <- result{ok, reason}
	}()
	select {
	case got := <-done:
		if !got.ok || got.reason != "" {
			t.Errorf("halt %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("halt blocked on a full queue")
	}
	for i := 0; i < 64; i++ {
		if sig := receive(t, w); sig.Kind != SignalWarn {
			t.Errorf("signal %d: %+v", i, sig)
		}
	}
	if sig := receive(t, w); sig.Kind != SignalHalt || sig.Reason != "wrong turn" {
		t.Errorf("halt %+v", sig)
	}
	noSignal(t, w)
	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == "signal-dropped" {
			t.Errorf("dropped halt %+v", rec.Event)
		}
	}
}

func TestAHaltHeldBetweenStepsIsDeliveredToTheNextStepWhenTheQueueIsFull(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	first := implementRef(2, 1)
	w.StepStarted(first, &Session{})
	for i := 0; i < 64; i++ {
		if _, err := w.Accept(Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: first.Key, Reason: "w"}); err != nil {
			t.Fatal(err)
		}
	}
	w.StepEnded(first, Outcome{State: StepOK})
	if _, err := w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Phase: "2", Kind: "implement"}, Reason: "too late"}); err != nil {
		t.Fatal(err)
	}
	next := StepRef{Key: StepKey{Run: "run-1", Phase: "3", Kind: "plan", Attempt: 1}}
	w.StepStarted(next, &Session{})
	defer w.StepEnded(next, Outcome{State: StepOK})
	for i := 0; i < 64; i++ {
		if sig := receive(t, w); sig.Kind != SignalWarn {
			t.Errorf("signal %d: %+v", i, sig)
		}
	}
	if sig := receive(t, w); sig.Kind != SignalHalt || sig.Step != (StepKey{Phase: "2", Kind: "implement"}) || sig.Reason != "watchdog signal rejected: phase-2/implement is ok" {
		t.Errorf("halt %+v", sig)
	}
	noSignal(t, w)
}

func TestASignalAfterTheLoopReturnsDoesNotBlock(t *testing.T) {
	r := newLoopRig(t)
	r.host.behaviour["rloop-p1-implement"] = "fail"
	w := &Watch{Store: r.store, Face: r.face, Poll: time.Hour}
	r.loop.Watcher = w
	if code := r.run(RunOptions{Phases: []string{"1"}}); code == 0 {
		t.Fatal("run unexpectedly succeeded")
	}
	type result struct {
		ok     bool
		reason string
	}
	done := make(chan result, 1)
	go func() {
		var last result
		for range 65 {
			last.ok, last.reason = w.Handle(Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: "1", Kind: "implement"}, Reason: "late"})
		}
		done <- last
	}()
	select {
	case got := <-done:
		if got.ok || !strings.Contains(got.reason, "the signal queue is full") {
			t.Errorf("last %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Handle blocked after Run")
	}
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
	g := &RecordGuard{Store: store}
	if _, err := g.Follow("run-1"); err != nil {
		t.Fatal(err)
	}
	w := newWatch(store)
	w.Store = g
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

func TestAcceptingASignalLoadsNoRunState(t *testing.T) {
	store := &countingStore{}
	g := &RecordGuard{Store: store}
	if _, err := g.Follow("run-1"); err != nil {
		t.Fatal(err)
	}
	if err := g.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: "1"}}); err != nil {
		t.Fatal(err)
	}
	store.loads.Store(0)
	w := &Watch{Store: g, Face: &fakeFace{}, Now: func() time.Time { return watchT0 }, Poll: time.Hour}
	w.StepStarted(implementRef(2, 1), &Session{})
	defer w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})
	rejected, _ := w.Accept(Signal{Kind: SignalHalt, Source: SourceDriver, Step: StepKey{Phase: "1", Kind: "implement"}, Reason: "stale"})
	if !rejected.Rejected || rejected.RejectReason != "phase 1 has landed" {
		t.Fatalf("rejected = %+v", rejected)
	}
	accepted, _ := w.Accept(Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: StepKey{Phase: "2", Kind: "implement"}, Reason: "slow"})
	if accepted.Rejected {
		t.Fatalf("accepted = %+v", accepted)
	}
	if n := store.loads.Load(); n != 0 {
		t.Fatalf("loads = %d", n)
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
	if fwd.Kind != SignalHalt || fwd.Step != (StepKey{Phase: "2", Kind: "implement"}) || fwd.Reason != "watchdog signal rejected: phase-2/implement is ok" {
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

	if ok, reason := rem.Restart("phase-2/implement", "", "", "", "", ""); ok || reason != "run halted" {
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
	if ok, reason := rem.Restart("phase-2/implement", "", "", "", "", ""); ok || reason != "run halted" {
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

func TestASignalNamingALandStageStepIsRejectedToTheCaller(t *testing.T) {
	store := &fakeStore{}
	w := newWatch(store)
	ref := implementRef(2, 1)
	w.StepStarted(ref, &Session{})
	w.StepEnded(ref, Outcome{State: StepOK})
	for _, kind := range []SignalKind{SignalWarn, SignalHalt} {
		ok, reason := w.Handle(Signal{
			Kind: kind, Source: SourceWatchdog,
			Step:   StepKey{Run: "run-1", Phase: "2", Kind: "gatefix"},
			Reason: "wrong turn",
		})
		if ok || reason != "phase-2/gatefix is not running" {
			t.Errorf("%s: accepted %v, reason %q", kind, ok, reason)
		}
	}
}

func TestAHeldHaltIsNotRetargetedToAStepOfAnotherPhase(t *testing.T) {
	w := newWatch(&fakeStore{})
	ref := implementRef(2, 1)
	w.StepStarted(ref, &Session{})
	w.StepEnded(ref, Outcome{State: StepOK})
	named := StepKey{Phase: "2", Kind: "gatefix"}
	if _, err := w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: named, Reason: "wrong turn"}); err != nil {
		t.Fatal(err)
	}
	noSignal(t, w)
	next := StepRef{Key: StepKey{Run: "run-1", Phase: "3", Kind: "plan", Attempt: 1}}
	w.StepStarted(next, &Session{})
	defer w.StepEnded(next, Outcome{State: StepOK})
	if got := receive(t, w); got.Step != named || got.Kind != SignalHalt {
		t.Errorf("halt %+v", got)
	}
}

func TestAHeldHaltIsDeliveredToTheNextStepOfItsPhase(t *testing.T) {
	w := newWatch(&fakeStore{})
	if _, err := w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Phase: "3", Kind: "plan"}, Reason: "wrong turn"}); err != nil {
		t.Fatal(err)
	}
	noSignal(t, w)
	next := StepRef{Key: StepKey{Run: "run-1", Phase: "3", Kind: "implement", Attempt: 1}}
	w.StepStarted(next, &Session{})
	defer w.StepEnded(next, Outcome{State: StepOK})
	if got := receive(t, w); got.Step != next.Key || got.Kind != SignalHalt {
		t.Errorf("halt %+v", got)
	}
}

func TestDrainReturnsHeldAndQueuedSignalsOnce(t *testing.T) {
	w := newWatch(&fakeStore{})
	ref := implementRef(2, 1)
	w.StepStarted(ref, &Session{})
	if _, err := w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: ref.Key, Reason: "first"}); err != nil {
		t.Fatal(err)
	}
	w.StepEnded(ref, Outcome{State: StepOK})
	named := StepKey{Run: "run-1", Phase: "2", Kind: "gatefix"}
	if _, err := w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: named, Reason: "second"}); err != nil {
		t.Fatal(err)
	}
	got := w.Drain()
	if len(got) != 2 || got[0].Kind != SignalHalt || got[0].Step != ref.Key || got[1].Kind != SignalHalt || got[1].Step != named {
		t.Fatalf("drained %+v", got)
	}
	if again := w.Drain(); len(again) != 0 {
		t.Errorf("second drain %+v", again)
	}
	next := StepRef{Key: StepKey{Run: "run-1", Phase: "3", Kind: "plan", Attempt: 1}}
	w.StepStarted(next, &Session{})
	defer w.StepEnded(next, Outcome{State: StepOK})
	noSignal(t, w)
}

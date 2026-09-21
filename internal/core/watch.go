package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Check interface {
	Name() string
	Run(ctx CheckContext) []Signal
}

type CheckContext struct {
	Step         StepRef
	Session      *Session
	Started, Now time.Time
	Repo         Repo
	Store        Store
	Plan         Plan
}

type Watch struct {
	Store  Store
	Face   Face
	Checks []Check
	Now    func() time.Time
	Poll   time.Duration
	Repo   Repo
	Plan   Plan
	Dog    *Watchdog
	Router *QuestionRouter

	PhaseCheck *PhaseCheck

	once     sync.Once
	mu       sync.Mutex
	signals  chan Signal
	restarts chan Restart
	held     *StepKey
	closed   chan struct{}
	runID    string
	live     *StepKey
	ended    map[StepKey]endedStep
	tickers  map[StepKey]*ticking
	seq      int
	halt     *Signal
	checking int
}

type endedStep struct {
	state StepState
	at    time.Time
}

type ticking struct {
	stop, done chan struct{}
}

func (w *Watch) init() {
	w.once.Do(func() {
		w.signals = make(chan Signal, 64)
		w.restarts = make(chan Restart)
		w.ended = map[StepKey]endedStep{}
		w.tickers = map[StepKey]*ticking{}
	})
}

func (w *Watch) now() time.Time {
	if w.Now == nil {
		return time.Now()
	}
	return w.Now()
}

func (w *Watch) poll() time.Duration {
	if w.Poll <= 0 {
		return defaultPoll
	}
	return w.Poll
}

func (w *Watch) Signals() <-chan Signal {
	w.init()
	return w.signals
}

func (w *Watch) Restarts() <-chan Restart {
	w.init()
	return w.restarts
}

func (w *Watch) Hold(key StepKey) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.held != nil && *w.held == key {
		return
	}
	if w.closed != nil {
		close(w.closed)
	}
	w.held, w.closed = &key, make(chan struct{})
}

func (w *Watch) Release(key StepKey) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.held != nil && *w.held == key {
		close(w.closed)
		w.held, w.closed = nil, nil
	}
}

func (w *Watch) holding() (StepKey, bool) {
	key, _, ok := w.hold()
	return key, ok
}

func (w *Watch) hold() (StepKey, <-chan struct{}, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.held == nil {
		return StepKey{}, nil, false
	}
	return *w.held, w.closed, true
}

func (w *Watch) target() (StepKey, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.held != nil:
		return *w.held, true
	case w.live != nil:
		return *w.live, true
	}
	return StepKey{}, false
}

func (w *Watch) latest(phase int, kind string) (StepState, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var key *StepKey
	for k := range w.ended {
		if k.Phase == phase && k.Kind == kind && (key == nil || k.Attempt > key.Attempt) {
			key = &k
		}
	}
	if key == nil {
		return "", false
	}
	return w.ended[*key].state, true
}

func (w *Watch) BeforePhase(ctx context.Context, ph Phase, base string) CheckOutcome {
	if w.PhaseCheck == nil {
		return CheckOutcome{Kind: phaseCheckSkipped}
	}
	w.init()
	w.mu.Lock()
	w.checking = ph.Number
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.checking = 0
		w.mu.Unlock()
	}()
	return w.PhaseCheck.Run(ctx, ph, base)
}

func (w *Watch) Route(ctx context.Context, q Question) bool {
	if w.Router != nil && w.Router.Route(ctx, q) {
		return true
	}
	if ctx.Err() == nil {
		key := q.Step
		key.Kind, _, _ = strings.Cut(key.Kind, "-rv-")
		w.Accept(Signal{Kind: SignalHalt, Source: SourceDriver, Step: key, Reason: watchdogGone})
	}
	return false
}

func (w *Watch) StepStarted(ref StepRef, s *Session) {
	w.init()
	started := w.now()
	t := &ticking{stop: make(chan struct{}), done: make(chan struct{})}
	w.mu.Lock()
	key := ref.Key
	w.live, w.runID = &key, key.Run
	delete(w.ended, key)
	w.tickers[key] = t
	if w.halt != nil {
		w.halt.Step = key
		select {
		case w.signals <- *w.halt:
			w.halt = nil
		default:
		}
	}
	w.mu.Unlock()
	go w.tick(ref, s, started, t)
	if w.Dog != nil {
		dir := ref.Worktree
		agent := ""
		if s != nil {
			agent = s.Agent
			if s.Dir != "" {
				dir = s.Dir
			}
		}
		w.Dog.Notify(fmt.Sprintf("step started phase-%d/%s agent %s worktree %s base %s", key.Phase, key.Kind, agent, dir, ref.Base), false, 0)
	}
}

func (w *Watch) StepEnded(ref StepRef, out Outcome) {
	w.init()
	w.mu.Lock()
	if w.live != nil && *w.live == ref.Key {
		w.live = nil
	}
	w.ended[ref.Key] = endedStep{state: out.State, at: w.now()}
	t := w.tickers[ref.Key]
	delete(w.tickers, ref.Key)
	w.mu.Unlock()
	if t != nil {
		close(t.stop)
		<-t.done
	}
	if w.Dog != nil {
		w.Dog.Notify(fmt.Sprintf("step ended phase-%d/%s %s %s", ref.Key.Phase, ref.Key.Kind, out.State, out.Reason), false, 0)
	}
}

func (w *Watch) tick(ref StepRef, s *Session, started time.Time, t *ticking) {
	defer close(t.done)
	if len(w.Checks) == 0 {
		return
	}
	ticker := time.NewTicker(w.poll())
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
		}
		for _, c := range w.Checks {
			ctx := CheckContext{Step: ref, Session: s, Started: started, Now: w.now(), Repo: w.Repo, Store: w.Store, Plan: w.Plan}
			for _, sig := range c.Run(ctx) {
				w.accept(sig, c.Name(), t.stop)
			}
		}
	}
}

func (w *Watch) Handle(sig Signal) (bool, string) {
	got, err := w.Accept(sig)
	if err != nil {
		return false, err.Error()
	}
	return !got.Rejected, got.RejectReason
}

func (w *Watch) Accept(sig Signal) (Signal, error) {
	return w.accept(sig, "", nil)
}

func (w *Watch) accept(sig Signal, check string, stop <-chan struct{}) (Signal, error) {
	w.init()
	w.mu.Lock()
	w.seq++
	sig.Seq, sig.At = w.seq, w.now()
	runID := w.runID
	if sig.Step.Run != "" {
		runID = sig.Step.Run
	}
	w.mu.Unlock()
	if reason := w.rejection(&sig, runID); reason != "" {
		sig.Rejected, sig.RejectReason = true, reason
	}
	key := sig.Step
	if err := w.Store.Append(runID, Record{Kind: RecordSignal, At: sig.At, Step: &key, Signal: &sig}); err != nil {
		return sig, fmt.Errorf("record signal: %w", err)
	}
	if !sig.Rejected {
		if sig.Kind == SignalHalt {
			w.Release(sig.Step)
		}
		w.forward(sig, stop)
		return sig, nil
	}
	if w.Face != nil {
		w.Face.Emit(Event{At: sig.At, Kind: "signal-rejected", Phase: sig.Step.Phase, Step: sig.Step.Kind, Fields: map[string]string{"source": string(sig.Source), "kind": string(sig.Kind), "reason": sig.RejectReason}})
	}
	if sig.RejectReason == phaseCheckHalt {
		return sig, nil
	}
	fwd := Signal{Seq: sig.Seq, Kind: SignalWarn, Source: sig.Source, Step: sig.Step, Evidence: sig.Evidence, At: sig.At}
	target, ok := w.target()
	if ok {
		fwd.Step = target
	}
	switch {
	case sig.Source == SourceWatchdog:
		fwd.Kind, fwd.Reason = SignalHalt, "watchdog signal rejected: "+sig.RejectReason
		if !ok && w.holdHalt(fwd) {
			return sig, nil
		}
		w.Release(fwd.Step)
	case check != "":
		fwd.Reason = fmt.Sprintf("check %s signal rejected: %s", check, sig.RejectReason)
	default:
		fwd.Reason = "driver signal rejected: " + sig.RejectReason
	}
	w.forward(fwd, stop)
	return sig, nil
}

func (w *Watch) holdHalt(halt Signal) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.live != nil {
		return false
	}
	w.halt = &halt
	return true
}

func (w *Watch) forward(sig Signal, stop <-chan struct{}) {
	select {
	case w.signals <- sig:
		return
	default:
	}
	select {
	case w.signals <- sig:
	case <-stop:
	}
}

func (w *Watch) rejection(sig *Signal, runID string) string {
	if sig.Kind != SignalWarn && sig.Kind != SignalHalt {
		return fmt.Sprintf("kind %q is not warn or halt", sig.Kind)
	}
	if sig.Step.Phase <= 0 {
		return fmt.Sprintf("step %q is not phase-<N>/<kind>", sig.Step.Kind)
	}
	w.mu.Lock()
	checking := w.checking
	w.mu.Unlock()
	if checking != 0 {
		switch {
		case sig.Step.Phase != checking || sig.Step.Kind != "check":
			return fmt.Sprintf("phase-%d/check is the only step during the phase check", checking)
		case sig.Kind == SignalHalt:
			return phaseCheckHalt
		}
		return ""
	}
	if st, err := w.Store.Load(runID); err == nil {
		for _, l := range st.Landed {
			if l.Phase == sig.Step.Phase {
				return fmt.Sprintf("phase %d has landed", l.Phase)
			}
		}
	}
	name := fmt.Sprintf("phase-%d/%s", sig.Step.Phase, sig.Step.Kind)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.live != nil && sameStep(sig.Step, *w.live) {
		sig.Step = *w.live
		return ""
	}
	for k, e := range w.ended {
		if sameStep(sig.Step, k) && e.state == StepOK {
			return name + " is ok"
		}
	}
	cutoff := w.now().Add(-w.poll())
	var recent *StepKey
	for k, e := range w.ended {
		if sameStep(sig.Step, k) && !e.at.Before(cutoff) && (recent == nil || k.Attempt > recent.Attempt) {
			recent = &k
		}
	}
	if recent != nil {
		sig.Step = *recent
		return ""
	}
	return name + " is not running"
}

package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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

	once      sync.Once
	mu        sync.Mutex
	signals   chan Signal
	parked    []Signal
	restarts  chan Restart
	held      *StepKey
	closed    chan struct{}
	runID     string
	live      *StepKey
	ended     map[StepKey]endedStep
	tickers   map[StepKey]*ticking
	seq       int
	halt      *Signal
	accepting int
	settled   *sync.Cond
	checking  string
	gone      bool
	goneHalt  bool
}

var errSignalDropped = errors.New("the signal queue is full; signal dropped")

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
		w.settled = sync.NewCond(&w.mu)
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
	w.mu.Lock()
	w.unpark()
	w.mu.Unlock()
	return w.signals
}

func (w *Watch) Restarts() <-chan Restart {
	w.init()
	return w.restarts
}

func (w *Watch) SeedSignals(signals []Signal) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, sig := range signals {
		if sig.Seq > w.seq {
			w.seq = sig.Seq
		}
	}
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

func (w *Watch) latest(phase, kind string) (StepState, bool) {
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
	w.checking = ph.ID
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.checking = ""
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
		w.haltGone(key)
	}
	return false
}

func (w *Watch) Withdraw(id string) {
	if w.Router != nil {
		w.Router.Withdraw(id)
	}
}

func (w *Watch) WatchdogGone() {
	w.init()
	w.mu.Lock()
	w.gone = true
	live := w.live
	w.mu.Unlock()
	if live != nil {
		w.haltGone(*live)
	}
}

func (w *Watch) Gone() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.gone
}

func (w *Watch) haltGone(key StepKey) {
	w.mu.Lock()
	if w.goneHalt {
		w.mu.Unlock()
		return
	}
	w.goneHalt = w.gone
	w.mu.Unlock()
	w.Accept(Signal{Kind: SignalHalt, Source: SourceDriver, Step: key, Reason: watchdogGone})
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
		if w.halt.Step.Phase == "" || w.halt.Step.Phase == key.Phase {
			w.halt.Step = key
		}
		w.parked = append(w.parked, *w.halt)
		w.halt = nil
		w.unpark()
	}
	gone := w.gone
	w.mu.Unlock()
	go w.tick(ref, s, started, t)
	if gone {
		w.haltGone(key)
	}
	if w.Dog != nil {
		dir := ref.Worktree
		agent := ""
		if s != nil {
			agent = s.Agent
			if s.Dir != "" {
				dir = s.Dir
			}
		}
		w.Dog.Post(fmt.Sprintf("step started phase-%s/%s agent %s worktree %s base %s", key.Phase, key.Kind, agent, dir, ref.Base))
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
		w.Dog.Post(fmt.Sprintf("step ended phase-%s/%s %s %s", ref.Key.Phase, ref.Key.Kind, out.State, out.Reason))
	}
}

func (w *Watch) tick(ref StepRef, s *Session, started time.Time, t *ticking) {
	defer close(t.done)
	if len(w.Checks) == 0 {
		return
	}
	broken := make([]bool, len(w.Checks))
	ticker := time.NewTicker(w.poll())
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
		}
		for i, c := range w.Checks {
			if broken[i] {
				continue
			}
			ctx := CheckContext{Step: ref, Session: s, Started: started, Now: w.now(), Repo: w.Repo, Store: w.Store, Plan: w.Plan}
			broken[i] = !w.runCheck(c, ctx)
		}
	}
}

func (w *Watch) runCheck(c Check, ctx CheckContext) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			key := ctx.Step.Key
			name := "unnamed"
			quietly(func() { name = c.Name() })
			ev, _ := panicked(key.Phase, key.Kind, "watch check "+name, r)
			quietly(func() { recordEvent(w.Store, w.Face, key.Run, ev) })
			ok = false
		}
	}()
	for _, sig := range c.Run(ctx) {
		w.accept(sig, c.Name())
	}
	return true
}

func (w *Watch) Handle(sig Signal) (bool, string) {
	got, err := w.Accept(sig)
	if err != nil {
		return false, err.Error()
	}
	return !got.Rejected, got.RejectReason
}

func (w *Watch) Accept(sig Signal) (Signal, error) {
	return w.accept(sig, "")
}

func (w *Watch) accept(sig Signal, check string) (Signal, error) {
	w.init()
	w.mu.Lock()
	w.seq++
	w.accepting++
	sig.Seq, sig.At = w.seq, w.now()
	runID := w.runID
	if sig.Step.Run != "" {
		runID = sig.Step.Run
	}
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.accepting--
		w.settled.Broadcast()
		w.mu.Unlock()
	}()
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
		if !w.forward(sig) {
			return sig, w.dropped(sig, runID)
		}
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
	if !w.forward(fwd) {
		if err := w.dropped(fwd, runID); !errors.Is(err, errSignalDropped) {
			return sig, err
		}
	}
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

func (w *Watch) Drain() []Signal {
	w.init()
	w.mu.Lock()
	defer w.mu.Unlock()
	for w.accepting > 0 {
		w.settled.Wait()
	}
	var out []Signal
	for {
		select {
		case sig := <-w.signals:
			out = append(out, sig)
		default:
			out = append(out, w.parked...)
			w.parked = nil
			if w.halt != nil {
				out = append(out, *w.halt)
				w.halt = nil
			}
			return out
		}
	}
}

func (w *Watch) unpark() {
	for len(w.parked) > 0 {
		select {
		case w.signals <- w.parked[0]:
			w.parked = w.parked[1:]
		default:
			return
		}
	}
}

func (w *Watch) forward(sig Signal) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.unpark()
	if len(w.parked) == 0 {
		select {
		case w.signals <- sig:
			return true
		default:
		}
	}
	if sig.Kind != SignalHalt {
		return false
	}
	w.parked = append(w.parked, sig)
	return true
}

func (w *Watch) dropped(sig Signal, runID string) error {
	ev := Event{At: w.now(), Kind: "signal-dropped", Phase: sig.Step.Phase, Step: sig.Step.Kind, Fields: map[string]string{"seq": strconv.Itoa(sig.Seq), "kind": string(sig.Kind), "source": string(sig.Source), "reason": sig.Reason}}
	if err := w.Store.Append(runID, Record{Kind: RecordEvent, At: ev.At, Event: &ev}); err != nil {
		return fmt.Errorf("record dropped signal: %w", err)
	}
	if w.Face != nil {
		w.Face.Emit(ev)
	}
	return fmt.Errorf("signal %d: %w", sig.Seq, errSignalDropped)
}

func (w *Watch) rejection(sig *Signal, runID string) string {
	if sig.Kind != SignalWarn && sig.Kind != SignalHalt {
		return fmt.Sprintf("kind %q is not warn or halt", sig.Kind)
	}
	if sig.Step.Phase == "" {
		return fmt.Sprintf("step %q is not phase-<N>/<kind>", sig.Step.Kind)
	}
	w.mu.Lock()
	checking := w.checking
	w.mu.Unlock()
	if checking != "" {
		switch {
		case sig.Step.Phase != checking || sig.Step.Kind != "check":
			return fmt.Sprintf("phase-%s/check is the only step during the phase check", checking)
		case sig.Kind == SignalHalt:
			return phaseCheckHalt
		}
		return ""
	}
	if st, err := w.Store.Load(runID); err == nil {
		for _, l := range st.Landed {
			if l.Phase == sig.Step.Phase {
				return fmt.Sprintf("phase %s has landed", l.Phase)
			}
		}
	}
	name := fmt.Sprintf("phase-%s/%s", sig.Step.Phase, sig.Step.Kind)
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

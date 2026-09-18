package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Hooks struct {
	OnHalt, OnWarn, OnDone string
}

type Lander interface {
	Land(ctx context.Context, phase Phase) (Landing, error)
}

type StepRunner interface {
	Run(ctx context.Context, ref StepRef, obs Observer) Outcome
}

type Watcher interface {
	BeforePhase(ctx context.Context, ph Phase)
	StepStarted(ref StepRef, s *Session)
	StepEnded(ref StepRef, out Outcome)
	Signals() <-chan Signal
	Restarts() <-chan Restart
	Route(ctx context.Context, q Question) (answered bool)
}

type Restart struct {
	Step               StepKey
	Addendum, Provider string
}

type nopWatcher struct{}

func (nopWatcher) BeforePhase(ctx context.Context, ph Phase)             {}
func (nopWatcher) StepStarted(ref StepRef, s *Session)                   {}
func (nopWatcher) StepEnded(ref StepRef, out Outcome)                    {}
func (nopWatcher) Signals() <-chan Signal                                { return nil }
func (nopWatcher) Restarts() <-chan Restart                              { return nil }
func (nopWatcher) Route(ctx context.Context, q Question) (answered bool) { return false }

const invariantQuestion = "invariant: a question never kills a step"

type RunOptions struct {
	From   int
	Phases []int
	Resume bool
}

type RunLoop struct {
	Plan     Plan
	TodoPath string
	Kinds    []StepKind
	Sessions *SessionManager
	Store    Store
	Face     Face
	Notifier Notifier
	Hooks    Hooks
	Lander   Lander
	Runners  map[string]StepRunner
	RunID    string

	Watcher      Watcher
	Ask          AskChannel
	RemedyWindow time.Duration
	MaxRestarts  int

	mu       sync.Mutex
	runDir   string
	live     *Session
	pending  map[int]bool
	blocked  []int
	restarts map[string]int
	open     map[*Session]int
}

func (l *RunLoop) watcher() Watcher {
	if l.Watcher == nil {
		return nopWatcher{}
	}
	return l.Watcher
}

type nopLander struct{}

func (nopLander) Land(ctx context.Context, phase Phase) (Landing, error) {
	return Landing{Phase: phase.Number}, nil
}

func (l *RunLoop) Run(ctx context.Context, opts RunOptions) int {
	l.runDir = l.Store.Dir(l.RunID)
	list, err := RunList(l.Plan, l.TodoPath, opts)
	if err != nil {
		return l.usage(err)
	}
	base, err := l.Sessions.Repo.HeadBranch()
	if err != nil {
		return l.usage(fmt.Errorf("head branch: %w", err))
	}
	var prior RunState
	if opts.Resume {
		if prior, err = l.Store.Load(l.RunID); err != nil {
			return l.usage(fmt.Errorf("load run %s: %w", l.RunID, err))
		}
		l.emit(Event{Kind: "human", Fields: map[string]string{"what": "resume"}})
	}
	l.pending = map[int]bool{}
	l.restarts = map[string]int{}
	for _, ev := range prior.Events {
		if ev.Kind == "restart" {
			l.restarts[ev.Fields["step"]]++
		}
	}
	if l.Ask != nil {
		qctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go l.serveQuestions(qctx)
	}
	for _, ph := range list {
		if !landed(prior, ph.Number) {
			l.pending[ph.Number] = true
		}
	}
	l.setRun(RunRunning, "")
	first, firstPhase, firstStep, firstReason := 0, 0, "", ""
	for _, ph := range list {
		if !l.pending[ph.Number] {
			continue
		}
		delete(l.pending, ph.Number)
		step, out, aborted := l.runPhase(ctx, ph, prior, base)
		if aborted {
			return 1
		}
		if out.State == StepOK {
			continue
		}
		code := l.block(ph, step, out)
		if first == 0 {
			first, firstPhase, firstStep, firstReason = code, ph.Number, step, out.Reason
		}
	}
	if len(l.blocked) == 0 {
		l.setRun(RunFinished, "")
		l.emit(Event{Kind: "finished"})
		l.fire(l.Hooks.OnDone, "finished", 0, "", "")
		return 0
	}
	l.setRun(RunHalted, firstReason)
	slices.Sort(l.blocked)
	l.emit(Event{Kind: "halt", Fields: map[string]string{"blocked": joinInts(l.blocked), "resume": "r-loop resume"}})
	l.fire(l.Hooks.OnHalt, "halted", firstPhase, firstStep, firstReason)
	return first
}

func (l *RunLoop) usage(err error) int {
	l.Face.Emit(Event{At: time.Now(), Kind: "error", Fields: map[string]string{"reason": err.Error()}})
	return 2
}

func RunList(plan Plan, todoPath string, opts RunOptions) ([]Phase, error) {
	unticked := plan.Unticked()
	for _, n := range opts.Phases {
		if !slices.Contains(unticked, n) {
			return nil, fmt.Errorf("phase %d is ticked or absent from %s", n, todoPath)
		}
	}
	var list []Phase
	for _, ph := range plan.Phases {
		n := ph.Number
		switch {
		case !slices.Contains(unticked, n):
		case len(opts.Phases) > 0 && !slices.Contains(opts.Phases, n):
		case n < opts.From:
		default:
			list = append(list, ph)
		}
	}
	slices.SortFunc(list, func(a, b Phase) int { return a.Number - b.Number })
	return list, nil
}

func landed(st RunState, phase int) bool {
	for _, l := range st.Landed {
		if l.Phase == phase {
			return true
		}
	}
	return false
}

func latestAttempt(st RunState, run string, phase int, kind string) (int, StepState) {
	attempt, state := 0, StepState("")
	for key, s := range st.Steps {
		if key.Run == run && key.Phase == phase && key.Kind == kind && key.Attempt > attempt {
			attempt, state = key.Attempt, s
		}
	}
	return attempt, state
}

func (l *RunLoop) runPhase(ctx context.Context, ph Phase, prior RunState, base string) (string, Outcome, bool) {
	n := ph.Number
	l.emit(Event{Kind: "phase-start", Phase: n, Fields: map[string]string{"phase": strconv.Itoa(n), "title": ph.Title}})
	last := &Session{Dir: filepath.Join(l.Sessions.Repo.Root(), fmt.Sprintf(".r-loop/wt/phase-%d", n))}
	l.watcher().BeforePhase(ctx, ph)
	for _, kind := range l.Kinds {
		attempt, state := latestAttempt(prior, l.RunID, n, kind.Name)
		if state == StepOK {
			l.advance(n, kind.Name)
			continue
		}
		ref, out, aborted := l.runAttempts(ctx, l.ref(ph, kind, attempt+1, base))
		if out.Session != nil {
			last = out.Session
		}
		if aborted || out.State != StepOK {
			return kind.Name, out, aborted
		}
		if out.Warning != "" {
			l.emit(Event{Kind: "warning", Phase: n, Step: kind.Name, Fields: map[string]string{"reason": out.Warning}})
			l.fire(l.Hooks.OnWarn, "warning", n, kind.Name, out.Warning)
		}
		l.advance(n, kind.Name)
		if kind.Name == "plan" && out.Session != nil {
			planPath, _ := ref.Vars["PlanPath"].(string)
			for _, a := range PlanAssumptions(planPath, os.DirFS(out.Session.Dir)) {
				l.emit(Event{Kind: "assumption", Phase: n, Step: kind.Name, Fields: map[string]string{"phase": strconv.Itoa(n), "text": a}})
			}
		}
	}
	lander := l.Lander
	if lander == nil {
		lander = nopLander{}
	}
	if l.Store.Aborted(l.RunID) {
		l.abort(n, "land")
		return "land", Outcome{}, true
	}
	landing, err := lander.Land(ctx, ph)
	if err != nil {
		return "land", Outcome{State: StepFailed, Reason: "land: " + err.Error(), Session: last}, false
	}
	l.emit(Event{Kind: "phase-state", Phase: n, Fields: map[string]string{"phase": strconv.Itoa(n), "state": string(PhaseLanded)}})
	l.emit(Event{Kind: "landed", Phase: n, Fields: map[string]string{"phase": strconv.Itoa(n), "merge": landing.MergeSHA, "gateSkipped": strconv.FormatBool(landing.GateSkipped)}})
	return "", Outcome{State: StepOK}, false
}

func (l *RunLoop) runAttempts(ctx context.Context, ref StepRef) (StepRef, Outcome, bool) {
	kind := ref.Kind
	for {
		out, aborted := l.runStep(ctx, ref)
		if aborted || out.State == StepOK {
			return ref, out, aborted
		}
		next, ok := l.awaitRestart(ctx, ref, kind, out)
		if !ok {
			return ref, out, false
		}
		ref = next
	}
}

func (l *RunLoop) awaitRestart(ctx context.Context, ref StepRef, kind StepKind, out Outcome) (StepRef, bool) {
	if l.RemedyWindow <= 0 || out.Halted {
		return StepRef{}, false
	}
	key := ref.Key
	step := fmt.Sprintf("phase-%d/%s", key.Phase, key.Kind)
	timer := time.NewTimer(l.RemedyWindow)
	defer timer.Stop()
	for {
		var rs Restart
		select {
		case <-ctx.Done():
			return StepRef{}, false
		case <-timer.C:
			return StepRef{}, false
		case rs = <-l.watcher().Restarts():
		}
		if rs.Step != key {
			continue
		}
		if l.restarts[step] >= l.MaxRestarts {
			l.emit(Event{Kind: "restart-refused", Phase: key.Phase, Step: key.Kind, Fields: map[string]string{"step": step, "reason": fmt.Sprintf("restart limit %d reached", l.MaxRestarts)}})
			return StepRef{}, false
		}
		l.restarts[step]++
		if rs.Provider != "" {
			kind.Row.Provider, kind.Row.Model, kind.Row.Effort = rs.Provider, "", ""
			if fb := kind.Row.Fallback; fb.Provider == rs.Provider {
				kind.Row.Model, kind.Row.Effort = fb.Model, fb.Effort
			}
		}
		next := l.ref(ref.Phase, kind, key.Attempt+1, ref.Base)
		next.Vars["Addendum"] = rs.Addendum
		l.emit(Event{Kind: "restart", Phase: key.Phase, Step: key.Kind, Fields: map[string]string{"step": step, "attempt": strconv.Itoa(next.Key.Attempt), "addendum": rs.Addendum, "provider": rs.Provider}})
		return next, true
	}
}

func (l *RunLoop) advance(phase int, kind string) {
	state := map[string]PhaseState{"plan": PhasePlanned, "implement": PhaseImplemented}[kind]
	if state == "" {
		return
	}
	l.emit(Event{Kind: "phase-state", Phase: phase, Fields: map[string]string{"phase": strconv.Itoa(phase), "state": string(state)}})
}

func (l *RunLoop) ref(ph Phase, kind StepKind, attempt int, base string) StepRef {
	n := ph.Number
	ref := StepRef{
		Key:      StepKey{Run: l.RunID, Phase: n, Kind: kind.Name, Attempt: attempt},
		Kind:     kind,
		Phase:    ph,
		Worktree: fmt.Sprintf(".r-loop/wt/phase-%d", n),
		Branch:   fmt.Sprintf("r-loop/phase-%d", n),
		Base:     base,
		RunDir:   l.runDir,
	}
	ref.Vars = StepVars(ref, l.Plan, l.TodoPath, l.runDir)
	return ref
}

func (l *RunLoop) runStep(ctx context.Context, ref StepRef) (Outcome, bool) {
	runner := l.Runners[ref.Kind.Check]
	if runner == nil {
		runner = singleRunner{sm: l.Sessions}
	}
	if l.Store.Aborted(l.RunID) {
		l.abort(ref.Key.Phase, ref.Key.Kind)
		return Outcome{}, true
	}
	l.setLive(nil)
	key := ref.Key
	if err := l.Store.Append(l.RunID, Record{Kind: RecordStep, At: time.Now(), Step: &key, State: StepQueued}); err != nil {
		return Outcome{State: StepFailed, Reason: "record: " + err.Error()}, false
	}
	l.emitStep(ref, StepQueued, "", nil)
	stepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan Outcome, 1)
	go func() { done <- runner.Run(stepCtx, ref, &loopObserver{l: l, ref: ref}) }()
	poll := l.Sessions.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case out := <-done:
			l.emitStep(ref, out.State, out.Reason, out.Session)
			return l.ended(ref, out)
		case sig := <-l.watcher().Signals():
			if sig.Kind != SignalHalt {
				l.warn(sig)
				continue
			}
			if !sameStep(sig.Step, key) {
				sig.Reason = fmt.Sprintf("halt for phase-%d/%s after it ended: %s", sig.Step.Phase, sig.Step.Kind, sig.Reason)
				l.warn(sig)
				continue
			}
			if s := l.liveSession(); s != nil {
				l.Sessions.Stop(s)
			}
			cancel()
			out := <-done
			out.State, out.Reason, out.Stalled, out.Halted = StepFailed, "watchdog: "+sig.Reason, false, true
			if err := l.Store.Append(l.RunID, Record{Kind: RecordStep, At: time.Now(), Step: &key, State: StepFailed, Reason: out.Reason}); err != nil {
				out.Reason += "; record: " + err.Error()
			}
			l.emitStep(ref, out.State, out.Reason, out.Session)
			return l.ended(ref, out)
		case <-ticker.C:
			if !l.Store.Aborted(l.RunID) {
				continue
			}
			cancel()
			out := <-done
			l.emitStep(ref, out.State, out.Reason, out.Session)
			l.watcher().StepEnded(ref, out)
			l.abort(ref.Key.Phase, ref.Key.Kind)
			return out, true
		}
	}
}

func (l *RunLoop) ended(ref StepRef, out Outcome) (Outcome, bool) {
	l.watcher().StepEnded(ref, out)
	if out.State != StepFailed || !strings.HasPrefix(out.Reason, "backstop") || out.Session == nil || !out.Session.OpenQuestion.Load() {
		return out, false
	}
	l.setRun(RunHalted, invariantQuestion)
	l.emit(Event{Kind: "halt", Phase: ref.Key.Phase, Step: ref.Key.Kind, Fields: map[string]string{"reason": invariantQuestion}})
	l.fire(l.Hooks.OnHalt, "halted", ref.Key.Phase, ref.Key.Kind, invariantQuestion)
	return out, true
}

func sameStep(a, b StepKey) bool {
	return a.Phase == b.Phase && a.Kind == b.Kind && (a.Attempt == 0 || a.Attempt == b.Attempt)
}

func (l *RunLoop) warn(sig Signal) {
	l.emit(Event{Kind: "warning", Phase: sig.Step.Phase, Step: sig.Step.Kind, Fields: map[string]string{"reason": sig.Reason, "source": string(sig.Source)}})
	l.fire(l.Hooks.OnWarn, "warning", sig.Step.Phase, sig.Step.Kind, sig.Reason)
}

func (l *RunLoop) serveQuestions(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case q, ok := <-l.Ask.Questions():
			if !ok {
				return
			}
			go l.question(ctx, q)
		}
	}
}

func (l *RunLoop) question(ctx context.Context, q Question) {
	if q.Step.Kind == "watchdog" {
		l.recordQuestion(q)
		l.emit(Event{Kind: "question", Step: q.Step.Kind, Fields: map[string]string{"id": q.ID, "text": q.Text}})
		l.askFace(q)
		return
	}
	s := l.askingSession(ctx, q.Step)
	if s == nil {
		return
	}
	if l.openQuestion(s, 1) == 1 {
		l.stepState(s, StepWaitingInput)
	}
	l.recordQuestion(q)
	l.emit(Event{Kind: "question", Phase: q.Step.Phase, Step: q.Step.Kind, Fields: map[string]string{"id": q.ID, "text": q.Text}})
	if !l.watcher().Route(ctx, q) && !l.askFace(q) {
		return
	}
	if l.openQuestion(s, -1) == 0 {
		l.stepState(s, StepRunning)
	}
}

func (l *RunLoop) askFace(q Question) bool {
	answer, err := l.Face.Ask(q)
	if err != nil {
		if !errors.Is(err, ErrNoInput) {
			l.emit(Event{Kind: "warning", Phase: q.Step.Phase, Step: q.Step.Kind, Fields: map[string]string{"reason": "ask " + q.ID + ": " + err.Error()}})
		}
		return false
	}
	if err := l.Ask.Answer(q.ID, answer, "maintainer", ""); err != nil {
		l.emit(Event{Kind: "warning", Phase: q.Step.Phase, Step: q.Step.Kind, Fields: map[string]string{"reason": "answer " + q.ID + ": " + err.Error()}})
		return false
	}
	q.Answer, q.AnsweredBy, q.AnsweredAt = answer, "maintainer", time.Now()
	l.recordQuestion(q)
	l.emit(Event{Kind: "human", Phase: q.Step.Phase, Step: q.Step.Kind, Fields: map[string]string{"what": "answer", "id": q.ID}})
	return true
}

func (l *RunLoop) openQuestion(s *Session, delta int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open == nil {
		l.open = map[*Session]int{}
	}
	l.open[s] += delta
	n := l.open[s]
	s.OpenQuestion.Store(n > 0)
	if n == 0 {
		delete(l.open, s)
	}
	return n
}

func (l *RunLoop) askingSession(ctx context.Context, key StepKey) *Session {
	poll := l.Sessions.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	for {
		if s := l.liveSession(); s != nil {
			k := s.Ref.Key
			if k.Run == key.Run && k.Phase == key.Phase && k.Attempt == key.Attempt && (key.Kind == k.Kind || strings.HasPrefix(key.Kind, k.Kind+"-rv-")) {
				return s
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(poll):
		}
	}
}

func (l *RunLoop) stepState(s *Session, state StepState) {
	key := s.Ref.Key
	if err := l.Store.Append(l.RunID, Record{Kind: RecordStep, At: time.Now(), Step: &key, State: state}); err != nil {
		l.emit(Event{Kind: "warning", Fields: map[string]string{"reason": "store: " + err.Error()}})
	}
	l.emitStep(s.Ref, state, "", s)
}

func (l *RunLoop) recordQuestion(q Question) {
	if err := l.Store.Append(l.RunID, Record{Kind: RecordQuestion, At: time.Now(), Question: &q}); err != nil {
		l.emit(Event{Kind: "warning", Fields: map[string]string{"reason": "store: " + err.Error()}})
	}
}

func (l *RunLoop) abort(phase int, step string) {
	l.setRun(RunHalted, ReasonAborted)
	ws, wt := sessionPlace(l.liveSession())
	l.emit(Event{Kind: "aborted", Phase: phase, Step: step, Fields: map[string]string{"workspace": ws, "worktree": wt}})
}

func (l *RunLoop) block(ph Phase, step string, out Outcome) int {
	n := ph.Number
	l.blocked = append(l.blocked, n)
	ws, wt := sessionPlace(out.Session)
	l.emit(Event{Kind: "phase-blocked", Phase: n, Step: step, Fields: map[string]string{"phase": strconv.Itoa(n), "reason": out.Reason, "workspace": ws, "worktree": wt}})
	l.fire(l.Hooks.OnWarn, "blocked", n, step, out.Reason)
	for _, d := range l.dependents(n) {
		if !l.pending[d] {
			continue
		}
		delete(l.pending, d)
		l.blocked = append(l.blocked, d)
		l.emit(Event{Kind: "phase-skipped", Phase: d, Fields: map[string]string{"phase": strconv.Itoa(d), "because": strconv.Itoa(n)}})
	}
	if out.Halted {
		return 5
	}
	if out.Stalled {
		return 3
	}
	return 1
}

func (l *RunLoop) dependents(n int) []int {
	reached := map[int]bool{n: true}
	for changed := true; changed; {
		changed = false
		for _, ph := range l.Plan.Phases {
			if reached[ph.Number] {
				continue
			}
			for _, d := range ph.DependsOn {
				if reached[d] {
					reached[ph.Number], changed = true, true
					break
				}
			}
		}
	}
	var out []int
	for _, ph := range l.Plan.Phases {
		if reached[ph.Number] && ph.Number != n {
			out = append(out, ph.Number)
		}
	}
	slices.Sort(out)
	return out
}

func sessionPlace(s *Session) (string, string) {
	if s == nil {
		return "", ""
	}
	return s.Workspace, s.Dir
}

func (l *RunLoop) setLive(s *Session) {
	l.mu.Lock()
	l.live = s
	l.mu.Unlock()
}

func (l *RunLoop) liveSession() *Session {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.live
}

type loopObserver struct {
	l   *RunLoop
	ref StepRef
}

func (o *loopObserver) Started(s *Session) {
	o.l.setLive(s)
	o.l.emitStep(o.ref, StepSpawned, "", s)
	o.l.emitStep(o.ref, StepRunning, "", s)
	o.l.watcher().StepStarted(o.ref, s)
}

func (o *loopObserver) Stalled(s *Session) {
	o.l.emitStep(o.ref, StepStalled, "", s)
	o.l.emit(Event{Kind: "stalled", Phase: o.ref.Key.Phase, Step: o.ref.Key.Kind, Fields: map[string]string{"workspace": s.Workspace, "worktree": s.Dir}})
}

func (o *loopObserver) Resumed(s *Session) {
	o.l.emitStep(o.ref, StepRunning, "", s)
}

func (l *RunLoop) emitStep(ref StepRef, state StepState, reason string, s *Session) {
	ws, _ := sessionPlace(s)
	ev := Event{At: time.Now(), Kind: "step", Phase: ref.Key.Phase, Step: ref.Key.Kind, Fields: map[string]string{
		"state":     string(state),
		"attempt":   strconv.Itoa(ref.Key.Attempt),
		"provider":  ref.Kind.Row.Provider,
		"reason":    reason,
		"workspace": ws,
	}}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Face.Emit(ev)
	l.writeReport()
}

func (l *RunLoop) emit(ev Event) {
	ev.At = time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Store.Append(l.RunID, Record{Kind: RecordEvent, At: ev.At, Event: &ev}); err != nil {
		l.Face.Emit(Event{At: ev.At, Kind: "warning", Fields: map[string]string{"reason": "store: " + err.Error()}})
	}
	l.Face.Emit(ev)
	l.writeReport()
}

func (l *RunLoop) setRun(status RunStatus, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Store.Append(l.RunID, Record{Kind: RecordRun, At: time.Now(), Run: status, Reason: reason}); err != nil {
		l.Face.Emit(Event{At: time.Now(), Kind: "warning", Fields: map[string]string{"reason": "store: " + err.Error()}})
	}
	l.writeReport()
}

func (l *RunLoop) writeReport() {
	st, err := l.Store.Load(l.RunID)
	if err == nil {
		err = os.WriteFile(l.reportPath(), []byte(Report(st, l.Plan)), 0o644)
	}
	if err != nil {
		l.Face.Emit(Event{At: time.Now(), Kind: "warning", Fields: map[string]string{"reason": "report: " + err.Error()}})
	}
}

func (l *RunLoop) reportPath() string {
	return filepath.Join(l.runDir, "report.md")
}

func (l *RunLoop) fire(hook, status string, phase int, step, reason string) {
	if l.Notifier == nil {
		return
	}
	p := ""
	if phase > 0 {
		p = strconv.Itoa(phase)
	}
	l.Notifier.Fire(hook, map[string]string{
		"R_LOOP_RUN":    l.RunID,
		"R_LOOP_STATUS": status,
		"R_LOOP_PHASE":  p,
		"R_LOOP_STEP":   step,
		"R_LOOP_REASON": reason,
		"R_LOOP_TODO":   l.TodoPath,
		"R_LOOP_REPORT": l.reportPath(),
	})
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

func StepVars(ref StepRef, plan Plan, todoPath, runDir string) map[string]any {
	ph := ref.Phase
	var criteria []string
	for _, it := range ph.Items {
		if !it.Done {
			criteria = append(criteria, "- [ ] "+it.Text)
		}
	}
	msName, msPhases := "", ""
	for _, m := range plan.Milestones {
		if m.Number == ph.Milestone {
			msName, msPhases = m.Name, joinInts(m.Phases)
		}
	}
	return map[string]any{
		"PhaseNumber":     ph.Number,
		"PhaseTitle":      ph.Title,
		"PhaseBlock":      ph.Block,
		"Criteria":        strings.Join(criteria, "\n"),
		"TodoPath":        todoPath,
		"SpecDir":         filepath.Dir(todoPath),
		"PlanPath":        phasePlanPath(ph.Number, ph.Title),
		"Branch":          ref.Branch,
		"Base":            ref.Base,
		"Worktree":        ref.Worktree,
		"Sentinel":        "",
		"RunDir":          runDir,
		"AskURL":          ref.AskURL,
		"PhaseWarnings":   "",
		"ReviewedKind":    "",
		"Round":           0,
		"Rounds":          0,
		"ReviewCommand":   "",
		"FindingsPath":    "",
		"FindingsFiles":   []FindingsFile(nil),
		"PriorFindings":   "",
		"PriorVerdicts":   "",
		"RoundTree":       "",
		"VerdictPath":     "",
		"ReportPath":      "",
		"MilestoneName":   msName,
		"MilestonePhases": msPhases,
		"Addendum":        "",
	}
}

func phasePlanPath(n int, title string) string {
	prefix := fmt.Sprintf(".task-plans/phase-%d", n)
	slug := kebab(title)
	if room := 60 - len(prefix) - len("-.md"); len(slug) > room {
		slug = strings.TrimRight(slug[:room], "-")
	}
	if slug == "" {
		return prefix + ".md"
	}
	return prefix + "-" + slug + ".md"
}

func kebab(s string) string {
	var b strings.Builder
	gap := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if gap && b.Len() > 0 {
				b.WriteByte('-')
			}
			gap = false
			b.WriteRune(r)
			continue
		}
		gap = true
	}
	return b.String()
}

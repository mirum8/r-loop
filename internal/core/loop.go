package core

import (
	"context"
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

	mu      sync.Mutex
	runDir  string
	live    *Session
	pending map[int]bool
	blocked []int
}

type nopLander struct{}

func (nopLander) Land(ctx context.Context, phase Phase) (Landing, error) {
	return Landing{Phase: phase.Number}, nil
}

func (l *RunLoop) Run(ctx context.Context, opts RunOptions) int {
	l.runDir = l.Store.Dir(l.RunID)
	list, err := l.runList(opts)
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

func (l *RunLoop) runList(opts RunOptions) ([]Phase, error) {
	unticked := l.Plan.Unticked()
	for _, n := range opts.Phases {
		if !slices.Contains(unticked, n) {
			return nil, fmt.Errorf("phase %d is ticked or absent from %s", n, l.TodoPath)
		}
	}
	var list []Phase
	for _, ph := range l.Plan.Phases {
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
	for _, kind := range l.Kinds {
		attempt, state := latestAttempt(prior, l.RunID, n, kind.Name)
		if state == StepOK {
			l.advance(n, kind.Name)
			continue
		}
		ref := l.ref(ph, kind, attempt+1, base)
		out, aborted := l.runStep(ctx, ref)
		if out.Session != nil {
			last = out.Session
		}
		if aborted || out.State != StepOK {
			return kind.Name, out, aborted
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
			return out, false
		case <-ticker.C:
			if !l.Store.Aborted(l.RunID) {
				continue
			}
			cancel()
			out := <-done
			l.emitStep(ref, out.State, out.Reason, out.Session)
			l.abort(ref.Key.Phase, ref.Key.Kind)
			return out, true
		}
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
		"FindingsFiles":   "",
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

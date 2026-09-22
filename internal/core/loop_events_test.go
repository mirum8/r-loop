package core

import (
	"context"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeWatcher struct {
	log      *callLog
	signals  chan Signal
	restarts chan Restart
	started  func(StepRef, *Session)
	ended    func(StepRef, Outcome)
	route    func(context.Context, Question) bool
}

func (w *fakeWatcher) BeforePhase(ctx context.Context, ph Phase, base string) CheckOutcome {
	w.log.record("Watcher.BeforePhase %d", ph.Number)
	return CheckOutcome{}
}

func (w *fakeWatcher) StepStarted(ref StepRef, s *Session) {
	w.log.record("Watcher.StepStarted %d %s %d", ref.Key.Phase, ref.Key.Kind, ref.Key.Attempt)
	if w.started != nil {
		w.started(ref, s)
	}
}

func (w *fakeWatcher) StepEnded(ref StepRef, out Outcome) {
	w.log.record("Watcher.StepEnded %d %s %d %s", ref.Key.Phase, ref.Key.Kind, ref.Key.Attempt, out.State)
	if w.ended != nil {
		w.ended(ref, out)
	}
}

func (w *fakeWatcher) Signals() <-chan Signal   { return w.signals }
func (w *fakeWatcher) Restarts() <-chan Restart { return w.restarts }

func (w *fakeWatcher) Route(ctx context.Context, q Question) bool {
	w.log.record("Watcher.Route %s", q.ID)
	return w.route != nil && w.route(ctx, q)
}

type eventsHost struct {
	*agentSim
	ask   *eventsAsk
	asked map[string]int
}

func (h *eventsHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	h.mu.Lock()
	b := h.behaviour[agent]
	h.mu.Unlock()
	if b == "hold" || b == "ask" || b == "ask-noinput" || b == "ask-fail" {
		h.record("SessionHost.Prompt %s", agent)
		return nil
	}
	return h.agentSim.Prompt(agent, text, wait, timeout)
}

func (h *eventsHost) spec(agent string) OpenSpec {
	return h.Started[agent]
}

func (h *eventsHost) finish(agent string) {
	h.repo.setChanges("code.go")
	writeFile(h.spec(agent).Env["R_LOOP_SENTINEL"], `{"outcome":"ok","reason":"","at":"2026-09-18T10:05:00Z"}`)
}

func (h *eventsHost) State(agent string) (AgentState, error) {
	h.mu.Lock()
	b := h.behaviour[agent]
	n := h.asked[agent]
	h.asked[agent] = n + 1
	h.mu.Unlock()
	if b != "ask" && b != "ask-noinput" && b != "ask-fail" {
		return h.agentSim.State(agent)
	}
	if n == 0 {
		env := h.spec(agent).Env
		phase, _ := strconv.Atoi(env["R_LOOP_PHASE"])
		key := StepKey{Run: env["R_LOOP_RUN"], Phase: phase, Kind: env["R_LOOP_STEP"], Attempt: 1}
		h.ask.Asked <- Question{ID: "q1", Step: key, Text: "which db?", Options: []string{"sqlite", "postgres"}}
		h.waitRecord(key, StepWaitingInput, 1)
	}
	if b == "ask" && n == 1 {
		h.waitRecord(StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}, StepRunning, 2)
		h.finish(agent)
	}
	if b == "ask-noinput" && n == 10 {
		h.finish(agent)
	}
	if b == "ask-fail" && n == 10 {
		writeFile(h.spec(agent).Env["R_LOOP_SENTINEL"], `{"outcome":"failed","reason":"gave up","at":"2026-09-18T10:05:00Z"}`)
	}
	return AgentWorking, nil
}

func (h *eventsHost) waitRecord(key StepKey, state StepState, n int) {
	for range 5000 {
		h.store.mu.Lock()
		recs := slices.Clone(h.store.Records[key.Run])
		h.store.mu.Unlock()
		seen := 0
		for _, rec := range recs {
			if rec.Kind == RecordStep && *rec.Step == key && rec.State == state {
				seen++
			}
		}
		if seen >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

type eventsAsk struct {
	fakeAskChannel
	answered func(id string)
}

func (a *eventsAsk) Answer(id, answer, by, citation string) error {
	a.fakeAskChannel.Answer(id, answer, by, citation)
	if a.answered != nil {
		a.answered(id)
	}
	return nil
}

type varsPrompts struct {
	mu        sync.Mutex
	addendums map[string]string
}

func (p *varsPrompts) Render(name string, vars map[string]any) (string, string, error) {
	p.mu.Lock()
	p.addendums[vars["Sentinel"].(string)] = vars["Addendum"].(string)
	p.mu.Unlock()
	return vars["PlanPath"].(string), "embedded:" + name, nil
}

type eventsRig struct {
	*loopRig
	watcher  *fakeWatcher
	ask      *eventsAsk
	ehost    *eventsHost
	prompts  *varsPrompts
	resolved []string
}

func newEventsRig(t *testing.T) *eventsRig {
	r := &eventsRig{loopRig: newLoopRig(t)}
	r.watcher = &fakeWatcher{log: r.shared, signals: make(chan Signal), restarts: make(chan Restart, 8)}
	r.ask = &eventsAsk{fakeAskChannel: fakeAskChannel{callLog: callLog{Shared: r.shared}, Asked: make(chan Question, 1)}}
	r.ehost = &eventsHost{agentSim: r.host, ask: r.ask, asked: map[string]int{}}
	r.prompts = &varsPrompts{addendums: map[string]string{}}
	sm := r.loop.Sessions
	sm.Host = r.ehost
	sm.Prompts = r.prompts
	var mu sync.Mutex
	sm.Resolve = func(provider, model, effort, askURL, mcp string) (ProviderArgs, error) {
		mu.Lock()
		r.resolved = append(r.resolved, provider+"/"+model+"/"+effort)
		mu.Unlock()
		return ProviderArgs{Kind: provider}, nil
	}
	r.loop.Watcher = r.watcher
	r.loop.Ask = r.ask
	r.loop.MaxRestarts = 2
	return r
}

func (r *eventsRig) agents() []string {
	return r.host.Agents
}

func (r *eventsRig) stepStates(kind string) []string {
	var out []string
	for _, ev := range r.events("step") {
		if ev.Step == kind {
			out = append(out, ev.Fields["state"])
		}
	}
	return out
}

func (r *eventsRig) restartOnFailure() {
	r.watcher.ended = func(ref StepRef, out Outcome) {
		if out.State == StepFailed {
			r.watcher.restarts <- Restart{Step: ref.Key, Addendum: "use the fake"}
		}
	}
}

func TestWarnSignalIsEmittedAndTheRunContinues(t *testing.T) {
	r := newEventsRig(t)
	r.watcher.started = func(ref StepRef, s *Session) {
		if ref.Key.Kind == "plan" {
			r.watcher.signals <- Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: ref.Key, Reason: "drifting"}
		}
	}

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	warnings := r.events("warning")
	if len(warnings) != 1 || warnings[0].Fields["reason"] != "drifting" || warnings[0].Phase != 2 || warnings[0].Step != "plan" {
		t.Errorf("warnings %+v", warnings)
	}
	if got := r.hooks(); !reflect.DeepEqual(got, []string{"warn-hook warning", "done-hook finished"}) {
		t.Errorf("hooks %v", got)
	}
	if env := r.notifier.Fired[0]; env["R_LOOP_REASON"] != "drifting" || env["R_LOOP_PHASE"] != "2" || env["R_LOOP_STEP"] != "plan" {
		t.Errorf("warn env %v", env)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"2"}) {
		t.Errorf("landed %v", got)
	}
}

func TestHaltSignalStopsTheSessionAndExits5(t *testing.T) {
	r := newEventsRig(t)
	r.host.behaviour["rloop-p1-implement"] = "hold"
	r.watcher.started = func(ref StepRef, s *Session) {
		if ref.Key.Phase == 1 && ref.Key.Kind == "implement" {
			r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: ref.Key, Reason: "rewriting the spec"}
		}
	}

	code := r.run(RunOptions{})

	if code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	if got := r.calls("SessionHost.Interrupt "); !reflect.DeepEqual(got, []string{"rloop-p1-implement"}) {
		t.Errorf("interrupted %v", got)
	}
	var failed []string
	for _, rec := range r.store.Records["run-1"] {
		if rec.Kind == RecordStep && rec.Step.Phase == 1 && rec.Step.Kind == "implement" && rec.State == StepFailed {
			failed = append(failed, rec.Reason)
		}
	}
	if !reflect.DeepEqual(failed, []string{"watchdog: rewriting the spec"}) {
		t.Errorf("failed implement records %q", failed)
	}
	blocked := r.events("phase-blocked")
	if len(blocked) != 1 || blocked[0].Fields["phase"] != "1" || blocked[0].Fields["reason"] != "watchdog: rewriting the spec" {
		t.Errorf("blocked %+v", blocked)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"2"}) {
		t.Errorf("landed %v", got)
	}
}

func TestRestartInsideTheWindowRerunsAsAttempt2WithTheAddendum(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	r.host.behaviour["rloop-p2-implement"] = "fail"
	r.restartOnFailure()

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	want := []string{"rloop-p2-plan", "rloop-p2-implement", "rloop-p2-implement-a2"}
	if got := r.agents(); !reflect.DeepEqual(got, want) {
		t.Errorf("spawned %v, want %v", got, want)
	}
	a2 := r.host.Opened[2]
	if got := r.prompts.addendums[a2.Env["R_LOOP_SENTINEL"]]; got != "use the fake" {
		t.Errorf("attempt 2 addendum %q", got)
	}
	if got := r.prompts.addendums[r.host.Opened[1].Env["R_LOOP_SENTINEL"]]; got != "" {
		t.Errorf("attempt 1 addendum %q", got)
	}
	if a2.CWD != r.host.Opened[1].CWD {
		t.Errorf("attempt 2 in %s, attempt 1 in %s", a2.CWD, r.host.Opened[1].CWD)
	}
	restarts := r.events("restart")
	if len(restarts) != 1 || restarts[0].Fields["attempt"] != "2" || restarts[0].Fields["addendum"] != "use the fake" || restarts[0].Step != "implement" {
		t.Errorf("restart events %+v", restarts)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"2"}) {
		t.Errorf("landed %v", got)
	}
}

func TestRestartOnAProviderUsesTheFallbackModelAndEffortOrNone(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"claude", "claude/sonnet/low"},
		{"gemini", "gemini//"},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			r := newEventsRig(t)
			r.loop.RemedyWindow = time.Minute
			kind := r.loop.Kinds[1]
			kind.Row.Model, kind.Row.Effort = "gpt", "high"
			kind.Row.Fallback = Fallback{Provider: "claude", Model: "sonnet", Effort: "low"}
			r.loop.Kinds[1] = kind
			r.host.behaviour["rloop-p2-implement"] = "fail"
			r.watcher.ended = func(ref StepRef, out Outcome) {
				if out.State == StepFailed {
					r.watcher.restarts <- Restart{Step: ref.Key, Provider: c.provider}
				}
			}

			code := r.run(RunOptions{Phases: []int{2}})

			if code != 0 {
				t.Fatalf("exit %d", code)
			}
			want := []string{"codex//", "codex/gpt/high", c.want}
			if !reflect.DeepEqual(r.resolved, want) {
				t.Errorf("resolved %v, want %v", r.resolved, want)
			}
			if got := r.stepEvents("implement", "provider"); got[len(got)-1] != c.provider {
				t.Errorf("attempt 2 events name provider %v", got)
			}
		})
	}
}

func (r *eventsRig) stepEvents(kind, field string) []string {
	var out []string
	for _, ev := range r.events("step") {
		if ev.Step == kind {
			out = append(out, ev.Fields[field])
		}
	}
	return out
}

func TestAThirdRestartIsRefusedAtMaxRestarts2(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	for _, a := range []string{"rloop-p2-implement", "rloop-p2-implement-a2", "rloop-p2-implement-a3", "rloop-p2-implement-a4"} {
		r.host.behaviour[a] = "fail"
	}
	r.restartOnFailure()

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	want := []string{"rloop-p2-plan", "rloop-p2-implement", "rloop-p2-implement-a2", "rloop-p2-implement-a3"}
	if got := r.agents(); !reflect.DeepEqual(got, want) {
		t.Errorf("spawned %v, want %v", got, want)
	}
	refused := r.events("restart-refused")
	if len(refused) != 1 || refused[0].Step != "implement" || !strings.Contains(refused[0].Fields["reason"], "2") {
		t.Errorf("restart-refused %+v", refused)
	}
	stored := 0
	for _, rec := range r.store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == "restart-refused" {
			stored++
		}
	}
	if stored != 1 {
		t.Errorf("restart-refused recorded %d times", stored)
	}
	blocked := r.events("phase-blocked")
	if len(blocked) != 1 || blocked[0].Fields["reason"] != "tests red" {
		t.Errorf("blocked %+v", blocked)
	}
}

func TestTheRemedyWindowExpiringBlocksThePhase(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = 30 * time.Millisecond
	r.host.behaviour["rloop-p1-implement"] = "fail"

	start := time.Now()
	code := r.run(RunOptions{Phases: []int{1}})

	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if waited := time.Since(start); waited < 30*time.Millisecond {
		t.Errorf("returned after %s, before the window", waited)
	}
	blocked := r.events("phase-blocked")
	if len(blocked) != 1 || blocked[0].Fields["reason"] != "tests red" {
		t.Errorf("blocked %+v", blocked)
	}
	if n := len(r.host.Opened); n != 2 {
		t.Errorf("%d sessions opened", n)
	}
}

func TestNoWindowWithoutAWatchdog(t *testing.T) {
	r := newEventsRig(t)
	r.host.behaviour["rloop-p1-implement"] = "fail"
	r.restartOnFailure()

	code := r.run(RunOptions{Phases: []int{1}})

	if code != 1 || len(r.host.Opened) != 2 {
		t.Errorf("exit %d, %d sessions", code, len(r.host.Opened))
	}
}

func TestAQuestionTheWatcherAnswersReturnsTheStepToRunning(t *testing.T) {
	r := newEventsRig(t)
	r.host.behaviour["rloop-p2-implement"] = "ask"
	r.watcher.route = func(ctx context.Context, q Question) bool {
		r.loop.Deliver(q.ID, "sqlite", "watchdog", "docs/spec.html:3")
		return true
	}

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	want := []string{"queued", "spawned", "running", "waiting-input", "running", "ok"}
	if got := r.stepStates("implement"); !reflect.DeepEqual(got, want) {
		t.Errorf("implement states %v, want %v", got, want)
	}
	if got := r.calls("AskChannel.Answer "); !reflect.DeepEqual(got, []string{`q1 "sqlite" watchdog "docs/spec.html:3"`}) {
		t.Errorf("answers %v", got)
	}
	var order []string
	for _, c := range r.shared.Calls() {
		switch {
		case c == "Store.Append run-1 question", c == "Face.Emit question", strings.HasPrefix(c, "Watcher.Route"), strings.HasPrefix(c, "AskChannel.Answer"):
			order = append(order, strings.Fields(c)[0])
		}
	}
	wantOrder := []string{"Store.Append", "Face.Emit", "Watcher.Route", "Store.Append", "AskChannel.Answer"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("order %v, want %v", order, wantOrder)
	}
	st, _ := r.store.Load("run-1")
	if len(st.Questions) != 1 || st.Questions[0].Answer != "sqlite" || st.Questions[0].AnsweredBy != "watchdog" {
		t.Errorf("questions %+v", st.Questions)
	}
	if human := r.events("human"); len(human) != 0 {
		t.Errorf("human %+v", human)
	}
}

func holdQuestions(ctx context.Context, q Question) bool {
	<-ctx.Done()
	return false
}

func TestAHeldQuestionKeepsTheRunAlivePastTheBackstop(t *testing.T) {
	r := newEventsRig(t)
	kind := r.loop.Kinds[1]
	kind.Row.Timeout = 3 * time.Minute
	r.loop.Kinds[1] = kind
	r.host.behaviour["rloop-p2-implement"] = "ask-noinput"
	r.watcher.route = holdQuestions

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if got := r.calls("Watcher.Route "); !reflect.DeepEqual(got, []string{"q1"}) {
		t.Errorf("routed %v", got)
	}
	if got := r.calls("AskChannel.Answer "); !reflect.DeepEqual(got, []string{`q1 "r-loop: phase-2/implement has ended; this question is withdrawn." withdrawn ""`}) {
		t.Errorf("answered %v", got)
	}
	st, _ := r.store.Load("run-1")
	if len(st.Questions) != 1 || st.Questions[0].AnsweredBy != "withdrawn" || st.Questions[0].Answer != "step ok" {
		t.Errorf("questions %+v", st.Questions)
	}
	if got := r.stepStates("implement"); slices.Contains(got, "failed") || !slices.Contains(got, "waiting-input") {
		t.Errorf("implement states %v", got)
	}
}

type backstopRunner struct{}

func (backstopRunner) Run(ctx context.Context, ref StepRef, obs Observer) Outcome {
	s := &Session{Ref: ref}
	s.OpenQuestion.Store(true)
	return Outcome{State: StepFailed, Reason: "backstop 1h0m0s", Session: s}
}

func TestABackstopInWaitingInputHaltsTheRunOnTheInvariant(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	r.loop.Runners = map[string]StepRunner{"plan-file": backstopRunner{}}

	code := r.run(RunOptions{})

	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	runs := r.runRecords()
	if last := runs[len(runs)-1]; last.Run != RunHalted || last.Reason != "invariant: a question never kills a step" {
		t.Errorf("last run record %+v", last)
	}
	if got := r.events("phase-blocked"); len(got) != 0 {
		t.Errorf("step failed as a block: %+v", got)
	}
	if n := len(r.calls("Watcher.StepEnded ")); n != 1 {
		t.Errorf("%d steps ran after the invariant", n)
	}
}

func TestBeforePhaseIsCalledBeforeTheFirstSpawn(t *testing.T) {
	r := newEventsRig(t)

	r.run(RunOptions{Phases: []int{2}})

	var order []string
	for _, c := range r.shared.Calls() {
		if strings.HasPrefix(c, "Watcher.") || strings.HasPrefix(c, "SessionHost.Open") {
			order = append(order, strings.SplitN(c, " ", 2)[0]+" "+strings.Fields(c)[1])
		}
	}
	want := []string{
		"Watcher.BeforePhase 2",
		"SessionHost.Open " + r.host.Opened[0].CWD,
		"Watcher.StepStarted 2",
		"Watcher.StepEnded 2",
		"SessionHost.Open " + r.host.Opened[1].CWD,
		"Watcher.StepStarted 2",
		"Watcher.StepEnded 2",
	}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("order\n got %v\nwant %v", order, want)
	}
}

func TestNopWatcherAnswersNothingAndEmitsNothing(t *testing.T) {
	var w Watcher = nopWatcher{}

	if w.Signals() != nil || w.Restarts() != nil || w.Route(context.Background(), Question{ID: "q1"}) {
		t.Error("nopWatcher is not silent")
	}
}

func TestAHaltForAnEndedStepOfTheLivePhaseHaltsTheLiveStep(t *testing.T) {
	r := newEventsRig(t)
	r.host.behaviour["rloop-p2-implement"] = "hold"
	r.watcher.started = func(ref StepRef, s *Session) {
		if ref.Key.Kind == "implement" {
			stale := StepKey{Run: "run-1", Phase: 2, Kind: "plan", Attempt: 1}
			r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: stale, Reason: "late"}
		}
	}

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	if got := r.calls("SessionHost.Interrupt "); !reflect.DeepEqual(got, []string{"rloop-p2-implement"}) {
		t.Errorf("interrupted %v", got)
	}
	if blocked := r.events("phase-blocked"); len(blocked) != 1 || blocked[0].Fields["reason"] != "watchdog: late" {
		t.Errorf("blocked %+v", blocked)
	}
}

func TestAStaleRestartForAnEarlierAttemptIsIgnored(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = 30 * time.Millisecond
	r.host.behaviour["rloop-p2-implement"] = "fail"
	r.host.behaviour["rloop-p2-implement-a2"] = "fail"
	r.watcher.ended = func(ref StepRef, out Outcome) {
		if out.State == StepFailed && ref.Key.Attempt == 1 {
			r.watcher.restarts <- Restart{Step: ref.Key}
			r.watcher.restarts <- Restart{Step: ref.Key}
		}
	}

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	want := []string{"rloop-p2-plan", "rloop-p2-implement", "rloop-p2-implement-a2"}
	if got := r.agents(); !reflect.DeepEqual(got, want) {
		t.Errorf("spawned %v, want %v", got, want)
	}
}

func TestAProviderOverrideLastsOneAttempt(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	kind := r.loop.Kinds[1]
	kind.Row.Model, kind.Row.Effort = "gpt", "high"
	r.loop.Kinds[1] = kind
	r.host.behaviour["rloop-p2-implement"] = "fail"
	r.host.behaviour["rloop-p2-implement-a2"] = "fail"
	r.watcher.ended = func(ref StepRef, out Outcome) {
		if out.State != StepFailed {
			return
		}
		provider := ""
		if ref.Key.Attempt == 1 {
			provider = "gemini"
		}
		r.watcher.restarts <- Restart{Step: ref.Key, Provider: provider}
	}

	r.run(RunOptions{Phases: []int{2}})

	want := []string{"codex//", "codex/gpt/high", "gemini//", "codex/gpt/high"}
	if !reflect.DeepEqual(r.resolved, want) {
		t.Errorf("resolved %v, want %v", r.resolved, want)
	}
}

func TestResumeKeepsTheRestartsAlreadySpent(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	for a := 1; a <= 3; a++ {
		r.store.Append("run-1", Record{Kind: RecordStep, Step: &StepKey{Run: "run-1", Phase: 2, Kind: "plan", Attempt: a}, State: StepOK})
		r.store.Append("run-1", Record{Kind: RecordStep, Step: &StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: a}, State: StepFailed})
	}
	for _, a := range []string{"2", "3"} {
		r.store.Append("run-1", Record{Kind: RecordEvent, Event: &Event{Kind: "restart", Phase: 2, Step: "implement", Fields: map[string]string{"step": "phase-2/implement", "attempt": a}}})
	}
	r.host.behaviour["rloop-p2-implement-a4"] = "fail"
	r.restartOnFailure()

	code := r.run(RunOptions{Phases: []int{2}, Resume: true})

	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if got := r.agents(); !reflect.DeepEqual(got, []string{"rloop-p2-implement-a4"}) {
		t.Errorf("spawned %v", got)
	}
	if refused := r.events("restart-refused"); len(refused) != 1 {
		t.Errorf("restart-refused %+v", refused)
	}
}

func TestTheStepStaysWaitingUntilItsLastQuestionIsAnswered(t *testing.T) {
	r := newEventsRig(t)
	gate := make(chan struct{})
	r.watcher.route = func(ctx context.Context, q Question) bool {
		<-gate
		r.loop.Deliver(q.ID, "yes", "watchdog", "docs/spec.html:1")
		return true
	}
	s := &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}}}
	r.loop.runDir = r.store.dir
	r.loop.setLive(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { r.loop.question(ctx, Question{ID: "q1", Step: s.Ref.Key}); done <- struct{}{} }()
	waitFor(t, func() bool { return len(r.calls("Watcher.Route ")) == 1 })
	go func() { r.loop.question(ctx, Question{ID: "q2", Step: s.Ref.Key}); done <- struct{}{} }()
	waitFor(t, func() bool { return len(r.calls("Watcher.Route ")) == 2 })
	gate <- struct{}{}
	<-done

	if !s.OpenQuestion.Load() {
		t.Error("backstop resumed with a question still open")
	}
	gate <- struct{}{}
	<-done
	if s.OpenQuestion.Load() {
		t.Error("step still frozen after the last answer")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 5000 {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never held")
}

func TestAStepThatEndsWithAnOpenQuestionWithdrawsItAndALateAnswerIsDropped(t *testing.T) {
	r := newEventsRig(t)
	r.watcher.route = holdQuestions
	r.host.behaviour["rloop-p2-implement"] = "ask-fail"

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	st, _ := r.store.Load("run-1")
	if len(st.Questions) != 1 || st.Questions[0].AnsweredBy != "withdrawn" {
		t.Errorf("questions %+v", st.Questions)
	}

	err := r.loop.Deliver("q1", "sqlite", "maintainer", "")

	if err == nil || !strings.Contains(err.Error(), "not open") {
		t.Fatalf("late answer err %v", err)
	}
	key := StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}
	st, _ = r.store.Load("run-1")
	if st.Steps[key] != StepFailed {
		t.Errorf("step %s after the late answer, want failed", st.Steps[key])
	}
	if got := r.stepStates("implement"); got[len(got)-1] != "failed" {
		t.Errorf("implement states %v", got)
	}
	if len(st.Questions) != 1 || st.Questions[0].AnsweredBy != "withdrawn" {
		t.Errorf("questions after the late answer %+v", st.Questions)
	}
	if human := r.events("human"); len(human) != 0 {
		t.Errorf("late answer counted as a human touch: %+v", human)
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 1 || !strings.HasSuffix(got[0], ` withdrawn ""`) {
		t.Errorf("answers %v", got)
	}
}

func TestAQuestionForAStepThatHasEndedIsWithdrawnWithoutTouchingTheStep(t *testing.T) {
	r := newEventsRig(t)
	r.loop.runDir = r.store.dir
	s := &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}}}
	r.loop.setLive(s)
	r.loop.Sessions.Finish(s, Outcome{State: StepFailed, Reason: "gave up", Session: s})

	r.loop.question(context.Background(), Question{ID: "q1", Step: s.Ref.Key, Text: "which db?"})

	if got := r.calls("Watcher.Route "); len(got) != 0 {
		t.Errorf("routed %v", got)
	}
	if s.OpenQuestion.Load() {
		t.Error("an ended step was frozen")
	}
	st, _ := r.store.Load("run-1")
	if st.Steps[s.Ref.Key] != StepFailed {
		t.Errorf("step %s, want failed", st.Steps[s.Ref.Key])
	}
	if len(st.Questions) != 1 || st.Questions[0].AnsweredBy != "withdrawn" {
		t.Errorf("questions %+v", st.Questions)
	}
	if err := r.loop.Deliver("q1", "sqlite", "maintainer", ""); err == nil {
		t.Error("answered a withdrawn question")
	}
}

func TestAnsweringAQuestionThatIsNotOpenIsAnError(t *testing.T) {
	r := newEventsRig(t)
	r.loop.runDir = r.store.dir

	err := r.loop.Deliver("q9", "yes", "maintainer", "")

	if err == nil || !strings.Contains(err.Error(), "q9") {
		t.Fatalf("err %v", err)
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 0 {
		t.Errorf("answered %v", got)
	}
}

func TestAHaltInTheRemedyWindowBlocksThePhaseWithExit5AndRefusesThePendingRestart(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	r.watcher.signals = make(chan Signal, 1)
	r.host.behaviour["rloop-p2-implement"] = "fail"
	r.watcher.ended = func(ref StepRef, out Outcome) {
		if out.State == StepFailed && ref.Key.Attempt == 1 {
			r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: ref.Key, Reason: "wrong turn"}
			r.watcher.restarts <- Restart{Step: ref.Key, Addendum: "again"}
		}
	}
	start := time.Now()

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	if time.Since(start) > 30*time.Second {
		t.Errorf("waited out the remedy window")
	}
	if want := []string{"rloop-p2-plan", "rloop-p2-implement"}; !reflect.DeepEqual(r.agents(), want) {
		t.Errorf("spawned %v, want %v", r.agents(), want)
	}
	if got := r.events("restart"); len(got) != 0 {
		t.Errorf("restarts %+v", got)
	}
	blocked := r.events("phase-blocked")
	if len(blocked) != 1 || blocked[0].Fields["phase"] != "2" || blocked[0].Fields["reason"] != "watchdog: wrong turn" {
		t.Errorf("blocked %+v", blocked)
	}
}

func TestAHaltForAStepThatEndedInAnEarlierPhaseExits5WithoutStoppingTheLiveStep(t *testing.T) {
	failed := StepKey{Run: "run-1", Phase: 1, Kind: "implement", Attempt: 1}
	halt := Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: failed, Reason: "wrong turn"}
	for name, arm := range map[string]func(r *eventsRig){
		"between phases": func(r *eventsRig) {
			r.watcher.ended = func(ref StepRef, out Outcome) {
				if ref.Key == failed {
					r.watcher.signals <- halt
				}
			}
		},
		"during the next phase's step": func(r *eventsRig) {
			r.watcher.started = func(ref StepRef, s *Session) {
				if ref.Key.Phase == 2 && ref.Key.Kind == "implement" {
					r.watcher.signals <- halt
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newEventsRig(t)
			r.watcher.signals = make(chan Signal, 1)
			r.host.behaviour["rloop-p1-implement"] = "fail"
			arm(r)

			code := r.run(RunOptions{})

			if code != 5 {
				t.Fatalf("exit %d, want 5", code)
			}
			if got := r.calls("SessionHost.Interrupt "); len(got) != 0 {
				t.Errorf("interrupted %v", got)
			}
			if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"2"}) {
				t.Errorf("landed %v", got)
			}
			runs := r.runRecords()
			if last := runs[len(runs)-1]; last.Run != RunHalted || last.Reason != "watchdog: wrong turn" {
				t.Errorf("last run record %+v", last)
			}
		})
	}
}

func TestAWatchdogHaltRecordsTheFailedStepBeforeStoppingTheSession(t *testing.T) {
	r := newEventsRig(t)
	r.host.behaviour["rloop-p1-implement"] = "hold"
	probe := &interruptProbe{eventsHost: r.ehost, store: r.store, recorded: make(chan bool, 1)}
	r.loop.Sessions.Host = probe
	r.watcher.started = func(ref StepRef, s *Session) {
		if ref.Key.Phase == 1 && ref.Key.Kind == "implement" {
			r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: ref.Key, Reason: "rewriting the spec"}
		}
	}

	if code := r.run(RunOptions{}); code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	if !<-probe.recorded {
		t.Error("session stopped before the failed transition was appended")
	}
}

type interruptProbe struct {
	*eventsHost
	store    *loopStore
	recorded chan bool
}

func (h *interruptProbe) Interrupt(agent string) error {
	h.store.mu.Lock()
	found := false
	for _, rec := range h.store.Records["run-1"] {
		if rec.Kind == RecordStep && rec.State == StepFailed && strings.HasPrefix(rec.Reason, "watchdog: ") {
			found = true
		}
	}
	h.store.mu.Unlock()
	h.recorded <- found
	return h.eventsHost.Interrupt(agent)
}

type abortProbe struct {
	store    *loopStore
	recorded chan bool
}

func (a abortProbe) Run(ctx context.Context, ref StepRef, obs Observer) Outcome {
	a.store.MarkAbort("run-1")
	<-ctx.Done()
	a.store.mu.Lock()
	found := false
	for _, rec := range a.store.Records["run-1"] {
		if rec.Kind == RecordRun && rec.Run == RunHalted && rec.Reason == ReasonAborted {
			found = true
		}
	}
	a.store.mu.Unlock()
	a.recorded <- found
	return Outcome{State: StepFailed, Reason: "interrupted"}
}

func TestAnAbortMidStepRecordsTheHaltedRunBeforeCancellingTheStep(t *testing.T) {
	r := newLoopRig(t)
	probe := abortProbe{store: r.store, recorded: make(chan bool, 1)}
	r.loop.Runners = map[string]StepRunner{"plan-file": probe}

	if code := r.run(RunOptions{Phases: []int{2}}); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !<-probe.recorded {
		t.Error("step cancelled before the aborted run was appended")
	}
	if got := r.events("aborted"); len(got) != 1 {
		t.Errorf("aborted %+v", got)
	}
}

func TestAnAbortDuringTheRemedyWindowStopsTheRunAtOnce(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = 2 * time.Second
	r.host.behaviour["rloop-p1-implement"] = "fail"
	r.watcher.ended = func(ref StepRef, out Outcome) {
		if out.State == StepFailed {
			r.store.MarkAbort("run-1")
		}
	}

	start := time.Now()
	code := r.run(RunOptions{Phases: []int{1}})

	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if waited := time.Since(start); waited >= time.Second {
		t.Errorf("abort honoured after %s", waited)
	}
	runs := r.runRecords()
	if last := runs[len(runs)-1]; last.Run != RunHalted || last.Reason != ReasonAborted {
		t.Errorf("last run record %+v", last)
	}
	if got := r.events("aborted"); len(got) != 1 || got[0].Phase != 1 || got[0].Step != "implement" || got[0].Fields["workspace"] != "ws-2" {
		t.Errorf("aborted %+v", got)
	}
	if got := r.events("phase-blocked"); len(got) != 0 {
		t.Errorf("blocked %+v", got)
	}
	if got := r.calls("Notifier.Fire "); len(got) != 0 {
		t.Errorf("hooks fired on abort: %v", got)
	}
}

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
	routes   bool
}

func (w *fakeWatcher) BeforePhase(ctx context.Context, ph Phase) {
	w.log.record("Watcher.BeforePhase %d", ph.Number)
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
	return w.routes
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
	if b == "hold" || b == "ask" || b == "ask-noinput" {
		h.record("SessionHost.Prompt %s", agent)
		return nil
	}
	return h.agentSim.Prompt(agent, text, wait, timeout)
}

func (h *eventsHost) spec(agent string) OpenSpec {
	for _, o := range h.Opened {
		if o.Label == agent {
			return o
		}
	}
	return OpenSpec{}
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
	if b != "ask" && b != "ask-noinput" {
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
	var out []string
	for _, o := range r.host.Opened {
		out = append(out, o.Label)
	}
	return out
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
	var last Record
	for _, rec := range r.store.Records["run-1"] {
		if rec.Kind == RecordStep && rec.Step.Phase == 1 && rec.Step.Kind == "implement" {
			last = rec
		}
	}
	if last.State != StepFailed || last.Reason != "watchdog: rewriting the spec" {
		t.Errorf("last implement record %+v", last)
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

func TestQuestionAnsweredThroughTheFaceReturnsToRunning(t *testing.T) {
	r := newEventsRig(t)
	r.host.behaviour["rloop-p2-implement"] = "ask"
	r.face.Answers = map[string]string{"q1": "sqlite"}

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	want := []string{"queued", "spawned", "running", "waiting-input", "running", "ok"}
	if got := r.stepStates("implement"); !reflect.DeepEqual(got, want) {
		t.Errorf("implement states %v, want %v", got, want)
	}
	if got := r.calls("AskChannel.Answer "); !reflect.DeepEqual(got, []string{`q1 "sqlite" maintainer ""`}) {
		t.Errorf("answers %v", got)
	}
	var order []string
	for _, c := range r.shared.Calls() {
		switch {
		case c == "Store.Append run-1 question", c == "Face.Emit question", strings.HasPrefix(c, "Watcher.Route"), strings.HasPrefix(c, "Face.Ask"), strings.HasPrefix(c, "AskChannel.Answer"):
			order = append(order, strings.Fields(c)[0])
		}
	}
	wantOrder := []string{"Store.Append", "Face.Emit", "Watcher.Route", "Face.Ask", "AskChannel.Answer", "Store.Append"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("order %v, want %v", order, wantOrder)
	}
	st, _ := r.store.Load("run-1")
	if len(st.Questions) != 1 || st.Questions[0].Answer != "sqlite" || st.Questions[0].AnsweredBy != "maintainer" {
		t.Errorf("questions %+v", st.Questions)
	}
	if human := r.events("human"); len(human) != 1 || human[0].Fields["what"] != "answer" {
		t.Errorf("human %+v", human)
	}
}

func TestAQuestionTheWatcherRoutesNeverReachesTheFace(t *testing.T) {
	r := newEventsRig(t)
	r.watcher.routes = true
	r.host.behaviour["rloop-p2-implement"] = "ask"

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if got := r.calls("Face.Ask "); len(got) != 0 {
		t.Errorf("face asked %v", got)
	}
	want := []string{"queued", "spawned", "running", "waiting-input", "running", "ok"}
	if got := r.stepStates("implement"); !reflect.DeepEqual(got, want) {
		t.Errorf("implement states %v, want %v", got, want)
	}
}

func TestErrNoInputKeepsTheRunAlivePastTheBackstop(t *testing.T) {
	r := newEventsRig(t)
	kind := r.loop.Kinds[1]
	kind.Row.Timeout = 3 * time.Minute
	r.loop.Kinds[1] = kind
	r.host.behaviour["rloop-p2-implement"] = "ask-noinput"

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if got := r.calls("Face.Ask "); !reflect.DeepEqual(got, []string{"q1"}) {
		t.Errorf("asked %v", got)
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 0 {
		t.Errorf("answered %v", got)
	}
	st, _ := r.store.Load("run-1")
	if len(st.Questions) != 1 || st.Questions[0].AnsweredBy != "" {
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

func TestAHaltForAnotherStepDoesNotStopTheLiveOne(t *testing.T) {
	r := newEventsRig(t)
	r.watcher.started = func(ref StepRef, s *Session) {
		if ref.Key.Kind == "implement" {
			stale := StepKey{Run: "run-1", Phase: 2, Kind: "plan", Attempt: 1}
			r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: stale, Reason: "late"}
		}
	}

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if got := r.calls("SessionHost.Interrupt "); len(got) != 0 {
		t.Errorf("interrupted %v", got)
	}
	if w := r.events("warning"); len(w) != 1 || !strings.Contains(w[0].Fields["reason"], "late") {
		t.Errorf("warnings %+v", w)
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
	r.loop.Face = &gatedFace{fakeFace: r.face, gate: gate}
	s := &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}}}
	r.loop.runDir = r.store.dir
	r.loop.setLive(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { r.loop.question(ctx, Question{ID: "q1", Step: s.Ref.Key}); done <- struct{}{} }()
	waitFor(t, func() bool { return len(r.calls("Face.Ask ")) == 1 })
	go func() { r.loop.question(ctx, Question{ID: "q2", Step: s.Ref.Key}); done <- struct{}{} }()
	waitFor(t, func() bool { return len(r.calls("Face.Ask ")) == 2 })
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

type gatedFace struct {
	*fakeFace
	gate chan struct{}
}

func (f *gatedFace) Ask(q Question) (string, error) {
	f.record("Face.Ask %s", q.ID)
	<-f.gate
	return "yes", nil
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

func TestAWatchdogQuestionGoesStraightToTheFaceWithoutTouchingTheStep(t *testing.T) {
	r := newEventsRig(t)
	r.face.Answers = map[string]string{"q7": "yes"}
	answered := make(chan string, 1)
	r.ask.answered = func(id string) { answered <- id }
	r.watcher.started = func(ref StepRef, s *Session) {
		if ref.Key.Kind != "implement" {
			return
		}
		r.ask.Asked <- Question{ID: "q7", Step: StepKey{Run: "run-1", Kind: "watchdog"}, Text: "run go mod download?"}
		select {
		case <-answered:
		case <-time.After(5 * time.Second):
			t.Error("the watchdog question was never answered")
		}
	}

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if got := r.calls("AskChannel.Answer "); !reflect.DeepEqual(got, []string{`q7 "yes" maintainer ""`}) {
		t.Errorf("answers %v", got)
	}
	if got := r.calls("Watcher.Route "); len(got) != 0 {
		t.Errorf("routed %v", got)
	}
	if got := r.stepStates("implement"); !reflect.DeepEqual(got, []string{"queued", "spawned", "running", "ok"}) {
		t.Errorf("implement states %v", got)
	}
	st, _ := r.store.Load("run-1")
	if len(st.Questions) != 1 || st.Questions[0].Answer != "yes" || st.Questions[0].AnsweredBy != "maintainer" {
		t.Errorf("questions %+v", st.Questions)
	}
}

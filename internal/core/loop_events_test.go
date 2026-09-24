package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

type lateHaltWatcher struct {
	*fakeWatcher
	armed atomic.Bool
	halts chan Signal
}

func (w *lateHaltWatcher) Signals() <-chan Signal {
	if w.armed.Load() {
		return w.halts
	}
	return nil
}

func (w *lateHaltWatcher) Restarts() <-chan Restart {
	w.armed.Store(true)
	return w.fakeWatcher.restarts
}

func (w *fakeWatcher) BeforePhase(ctx context.Context, ph Phase, base string) CheckOutcome {
	w.log.record("Watcher.BeforePhase %s", ph.ID)
	return CheckOutcome{}
}

func (w *fakeWatcher) StepStarted(ref StepRef, s *Session) {
	w.log.record("Watcher.StepStarted %s %s %d", ref.Key.Phase, ref.Key.Kind, ref.Key.Attempt)
	if w.started != nil {
		w.started(ref, s)
	}
}

func (w *fakeWatcher) StepEnded(ref StepRef, out Outcome) {
	w.log.record("Watcher.StepEnded %s %s %d %s", ref.Key.Phase, ref.Key.Kind, ref.Key.Attempt, out.State)
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
	ask     *eventsAsk
	asked   map[string]int
	typed   map[string]bool
	refuse  map[string]int
	stopped map[string]AgentState
}

type panicAnswerHost struct{ *eventsHost }

func (h panicAnswerHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	if strings.HasPrefix(text, "r-loop: answer to ") {
		panic("boom")
	}
	return h.eventsHost.Prompt(agent, text, wait, timeout)
}

type panicOnceAnswerHost struct {
	*eventsHost
	panicked atomic.Bool
}

func (h *panicOnceAnswerHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	if strings.HasPrefix(text, "r-loop: answer to ") && h.panicked.CompareAndSwap(false, true) {
		panic("boom")
	}
	return h.eventsHost.Prompt(agent, text, wait, timeout)
}

type panicAnswerAsk struct{ *eventsAsk }

func (panicAnswerAsk) Answer(string, string, string, string) error { panic("boom") }

type panicFirstQuestionStore struct {
	Store
	panicked bool
}

type panicAnswerReleaseStore struct {
	Store
	panicked bool
}

func (s *panicAnswerReleaseStore) Append(runID string, rec Record) error {
	if rec.Kind == RecordStep && rec.State == StepRunning && !s.panicked {
		s.panicked = true
		panic("release boom")
	}
	return s.Store.Append(runID, rec)
}

func (s *panicFirstQuestionStore) Append(runID string, rec Record) error {
	if rec.Kind == RecordQuestion && !s.panicked {
		s.panicked = true
		panic("boom")
	}
	return s.Store.Append(runID, rec)
}

func TestAPanicBeforeQuestionRegistrationAnswersTheAskingPane(t *testing.T) {
	r := newEventsRig(t)
	r.loop.runDir = r.store.dir
	r.loop.Store = &panicFirstQuestionStore{Store: r.store}
	key := StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}
	s := &Session{Ref: StepRef{Key: key}, Agent: "rloop-p2-implement"}
	r.loop.setLive(s)
	r.ehost.stopped[s.Agent] = AgentIdle

	r.loop.question(context.Background(), Question{ID: "q1", Step: key, Text: "which db?"})

	waitFor(t, func() bool { return len(r.calls("SessionHost.Typed ")) == 1 })
	if got := r.calls("SessionHost.Typed "); !reflect.DeepEqual(got, []string{`rloop-p2-implement "r-loop: answer to q1 (by r-loop): withdrawn (panic in question q1: boom); continue without an answer"`}) {
		t.Errorf("typed %v", got)
	}
}

func TestAPanicRoutingAQuestionWithdrawsItAndTheStepGoesOn(t *testing.T) {
	r := newEventsRig(t)
	r.host.behaviour["rloop-p2-implement"] = "ask"
	r.watcher.route = func(context.Context, Question) bool { panic("boom") }

	if code := r.run(RunOptions{Phases: []string{"2"}}); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	want := []string{"queued", "spawned", "running", "waiting-input", "running", "ok"}
	if got := r.stepStates("implement"); !reflect.DeepEqual(got, want) {
		t.Errorf("implement states %v, want %v", got, want)
	}
	st, err := r.store.Load("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Questions) != 1 || st.Questions[0].ID != "q1" || st.Questions[0].AnsweredBy != "withdrawn" || st.Questions[0].Answer != "panic in question q1: boom" {
		t.Errorf("questions %+v", st.Questions)
	}
	if got := r.calls("SessionHost.Typed "); !reflect.DeepEqual(got, []string{`rloop-p2-implement "r-loop: answer to q1 (by r-loop): withdrawn (panic in question q1: boom); continue without an answer"`}) {
		t.Errorf("typed %v", got)
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 1 || !strings.Contains(got[0], "q1") || !strings.Contains(got[0], "withdrawn") {
		t.Errorf("answers %v", got)
	}
	if got := r.events("error"); len(got) == 0 || got[0].Fields["reason"] != "panic in question q1: boom" {
		t.Errorf("error events %+v", got)
	}
}

func TestAPanicTypingAnAnswerWithdrawsTheQuestion(t *testing.T) {
	r := newEventsRig(t)
	r.host.behaviour["rloop-p2-implement"] = "ask"
	r.watcher.route = func(ctx context.Context, q Question) bool {
		r.loop.Deliver(q.ID, "sqlite", "watchdog", "")
		return true
	}
	r.loop.Sessions.Host = panicAnswerHost{r.ehost}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- r.loop.Run(ctx, RunOptions{Phases: []string{"2"}}) }()
	waitFor(t, func() bool {
		r.store.mu.Lock()
		defer r.store.mu.Unlock()
		var withdrawn, panicked bool
		for _, rec := range r.store.Records["run-1"] {
			if rec.Kind == RecordQuestion && rec.Question != nil && rec.Question.ID == "q1" && rec.Question.AnsweredBy == "withdrawn" && rec.Question.Answer == "panic in answer hand-off q1: boom" {
				withdrawn = true
			}
			if rec.Kind == RecordEvent && rec.Event != nil && rec.Event.Kind == "error" && rec.Event.Fields["reason"] == "panic in answer hand-off q1: boom" {
				panicked = true
			}
		}
		return withdrawn && panicked
	})
	cancel()
	select {
	case code := <-done:
		if code != 4 {
			t.Errorf("exit %d, want 4", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not stop")
	}
}

func TestAPanicTypingAnAnswerSendsWithdrawalToTheAskingPane(t *testing.T) {
	r := newEventsRig(t)
	r.host.behaviour["rloop-p2-implement"] = "ask"
	r.watcher.route = func(ctx context.Context, q Question) bool {
		r.loop.Deliver(q.ID, "sqlite", "watchdog", "")
		return true
	}
	r.loop.Sessions.Host = &panicOnceAnswerHost{eventsHost: r.ehost}

	if code := r.run(RunOptions{Phases: []string{"2"}}); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	got := r.calls("SessionHost.Typed ")
	if len(got) != 1 || !strings.Contains(got[0], "withdrawn (panic in answer hand-off q1: boom); continue without an answer") {
		t.Errorf("typed %v", got)
	}
}

func TestAPanicAfterAnswerDeliveryDoesNotSendWithdrawal(t *testing.T) {
	r := newEventsRig(t)
	r.loop.Store = &panicAnswerReleaseStore{Store: r.store}
	r.ehost.stopped["rloop-p2-implement"] = AgentIdle
	key := StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}
	s := &Session{Ref: StepRef{Key: key}, Agent: "rloop-p2-implement"}
	q := Question{ID: "q1", Step: key, Text: "which db?"}
	r.loop.track(q, s, s.Agent)
	r.loop.openQuestion(s, 1)
	open, ok := r.loop.answer(q.ID, "sqlite", "watchdog", "")
	if !ok {
		t.Fatal("question was not claimed")
	}

	r.loop.hand(open, answerMessage(q.ID, "sqlite", "watchdog", ""))

	got := r.calls("SessionHost.Typed ")
	if len(got) != 1 || !strings.Contains(got[0], "sqlite") {
		t.Errorf("typed %v, want only the delivered answer", got)
	}
}

func TestAPanicAnsweringALandStageQuestionIsRecordedWithoutACrash(t *testing.T) {
	r := newEventsRig(t)
	r.loop.Ask = panicAnswerAsk{r.ask}
	q := Question{ID: "q-gatefix", Step: StepKey{Run: "run-1", Phase: "1", Kind: "gatefix", Attempt: 1}, Text: "which?"}
	done := make(chan struct{})
	go func() { r.loop.question(context.Background(), q); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("question did not return")
	}
	if _, err := os.Stat(filepath.Join(r.store.dir, "report.md")); err != nil {
		t.Fatalf("report was not written in the test run directory: %v", err)
	}
	if got := r.events("error"); len(got) == 0 || got[0].Fields["reason"] != "panic in question q-gatefix: boom" {
		t.Errorf("error events %+v", got)
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	var last *Question
	for _, rec := range r.store.Records["run-1"] {
		if rec.Kind == RecordQuestion && rec.Question != nil && rec.Question.ID == q.ID {
			last = rec.Question
		}
	}
	if last == nil || last.AnsweredBy != "withdrawn" || last.Answer != "panic in question q-gatefix: boom" {
		t.Errorf("question %+v", last)
	}
}

func (h *eventsHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	key := role(agent)
	if strings.HasPrefix(text, "r-loop: answer to ") {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.refuse[key] > 0 {
			h.refuse[key]--
			h.record("SessionHost.Refused %s", agent)
			return errors.New("herdr agent prompt: agent_blocked")
		}
		h.typed[key] = true
		h.record("SessionHost.Typed %s %q", agent, text)
		return nil
	}
	h.mu.Lock()
	b := h.behaviour[key]
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
	writeFile(h.spec(agent).Env["R_LOOP_SENTINEL"], `{"outcome":"ok","reason":""}`)
}

func (h *eventsHost) State(agent string) (AgentState, error) {
	h.mu.Lock()
	key := role(agent)
	b := h.behaviour[key]
	n := h.asked[key]
	h.asked[key] = n + 1
	typed := h.typed[key]
	stopped, isStopped := h.stopped[key]
	h.mu.Unlock()
	if isStopped {
		return stopped, nil
	}
	if b != "ask" && b != "ask-noinput" && b != "ask-fail" {
		return h.agentSim.State(agent)
	}
	if n == 0 {
		env := h.spec(agent).Env
		phase, _ := strconv.Atoi(env["R_LOOP_PHASE"])
		key := StepKey{Run: env["R_LOOP_RUN"], Phase: strconv.Itoa(phase), Kind: env["R_LOOP_STEP"], Attempt: 1}
		h.ask.Asked <- Question{ID: "q1", Step: key, Text: "which db?", Options: []string{"sqlite", "postgres"}}
		h.waitRecord(key, StepWaitingInput, 1)
	}
	if b == "ask" && !typed {
		return AgentIdle, nil
	}
	if b == "ask" {
		h.waitRecord(StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}, StepRunning, 2)
		h.finish(agent)
	}
	if b == "ask-noinput" && n == 10 {
		h.finish(agent)
	}
	if b == "ask-fail" && n == 10 {
		writeFile(h.spec(agent).Env["R_LOOP_SENTINEL"], `{"outcome":"failed","reason":"gave up"}`)
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
	r.loop.runDir = r.store.dir
	r.watcher = &fakeWatcher{log: r.shared, signals: make(chan Signal), restarts: make(chan Restart, 8)}
	r.ask = &eventsAsk{fakeAskChannel: fakeAskChannel{callLog: callLog{Shared: r.shared}, Asked: make(chan Question, 1)}}
	r.ehost = &eventsHost{agentSim: r.host, ask: r.ask, asked: map[string]int{}, typed: map[string]bool{}, refuse: map[string]int{}, stopped: map[string]AgentState{}}
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
	agents := append([]string(nil), r.host.Agents...)
	for i := range agents {
		agents[i] = role(agents[i])
	}
	return agents
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

func TestThePhaseCheckStartReachesTheFaceBeforeTheWorktreeAndTheCheckPrompt(t *testing.T) {
	r := newCheckRig(t)
	if code := r.run(RunOptions{Phases: []string{"1"}}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	calls := r.shared.Calls()
	phase := indexOf(calls, "Face.Emit phase-start")
	start := indexOf(calls, "Face.Emit phase-check-start")
	worktree := indexOf(calls, "Repo.AddWorktree .r-loop/wt/phase-1")
	prompt := indexOf(calls, `SessionHost.Prompt rloop-wd-run-1 "check phase 1`)
	if phase < 0 || start < 0 || worktree < 0 || prompt < 0 || !(phase < start && start < worktree && worktree < prompt) {
		t.Fatalf("event order: phase %d, start %d, worktree %d, prompt %d in %q", phase, start, worktree, prompt, calls)
	}
	if got := r.events("phase-check-start"); len(got) != 1 || got[0].Phase != "1" || got[0].Fields["phase"] != "1" {
		t.Fatalf("start events %+v", got)
	}
	stored := 0
	for _, rec := range r.store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == "phase-check-start" {
			stored++
		}
	}
	if stored != 1 {
		t.Errorf("stored %d start events, want 1", stored)
	}
	if start == 0 || !strings.HasPrefix(calls[start-1], "Store.Append run-1") {
		t.Errorf("start event was not stored before display: %q", calls)
	}
}

func TestEachPhaseCheckStartIsFollowedByOneResult(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
		make func(*testing.T) *loopRig
	}{
		{"ran", "phase-check", func(t *testing.T) *loopRig { return newCheckRig(t).loopRig }},
		{"timed out", "phase-check-timeout", func(t *testing.T) *loopRig {
			r := newCheckRig(t)
			r.dogHost.err = errors.New("herdr agent prompt: herdr: timeout: no answer within 10m0s")
			return r.loopRig
		}},
		{"skipped", "phase-check-skipped", func(t *testing.T) *loopRig {
			r := newLoopRig(t)
			r.loop.Watcher = &Watch{Store: r.store, Face: r.face}
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.make(t)
			if code := r.run(RunOptions{Phases: []string{"1"}}); code != 0 {
				t.Fatalf("exit %d", code)
			}
			var kinds []string
			for _, ev := range r.face.Events {
				if strings.HasPrefix(ev.Kind, "phase-check") {
					kinds = append(kinds, ev.Kind)
				}
			}
			if want := []string{"phase-check-start", tc.want}; !reflect.DeepEqual(kinds, want) {
				t.Fatalf("kinds %v, want %v", kinds, want)
			}
		})
	}
}

func TestAWatcherWithNoOutcomeStillEndsTheCheck(t *testing.T) {
	r := newEventsRig(t)
	if code := r.run(RunOptions{Phases: []string{"2"}}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var kinds []string
	for _, ev := range r.face.Events {
		if strings.HasPrefix(ev.Kind, "phase-check") {
			kinds = append(kinds, ev.Kind)
		}
	}
	if want := []string{"phase-check-start", "phase-check-skipped"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	if got := r.events("phase-check-skipped"); len(got) != 1 || got[0].Phase != "2" || got[0].Fields["reason"] != "no phase check" {
		t.Errorf("skip events %+v", got)
	}
}

func TestNoPhaseCheckStartWithoutAWatcher(t *testing.T) {
	r := newLoopRig(t)
	if code := r.run(RunOptions{Phases: []string{"1"}}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := r.events("phase-check-start"); len(got) != 0 {
		t.Errorf("start events %+v", got)
	}
}

func TestWarnSignalIsEmittedAndTheRunContinues(t *testing.T) {
	r := newEventsRig(t)
	r.watcher.started = func(ref StepRef, s *Session) {
		if ref.Key.Kind == "plan" {
			r.watcher.signals <- Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: ref.Key, Reason: "drifting"}
		}
	}

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	warnings := r.events("warning")
	if len(warnings) != 1 || warnings[0].Fields["reason"] != "drifting" || warnings[0].Phase != "2" || warnings[0].Step != "plan" {
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
		if ref.Key.Phase == "1" && ref.Key.Kind == "implement" {
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
		if rec.Kind == RecordStep && rec.Step.Phase == "1" && rec.Step.Kind == "implement" && rec.State == StepFailed {
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

	code := r.run(RunOptions{Phases: []string{"2"}})

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

			code := r.run(RunOptions{Phases: []string{"2"}})

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

	code := r.run(RunOptions{Phases: []string{"2"}})

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
	code := r.run(RunOptions{Phases: []string{"1"}})

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

	code := r.run(RunOptions{Phases: []string{"1"}})

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

	code := r.run(RunOptions{Phases: []string{"2"}})

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
	if got := r.calls("SessionHost.Typed "); !reflect.DeepEqual(got, []string{`rloop-p2-implement "r-loop: answer to q1 (by watchdog, citing docs/spec.html:3): sqlite"`}) {
		t.Errorf("typed %v", got)
	}
	var order []string
	for _, c := range r.shared.Calls() {
		switch {
		case c == "Store.Append run-1 question", c == "Face.Emit question", strings.HasPrefix(c, "Watcher.Route"), strings.HasPrefix(c, "AskChannel.Answer"):
			order = append(order, strings.Fields(c)[0])
		}
	}
	for _, c := range r.shared.Calls() {
		if strings.HasPrefix(c, "SessionHost.Typed") {
			order = append(order, strings.Fields(c)[0])
		}
	}
	wantOrder := []string{"Store.Append", "Face.Emit", "Watcher.Route", "Store.Append", "AskChannel.Answer", "SessionHost.Typed"}
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

	code := r.run(RunOptions{Phases: []string{"2"}})

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

	r.run(RunOptions{Phases: []string{"2"}})

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
			stale := StepKey{Run: "run-1", Phase: "2", Kind: "plan", Attempt: 1}
			r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: stale, Reason: "late"}
		}
	}

	code := r.run(RunOptions{Phases: []string{"2"}})

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

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	want := []string{"rloop-p2-plan", "rloop-p2-implement", "rloop-p2-implement-a2"}
	if got := r.agents(); !reflect.DeepEqual(got, want) {
		t.Errorf("spawned %v, want %v", got, want)
	}
}

func TestARestartTheLoopTakesWhileAHaltIsPendingIsAnsweredWithTheHalt(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	r.loop.Sessions.Poll = time.Hour
	r.loop.runDir = r.store.Dir("run-1")
	r.loop.restarts = map[string]int{}
	key := StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}
	w := &lateHaltWatcher{fakeWatcher: r.watcher, halts: make(chan Signal, 1)}
	w.halts <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: key, Reason: "wrong turn"}
	reply := make(chan string, 1)
	r.watcher.restarts <- Restart{Step: key, Addendum: "again", Reply: reply}
	r.loop.Watcher = w
	kind := r.loop.Kinds[1]
	out := Outcome{State: StepFailed, Reason: "backstop"}
	_, ok, aborted := r.loop.awaitRestart(context.Background(), StepRef{Key: key, Kind: kind}, kind, &out)
	if ok || aborted {
		t.Errorf("restart accepted %t aborted %t", ok, aborted)
	}
	select {
	case reason := <-reply:
		if reason != "run halted: watchdog: wrong turn" {
			t.Errorf("reason %q", reason)
		}
	default:
		t.Error("missing reply")
	}
	if !out.Halted {
		t.Errorf("outcome %+v", out)
	}
	if got := r.events("restart"); len(got) != 0 {
		t.Errorf("restarts %+v", got)
	}
}

func TestAStaleRestartIsAnsweredAsNotWaiting(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = 30 * time.Millisecond
	r.host.behaviour["rloop-p2-implement"] = "fail"
	r.host.behaviour["rloop-p2-implement-a2"] = "fail"
	first, stale := make(chan string, 1), make(chan string, 1)
	r.watcher.ended = func(ref StepRef, out Outcome) {
		if out.State == StepFailed && ref.Key.Attempt == 1 {
			r.watcher.restarts <- Restart{Step: ref.Key, Reply: first}
			r.watcher.restarts <- Restart{Step: ref.Key, Reply: stale}
		}
	}
	code := r.run(RunOptions{Phases: []string{"2"}})
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	select {
	case reason := <-first:
		if reason != "" {
			t.Errorf("first reply %q", reason)
		}
	default:
		t.Error("missing first reply")
	}
	select {
	case reason := <-stale:
		if reason != "phase-2/implement attempt 1 is not waiting for a restart" {
			t.Errorf("stale reply %q", reason)
		}
	default:
		t.Error("missing stale reply")
	}
	if got := r.agents(); !reflect.DeepEqual(got, []string{"rloop-p2-plan", "rloop-p2-implement", "rloop-p2-implement-a2"}) {
		t.Errorf("agents %v", got)
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

	r.run(RunOptions{Phases: []string{"2"}})

	want := []string{"codex//", "codex/gpt/high", "gemini//", "codex/gpt/high"}
	if !reflect.DeepEqual(r.resolved, want) {
		t.Errorf("resolved %v, want %v", r.resolved, want)
	}
}

func TestResumeKeepsTheRestartsAlreadySpent(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	for a := 1; a <= 3; a++ {
		r.store.Append("run-1", Record{Kind: RecordStep, Step: &StepKey{Run: "run-1", Phase: "2", Kind: "plan", Attempt: a}, State: StepOK})
		r.store.Append("run-1", Record{Kind: RecordStep, Step: &StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: a}, State: StepFailed})
	}
	for _, a := range []string{"2", "3"} {
		r.store.Append("run-1", Record{Kind: RecordEvent, Event: &Event{Kind: "restart", Phase: "2", Step: "implement", Fields: map[string]string{"step": "phase-2/implement", "attempt": a}}})
	}
	r.host.behaviour["rloop-p2-implement-a4"] = "fail"
	r.restartOnFailure()

	code := r.run(RunOptions{Phases: []string{"2"}, Resume: true})

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
	s := &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}}, Agent: "rloop-p2-implement"}
	r.host.idle["rloop-p2-implement"] = true
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
	waitFor(t, func() bool { return len(r.calls("SessionHost.Typed ")) == 1 })

	if !s.OpenQuestion.Load() {
		t.Error("backstop resumed with a question still open")
	}
	gate <- struct{}{}
	<-done
	waitFor(t, func() bool { return !s.OpenQuestion.Load() })
	if got := r.calls("SessionHost.Typed "); len(got) != 2 {
		t.Errorf("typed %v", got)
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

type spawnHaltHost struct {
	*eventsHost
	hook   string
	trig   bool
	trigMu sync.Mutex
	r      *eventsRig
	t      *testing.T
	key    StepKey
}

func (h *spawnHaltHost) trigger(method, name string) {
	if h.hook != method || !strings.Contains(name, "p1-implement") && !strings.Contains(name, "p1 implement") {
		return
	}
	h.trigMu.Lock()
	if h.trig {
		h.trigMu.Unlock()
		return
	}
	h.trig = true
	h.trigMu.Unlock()
	h.r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: h.key, Reason: "wrong turn"}
	waitFor(h.t, func() bool {
		for _, rec := range recordsForStep(h.r.store, h.key) {
			if rec.Kind == RecordStep && rec.Step != nil && *rec.Step == h.key && rec.State == StepFailed {
				return true
			}
		}
		return false
	})
}

func (h *spawnHaltHost) AgentPane(agent string) (string, error) {
	h.trigger("AgentPane", agent)
	return h.eventsHost.AgentPane(agent)
}

func (h *spawnHaltHost) Start(pane, name, kind string, args []string) (Agent, error) {
	h.trigger("Start", name)
	return h.eventsHost.Start(pane, name, kind, args)
}

func (h *spawnHaltHost) Open(spec OpenSpec) (Workspace, error) {
	h.trigger("Open", spec.Label)
	return h.eventsHost.Open(spec)
}

func recordsForStep(store *loopStore, key StepKey) []Record {
	store.mu.Lock()
	defer store.mu.Unlock()
	var out []Record
	for _, rec := range store.Records[key.Run] {
		if rec.Kind == RecordStep && rec.Step != nil && *rec.Step == key {
			out = append(out, rec)
		}
	}
	return out
}

func assertStepRecords(t *testing.T, store *loopStore, key StepKey, states []StepState, reasons []string) {
	t.Helper()
	recs := recordsForStep(store, key)
	if len(recs) != len(states) {
		t.Fatalf("records %+v, want states %v", recs, states)
	}
	for i, rec := range recs {
		if rec.State != states[i] || rec.Reason != reasons[i] {
			t.Errorf("record %d = %s %q, want %s %q", i, rec.State, rec.Reason, states[i], reasons[i])
		}
	}
}

func TestAHaltBeforeTheWorkspaceOpensLeavesOneFailedRecordAndOpensNothing(t *testing.T) {
	r := newEventsRig(t)
	key := StepKey{Run: "run-1", Phase: "1", Kind: "implement", Attempt: 1}
	r.loop.Sessions.Host = &spawnHaltHost{eventsHost: r.ehost, hook: "AgentPane", r: r, t: t, key: key}
	if code := r.run(RunOptions{Phases: []string{"1"}}); code != 5 {
		t.Fatalf("exit %d", code)
	}
	assertStepRecords(t, r.store, key, []StepState{StepQueued, StepFailed}, []string{"", "watchdog: wrong turn"})
	for _, spec := range r.host.Opened {
		if strings.Contains(spec.Label, "p1 implement") {
			t.Errorf("opened %s", spec.Label)
		}
	}
	for _, agent := range r.host.Agents {
		if strings.Contains(agent, "p1-implement") {
			t.Errorf("started %s", agent)
		}
	}
}

func TestAHaltWhileTheAgentStartsIsTheLastRecordAndInterruptsTheAgent(t *testing.T) {
	r := newEventsRig(t)
	key := StepKey{Run: "run-1", Phase: "1", Kind: "implement", Attempt: 1}
	r.loop.Sessions.Host = &spawnHaltHost{eventsHost: r.ehost, hook: "Start", r: r, t: t, key: key}
	if code := r.run(RunOptions{Phases: []string{"1"}}); code != 5 {
		t.Fatalf("exit %d", code)
	}
	assertStepRecords(t, r.store, key, []StepState{StepQueued, StepSpawned, StepFailed}, []string{"", "", "watchdog: wrong turn"})
	if !slices.Contains(r.calls("SessionHost.Interrupt "), "rloop-p1-implement") {
		t.Errorf("interrupts %v", r.calls("SessionHost.Interrupt "))
	}
}

func TestAHaltAfterTheRunnerRecordedOkLeavesOnlyTheOkRecord(t *testing.T) {
	r := newEventsRig(t)
	key := StepKey{Run: "run-1", Phase: "1", Kind: "implement", Attempt: 1}
	r.watcher.signals = make(chan Signal, 1)
	runner := stepRunnerFunc(func(ctx context.Context, ref StepRef, obs Observer) Outcome {
		if ref.Key != key {
			return (singleRunner{sm: r.loop.Sessions}).Run(ctx, ref, obs)
		}
		out := r.loop.Sessions.Finish(&Session{Ref: ref}, Outcome{State: StepOK})
		r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: key, Reason: "wrong turn"}
		return out
	})
	r.loop.Runners = map[string]StepRunner{"plan-file": runner, "diff": runner}
	if code := r.run(RunOptions{}); code != 5 {
		t.Fatalf("exit %d", code)
	}
	assertStepRecords(t, r.store, key, []StepState{StepQueued, StepOK}, []string{"", ""})
	if slices.Contains(r.calls("Land "), "1") {
		t.Errorf("landed phase 1")
	}
	found := false
	for _, ev := range r.events("warning") {
		if strings.HasPrefix(ev.Fields["reason"], "halt for phase-1/implement after it ended") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings %+v", r.events("warning"))
	}
}

func TestAHaltAfterTheRunnerRecordedFailedKeepsItsRecordAndHalts(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Hour
	key := StepKey{Run: "run-1", Phase: "1", Kind: "implement", Attempt: 1}
	r.watcher.signals = make(chan Signal, 1)
	runner := stepRunnerFunc(func(ctx context.Context, ref StepRef, obs Observer) Outcome {
		if ref.Key != key {
			return (singleRunner{sm: r.loop.Sessions}).Run(ctx, ref, obs)
		}
		out := r.loop.Sessions.Finish(&Session{Ref: ref}, Outcome{State: StepFailed, Reason: "sentinel failed: x"})
		r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: key, Reason: "wrong turn"}
		return out
	})
	r.loop.Runners = map[string]StepRunner{"plan-file": runner, "diff": runner}
	start := time.Now()
	if code := r.run(RunOptions{}); code != 5 {
		t.Fatalf("exit %d", code)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("waited for remedy window")
	}
	assertStepRecords(t, r.store, key, []StepState{StepQueued, StepFailed}, []string{"", "sentinel failed: x"})
}

func TestAHaltMidRunLeavesExactlyOneFailedRecordAndInterruptsTheAgent(t *testing.T) {
	r := newEventsRig(t)
	r.host.behaviour["rloop-p1-implement"] = "hold"
	key := StepKey{Run: "run-1", Phase: "1", Kind: "implement", Attempt: 1}
	r.watcher.started = func(ref StepRef, s *Session) {
		if ref.Key == key {
			r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: key, Reason: "wrong turn"}
		}
	}
	if code := r.run(RunOptions{}); code != 5 {
		t.Fatalf("exit %d", code)
	}
	recs := recordsForStep(r.store, key)
	if len(recs) == 0 || recs[len(recs)-1].State != StepFailed || recs[len(recs)-1].Reason != "watchdog: wrong turn" {
		t.Fatalf("records %+v", recs)
	}
	n := 0
	for _, rec := range recs {
		if rec.State == StepFailed {
			n++
		}
	}
	if n != 1 {
		t.Errorf("failed records %d: %+v", n, recs)
	}
	if !slices.Contains(r.calls("SessionHost.Interrupt "), "rloop-p1-implement") {
		t.Errorf("interrupts %v", r.calls("SessionHost.Interrupt "))
	}
}

func TestAHaltDuringTheReviewHalfInterruptsEveryReviewer(t *testing.T) {
	for _, tc := range []string{"opening", "waiting"} {
		t.Run(tc, func(t *testing.T) {
			r := newEventsRig(t)
			key := StepKey{Run: "run-1", Phase: "1", Kind: "implement", Attempt: 1}
			kind := r.loop.Kinds[1]
			kind.Row.Reviewers, kind.Row.Rounds = []Reviewer{{Provider: "claude"}}, 1
			r.loop.Kinds[1] = kind
			r.loop.Runners = map[string]StepRunner{"diff": singleRunner{sm: r.loop.Sessions, review: func(ctx context.Context, ref StepRef, s *Session, obs Observer) Outcome {
				if ref.Key != key {
					return Outcome{State: StepOK, Session: s}
				}
				rv := &Session{Agent: "rloop-p1-implement-rv-claude", Pane: "pane-rv", Reviewer: "claude"}
				if tc == "waiting" {
					s.setReviewers([]*Session{rv})
				}
				r.watcher.signals <- Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: ref.Key, Reason: "wrong turn"}
				if tc == "opening" {
					waitFor(t, func() bool {
						for _, rec := range recordsForStep(r.store, key) {
							if rec.State == StepFailed {
								return true
							}
						}
						return false
					})
					s.setReviewers([]*Session{rv})
				} else {
					<-ctx.Done()
				}
				return Outcome{State: StepFailed, Reason: "interrupted", Session: s}
			}}}
			if code := r.run(RunOptions{Phases: []string{"1"}}); code != 5 {
				t.Fatalf("exit %d", code)
			}
			calls := r.calls("SessionHost.Interrupt ")
			for _, agent := range []string{"rloop-p1-implement", "rloop-p1-implement-rv-claude"} {
				if !slices.Contains(calls, agent) {
					t.Errorf("interrupts %v missing %s", calls, agent)
				}
			}
			recs := recordsForStep(r.store, key)
			n := 0
			for _, rec := range recs {
				if rec.State == StepFailed {
					n++
				}
			}
			if n != 1 || recs[len(recs)-1].State != StepFailed {
				t.Errorf("records %+v", recs)
			}
		})
	}
}

func TestALandStageQuestionIsWithdrawnAtOnce(t *testing.T) {
	for _, kind := range []string{"gatefix", "gate", "milestone", "gatefix-rv-codex"} {
		t.Run(kind, func(t *testing.T) {
			r := newEventsRig(t)
			q := Question{ID: "q-" + kind, Step: StepKey{Run: "run-1", Phase: "1", Kind: kind, Attempt: 1}, Text: "which?"}
			done := make(chan struct{})
			go func() { r.loop.question(context.Background(), q); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("question did not return")
			}
			found := false
			for _, rec := range r.store.Records["run-1"] {
				if rec.Kind == RecordQuestion && rec.Question != nil && rec.Question.ID == q.ID && rec.Question.AnsweredBy == "withdrawn" && rec.Question.Answer == "land-stage step" {
					found = true
				}
			}
			if !found {
				t.Errorf("question records %+v", r.store.Records["run-1"])
			}
			if got := r.calls("AskChannel.Answer "); len(got) == 0 || !strings.Contains(strings.Join(got, " "), q.ID) || !strings.Contains(strings.Join(got, " "), "withdrawn") {
				t.Errorf("answers %v", got)
			}
			if got := r.calls("Watcher.Route "); len(got) != 0 {
				t.Errorf("routes %v", got)
			}
		})
	}
}

func TestAStepThatEndsWithAnOpenQuestionWithdrawsItAndALateAnswerIsDropped(t *testing.T) {
	r := newEventsRig(t)
	r.watcher.route = holdQuestions
	r.host.behaviour["rloop-p2-implement"] = "ask-fail"

	code := r.run(RunOptions{Phases: []string{"2"}})

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
	key := StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}
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
	s := &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}}}
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

	code := r.run(RunOptions{Phases: []string{"2"}})

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
	failed := StepKey{Run: "run-1", Phase: "1", Kind: "implement", Attempt: 1}
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
				if ref.Key.Phase == "2" && ref.Key.Kind == "implement" {
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
		if ref.Key.Phase == "1" && ref.Key.Kind == "implement" {
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

	if code := r.run(RunOptions{Phases: []string{"2"}}); code != 1 {
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
	code := r.run(RunOptions{Phases: []string{"1"}})

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
	if got := r.events("aborted"); len(got) != 1 || got[0].Phase != "1" || got[0].Step != "implement" || got[0].Fields["workspace"] != "ws-2" {
		t.Errorf("aborted %+v", got)
	}
	if got := r.events("phase-blocked"); len(got) != 0 {
		t.Errorf("blocked %+v", got)
	}
	if got := r.calls("Notifier.Fire "); len(got) != 0 {
		t.Errorf("hooks fired on abort: %v", got)
	}
}

func TestAnInterruptDuringTheRemedyWindowHaltsAtOnce(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = 2 * time.Second
	r.host.behaviour["rloop-p1-implement"] = "fail"
	ctx, cancel := context.WithCancelCause(context.Background())
	r.watcher.ended = func(ref StepRef, out Outcome) {
		if out.State == StepFailed {
			cancel(errors.New("SIGTERM"))
		}
	}
	start := time.Now()
	code := r.loop.Run(ctx, RunOptions{Phases: []string{"1"}})
	if code != 4 || time.Since(start) >= time.Second || len(r.events("phase-blocked")) != 0 {
		t.Fatalf("exit %d, elapsed %s, blocked %+v", code, time.Since(start), r.events("phase-blocked"))
	}
}

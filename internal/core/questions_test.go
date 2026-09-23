package core

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const askingAgent = "rloop-p3-implement"

type routerRig struct {
	*eventsRig
	root    string
	host    *fakeSessionHost
	router  *QuestionRouter
	session *Session
}

func newRouterRig(t *testing.T) *routerRig {
	r := &routerRig{eventsRig: newEventsRig(t), host: &fakeSessionHost{}}
	r.root = r.repo.RootDir
	for _, f := range []string{"docs/x/spec.html", "internal/landed.go", ".r-loop/runs/run-1/notes.md", ".r-loop/wt/phase-3/internal/fresh.go"} {
		writeFile(filepath.Join(r.root, f), "line one\nline two\n")
	}
	r.loop.runDir = r.store.dir
	r.router = &QuestionRouter{
		Dog:     newWatchdog(r.host, r.store, ProviderArgs{Kind: "claude"}),
		Deliver: r.loop.Deliver,
		Repo:    r.repo,
	}
	r.session = &Session{Ref: StepRef{Key: routedQuestion().Step}, Agent: askingAgent}
	r.ehost.idle[askingAgent] = true
	return r
}

func routedQuestion() Question {
	return Question{ID: "q1", Step: StepKey{Run: "run-1", Phase: "3", Kind: "implement", Attempt: 1}, Text: "which db?", Options: []string{"sqlite", "postgres"}, Recommended: "sqlite"}
}

func (r *routerRig) route(t *testing.T) {
	t.Helper()
	r.routeAs(t, routedQuestion(), askingAgent)
}

func (r *routerRig) routeAs(t *testing.T, q Question, agent string) {
	t.Helper()
	r.loop.track(q, r.session, agent)
	r.loop.openQuestion(r.session, 1)
	if !r.router.Route(context.Background(), q) {
		t.Fatal("Route returned false with a live watchdog")
	}
}

func (r *routerRig) questions() []Question {
	st, _ := r.store.Load("run-1")
	return st.Questions
}

func (r *routerRig) typed() []string {
	return r.calls("SessionHost.Typed ")
}

func (r *routerRig) waitTyped(t *testing.T, n int) []string {
	t.Helper()
	waitFor(t, func() bool { return len(r.typed()) >= n })
	return r.typed()
}

func TestRouteHandsTheQuestionToTheWatchdogAndReturns(t *testing.T) {
	r := newRouterRig(t)

	r.route(t)

	want := `SessionHost.Prompt rloop-wd-run-1 "question q1 from phase-3/implement: which db? options: sqlite, postgres recommended: sqlite" false 0s`
	if got := r.host.Calls(); len(got) != 1 || got[0] != want {
		t.Errorf("watchdog prompts %q", got)
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 0 {
		t.Errorf("answered %q", got)
	}
}

func TestACitedAnswerIsLoggedAndTypedIntoTheAskingPane(t *testing.T) {
	r := newRouterRig(t)

	r.route(t)
	ok, reason := r.router.Answer("q1", "sqlite", "docs/x/spec.html:2")

	if !ok || reason != "" {
		t.Fatalf("answer %t %q", ok, reason)
	}
	if got := r.waitTyped(t, 1); len(got) != 1 || got[0] != askingAgent+` "r-loop: answer to q1 (by watchdog, citing docs/x/spec.html:2): sqlite"` {
		t.Errorf("typed %q", got)
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 1 || got[0] != `q1 "sqlite" watchdog "docs/x/spec.html:2"` {
		t.Errorf("ask %q", got)
	}
	if qs := r.questions(); len(qs) != 1 || qs[0].Answer != "sqlite" || qs[0].AnsweredBy != "watchdog" || qs[0].Citation != "docs/x/spec.html:2" {
		t.Errorf("questions %+v", qs)
	}
	evs := r.events("question-answered")
	if len(evs) != 1 {
		t.Fatalf("events %+v", r.face.Events)
	}
	ev := evs[0]
	if ev.Phase != "3" || ev.Step != "implement" || ev.Fields["by"] != "watchdog" || ev.Fields["citation"] != "docs/x/spec.html:2" || ev.Fields["answer"] != "sqlite" {
		t.Errorf("event %+v", ev)
	}
	waitFor(t, func() bool { return !r.session.OpenQuestion.Load() })
}

func stillOpen(t *testing.T, r *routerRig) {
	t.Helper()
	if got := r.calls("AskChannel.Answer "); len(got) != 0 {
		t.Errorf("ask %q", got)
	}
	if ok, reason := r.router.Answer("q1", "sqlite", "docs/x/spec.html:1"); !ok {
		t.Fatalf("the question did not stay open: %q", reason)
	}
	r.waitTyped(t, 1)
}

func TestACitationToAFileOnlyInTheWorktreeIsRefusedAndTheQuestionStaysOpen(t *testing.T) {
	r := newRouterRig(t)

	r.route(t)
	ok, reason := r.router.Answer("q1", "sqlite", "internal/fresh.go:1")

	if ok || !strings.Contains(reason, "internal/fresh.go") {
		t.Fatalf("answer %t %q", ok, reason)
	}
	stillOpen(t, r)
}

func TestACitationUnderRLoopIsRefused(t *testing.T) {
	for _, citation := range []string{".r-loop/runs/run-1/notes.md:1", ".r-loop/wt/phase-3/internal/fresh.go:1", "./.r-loop/runs/run-1/notes.md:1"} {
		t.Run(citation, func(t *testing.T) {
			r := newRouterRig(t)

			r.route(t)
			ok, reason := r.router.Answer("q1", "sqlite", citation)

			if ok || !strings.Contains(reason, ".r-loop") {
				t.Fatalf("answer %t %q", ok, reason)
			}
			stillOpen(t, r)
		})
	}
}

func TestAMalformedEscapingOrEmptyCitationIsRefused(t *testing.T) {
	for _, citation := range []string{"", "  ", "docs/x/spec.html", "docs/x/spec.html:two", "docs/x spec.html:2", "../outside.go:1", "/etc/hosts:1", "docs/x:1", "maintainer:q7"} {
		t.Run(citation, func(t *testing.T) {
			r := newRouterRig(t)
			writeFile(filepath.Join(filepath.Dir(r.root), "outside.go"), "x\n")

			r.route(t)
			ok, reason := r.router.Answer("q1", "sqlite", citation)

			if ok || !strings.Contains(reason, "stays open") {
				t.Fatalf("citation %q: %t %q", citation, ok, reason)
			}
			stillOpen(t, r)
		})
	}
}

func TestAnAnswerTheMaintainerGaveTheWatchdogIsTypedAsTheMaintainers(t *testing.T) {
	r := newRouterRig(t)

	r.route(t)
	ok, reason := r.router.Answer("q1", "postgres", "maintainer")

	if !ok {
		t.Fatalf("answer %t %q", ok, reason)
	}
	if got := r.waitTyped(t, 1); got[0] != askingAgent+` "r-loop: answer to q1 (by maintainer): postgres"` {
		t.Errorf("typed %q", got)
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 1 || got[0] != `q1 "postgres" maintainer ""` {
		t.Errorf("ask %q", got)
	}
	if qs := r.questions(); len(qs) != 1 || qs[0].Answer != "postgres" || qs[0].AnsweredBy != "maintainer" {
		t.Errorf("questions %+v", qs)
	}
	if human := r.events("human"); len(human) != 1 || human[0].Fields["id"] != "q1" {
		t.Errorf("human %+v", human)
	}
}

func TestAnAnswerForAQuestionThatIsNotOpenIsRefused(t *testing.T) {
	r := newRouterRig(t)

	ok, reason := r.router.Answer("q9", "sqlite", "docs/x/spec.html:1")

	if ok || !strings.Contains(reason, "not open") {
		t.Errorf("answer %t %q", ok, reason)
	}
}

func TestASecondAnswerToTheSameQuestionIsRefused(t *testing.T) {
	r := newRouterRig(t)
	r.route(t)
	r.router.Answer("q1", "sqlite", "docs/x/spec.html:1")

	ok, reason := r.router.Answer("q1", "postgres", "docs/x/spec.html:1")

	if ok || !strings.Contains(reason, "not open") {
		t.Errorf("second answer %t %q", ok, reason)
	}
	if got := r.waitTyped(t, 1); len(got) != 1 {
		t.Errorf("typed %q", got)
	}
}

func TestTheAnswerWaitsUntilTheAgentStopsWorking(t *testing.T) {
	r := newRouterRig(t)
	r.ehost.idle[askingAgent] = false
	r.route(t)

	r.router.Answer("q1", "sqlite", "docs/x/spec.html:1")

	time.Sleep(30 * time.Millisecond)
	if got := r.typed(); len(got) != 0 {
		t.Fatalf("typed into a working agent: %q", got)
	}
	if !r.session.OpenQuestion.Load() {
		t.Fatal("the step left waiting-input before its answer was typed")
	}
	r.ehost.mu.Lock()
	r.ehost.idle[askingAgent] = true
	r.ehost.mu.Unlock()
	r.waitTyped(t, 1)
	waitFor(t, func() bool { return !r.session.OpenQuestion.Load() })
}

func TestABlockedAgentGetsTheAnswerOnALaterTry(t *testing.T) {
	r := newRouterRig(t)
	r.ehost.refuse[askingAgent] = 2
	r.route(t)

	r.router.Answer("q1", "sqlite", "docs/x/spec.html:1")

	if got := r.waitTyped(t, 1); len(got) != 1 {
		t.Errorf("typed %q", got)
	}
	if got := r.calls("SessionHost.Refused "); len(got) != 2 {
		t.Errorf("refused %q", got)
	}
}

func TestAReviewerQuestionIsTypedIntoTheReviewerPane(t *testing.T) {
	r := newRouterRig(t)
	reviewer := &Session{Reviewer: "claude", Agent: "rloop-p3-implement-rv-claude-r2"}
	r.session.setReviewers([]*Session{{Reviewer: "codex", Agent: "rloop-p3-implement-rv-codex-r2"}, reviewer})
	r.ehost.idle[reviewer.Agent] = true
	r.loop.setLive(r.session)
	r.watcher.route = r.router.Route
	q := routedQuestion()
	q.Step.Kind = "implement-rv-claude"

	r.loop.question(context.Background(), q)
	if ok, reason := r.router.Answer("q1", "sqlite", "docs/x/spec.html:1"); !ok {
		t.Fatalf("answer refused: %s", reason)
	}

	if got := r.waitTyped(t, 1); len(got) != 1 || !strings.HasPrefix(got[0], reviewer.Agent+" ") {
		t.Errorf("typed %q", got)
	}
	waitFor(t, func() bool { return !r.session.OpenQuestion.Load() })
}

func TestAnAgentGoneBeforeItsAnswerIsTypedWithdrawsTheQuestion(t *testing.T) {
	r := newRouterRig(t)
	r.ehost.stopped[askingAgent] = AgentGone
	r.route(t)

	r.router.Answer("q1", "sqlite", "docs/x/spec.html:1")

	waitFor(t, func() bool {
		qs := r.questions()
		return len(qs) == 1 && qs[0].AnsweredBy == "withdrawn"
	})
	if qs := r.questions(); qs[0].Answer != "agent gone" {
		t.Errorf("questions %+v", qs)
	}
	if r.session.OpenQuestion.Load() {
		t.Error("the step stayed in waiting-input")
	}
	if got := r.typed(); len(got) != 0 {
		t.Errorf("typed %q", got)
	}
}

func TestAStepThatEndsBeforeTheAnswerIsTypedGetsNothing(t *testing.T) {
	r := newRouterRig(t)
	r.ehost.idle[askingAgent] = false
	r.route(t)
	r.router.Answer("q1", "sqlite", "docs/x/spec.html:1")

	r.session.end()
	r.loop.withdrawStep(r.session.Ref.Key, StepFailed)
	r.ehost.mu.Lock()
	r.ehost.idle[askingAgent] = true
	r.ehost.mu.Unlock()

	time.Sleep(30 * time.Millisecond)
	if got := r.typed(); len(got) != 0 {
		t.Errorf("typed into an ended step: %q", got)
	}
	if qs := r.questions(); len(qs) != 1 || qs[0].AnsweredBy != "withdrawn" || qs[0].Answer != "step failed" {
		t.Errorf("questions %+v", qs)
	}
}

func loopQuestion(t *testing.T, router *QuestionRouter, r *eventsRig) (*Session, *Watch) {
	t.Helper()
	w := &Watch{Store: r.store, Router: router}
	r.loop.Watcher = w
	s := &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}}, Agent: "rloop-p2-implement"}
	r.host.idle[s.Agent] = true
	w.StepStarted(s.Ref, s)
	r.loop.runDir = r.store.dir
	r.loop.setLive(s)
	r.loop.question(context.Background(), Question{ID: "q1", Step: s.Ref.Key, Text: "which db?"})
	return s, w
}

func TestAStepQuestionReachesTheWatchdogAndItsAnswerReleasesTheStep(t *testing.T) {
	r := newEventsRig(t)
	host := &fakeSessionHost{}
	writeFile(filepath.Join(r.repo.RootDir, "docs/x/spec.html"), "one\ntwo\n")
	router := &QuestionRouter{Dog: newWatchdog(host, r.store, ProviderArgs{Kind: "claude"}), Deliver: r.loop.Deliver, Repo: r.repo}
	s, _ := loopQuestion(t, router, r)

	if got := host.Calls(); len(got) != 1 {
		t.Fatalf("watchdog prompts %q", got)
	}
	if !s.OpenQuestion.Load() {
		t.Error("backstop running while the watchdog holds the question")
	}
	if ok, reason := router.Answer("q1", "sqlite", "docs/x/spec.html:2"); !ok {
		t.Fatalf("answer refused: %s", reason)
	}

	waitFor(t, func() bool { return !s.OpenQuestion.Load() })
	if got := r.calls("SessionHost.Typed "); !reflect.DeepEqual(got, []string{`rloop-p2-implement "r-loop: answer to q1 (by watchdog, citing docs/x/spec.html:2): sqlite"`}) {
		t.Errorf("typed %q", got)
	}
	if got := r.stepStates("implement"); !reflect.DeepEqual(got, []string{"waiting-input", "running"}) {
		t.Errorf("states %v", got)
	}
}

func goneHalt(t *testing.T, r *eventsRig) Signal {
	t.Helper()
	select {
	case sig := <-r.loop.Watcher.Signals():
		return sig
	case <-time.After(5 * time.Second):
		t.Fatal("no halt")
		return Signal{}
	}
}

func assertOneHaltAndNeverAnswered(t *testing.T, r *eventsRig, s *Session, sig Signal) {
	t.Helper()
	want := Signal{Seq: 1, Kind: SignalHalt, Source: SourceDriver, Step: s.Ref.Key, Reason: "the watchdog is gone", At: sig.At}
	if sig != want {
		t.Errorf("signal %+v, want %+v", sig, want)
	}
	select {
	case more := <-r.loop.Watcher.Signals():
		t.Errorf("a second signal %+v", more)
	case <-time.After(30 * time.Millisecond):
	}
	st, _ := r.store.Load("run-1")
	if len(st.Signals) != 1 || st.Signals[0].Reason != "the watchdog is gone" || st.Signals[0].Source != SourceDriver {
		t.Errorf("recorded signals %+v", st.Signals)
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 0 {
		t.Errorf("answered %q", got)
	}
	if len(st.Questions) != 1 || st.Questions[0].AnsweredBy != "" {
		t.Errorf("questions %+v", st.Questions)
	}
	if !s.OpenQuestion.Load() {
		t.Error("the question was released")
	}
}

func TestAQuestionTheWatchdogCannotBeToldOfHaltsTheRunOnce(t *testing.T) {
	r := newEventsRig(t)
	host := &fakeSessionHost{Err: errors.New("herdr agent prompt: pane closed")}
	router := &QuestionRouter{Dog: newWatchdog(host, r.store, ProviderArgs{Kind: "claude"}), Deliver: r.loop.Deliver, Repo: r.repo}
	s, _ := loopQuestion(t, router, r)

	sig := goneHalt(t, r)

	assertOneHaltAndNeverAnswered(t, r, s, sig)
}

func TestAWatchdogThatGoesWhileItHoldsAQuestionHaltsTheRunOnce(t *testing.T) {
	r := newEventsRig(t)
	host := &fakeSessionHost{}
	router := &QuestionRouter{Dog: newWatchdog(host, r.store, ProviderArgs{Kind: "claude"}), Deliver: r.loop.Deliver, Repo: r.repo}
	s, w := loopQuestion(t, router, r)

	router.Dog.mu.Lock()
	router.Dog.gone = true
	router.Dog.mu.Unlock()
	w.WatchdogGone()
	sig := goneHalt(t, r)

	assertOneHaltAndNeverAnswered(t, r, s, sig)
}

func TestAQuestionForAGoneWatchdogHaltsTheRunAtOnce(t *testing.T) {
	r := newEventsRig(t)
	host := &fakeSessionHost{}
	router := &QuestionRouter{Dog: newWatchdog(host, r.store, ProviderArgs{Kind: "claude"}), Deliver: r.loop.Deliver, Repo: r.repo}
	router.Dog.gone = true
	s, _ := loopQuestion(t, router, r)

	sig := goneHalt(t, r)

	assertOneHaltAndNeverAnswered(t, r, s, sig)
	if got := host.Calls(); len(got) != 0 {
		t.Errorf("prompted a gone watchdog %q", got)
	}
}

func TestWithoutAWatchdogRouteReturnsFalseAtOnce(t *testing.T) {
	r := newRouterRig(t)
	r.router.Dog = nil

	if r.router.Route(context.Background(), routedQuestion()) {
		t.Fatal("Route returned true with no watchdog")
	}
}

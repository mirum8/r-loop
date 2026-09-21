package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type routerRig struct {
	*eventsRig
	root   string
	host   *fakeSessionHost
	router *QuestionRouter
}

func newRouterRig(t *testing.T, poll time.Duration) *routerRig {
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
		poll:    poll,
	}
	return r
}

func routedQuestion() Question {
	return Question{ID: "q1", Step: StepKey{Run: "run-1", Phase: 3, Kind: "implement", Attempt: 1}, Text: "which db?", Options: []string{"sqlite", "postgres"}, Recommended: "sqlite"}
}

func (r *routerRig) route(t *testing.T) <-chan bool {
	t.Helper()
	r.loop.track(routedQuestion(), &Session{Ref: StepRef{Key: routedQuestion().Step}})
	routed := make(chan bool, 1)
	go func() { routed <- r.router.Route(context.Background(), routedQuestion()) }()
	waitFor(t, func() bool { return len(r.host.Calls()) == 1 })
	return routed
}

func (r *routerRig) questions() []Question {
	st, _ := r.store.Load("run-1")
	return st.Questions
}

func routeResult(t *testing.T, routed <-chan bool) bool {
	t.Helper()
	select {
	case ok := <-routed:
		return ok
	case <-time.After(5 * time.Second):
		t.Fatal("Route did not return")
		return false
	}
}

func TestACitedAnswerReachesTheSessionWithItsCitationLogged(t *testing.T) {
	r := newRouterRig(t, time.Hour)

	routed := r.route(t)
	ok, reason := r.router.Answer("q1", "sqlite", "docs/x/spec.html:2")

	if !ok || reason != "" {
		t.Fatalf("answer %t %q", ok, reason)
	}
	if !routeResult(t, routed) {
		t.Fatal("Route returned false for an accepted answer")
	}
	want := `SessionHost.Prompt rloop-wd-run-1 "question q1 from phase-3/implement: which db? options: sqlite, postgres recommended: sqlite" false 0s`
	if got := r.host.Calls(); len(got) != 1 || got[0] != want {
		t.Errorf("watchdog prompts %q", got)
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
	if ev.Kind != "question-answered" || ev.Phase != 3 || ev.Step != "implement" || ev.Fields["by"] != "watchdog" || ev.Fields["citation"] != "docs/x/spec.html:2" || ev.Fields["answer"] != "sqlite" {
		t.Errorf("event %+v", ev)
	}
}

func stillOpen(t *testing.T, r *routerRig, routed <-chan bool) {
	t.Helper()
	select {
	case ok := <-routed:
		t.Fatalf("Route returned %t for a refused answer", ok)
	case <-time.After(30 * time.Millisecond):
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 0 {
		t.Errorf("ask %q", got)
	}
	if ok, reason := r.router.Answer("q1", "sqlite", "docs/x/spec.html:1"); !ok || !routeResult(t, routed) {
		t.Fatalf("the question did not stay open: %t %q", ok, reason)
	}
}

func TestACitationToAFileOnlyInTheWorktreeIsRefusedAndTheQuestionStaysOpen(t *testing.T) {
	r := newRouterRig(t, time.Hour)

	routed := r.route(t)
	ok, reason := r.router.Answer("q1", "sqlite", "internal/fresh.go:1")

	if ok || !strings.Contains(reason, "internal/fresh.go") {
		t.Fatalf("answer %t %q", ok, reason)
	}
	stillOpen(t, r, routed)
}

func TestACitationUnderRLoopIsRefused(t *testing.T) {
	for _, citation := range []string{".r-loop/runs/run-1/notes.md:1", ".r-loop/wt/phase-3/internal/fresh.go:1", "./.r-loop/runs/run-1/notes.md:1"} {
		t.Run(citation, func(t *testing.T) {
			r := newRouterRig(t, time.Hour)

			routed := r.route(t)
			ok, reason := r.router.Answer("q1", "sqlite", citation)

			if ok || !strings.Contains(reason, ".r-loop") {
				t.Fatalf("answer %t %q", ok, reason)
			}
			stillOpen(t, r, routed)
		})
	}
}

func TestAMalformedEscapingOrEmptyCitationIsRefused(t *testing.T) {
	for _, citation := range []string{"", "  ", "docs/x/spec.html", "docs/x/spec.html:two", "docs/x spec.html:2", "../outside.go:1", "/etc/hosts:1", "docs/x:1", "maintainer:q7"} {
		t.Run(citation, func(t *testing.T) {
			r := newRouterRig(t, time.Hour)
			writeFile(filepath.Join(filepath.Dir(r.root), "outside.go"), "x\n")

			routed := r.route(t)
			ok, reason := r.router.Answer("q1", "sqlite", citation)

			if ok || !strings.Contains(reason, "stays open") {
				t.Fatalf("citation %q: %t %q", citation, ok, reason)
			}
			stillOpen(t, r, routed)
		})
	}
}

func TestAnAnswerTheMaintainerGaveTheWatchdogIsDeliveredAsTheMaintainers(t *testing.T) {
	r := newRouterRig(t, time.Hour)

	routed := r.route(t)
	ok, reason := r.router.Answer("q1", "postgres", "maintainer")

	if !ok || !routeResult(t, routed) {
		t.Fatalf("answer %t %q", ok, reason)
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
	r := newRouterRig(t, time.Hour)

	ok, reason := r.router.Answer("q9", "sqlite", "docs/x/spec.html:1")

	if ok || !strings.Contains(reason, "not open") {
		t.Errorf("answer %t %q", ok, reason)
	}
}

func TestAQuestionStaysWithALiveWatchdog(t *testing.T) {
	r := newRouterRig(t, 5*time.Millisecond)

	routed := r.route(t)

	select {
	case ok := <-routed:
		t.Fatalf("Route returned %t while the watchdog is live", ok)
	case <-time.After(50 * time.Millisecond):
	}
	if ok, reason := r.router.Answer("q1", "sqlite", "docs/x/spec.html:1"); !ok || !routeResult(t, routed) {
		t.Fatalf("answer %t %q", ok, reason)
	}
}

func TestAWatchdogThatGoesAwayHandsTheQuestionBack(t *testing.T) {
	r := newRouterRig(t, 5*time.Millisecond)

	routed := r.route(t)
	r.router.Dog.mu.Lock()
	r.router.Dog.gone = true
	r.router.Dog.mu.Unlock()

	if routeResult(t, routed) {
		t.Fatal("Route returned true after the watchdog went away")
	}
	if ok, reason := r.router.Answer("q1", "sqlite", "docs/x/spec.html:1"); ok || !strings.Contains(reason, "not open") {
		t.Errorf("late answer %t %q", ok, reason)
	}
}

func TestWithoutAWatchdogRouteReturnsFalseAtOnce(t *testing.T) {
	r := newRouterRig(t, time.Hour)
	r.router.Dog = nil

	if r.router.Route(context.Background(), routedQuestion()) {
		t.Fatal("Route returned true with no watchdog")
	}
}

func TestAnUnreachableWatchdogIsNotAskedAgain(t *testing.T) {
	r := newRouterRig(t, time.Hour)
	r.router.Dog.gone = true

	if r.router.Route(context.Background(), routedQuestion()) {
		t.Fatal("Route returned true")
	}
	if got := r.host.Calls(); len(got) != 0 {
		t.Errorf("prompted %q", got)
	}
}

func loopQuestion(t *testing.T, router *QuestionRouter, r *eventsRig) (*Session, context.CancelFunc, chan struct{}) {
	t.Helper()
	w := &Watch{Store: r.store, Router: router}
	r.loop.Watcher = w
	s := &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}}}
	w.StepStarted(s.Ref, s)
	r.loop.runDir = r.store.dir
	r.loop.setLive(s)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { r.loop.question(ctx, Question{ID: "q1", Step: s.Ref.Key, Text: "which db?"}); close(done) }()
	return s, cancel, done
}

func TestAStepQuestionReachesTheWatchdogAndItsAnswerReleasesTheStep(t *testing.T) {
	r := newEventsRig(t)
	host := &fakeSessionHost{}
	writeFile(filepath.Join(r.repo.RootDir, "docs/x/spec.html"), "one\ntwo\n")
	router := &QuestionRouter{Dog: newWatchdog(host, r.store, ProviderArgs{Kind: "claude"}), Deliver: r.loop.Deliver, Repo: r.repo, poll: time.Hour}
	s, _, done := loopQuestion(t, router, r)

	waitFor(t, func() bool { return len(host.Calls()) == 1 })
	if !s.OpenQuestion.Load() {
		t.Error("backstop running while the watchdog holds the question")
	}
	if ok, reason := router.Answer("q1", "sqlite", "docs/x/spec.html:2"); !ok {
		t.Fatalf("answer refused: %s", reason)
	}
	<-done

	if s.OpenQuestion.Load() {
		t.Error("step still frozen after the watchdog answered")
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 1 || got[0] != `q1 "sqlite" watchdog "docs/x/spec.html:2"` {
		t.Errorf("answered %q", got)
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

func assertNeverAnswered(t *testing.T, r *eventsRig, s *Session, sig Signal) {
	t.Helper()
	want := Signal{Seq: 1, Kind: SignalHalt, Source: SourceDriver, Step: s.Ref.Key, Reason: "the watchdog is gone", At: sig.At}
	if sig != want {
		t.Errorf("signal %+v, want %+v", sig, want)
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

func TestAWatchdogThatGoesAwayHaltsTheRunAndTheQuestionIsNeverAnswered(t *testing.T) {
	r := newEventsRig(t)
	host := &fakeSessionHost{}
	router := &QuestionRouter{Dog: newWatchdog(host, r.store, ProviderArgs{Kind: "claude"}), Deliver: r.loop.Deliver, Repo: r.repo, poll: 5 * time.Millisecond}
	s, _, done := loopQuestion(t, router, r)

	waitFor(t, func() bool { return len(host.Calls()) == 1 })
	router.Dog.mu.Lock()
	router.Dog.gone = true
	router.Dog.mu.Unlock()
	sig := goneHalt(t, r)
	<-done

	assertNeverAnswered(t, r, s, sig)
}

func TestAQuestionForAGoneWatchdogHaltsTheRunAtOnce(t *testing.T) {
	r := newEventsRig(t)
	host := &fakeSessionHost{}
	router := &QuestionRouter{Dog: newWatchdog(host, r.store, ProviderArgs{Kind: "claude"}), Deliver: r.loop.Deliver, Repo: r.repo, poll: time.Hour}
	router.Dog.gone = true
	s, _, done := loopQuestion(t, router, r)

	sig := goneHalt(t, r)
	<-done

	assertNeverAnswered(t, r, s, sig)
	if got := host.Calls(); len(got) != 0 {
		t.Errorf("prompted a gone watchdog %q", got)
	}
}

func TestARunThatEndsWhileTheWatchdogHoldsAQuestionAnswersNothing(t *testing.T) {
	r := newEventsRig(t)
	host := &fakeSessionHost{}
	router := &QuestionRouter{Dog: newWatchdog(host, r.store, ProviderArgs{Kind: "claude"}), Deliver: r.loop.Deliver, Repo: r.repo, poll: time.Hour}
	_, cancel, done := loopQuestion(t, router, r)

	waitFor(t, func() bool { return len(host.Calls()) == 1 })
	cancel()
	<-done

	if got := r.calls("AskChannel.Answer "); len(got) != 0 {
		t.Errorf("answered after the run ended: %q", got)
	}
}

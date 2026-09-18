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

func newRouterRig(t *testing.T, window time.Duration) *routerRig {
	r := &routerRig{eventsRig: newEventsRig(t), host: &fakeSessionHost{}}
	r.root = r.repo.RootDir
	for _, f := range []string{"docs/x/spec.html", "internal/landed.go", ".r-loop/runs/run-1/notes.md", ".r-loop/wt/phase-3/internal/fresh.go"} {
		writeFile(filepath.Join(r.root, f), "line one\nline two\n")
	}
	r.loop.runDir = r.store.dir
	r.router = &QuestionRouter{
		Dog:          newWatchdog(r.host, r.store, ProviderArgs{Kind: "claude"}),
		Deliver:      r.loop.Deliver,
		Repo:         r.repo,
		AnswerWindow: window,
	}
	return r
}

func routedQuestion() Question {
	return Question{ID: "q1", Step: StepKey{Run: "run-1", Phase: 3, Kind: "implement", Attempt: 1}, Text: "which db?", Options: []string{"sqlite", "postgres"}}
}

func (r *routerRig) route(t *testing.T) <-chan bool {
	t.Helper()
	r.loop.track(routedQuestion(), nil)
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
	want := `SessionHost.Prompt rloop-watchdog "question q1 from phase-3/implement: which db? options: sqlite, postgres" false 0s`
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

func TestACitationToAFileOnlyInTheWorktreeIsRejectedThenEscalated(t *testing.T) {
	r := newRouterRig(t, time.Hour)

	routed := r.route(t)
	ok, reason := r.router.Answer("q1", "sqlite", "internal/fresh.go:1")

	if ok || !strings.Contains(reason, "internal/fresh.go") {
		t.Fatalf("answer %t %q", ok, reason)
	}
	if routeResult(t, routed) {
		t.Fatal("Route returned true for a rejected citation")
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 0 {
		t.Errorf("ask %q", got)
	}
	if qs := r.questions(); len(qs) != 0 {
		t.Errorf("questions %+v", qs)
	}
}

func TestACitationUnderRLoopIsRejected(t *testing.T) {
	for _, citation := range []string{".r-loop/runs/run-1/notes.md:1", ".r-loop/wt/phase-3/internal/fresh.go:1", "./.r-loop/runs/run-1/notes.md:1"} {
		t.Run(citation, func(t *testing.T) {
			r := newRouterRig(t, time.Hour)

			routed := r.route(t)
			ok, reason := r.router.Answer("q1", "sqlite", citation)

			if ok || !strings.Contains(reason, ".r-loop") {
				t.Fatalf("answer %t %q", ok, reason)
			}
			if routeResult(t, routed) {
				t.Fatal("Route returned true")
			}
		})
	}
}

func TestAMalformedOrEscapingCitationIsRejected(t *testing.T) {
	for _, citation := range []string{"docs/x/spec.html", "docs/x/spec.html:two", "docs/x spec.html:2", "../outside.go:1", "/etc/hosts:1", "docs/x:1"} {
		t.Run(citation, func(t *testing.T) {
			r := newRouterRig(t, time.Hour)
			writeFile(filepath.Join(filepath.Dir(r.root), "outside.go"), "x\n")

			routed := r.route(t)
			ok, _ := r.router.Answer("q1", "sqlite", citation)

			if ok || routeResult(t, routed) {
				t.Fatalf("citation %q accepted", citation)
			}
		})
	}
}

func TestAnEmptyCitationEscalatesAtOnce(t *testing.T) {
	r := newRouterRig(t, time.Hour)

	routed := r.route(t)
	ok, reason := r.router.Answer("q1", "", "")

	if ok || reason == "" {
		t.Fatalf("answer %t %q", ok, reason)
	}
	if routeResult(t, routed) {
		t.Fatal("Route returned true for an escalation")
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 0 {
		t.Errorf("ask %q", got)
	}
}

func TestAWatchdogAnswerAfterTheMaintainerWonIsRefusedAndNeverRecorded(t *testing.T) {
	r := newRouterRig(t, time.Hour)

	routed := r.route(t)
	if err := r.loop.Answer("q1", "postgres", "maintainer"); err != nil {
		t.Fatal(err)
	}
	ok, reason := r.router.Answer("q1", "sqlite", "docs/x/spec.html:2")

	if ok || !strings.Contains(reason, "not open") {
		t.Fatalf("answer %t %q", ok, reason)
	}
	if !routeResult(t, routed) {
		t.Error("Route handed an answered question on to the face")
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 1 || got[0] != `q1 "postgres" maintainer ""` {
		t.Errorf("ask %q", got)
	}
	if qs := r.questions(); len(qs) != 1 || qs[0].Answer != "postgres" || qs[0].AnsweredBy != "maintainer" {
		t.Errorf("questions %+v", qs)
	}
	if evs := r.events("question-answered"); len(evs) != 0 {
		t.Errorf("events %+v", evs)
	}
}

func TestAnAnswerForAQuestionThatIsNotOpenIsRefused(t *testing.T) {
	r := newRouterRig(t, time.Hour)

	ok, reason := r.router.Answer("q9", "sqlite", "docs/x/spec.html:1")

	if ok || !strings.Contains(reason, "not open") {
		t.Errorf("answer %t %q", ok, reason)
	}
}

func TestTheWindowExpiringReturnsFalseAndClosesTheQuestion(t *testing.T) {
	r := newRouterRig(t, 20*time.Millisecond)

	routed := r.route(t)

	if routeResult(t, routed) {
		t.Fatal("Route returned true after the window")
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
	var w Watch
	if w.Route(context.Background(), routedQuestion()) {
		t.Fatal("Watch without a router routed")
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

func TestTheWindowExpiringHandsTheQuestionToTheFaceWithTheBackstopFrozen(t *testing.T) {
	r := newEventsRig(t)
	gate := make(chan struct{})
	r.loop.Face = &gatedFace{fakeFace: r.face, gate: gate}
	host := &fakeSessionHost{}
	router := &QuestionRouter{
		Dog:          newWatchdog(host, r.store, ProviderArgs{Kind: "claude"}),
		Deliver:      r.loop.Deliver,
		Repo:         r.repo,
		AnswerWindow: 50 * time.Millisecond,
	}
	r.loop.Watcher = &Watch{Store: r.store, Router: router}
	s := &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}}}
	r.loop.runDir = r.store.dir
	r.loop.setLive(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { r.loop.question(ctx, Question{ID: "q1", Step: s.Ref.Key, Text: "which db?"}); close(done) }()
	waitFor(t, func() bool { return len(host.Calls()) == 1 })
	if !s.OpenQuestion.Load() {
		t.Error("backstop running while the watchdog holds the question")
	}
	waitFor(t, func() bool { return len(r.calls("Face.Ask ")) == 1 })
	if !s.OpenQuestion.Load() {
		t.Error("backstop running while the face holds the question")
	}
	gate <- struct{}{}
	<-done

	if s.OpenQuestion.Load() {
		t.Error("step still frozen after the maintainer answered")
	}
	if got := r.calls("AskChannel.Answer "); len(got) != 1 || !strings.Contains(got[0], "maintainer") {
		t.Errorf("answered %q", got)
	}
	var states []StepState
	for _, rec := range r.store.Records["run-1"] {
		if rec.Kind == RecordStep {
			states = append(states, rec.State)
		}
	}
	if len(states) != 2 || states[0] != StepWaitingInput || states[1] != StepRunning {
		t.Errorf("states %v", states)
	}
}

func TestAWatchdogAnswerThroughTheLoopReleasesTheStepWithoutTheFace(t *testing.T) {
	r := newEventsRig(t)
	host := &fakeSessionHost{}
	root := r.repo.RootDir
	writeFile(filepath.Join(root, "docs/x/spec.html"), "one\ntwo\n")
	router := &QuestionRouter{
		Dog:          newWatchdog(host, r.store, ProviderArgs{Kind: "claude"}),
		Deliver:      r.loop.Deliver,
		Repo:         r.repo,
		AnswerWindow: time.Hour,
	}
	r.loop.Watcher = &Watch{Store: r.store, Router: router}
	s := &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}}}
	r.loop.runDir = r.store.dir
	r.loop.setLive(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { r.loop.question(ctx, Question{ID: "q1", Step: s.Ref.Key, Text: "which db?"}); close(done) }()
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
	if got := r.calls("Face.Ask "); len(got) != 0 {
		t.Errorf("face asked %q", got)
	}
}

func TestARunThatEndsWhileTheWatchdogHoldsAQuestionNeverAsksTheFace(t *testing.T) {
	r := newEventsRig(t)
	host := &fakeSessionHost{}
	r.loop.Watcher = &Watch{Store: r.store, Router: &QuestionRouter{
		Dog:          newWatchdog(host, r.store, ProviderArgs{Kind: "claude"}),
		Deliver:      r.loop.Deliver,
		Repo:         r.repo,
		AnswerWindow: time.Hour,
	}}
	s := &Session{Ref: StepRef{Key: StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}}}
	r.loop.runDir = r.store.dir
	r.loop.setLive(s)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { r.loop.question(ctx, Question{ID: "q1", Step: s.Ref.Key, Text: "which db?"}); close(done) }()
	waitFor(t, func() bool { return len(host.Calls()) == 1 })
	cancel()
	<-done

	if got := r.calls("Face.Ask "); len(got) != 0 {
		t.Errorf("face asked after the run ended: %q", got)
	}
}

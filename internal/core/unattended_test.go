package core

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func askUnanswered(t *testing.T, timeout time.Duration, q Question) (*eventsRig, chan struct{}) {
	t.Helper()
	r := newEventsRig(t)
	gate := make(chan struct{})
	r.loop.Face = &gatedFace{fakeFace: r.face, gate: gate}
	r.loop.QuestionTimeout = timeout
	s := &Session{Ref: StepRef{Key: q.Step}}
	r.loop.runDir = r.store.dir
	r.loop.setLive(s)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go r.loop.question(ctx, q)
	waitFor(t, func() bool { return len(r.calls("Face.Ask ")) == 1 })
	return r, gate
}

func TestAnUnansweredQuestionTimesOutToTheAgentsRecommendation(t *testing.T) {
	key := StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}
	r, _ := askUnanswered(t, 20*time.Millisecond, Question{ID: "q1", Step: key, Text: "which db?", Options: []string{"sqlite", "postgres"}, Recommended: "sqlite"})

	waitFor(t, func() bool { return len(r.calls("AskChannel.Answer ")) == 1 })

	text := "No answer within 20ms. Proceed with your recommendation: sqlite"
	if got := r.calls("AskChannel.Answer "); !reflect.DeepEqual(got, []string{`q1 "` + text + `" timeout ""`}) {
		t.Errorf("answers %v", got)
	}
	if got := r.calls("Face.Withdraw "); !reflect.DeepEqual(got, []string{"q1"}) {
		t.Errorf("withdrawn %v", got)
	}
	st, _ := r.store.Load("run-1")
	if len(st.Questions) != 1 || st.Questions[0].AnsweredBy != "timeout" || st.Questions[0].Answer != text {
		t.Errorf("questions %+v", st.Questions)
	}
	if got := r.events("question-answered"); len(got) != 1 || got[0].Fields["by"] != "timeout" {
		t.Errorf("question-answered %+v", got)
	}
	if got := r.events("human"); len(got) != 0 {
		t.Errorf("a timeout counted as a human touch: %+v", got)
	}
	rep := Report(st, Plan{})
	if !strings.Contains(rep, "## Automatic decisions\n\n- q1 phase 2 implement: timed out; the agent took "+text+"\n") {
		t.Errorf("report:\n%s", rep)
	}
}

func TestAQuestionWithNoRecommendationTimesOutToTheSafestOption(t *testing.T) {
	key := StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}
	r, _ := askUnanswered(t, 20*time.Millisecond, Question{ID: "q1", Step: key, Text: "which db?"})

	waitFor(t, func() bool { return len(r.calls("AskChannel.Answer ")) == 1 })

	want := `q1 "No answer within 20ms. Proceed with the option you judge safest and name it in your sentinel's reason." timeout ""`
	if got := r.calls("AskChannel.Answer "); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("answers %v", got)
	}
}

func TestWithoutAQuestionTimeoutAnOpenQuestionWaitsForAnAnswer(t *testing.T) {
	key := StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}
	r, gate := askUnanswered(t, 0, Question{ID: "q1", Step: key, Text: "which db?", Recommended: "sqlite"})

	time.Sleep(50 * time.Millisecond)

	if got := r.calls("AskChannel.Answer "); len(got) != 0 {
		t.Fatalf("answered without the flag: %v", got)
	}
	close(gate)
	waitFor(t, func() bool { return len(r.calls("AskChannel.Answer ")) == 1 })
	if got := r.calls("AskChannel.Answer "); !reflect.DeepEqual(got, []string{`q1 "yes" maintainer ""`}) {
		t.Errorf("answers %v", got)
	}
}

func TestAnAnswerBeforeTheTimeoutWins(t *testing.T) {
	key := StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}
	r, gate := askUnanswered(t, 30*time.Millisecond, Question{ID: "q1", Step: key, Text: "which db?", Recommended: "sqlite"})

	close(gate)
	waitFor(t, func() bool { return len(r.calls("AskChannel.Answer ")) == 1 })
	time.Sleep(60 * time.Millisecond)

	if got := r.calls("AskChannel.Answer "); !reflect.DeepEqual(got, []string{`q1 "yes" maintainer ""`}) {
		t.Errorf("answers %v", got)
	}
	if got := r.events("note"); len(got) != 0 {
		t.Errorf("notes %+v", got)
	}
}

func fallbackRemedies(t *testing.T, store *fakeStore, allow ...string) (*Remedies, *Watch) {
	t.Helper()
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, allow...)
	rem.Fallbacks = map[string]Fallback{"implement": {Provider: "claude", Model: "sonnet", Effort: "low"}}
	return rem, w
}

func TestAnAllowListedProviderRestartOnTheRowsFallbackIsAccepted(t *testing.T) {
	store := &fakeStore{}
	rem, w := fallbackRemedies(t, store, "provider")
	rem.Propose("provider", "switch to the fallback", "codex usage limit reached", "")
	got := make(chan Restart, 1)
	go func() { got <- <-w.Restarts() }()

	ok, reason := rem.Restart("phase-2/implement", "", "claude", "")

	if !ok || reason != "" {
		t.Fatalf("restart %v %q", ok, reason)
	}
	if rs := <-got; rs.Provider != "claude" || rs.Remedy != "remedy-1" {
		t.Errorf("restart %+v", rs)
	}
}

func TestAProviderRestartOnAnotherProviderNeedsAConsentQuestion(t *testing.T) {
	store := &fakeStore{}
	rem, w := fallbackRemedies(t, store, "provider")
	rem.Propose("provider", "switch provider", "codex usage limit reached", "")
	go func() { <-w.Restarts() }()

	ok, reason := rem.Restart("phase-2/implement", "", "gemini", "")
	if ok || !strings.Contains(reason, "gemini is not the row's fallback") || !strings.Contains(reason, "ask_user") {
		t.Fatalf("restart without consent %v %q", ok, reason)
	}
	if ok, reason := rem.Restart("phase-2/implement", "", "gemini", "q8"); ok || !strings.Contains(reason, "q8 is not a question the maintainer answered") {
		t.Fatalf("restart on a timed-out question %v %q", ok, reason)
	}

	ok, reason = rem.Restart("phase-2/implement", "", "gemini", "q9")

	if !ok {
		t.Fatalf("restart %v %q", ok, reason)
	}
	recs := remedyRecords(store)
	if len(recs) != 2 || recs[1].Class != "provider" || recs[1].Command != "restart phase-2/implement on gemini" || recs[1].Consent != "maintainer" {
		t.Errorf("records %+v", recs)
	}
}

func TestAProviderRestartWithOnlyTheAllowListAndNoRemedyAcceptsOnlyTheFallback(t *testing.T) {
	store := &fakeStore{}
	rem, w := fallbackRemedies(t, store, "provider")
	go func() { <-w.Restarts() }()

	if ok, reason := rem.Restart("phase-2/implement", "", "gemini", ""); ok || !strings.Contains(reason, "is not the row's fallback") {
		t.Errorf("gemini restart %v %q", ok, reason)
	}
	if ok, reason := rem.Restart("phase-2/implement", "", "claude", ""); !ok {
		t.Errorf("fallback restart %v %q", ok, reason)
	}
}

func TestAFallbackRestartRunsOnTheFallbacksModelAndEffortAndTheReportNamesAllThree(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	kind := r.loop.Kinds[1]
	kind.Row.Model, kind.Row.Effort = "gpt", "high"
	kind.Row.Fallback = Fallback{Provider: "claude", Model: "sonnet"}
	r.loop.Kinds[1] = kind
	r.host.behaviour["rloop-p2-implement"] = "fail"
	w := &Watch{Store: r.store}
	r.loop.Watcher = w
	rem := newRemedies(w, r.store, "provider")
	rem.Fallbacks = map[string]Fallback{"implement": kind.Row.Fallback}
	decided := make(chan string, 1)
	go func() {
		for {
			if key, ok := w.holding(); ok && key.Kind == "implement" {
				break
			}
			time.Sleep(time.Millisecond)
		}
		rem.Propose("provider", "switch to the fallback", "codex usage limit reached", "")
		_, reason := rem.Restart("phase-2/implement", "", "claude", "")
		decided <- reason
	}()

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if reason := <-decided; reason != "" {
		t.Fatalf("restart %q", reason)
	}
	if want := []string{"codex//", "codex/gpt/high", "claude/sonnet/"}; !reflect.DeepEqual(r.resolved, want) {
		t.Errorf("resolved %v, want %v", r.resolved, want)
	}
	rep := r.report(t)
	if line := "- phase 2 implement: restart as attempt 2 on claude model sonnet effort provider default (remedy: switch to the fallback)\n"; !strings.Contains(rep, line) {
		t.Errorf("report missing %q:\n%s", line, rep)
	}
}

func TestAnAllowListedRestartClassDoesNotSwitchProviders(t *testing.T) {
	store := &fakeStore{}
	rem, w := fallbackRemedies(t, store, "restart", "retry")
	go func() { <-w.Restarts() }()

	ok, reason := rem.Restart("phase-2/implement", "use claude", "claude", "")

	if ok || reason != "no authorised remedy" {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestAMaintainerApprovedProviderRemedyDoesNotAuthoriseAnotherProviderWithoutAsking(t *testing.T) {
	store := &fakeStore{}
	rem, w := fallbackRemedies(t, store)
	rem.Propose("provider", "switch to claude", "codex usage limit reached", "q9")
	go func() { <-w.Restarts() }()

	ok, reason := rem.Restart("phase-2/implement", "", "gemini", "")

	if ok || !strings.Contains(reason, "gemini is not the row's fallback") {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestAMaintainerApprovedProviderRemedyNamingTheProviderIsNotAskedAgain(t *testing.T) {
	store := &fakeStore{}
	rem, w := fallbackRemedies(t, store)
	rem.Propose("provider", "restart phase-2/implement on gemini", "codex usage limit reached", "q9")
	got := make(chan Restart, 1)
	go func() { got <- <-w.Restarts() }()

	ok, reason := rem.Restart("phase-2/implement", "", "gemini", "")

	if !ok || reason != "" {
		t.Fatalf("restart %v %q", ok, reason)
	}
	if rs := <-got; rs.Provider != "gemini" || rs.Remedy != "remedy-1" {
		t.Errorf("restart %+v", rs)
	}
	if recs := remedyRecords(store); len(recs) != 1 {
		t.Errorf("records %+v", recs)
	}
}

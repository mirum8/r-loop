package core

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

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

func TestAProviderRestartOnAnotherProviderNeedsTheMaintainersWord(t *testing.T) {
	store := &fakeStore{}
	rem, w := fallbackRemedies(t, store, "provider")
	rem.Propose("provider", "switch provider", "codex usage limit reached", "")
	go func() { <-w.Restarts() }()

	ok, reason := rem.Restart("phase-2/implement", "", "gemini", "")
	if ok || !strings.Contains(reason, "gemini is not the row's fallback") || !strings.Contains(reason, "maintainer_said") {
		t.Fatalf("restart without consent %v %q", ok, reason)
	}
	if ok, reason := rem.Restart("phase-2/implement", "", "gemini", " "); ok || !strings.Contains(reason, "maintainer_said") {
		t.Fatalf("restart on a blank quote %v %q", ok, reason)
	}

	ok, reason = rem.Restart("phase-2/implement", "", "gemini", "yes, use gemini")

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
	rem.Propose("provider", "switch to claude", "codex usage limit reached", "yes, go ahead")
	go func() { <-w.Restarts() }()

	ok, reason := rem.Restart("phase-2/implement", "", "gemini", "")

	if ok || !strings.Contains(reason, "gemini is not the row's fallback") {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestAMaintainerApprovedProviderRemedyNamingTheProviderIsNotAskedAgain(t *testing.T) {
	store := &fakeStore{}
	rem, w := fallbackRemedies(t, store)
	rem.Propose("provider", "restart phase-2/implement on gemini", "codex usage limit reached", "yes, go ahead")
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

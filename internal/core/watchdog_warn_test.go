package core

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type warnPrompts struct {
	t       *testing.T
	repo    *loopRepo
	mu      sync.Mutex
	reviews []map[string]any
	fixes   []map[string]any
}

func (p *warnPrompts) Render(name string, vars map[string]any) (string, string, error) {
	copied := make(map[string]any, len(vars)+1)
	for k, v := range vars {
		copied[k] = v
	}
	copied["prompt"] = name
	switch name {
	case "review-security":
		p.mu.Lock()
		p.reviews = append(p.reviews, copied)
		p.mu.Unlock()
		p.repo.mu.Lock()
		p.repo.changes = nil
		p.repo.mu.Unlock()
		writeFindings(p.t, copied, "ok", 1)
		return "r-loop: review", "embedded:" + name, nil
	case "fix":
		p.mu.Lock()
		p.fixes = append(p.fixes, copied)
		p.mu.Unlock()
		p.repo.setChanges("a.go")
		writeVerdict(p.t, copied, entry("codex-r1-1", "real", "P1", true, ""))
		return "r-loop: fix", "embedded:" + name, nil
	}
	return vars["PlanPath"].(string), "embedded:" + name, nil
}

func (p *warnPrompts) captured() (reviews, fixes []map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.reviews...), append([]map[string]any(nil), p.fixes...)
}

type warnHost struct{ *eventsHost }

func (h warnHost) State(agent string) (AgentState, error) {
	if role(agent) != "rloop-p2-implement" {
		return AgentIdle, nil
	}
	return h.eventsHost.State(agent)
}

func (h warnHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	if strings.Contains(text, "watchdog warning") {
		h.record("SessionHost.PromptedWarning %s %q", agent, text)
	}
	return h.eventsHost.Prompt(agent, text, wait, timeout)
}

func newWarnRig(t *testing.T, reviewed bool) (*eventsRig, *warnPrompts) {
	r := newEventsRig(t)
	r.host.behaviour["rloop-p2-implement"] = "hold"
	r.loop.Sessions.StallGrace = 1000 * time.Hour
	r.loop.Sessions.Host = warnHost{r.ehost}
	prompts := &warnPrompts{t: t, repo: r.repo}
	r.loop.Sessions.Prompts = prompts
	if reviewed {
		r.loop.Kinds[1].Row.Reviewers = []Reviewer{{Provider: "codex", Prompt: "review-security"}}
		r.loop.Kinds[1].Row.Rounds = 1
		r.loop.Kinds[1].Row.ReviewTimeout = time.Hour
		r.loop.Runners = DefaultRunners(r.loop.Sessions, r.loop.Kinds)
	}
	return r, prompts
}

func (r *eventsRig) warningSubmits() []string {
	var out []string
	for _, c := range r.calls("SessionHost.Submit ") {
		if strings.Contains(c, "watchdog warning") {
			out = append(out, c)
		}
	}
	return out
}

func (r *eventsRig) awaitWarningEvent(t *testing.T) {
	waitFor(t, func() bool { return len(r.events("warning")) > 0 })
}

func (r *eventsRig) awaitWarningSubmit(within time.Duration) {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) && len(r.warningSubmits()) == 0 {
		time.Sleep(time.Millisecond)
	}
}

func (r *eventsRig) finishIdle(agent string) {
	r.ehost.finish(agent)
	r.ehost.mu.Lock()
	r.ehost.stopped[role(agent)] = AgentIdle
	r.ehost.mu.Unlock()
}

func warningsVar(t *testing.T, label string, vars map[string]any) string {
	t.Helper()
	got, ok := vars["WatchdogWarnings"].(string)
	if !ok {
		t.Errorf("%s vars carry no WatchdogWarnings string: %v", label, vars["WatchdogWarnings"])
	}
	return got
}

func TestWatchdogWarnForTheLiveWorkingStepIsTypedIntoItsPane(t *testing.T) {
	r, _ := newWarnRig(t, false)
	r.watcher.started = func(ref StepRef, s *Session) {
		if ref.Key.Kind != "implement" {
			return
		}
		r.watcher.signals <- Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: ref.Key, Reason: "tests are being skipped"}
		go func() {
			r.awaitWarningEvent(t)
			r.awaitWarningSubmit(2 * time.Second)
			r.finishIdle(s.Agent)
		}()
	}

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 0 {
		t.Fatalf("exit %d, want 0; blocked %+v", code, r.events("phase-blocked"))
	}
	want := `rloop-p2-implement "r-loop: watchdog warning: tests are being skipped"`
	if got := r.warningSubmits(); len(got) != 1 || got[0] != want {
		t.Errorf("warning submits %q, want [%s]", got, want)
	}
	if got := r.calls("SessionHost.PromptedWarning "); len(got) != 0 {
		t.Errorf("warning went through Prompt: %q", got)
	}
	if got := r.events("warning"); len(got) != 1 || got[0].Fields["reason"] != "tests are being skipped" {
		t.Errorf("warning events %+v", got)
	}
}

func TestWatchdogWarnDuringTheWorkHalfReachesTheReviewerAndFixPrompts(t *testing.T) {
	r, prompts := newWarnRig(t, true)
	r.watcher.started = func(ref StepRef, s *Session) {
		if ref.Key.Kind != "implement" {
			return
		}
		r.watcher.signals <- Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: ref.Key, Reason: "tests are being skipped"}
		go func() {
			r.awaitWarningEvent(t)
			r.awaitWarningSubmit(2 * time.Second)
			r.finishIdle(s.Agent)
		}()
	}

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 0 {
		t.Fatalf("exit %d, want 0; blocked %+v", code, r.events("phase-blocked"))
	}
	reviews, fixes := prompts.captured()
	if len(reviews) != 1 || len(fixes) != 1 {
		t.Fatalf("rendered %d reviewer and %d fix prompts, want 1 and 1", len(reviews), len(fixes))
	}
	if got := warningsVar(t, "reviewer", reviews[0]); got != "- tests are being skipped" {
		t.Errorf("reviewer WatchdogWarnings %q", got)
	}
	if got := warningsVar(t, "fix", fixes[0]); got != "- tests are being skipped" {
		t.Errorf("fix WatchdogWarnings %q", got)
	}
}

func TestWatchdogWarnForAnotherStepIsNotTypedIntoTheLivePane(t *testing.T) {
	r, _ := newWarnRig(t, false)
	r.watcher.started = func(ref StepRef, s *Session) {
		if ref.Key.Kind != "implement" {
			return
		}
		other := ref.Key
		other.Kind = "plan"
		r.watcher.signals <- Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: other, Reason: "plan drifted"}
		go func() {
			r.awaitWarningEvent(t)
			r.awaitWarningSubmit(300 * time.Millisecond)
			r.finishIdle(s.Agent)
		}()
	}

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 0 {
		t.Fatalf("exit %d, want 0; blocked %+v", code, r.events("phase-blocked"))
	}
	if got := r.warningSubmits(); len(got) != 0 {
		t.Errorf("warning submits %q, want none", got)
	}
	if got := r.events("warning"); len(got) != 1 || got[0].Fields["reason"] != "plan drifted" || got[0].Step != "plan" {
		t.Errorf("warning events %+v", got)
	}
}

func TestWatchdogWarnToAnIdleStepIsNotTypedButReachesItsReviewers(t *testing.T) {
	r, prompts := newWarnRig(t, true)
	r.watcher.started = func(ref StepRef, s *Session) {
		if ref.Key.Kind != "implement" {
			return
		}
		r.ehost.mu.Lock()
		r.ehost.stopped["rloop-p2-implement"] = AgentIdle
		r.ehost.mu.Unlock()
		r.watcher.signals <- Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: ref.Key, Reason: "tests are being skipped"}
		go func() {
			r.awaitWarningEvent(t)
			r.awaitWarningSubmit(300 * time.Millisecond)
			r.finishIdle(s.Agent)
		}()
	}

	code := r.run(RunOptions{Phases: []string{"2"}})

	if code != 0 {
		t.Fatalf("exit %d, want 0; blocked %+v", code, r.events("phase-blocked"))
	}
	if got := r.warningSubmits(); len(got) != 0 {
		t.Errorf("warning submits %q, want none", got)
	}
	reviews, fixes := prompts.captured()
	if len(reviews) != 1 || len(fixes) != 1 {
		t.Fatalf("rendered %d reviewer and %d fix prompts, want 1 and 1", len(reviews), len(fixes))
	}
	if got := warningsVar(t, "reviewer", reviews[0]); got != "- tests are being skipped" {
		t.Errorf("reviewer WatchdogWarnings %q", got)
	}
}

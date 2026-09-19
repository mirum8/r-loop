package core

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type checkHost struct {
	fakeSessionHost
	onPrompt func(text string)
	err      error
}

func (h *checkHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	h.record("SessionHost.Prompt %s %q %t %s", agent, text, wait, timeout)
	if h.onPrompt != nil {
		h.onPrompt(text)
	}
	return h.err
}

type planVars struct {
	mu   sync.Mutex
	vars []map[string]any
}

func (p *planVars) Render(name string, vars map[string]any) (string, string, error) {
	if name == "plan" {
		p.mu.Lock()
		p.vars = append(p.vars, vars)
		p.mu.Unlock()
	}
	return pathPrompts{}.Render(name, vars)
}

type checkRig struct {
	*loopRig
	dogHost *checkHost
	watch   *Watch
	prompts *planVars
}

func newCheckRig(t *testing.T) *checkRig {
	r := newLoopRig(t)
	r.loop.Plan.Phases[0].Files = []string{"internal/core/a.go", "internal/core/a_test.go"}
	r.loop.Plan.Phases[0].Risk = "security"
	r.loop.Plan.Phases[0].Block = "### Phase 1 — Core types\n- [ ] do it"
	prompts := &planVars{}
	r.loop.Sessions.Prompts = prompts
	host := &checkHost{fakeSessionHost: fakeSessionHost{callLog: callLog{Shared: r.shared}}}
	dog := &Watchdog{Host: host, Store: r.store, Face: r.face, RunID: "run-1", Sleep: func(time.Duration) {}}
	w := &Watch{Store: r.store, Face: r.face, PhaseCheck: &PhaseCheck{Dog: dog, Repo: r.repo, Timeout: 10 * time.Minute}}
	r.loop.Watcher = w
	return &checkRig{loopRig: r, dogHost: host, watch: w, prompts: prompts}
}

func (r *checkRig) signal(kind SignalKind, step string, reason string) (bool, string) {
	return r.watch.Handle(Signal{Kind: kind, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: 1, Kind: step}, Reason: reason})
}

func (r *checkRig) planWarnings(t *testing.T) string {
	t.Helper()
	r.prompts.mu.Lock()
	defer r.prompts.mu.Unlock()
	if len(r.prompts.vars) == 0 {
		t.Fatal("plan never rendered")
	}
	return r.prompts.vars[0]["PhaseWarnings"].(string)
}

func (r *checkRig) report(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.store.dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func indexOf(calls []string, prefix string) int {
	return slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func TestPhaseCheckCreatesTheWorktreeThenWaitsOnTheCheckPromptBeforeThePlanSpawns(t *testing.T) {
	r := newCheckRig(t)

	if code := r.run(RunOptions{Phases: []int{1}}); code != 0 {
		t.Fatalf("exit %d", code)
	}

	calls := r.shared.Calls()
	add := indexOf(calls, "Repo.AddWorktree .r-loop/wt/phase-1 r-loop/phase-1 main")
	check := indexOf(calls, `SessionHost.Prompt rloop-wd-run-1 "check phase 1`)
	spawn := indexOf(calls, "SessionHost.Open")
	if add < 0 || check < 0 || spawn < 0 || !(add < check && check < spawn) {
		t.Fatalf("worktree %d, check %d, spawn %d in\n%q", add, check, spawn, calls)
	}
	prompt := calls[check]
	for _, want := range []string{
		"check phase 1",
		"worktree " + filepath.Join(r.repo.RootDir, ".r-loop/wt/phase-1"),
		"base main",
		"Files: internal/core/a.go, internal/core/a_test.go",
		"Risk: security",
		"### Phase 1 — Core types",
		"true 10m0s",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("check prompt %s\nmissing %q", prompt, want)
		}
	}
}

func TestAWarnDuringTheCheckIsShownBeforeThePlanStepAndReachesThePlanPrompt(t *testing.T) {
	r := newCheckRig(t)
	var accepted bool
	r.dogHost.onPrompt = func(text string) {
		if strings.HasPrefix(text, "check phase 1") {
			accepted, _ = r.signal(SignalWarn, "check", "Files: misses internal/core/b.go")
		}
	}

	if code := r.run(RunOptions{Phases: []int{1}}); code != 0 {
		t.Fatalf("exit %d", code)
	}

	if !accepted {
		t.Fatal("warn not accepted")
	}
	warn := slices.IndexFunc(r.face.Events, func(ev Event) bool {
		return ev.Kind == "warning" && ev.Step == "check" && ev.Fields["reason"] == "Files: misses internal/core/b.go"
	})
	plan := slices.IndexFunc(r.face.Events, func(ev Event) bool { return ev.Kind == "step" && ev.Step == "plan" })
	if warn < 0 || plan < 0 || warn > plan {
		t.Fatalf("warning at %d, first plan event at %d", warn, plan)
	}
	if got := r.planWarnings(t); got != "- Files: misses internal/core/b.go" {
		t.Errorf("PhaseWarnings %q", got)
	}
	if !strings.Contains(r.report(t), "phase 1 phase check: Files: misses internal/core/b.go — landed") {
		t.Errorf("report\n%s", r.report(t))
	}
}

func TestACleanCheckLeavesPhaseWarningsEmptyAndReportsNoDisagreement(t *testing.T) {
	r := newCheckRig(t)

	if code := r.run(RunOptions{Phases: []int{1}}); code != 0 {
		t.Fatalf("exit %d", code)
	}

	if got := r.planWarnings(t); got != "" {
		t.Errorf("PhaseWarnings %q", got)
	}
	if !strings.Contains(r.report(t), "phase 1 phase check: no disagreement — landed") {
		t.Errorf("report\n%s", r.report(t))
	}
}

func TestAHaltDuringTheCheckIsRejectedAndThePhaseStillRuns(t *testing.T) {
	r := newCheckRig(t)
	var accepted bool
	var reason string
	r.dogHost.onPrompt = func(text string) {
		if strings.HasPrefix(text, "check phase 1") {
			accepted, reason = r.signal(SignalHalt, "check", "too deep a cut")
		}
	}

	code := r.run(RunOptions{Phases: []int{1}})

	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if accepted || reason != "phase check may only warn" {
		t.Errorf("accepted %t reason %q", accepted, reason)
	}
	if !slices.Contains(r.calls("Land "), "1") {
		t.Errorf("phase 1 did not land: %q", r.calls("Land "))
	}
	if got := r.events("halt"); len(got) != 0 {
		t.Errorf("halt events %+v", got)
	}
	var rejected []Signal
	for _, rec := range r.store.Records["run-1"] {
		if rec.Kind == RecordSignal && rec.Signal.Rejected {
			rejected = append(rejected, *rec.Signal)
		}
	}
	if len(rejected) != 1 || rejected[0].RejectReason != "phase check may only warn" {
		t.Errorf("rejected %+v", rejected)
	}
}

func TestAnotherStepSignalDuringTheCheckIsRejected(t *testing.T) {
	r := newCheckRig(t)
	var accepted bool
	var reason string
	r.dogHost.onPrompt = func(text string) {
		if strings.HasPrefix(text, "check phase 1") {
			accepted, reason = r.signal(SignalWarn, "implement", "early")
		}
	}

	r.run(RunOptions{Phases: []int{1}})

	if accepted || reason != "phase-1/check is the only step during the phase check" {
		t.Errorf("accepted %t reason %q", accepted, reason)
	}
}

func TestACheckTimeoutIsRecordedWithoutAHalt(t *testing.T) {
	for name, err := range map[string]error{
		"timeout": errors.New("herdr agent prompt: herdr: timeout: no answer within 10m0s"),
		"blocked": errors.New("herdr agent prompt: herdr: agent_blocked: waiting on a permission"),
	} {
		t.Run(name, func(t *testing.T) {
			r := newCheckRig(t)
			r.dogHost.err = err

			code := r.run(RunOptions{Phases: []int{1}})

			if code != 0 {
				t.Fatalf("exit %d", code)
			}
			if got := r.events("phase-check-timeout"); len(got) != 1 || got[0].Phase != 1 {
				t.Errorf("timeout events %+v", got)
			}
			if !slices.Contains(r.calls("Land "), "1") {
				t.Errorf("phase 1 did not land")
			}
			if !strings.Contains(r.report(t), "phase 1 phase check: timed out — landed") {
				t.Errorf("report\n%s", r.report(t))
			}
		})
	}
}

func TestWithoutAWatchdogTheCheckIsSkippedAndThePhaseRuns(t *testing.T) {
	r := newLoopRig(t)
	r.loop.Watcher = &Watch{Store: r.store, Face: r.face}

	code := r.run(RunOptions{Phases: []int{1}})

	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := r.events("phase-check-skipped"); len(got) != 1 || got[0].Phase != 1 {
		t.Errorf("skipped events %+v", got)
	}
}

func TestReportPhaseCheckLinesFollowWhatThePhaseDid(t *testing.T) {
	ev := func(kind string, phase int, f map[string]string) Event {
		return Event{Kind: kind, Phase: phase, Fields: f}
	}
	st := RunState{Events: []Event{
		ev("phase-check", 1, map[string]string{"result": "no disagreement"}),
		ev("landed", 1, nil),
		ev("phase-check", 2, map[string]string{"result": "Risk: none, but it touches auth"}),
		ev("phase-blocked", 2, map[string]string{"reason": "tests red"}),
		ev("phase-check-timeout", 3, nil),
		ev("phase-blocked", 3, map[string]string{"reason": "watchdog: off the plan"}),
		ev("phase-check-skipped", 4, nil),
		ev("phase-check", 5, map[string]string{"result": "no disagreement"}),
		ev("halt", 5, map[string]string{"reason": invariantQuestion}),
		ev("halt", 0, map[string]string{"blocked": "2, 3"}),
	}}

	got := Report(st, Plan{})

	want := "\n## Phase checks\n\n" +
		"- phase 1 phase check: no disagreement — landed\n" +
		"- phase 2 phase check: Risk: none, but it touches auth — failed\n" +
		"- phase 3 phase check: timed out — halted\n" +
		"- phase 4 phase check: skipped\n" +
		"- phase 5 phase check: no disagreement — halted\n"
	if !strings.Contains(got, want) {
		t.Errorf("report\n%s\nwant\n%s", got, want)
	}
}

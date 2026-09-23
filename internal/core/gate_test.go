package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
)

type suiteFunc func(ctx context.Context, phase core.Phase) (string, error)

func (f suiteFunc) Command(ctx context.Context, phase core.Phase) (string, error) {
	return f(ctx, phase)
}

const itemPlan = "status: planned\n\n## Summary\n\ns\n\n## Changes\n\nc\n\n## Tests\n\n- t\n\n## Assumptions\n\nnone\n\n## Gate\n\n`sh feature_test.sh`\n"

func (e *landEnv) itemWork(n int, files map[string]string) {
	e.t.Helper()
	wt := fmt.Sprintf(".r-loop/wt/phase-%d", n)
	if err := e.repo.AddWorktree(wt, fmt.Sprintf("r-loop/phase-%d", n), "main"); err != nil {
		e.t.Fatal(err)
	}
	for path, content := range files {
		writeFile(e.t, filepath.Join(e.worktree(n), path), content)
	}
	if _, err := e.repo.CommitAll(wt, fmt.Sprintf("r-loop: phase %d implement", n)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *landEnv) itemGate(suite string) (*core.LandGate, *[]string) {
	var calls []string
	g := e.gate()
	g.Suite = suiteFunc(func(ctx context.Context, phase core.Phase) (string, error) {
		calls = append(calls, phase.Title)
		return suite, nil
	})
	return g, &calls
}

func TestItemGateLandsWhenTheTestsAreRedAtBaseAndGreenAfterTheMerge(t *testing.T) {
	e := newLandEnv(t)
	e.itemWork(1, map[string]string{
		".task-plans/phase-1-first.md": itemPlan,
		"feature_test.sh":              "test -f feature.txt\n",
		"feature.txt":                  "new\n",
	})
	g, calls := e.itemGate("test -f a.txt")

	landing, err := g.Land(context.Background(), phaseOne(""))

	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if landing.GateSkipped || landing.MergeSHA != e.head() {
		t.Errorf("landing = %+v", landing)
	}
	if len(*calls) != 1 {
		t.Errorf("suite asked %d times", len(*calls))
	}
	if st := gitCmd(t, e.root, "status", "--porcelain"); st != "" {
		t.Errorf("primary tree not clean:\n%s", st)
	}
	if wts := gitCmd(t, e.root, "worktree", "list"); strings.Contains(wts, "phase-1-red") {
		t.Errorf("red worktree left behind:\n%s", wts)
	}
}

func TestItemGateRefusesTestsThatPassWithoutTheChange(t *testing.T) {
	e := newLandEnv(t)
	e.itemWork(1, map[string]string{
		".task-plans/phase-1-first.md": itemPlan,
		"feature_test.sh":              "test -f a.txt\n",
		"feature.txt":                  "new\n",
	})
	head := e.head()
	g, _ := e.itemGate("true")

	_, err := g.Land(context.Background(), phaseOne(""))

	if !errors.Is(err, core.ErrGate) || !strings.Contains(err.Error(), "do not exercise the change") {
		t.Fatalf("err = %v, want ErrGate naming the red check", err)
	}
	e.assertUntouched(head)
}

func TestItemGateRefusesAPhaseWithoutTests(t *testing.T) {
	e := newLandEnv(t)
	e.itemWork(1, map[string]string{
		".task-plans/phase-1-first.md": itemPlan,
		"feature.txt":                  "new\n",
	})
	head := e.head()
	g, _ := e.itemGate("true")

	_, err := g.Land(context.Background(), phaseOne(""))

	if !errors.Is(err, core.ErrGate) || !strings.Contains(err.Error(), "no test file") {
		t.Fatalf("err = %v", err)
	}
	e.assertUntouched(head)
}

func TestItemGateFailsAfterTheMergeWhenTheSuiteIsRed(t *testing.T) {
	e := newLandEnv(t)
	e.itemWork(1, map[string]string{
		".task-plans/phase-1-first.md": itemPlan,
		"feature_test.sh":              "test -f feature.txt\n",
		"feature.txt":                  "new\n",
	})
	head := e.head()
	g, _ := e.itemGate("test -f missing.txt")

	_, err := g.Land(context.Background(), phaseOne(""))

	if !errors.Is(err, core.ErrGate) || !strings.Contains(err.Error(), "sh feature_test.sh && test -f missing.txt") {
		t.Fatalf("err = %v", err)
	}
	e.assertUntouched(head)
}

func TestItemGateWithoutAGateSectionIsNoGate(t *testing.T) {
	e := newLandEnv(t)
	e.itemWork(1, map[string]string{
		".task-plans/phase-1-first.md": strings.TrimSuffix(itemPlan, "## Gate\n\n`sh feature_test.sh`\n"),
		"feature_test.sh":              "test -f feature.txt\n",
	})
	head := e.head()
	g, _ := e.itemGate("true")
	g.FixRounds = 1
	g.Runner = runnerFunc(func(ctx context.Context, ref core.StepRef, obs core.Observer) core.Outcome {
		t.Fatal("gate-fix ran for a missing gate")
		return core.Outcome{}
	})

	_, err := g.Land(context.Background(), phaseOne(""))

	if !errors.Is(err, core.ErrNoGate) || !strings.Contains(err.Error(), "missing ## Gate") {
		t.Fatalf("err = %v", err)
	}
	e.assertUntouched(head)
}

func TestSuiteFailureBlocksBeforeTheMerge(t *testing.T) {
	e := newLandEnv(t)
	e.itemWork(1, map[string]string{"feature.txt": "new\n"})
	head := e.head()
	g := e.gate()
	g.Suite = suiteFunc(func(ctx context.Context, phase core.Phase) (string, error) {
		return "", fmt.Errorf("%w: gate step failed", core.ErrNoGate)
	})

	_, err := g.Land(context.Background(), phaseOne(""))

	if !errors.Is(err, core.ErrNoGate) {
		t.Fatalf("err = %v", err)
	}
	e.assertUntouched(head)
}

func TestAnExplicitDoneWhenNeverAsksTheSuite(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	g, calls := e.itemGate("false")

	if _, err := g.Land(context.Background(), phaseOne("test -f feature.txt")); err != nil {
		t.Fatalf("Land: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("suite asked for a phase with Done when")
	}
}

type probeHost struct {
	reportHost
	command string
}

func (h *probeHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	h.mu.Lock()
	cwd := h.opened[len(h.opened)-1].CWD
	h.mu.Unlock()
	report, sentinel, _ := strings.Cut(text, "\n")
	if err := os.WriteFile(filepath.Join(cwd, report), []byte("`"+h.command+"`\n\nfrom the Makefile\n"), 0o644); err != nil {
		return err
	}
	data, _ := json.Marshal(map[string]string{"outcome": h.outcome, "reason": "probe failed"})
	return os.WriteFile(sentinel, data, 0o644)
}

func (e *landEnv) probe(outcome, command string) (*core.GateProbe, *probeHost) {
	host := &probeHost{reportHost: reportHost{outcome: outcome}, command: command}
	e.store.dir = filepath.Join(e.root, ".r-loop", "runs", "run1")
	if err := os.MkdirAll(e.store.dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	sm := &core.SessionManager{
		Host:    host,
		Repo:    e.repo,
		Prompts: &reportPrompts{},
		Store:   e.store,
		Resolve: func(provider, model, effort, askURL, mcp string) (core.ProviderArgs, error) {
			return core.ProviderArgs{Kind: provider}, nil
		},
		Poll: 5 * time.Millisecond,
	}
	return &core.GateProbe{
		Sessions: sm,
		Repo:     e.repo,
		Kind:     core.StepKind{Name: "gate", Prompt: "gate", Check: "report", Row: core.StepRow{Provider: "claude"}},
		Plan:     core.Plan{Path: e.todo},
		RunID:    "run1",
		RunDir:   e.store.dir,
		Face:     e.face,
		Timeout:  time.Minute,
	}, host
}

func TestGateProbeRecordsTheCommandOnceAndReusesIt(t *testing.T) {
	e := newLandEnv(t)
	p, host := e.probe("ok", "test -f a.txt")

	first, err := p.Command(context.Background(), phaseOne(""))
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	second, err := p.Command(context.Background(), phaseTwo(""))
	if err != nil {
		t.Fatalf("Command 2: %v", err)
	}

	if first != "test -f a.txt" || second != first {
		t.Errorf("commands = %q, %q", first, second)
	}
	if len(host.opened) != 1 || host.opened[0].CWD != e.root {
		t.Errorf("opened = %+v, want one session in the primary tree", host.opened)
	}
	evs := e.store.events("gate-discovered")
	if len(evs) != 1 || evs[0].Fields["command"] != "test -f a.txt" {
		t.Errorf("gate-discovered events = %+v", evs)
	}
}

func TestGateProbeEmitsTheSessionsFinalStepState(t *testing.T) {
	e := newLandEnv(t)
	p, _ := e.probe("ok", "test -f a.txt")

	if _, err := p.Command(context.Background(), phaseOne("")); err != nil {
		t.Fatalf("Command: %v", err)
	}

	var got []string
	for _, ev := range e.store.events("step") {
		got = append(got, ev.Step+" "+ev.Fields["state"])
	}
	want := []string{"gate running", "gate ok"}
	if !slices.Equal(got, want) {
		t.Fatalf("step events %q, want %q", got, want)
	}
}

func TestGateProbeOnResumeReadsTheRecordedCommand(t *testing.T) {
	e := newLandEnv(t)
	first, host := e.probe("ok", "test -f a.txt")
	if _, err := first.Command(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}
	resumed, _ := e.probe("ok", "unused")
	resumed.Sessions.Host = host

	got, err := resumed.Command(context.Background(), phaseTwo(""))

	if err != nil || got != "test -f a.txt" {
		t.Fatalf("Command = %q, %v", got, err)
	}
	if len(host.opened) != 1 {
		t.Errorf("probe ran again on resume: %+v", host.opened)
	}
}

func TestGateProbeRefusesACommandRedAtBase(t *testing.T) {
	e := newLandEnv(t)
	p, _ := e.probe("ok", "test -f missing.txt")

	_, err := p.Command(context.Background(), phaseOne(""))

	if !errors.Is(err, core.ErrNoGate) || !strings.Contains(err.Error(), "fails on main") {
		t.Fatalf("err = %v", err)
	}
	if len(e.store.events("gate-discovered")) != 0 {
		t.Errorf("a red gate was recorded")
	}
}

func TestGateProbeFailedSessionIsNoGate(t *testing.T) {
	e := newLandEnv(t)
	p, _ := e.probe("failed", "true")

	_, err := p.Command(context.Background(), phaseOne(""))

	if !errors.Is(err, core.ErrNoGate) || !strings.Contains(err.Error(), "probe failed") {
		t.Fatalf("err = %v", err)
	}
}

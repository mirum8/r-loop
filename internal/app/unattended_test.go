package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"r-loop/internal/core"
)

func TestUnattendedAddsItsAllowListAndTellsTheWatchdogAndSaysSoInTheBanner(t *testing.T) {
	f := newResumeFixture(t, "watchdog:\n  allow:\n    - locks\n")

	w, err := f.preflight(f.todo, "--plain", "--unattended")
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"locks", "deps", "ports", "restart", "retry", "provider"}
	if !slices.Equal(w.Remedies.Allow, want) || !slices.Equal(w.Dog.Allow, want) {
		t.Errorf("allow remedies %v dog %v", w.Remedies.Allow, w.Dog.Allow)
	}
	if !w.Dog.Unattended {
		t.Error("the watchdog is not told the run is unattended")
	}
	if w.Remedies.Fallbacks == nil {
		t.Error("remedies know no fallbacks")
	}
	if out := f.out.String(); !strings.Contains(out, "mode: unattended  allow + deps, ports, restart, retry, provider\n") || strings.Contains(out, "no watchdog") {
		t.Errorf("banner:\n%s", out)
	}
}

func TestWithoutTheFlagTheRunIsAttendedAndNothingChanges(t *testing.T) {
	f := newResumeFixture(t, "watchdog:\n  allow:\n    - locks\n")

	w, err := f.preflight(f.todo, "--plain")
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(w.Remedies.Allow, []string{"locks"}) || !slices.Equal(w.Dog.Allow, []string{"locks"}) || w.Dog.Unattended {
		t.Errorf("allow %v %v unattended %t", w.Remedies.Allow, w.Dog.Allow, w.Dog.Unattended)
	}
	if out := f.out.String(); !strings.Contains(out, "mode: attended\n") || strings.Contains(out, "unattended") {
		t.Errorf("banner:\n%s", out)
	}
}

func TestResumeUnattendedAppliesTheModeToTheResumedRun(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	if _, code := f.firstRun(first, "--phases", "1"); code != 1 {
		t.Fatalf("first run exit %d", code)
	}
	f.out.Reset()

	w, _, err := PrepareResume([]string{"--plain", "--unattended"}, f.env)
	if err != nil {
		t.Fatal(err)
	}

	if !w.Opts.Unattended || !w.Dog.Unattended || !slices.Contains(w.Remedies.Allow, "restart") {
		t.Errorf("opts %+v unattended %t allow %v", w.Opts, w.Dog.Unattended, w.Remedies.Allow)
	}
	if !strings.Contains(f.out.String(), "mode: unattended") {
		t.Errorf("banner:\n%s", f.out.String())
	}
	w.Store.ClearCurrent()
}

const unattendedTodo = `# t

### Phase 1 — one
**Depends on:** —
- [ ] a

### Phase 2 — two
**Depends on:** —
- [ ] b

### Phase 3 — three
**Depends on:** Phase 2
- [ ] c

### Phase 4 — four
**Depends on:** —
- [ ] d
`

const unattendedConfig = `steps:
  plan:
    rounds: 0
  implement:
    rounds: 0
watchdog:
  maxRestarts: 1
  remedyWindow: 500ms
  stallGrace: 30ms
`

type unattendedHost struct {
	*simHost
	t      *testing.T
	idle   map[string]bool
	asking string
	mu     sync.Mutex
	url    string
	answer string
}

func (h *unattendedHost) Start(pane, name, kind string, args []string) (core.Agent, error) {
	if name == h.asking {
		for _, a := range args {
			if u, ok := strings.CutPrefix(a, "mcp_servers.r-loop.url="); ok {
				h.mu.Lock()
				h.url = u
				h.mu.Unlock()
			}
		}
	}
	return h.simHost.Start(pane, name, kind, args)
}

func (h *unattendedHost) State(agent string) (core.AgentState, error) {
	if h.idle[agent] {
		return core.AgentIdle, nil
	}
	return core.AgentWorking, nil
}

func (h *unattendedHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	if agent != h.asking || strings.HasPrefix(text, "r-loop:") {
		return h.simHost.Prompt(agent, text, wait, timeout)
	}
	h.mu.Lock()
	url := h.url
	h.mu.Unlock()
	go func() {
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: url, MaxRetries: -1}, nil)
		if err != nil {
			h.t.Error(err)
			return
		}
		defer cs.Close()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ask_watchdog", Arguments: map[string]any{"question": "which db?", "options": []string{"sqlite", "postgres"}, "recommended": "sqlite"}})
		if err != nil {
			h.t.Error(err)
			return
		}
		out, _ := res.StructuredContent.(map[string]any)
		h.mu.Lock()
		h.answer, _ = out["answer"].(string)
		h.mu.Unlock()
		h.simHost.Prompt(agent, text, wait, timeout)
	}()
	return nil
}

type restartingDog struct {
	dogHost
	remedies *core.Remedies
	mu       sync.Mutex
	replies  []string
}

func (d *restartingDog) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	d.dogHost.Prompt(agent, text, wait, timeout)
	rest, ok := strings.CutPrefix(text, "step ended ")
	if !ok {
		return nil
	}
	step, state, _ := strings.Cut(rest, " ")
	if !strings.HasPrefix(state, "failed") {
		return nil
	}
	go func() {
		accepted, reason := d.remedies.Restart(step, "", "", "")
		d.mu.Lock()
		d.replies = append(d.replies, step+" "+map[bool]string{true: "accepted", false: reason}[accepted])
		d.mu.Unlock()
	}()
	return nil
}

func TestAnUnattendedFourPhaseRunFinishesWithNoHumanTouch(t *testing.T) {
	f := newFixture(t)
	f.write("docs/topic/todo.md", unattendedTodo)
	f.write(".r-loop/config.yaml", unattendedConfig)
	f.commit()
	f.env.PID = os.Getpid()
	w, err := f.preflight(f.todo, "--plain", "--unattended")
	if err != nil {
		t.Fatal(err)
	}
	sim := newSim()
	sim.hang["rloop-p1-implement"] = true
	sim.fail["rloop-p2-implement"] = true
	sim.fail["rloop-p2-implement-a2"] = true
	host := &unattendedHost{simHost: sim, t: t, idle: map[string]bool{"rloop-p1-implement": true}, asking: "rloop-p4-implement"}
	lander := f.sim(w, sim)
	w.Loop.Sessions.Host = host
	w.Loop.RemedyWindow = w.Config.Watchdog.RemedyWindow
	dog := &restartingDog{dogHost: *answeringDog(w, "sqlite", "docs/topic/todo.md:1"), remedies: w.Remedies}
	w.Dog.Host = dog

	code := w.Execute(core.RunOptions{})

	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, f.out)
	}
	if !slices.Equal(lander.landed, []string{"1", "4"}) {
		t.Errorf("landed %v", lander.landed)
	}
	started := sim.startedAgents()
	for _, agent := range []string{"rloop-p1-implement-a2", "rloop-p2-implement-a2"} {
		if !slices.Contains(started, agent) {
			t.Errorf("%s never started: %v", agent, started)
		}
	}
	if slices.Contains(started, "rloop-p2-implement-a3") || slices.ContainsFunc(started, func(a string) bool { return strings.HasPrefix(a, "rloop-p3-") }) {
		t.Errorf("started %v", started)
	}
	dog.mu.Lock()
	replies := slices.Clone(dog.replies)
	dog.mu.Unlock()
	if want := []string{"phase-1/implement accepted", "phase-2/implement accepted", "phase-2/implement restart limit 1 reached"}; !slices.Equal(replies, want) {
		t.Errorf("restart replies %q, want %q", replies, want)
	}
	if host.answer != "sqlite" {
		t.Errorf("the agent received %q", host.answer)
	}
	st := f.load(w.Loop.RunID)
	if nudges := stepEvents(st, "nudge"); len(nudges) != 1 || nudges[0].Phase != "1" {
		t.Errorf("nudges %+v", nudges)
	}
	report, err := os.ReadFile(filepath.Join(w.Store.Dir(w.Loop.RunID), "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	rep := string(report)
	decisions := "## Automatic decisions\n\n" +
		"- phase 1 implement: nudge\n" +
		"- phase 1 implement: restart as attempt 2 (remedy: allow-list)\n" +
		"- phase 2 implement: restart as attempt 2 (remedy: allow-list)\n" +
		"- phase 2 blocked: tests red\n" +
		"- phase 3 skipped: depends on blocked phase 2\n"
	for _, want := range []string{"human touches: 0\n", decisions, "\nr-loop resume\n"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report missing %q:\n%s", want, rep)
		}
	}
	if !strings.Contains(f.out.String(), "r-loop resume") {
		t.Errorf("no resume line:\n%s", f.out)
	}
}

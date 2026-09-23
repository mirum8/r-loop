package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"r-loop/internal/core"
	"r-loop/internal/herdr"
)

type dogHost struct {
	mu       sync.Mutex
	calls    []string
	startErr error
	stale    map[string]string
	onPrompt func(text string)
	question func(id string)
	blocked  string
	state    core.AgentState
}

func (h *dogHost) record(format string, args ...any) {
	h.mu.Lock()
	h.calls = append(h.calls, fmt.Sprintf(format, args...))
	h.mu.Unlock()
}

func (h *dogHost) Calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

func (h *dogHost) Reachable() error { return nil }
func (h *dogHost) Open(spec core.OpenSpec) (core.Workspace, error) {
	return core.Workspace{}, nil
}
func (h *dogHost) Start(pane, name, kind string, args []string) (core.Agent, error) {
	h.record("Start %s %s %s %s", pane, name, kind, strings.Join(args, " "))
	return core.Agent{Name: name, Pane: pane}, h.startErr
}
func (h *dogHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	h.record("Prompt %s %s", agent, text)
	if h.blocked != "" && strings.HasPrefix(text, h.blocked) {
		return errors.New("herdr: agent_blocked")
	}
	if h.onPrompt != nil {
		h.onPrompt(text)
	}
	if rest, ok := strings.CutPrefix(text, "question "); ok && h.question != nil {
		id, _, _ := strings.Cut(rest, " ")
		go h.question(id)
	}
	return nil
}
func (h *dogHost) State(agent string) (core.AgentState, error) {
	if h.state != "" {
		return h.state, nil
	}
	return core.AgentWorking, nil
}
func (h *dogHost) AgentPane(agent string) (string, error)                 { return h.stale[agent], nil }
func (h *dogHost) Read(agent string, lines int) (string, error)           { return "", nil }
func (h *dogHost) Interrupt(agent string) error                           { return nil }
func (h *dogHost) Tag(workspaceID string, tokens map[string]string) error { return nil }
func (h *dogHost) Close(workspaceID string) error                         { return nil }
func (h *dogHost) ClosePane(pane string) error {
	h.record("ClosePane %s", pane)
	return nil
}
func (h *dogHost) Split(pane, direction, cwd string) (string, error) {
	h.record("Split %q %s %s", pane, direction, cwd)
	return "wd-pane", nil
}

func (h *dogHost) prompted(prefix string) bool {
	for _, c := range h.Calls() {
		name, rest, _ := strings.Cut(strings.TrimPrefix(c, "Prompt "), " ")
		if strings.HasPrefix(c, "Prompt ") && strings.HasPrefix(name, "rloop-wd-") && strings.HasPrefix(rest, prefix) {
			return true
		}
	}
	return false
}

func TestWireHandsTheLoopAWatchWithTheShippedChecksAndTheRemedyWindow(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)

	w, err := f.preflight(f.todo, "--plain")
	if err != nil {
		t.Fatal(err)
	}

	if w.Loop.Watcher != w.Watch || len(w.Watch.Checks) != 5 {
		t.Fatalf("watcher %v, checks %d", w.Loop.Watcher, len(w.Watch.Checks))
	}
	if w.Loop.RemedyWindow != 10*time.Minute {
		t.Errorf("remedy window %s", w.Loop.RemedyWindow)
	}
}

func TestExecuteStartsTheWatchdogAndAHaltThroughItsMCPSurfaceExits5(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.hang["rloop-p1-implement"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	dog := &dogHost{}
	w.Dog.Host = dog
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	for deadline := time.Now().Add(10 * time.Second); !dog.prompted("step started phase-1/implement"); {
		if time.Now().After(deadline) {
			t.Fatalf("implement never reported to the watchdog: %q", dog.Calls())
		}
		time.Sleep(5 * time.Millisecond)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: w.Ask.WatchdogURL(), MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "signal", Arguments: map[string]any{"kind": "halt", "step": "phase-1/implement", "reason": "off the plan", "evidence": "docs/topic/todo.md:1"}})
	if err != nil {
		t.Fatal(err)
	}

	if out, _ := res.StructuredContent.(map[string]any); out["accepted"] != true {
		t.Errorf("signal result %v", res.StructuredContent)
	}
	select {
	case code := <-done:
		if code != 5 {
			t.Errorf("exit %d, want 5\n%s", code, f.out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run never halted")
	}
	runDir := w.Store.Dir(w.Loop.RunID)
	mcpPath := filepath.Join(runDir, "watchdog.mcp.json")
	calls := dog.Calls()
	if len(calls) < 3 || calls[0] != `Split "driver-pane" right `+f.root || calls[1] != "Start wd-pane "+core.WatchdogName(w.Loop.RunID)+" claude --model opus --effort high --mcp-config "+mcpPath || !strings.Contains(calls[2], runDir) {
		t.Errorf("watchdog calls %q", calls)
	}
	if data, _ := os.ReadFile(mcpPath); !strings.Contains(string(data), w.Ask.WatchdogURL()) {
		t.Errorf("mcp config %s", data)
	}
	if info, err := os.Stat(mcpPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mcp config mode %v, %v", info, err)
	}
	if !dog.prompted("step ended phase-1/implement failed watchdog: off the plan") {
		t.Errorf("no step ended for implement: %q", calls)
	}
	if last := calls[len(calls)-1]; last != "ClosePane wd-pane" {
		t.Errorf("watchdog pane left open: %q", calls)
	}
}

func TestExecuteRegistersProposeRemedyAndRestartStepOnTheWatchdogSurface(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.hang["rloop-p1-implement"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	dog := &dogHost{}
	w.Dog.Host = dog
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	for deadline := time.Now().Add(10 * time.Second); !dog.prompted("step started phase-1/implement"); {
		if time.Now().After(deadline) {
			t.Fatalf("implement never reported to the watchdog: %q", dog.Calls())
		}
		time.Sleep(5 * time.Millisecond)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: w.Ask.WatchdogURL(), MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) map[string]any {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		out, _ := res.StructuredContent.(map[string]any)
		return out
	}

	proposed := call("propose_remedy", map[string]any{"class": "git", "command": "git reset --hard", "why": "dirty"})
	restarted := call("restart_step", map[string]any{"step": "phase-1/implement", "addendum": "again"})
	answered := call("answer_question", map[string]any{"id": "q-none", "answer": "sqlite", "citation": "docs/topic/todo.md:1"})
	call("signal", map[string]any{"kind": "halt", "step": "phase-1/implement", "reason": "done here", "evidence": "docs/topic/todo.md:1"})

	if proposed["decision"] != "refused" || proposed["reason"] != `class "git" is not a remedy class` {
		t.Errorf("propose_remedy %v", proposed)
	}
	if restarted["accepted"] != false || restarted["reason"] != "run halted" {
		t.Errorf("restart_step %v", restarted)
	}
	if answered["accepted"] != false || answered["reason"] != "question q-none is not open" {
		t.Errorf("answer_question %v", answered)
	}
	if w.Watch.Router != w.Router || w.Router.Dog != w.Dog {
		t.Errorf("router %+v", w.Router)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run never halted")
	}
}

func TestExecuteSendsThePhaseCheckToTheWatchdogBeforeThePlanStep(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	dog := &dogHost{}
	w.Dog.Host = dog

	if code := w.Execute(core.RunOptions{Phases: []string{"1"}}); code != 0 {
		t.Fatalf("exit %d\n%s", code, f.out)
	}

	if pc := w.Watch.PhaseCheck; pc == nil || pc.Dog != w.Dog || pc.Timeout != 10*time.Minute || pc.Backlog {
		t.Fatalf("phase check %+v", pc)
	}
	calls := dog.Calls()
	check := slices.IndexFunc(calls, func(c string) bool {
		return strings.HasPrefix(c, "Prompt "+core.WatchdogName(w.Loop.RunID)+" check phase 1 ")
	})
	plan := slices.IndexFunc(calls, func(c string) bool {
		return strings.HasPrefix(c, "Prompt "+core.WatchdogName(w.Loop.RunID)+" step started phase-1/plan")
	})
	if check < 0 || plan < 0 || check > plan {
		t.Errorf("check %d, plan %d in %q", check, plan, calls)
	}
	if got := stepEvents(f.load(w.Loop.RunID), "phase-check"); len(got) != 1 || got[0].Fields["result"] != "no disagreement" {
		t.Errorf("phase-check events %+v", got)
	}
}

func TestABacklogRunWiresThePhaseCheckForBacklogItems(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	path := filepath.Join(f.root, "issues-x-2026-09-23.md")
	if err := os.WriteFile(path, []byte("# X\n\n- [ ] [#1] first\n      - crit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.commit()
	w, err := f.preflight(path, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	w.Dog.Host = &dogHost{}
	if err := w.startWatchdog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.Watch.PhaseCheck == nil || !w.Watch.PhaseCheck.Backlog {
		t.Fatalf("backlog phase check %+v", w.Watch.PhaseCheck)
	}
}

func TestAWatchdogThatFailsToStartExits4NamingTheHerdrCode(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	dog := &dogHost{startErr: herdr.Error{Code: "pane_not_found", Message: "no such pane"}}
	w.Dog.Host = dog

	code := w.Execute(core.RunOptions{Phases: []string{"1"}})

	if code != 4 {
		t.Fatalf("exit %d, want 4", code)
	}
	if !strings.Contains(f.err.String(), "pane_not_found") {
		t.Errorf("stderr %q", f.err.String())
	}
	if got := sim.promptedAgents(); len(got) != 0 {
		t.Errorf("steps ran: %v", got)
	}
	if calls := dog.Calls(); calls[len(calls)-1] != "ClosePane wd-pane" {
		t.Errorf("watchdog pane left open: %q", calls)
	}
}

func TestAWatchdogAskFlagWithTheConfigPathEmbeddedStillGetsItsConfig(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig+"providers:\n  claude:\n    kind: claude\n    modelFlag: --model {model}\n    askFlag: --cfg={mcpConfig}\n    doneSignal: sentinel\n    ask: mcp\n    review: /code-review\n")
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, newSim())
	dog := &dogHost{}
	w.Dog.Host = dog

	if code := w.Execute(core.RunOptions{Phases: []string{"1"}}); code != 0 {
		t.Fatalf("exit %d\n%s", code, f.out)
	}

	mcpPath := filepath.Join(w.Store.Dir(w.Loop.RunID), "watchdog.mcp.json")
	if calls := dog.Calls(); len(calls) < 2 || calls[1] != "Start wd-pane "+core.WatchdogName(w.Loop.RunID)+" claude --model opus --cfg="+mcpPath {
		t.Errorf("watchdog calls %q", calls)
	}
	if data, _ := os.ReadFile(mcpPath); !strings.Contains(string(data), "/mcp/watchdog/") {
		t.Errorf("mcp config %q", data)
	}
}

func TestAWatchdogProviderWithoutMCPIsRefusedInPreflight(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  plainbot:\n    kind: codex\n    doneSignal: sentinel\n    ask: none\nwatchdog:\n  provider: plainbot\n")
	f.commit()

	_, err := f.preflight(f.todo, "--plain")

	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), "watchdog.provider: provider plainbot has no MCP ask channel") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestAStepSessionProviderWithoutMCPIsRefusedInPreflight(t *testing.T) {
	provider := "providers:\n  plainbot:\n    kind: codex\n    doneSignal: sentinel\n    review: plainbot review\n    ask: none\n"
	for field, cfg := range map[string]string{
		"steps.plan.provider":  "steps:\n  plan:\n    provider: plainbot\n",
		"steps.plan.fallback":  "steps:\n  plan:\n    fallback: plainbot\n",
		"steps.plan.reviewers": "steps:\n  plan:\n    reviewers:\n      - plainbot\n",
		"land.fix.provider":    "land:\n  fix:\n    provider: plainbot\n",
	} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			f.write(".r-loop/config.yaml", provider+cfg)
			f.commit()

			_, err := f.preflight(f.todo, "--plain")

			if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), field+": provider plainbot has no MCP ask channel") {
				t.Fatalf("code=%d err=%v", code, err)
			}
		})
	}
}

func answeringDog(w *Wiring, answer, citation string) *dogHost {
	return &dogHost{question: func(id string) {
		for {
			if ok, reason := w.Router.Answer(id, answer, citation); ok || !strings.Contains(reason, "not open") {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}}
}

func TestAWatchdogFoundGoneHaltsTheRunWithoutWaitingForAQuestion(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.hang["rloop-p1-implement"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	w.Dog.Host = &dogHost{blocked: "step started ", state: core.AgentGone}
	w.Dog.Sleep = func(time.Duration) {}
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()

	select {
	case code := <-done:
		if code != 5 {
			t.Errorf("exit %d, want 5\n%s", code, f.out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run kept going with the watchdog gone")
	}
	st := f.load(w.Loop.RunID)
	if len(st.Signals) != 1 || st.Signals[0].Source != core.SourceDriver || st.Signals[0].Kind != core.SignalHalt || st.Signals[0].Reason != "the watchdog is gone" {
		t.Errorf("signals %+v", st.Signals)
	}
}

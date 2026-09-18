package app

import (
	"context"
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
	return nil
}
func (h *dogHost) State(agent string) (core.AgentState, error)  { return core.AgentWorking, nil }
func (h *dogHost) Read(agent string, lines int) (string, error) { return "", nil }
func (h *dogHost) Interrupt(agent string) error                 { return nil }
func (h *dogHost) Close(workspaceID string) error               { return nil }
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
		if strings.HasPrefix(c, "Prompt rloop-watchdog "+prefix) {
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
	go func() { done <- w.Execute(core.RunOptions{Phases: []int{1}}) }()
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
	if len(calls) < 3 || calls[0] != `Split "" right `+f.root || calls[1] != "Start wd-pane rloop-watchdog claude --model sonnet --mcp-config "+mcpPath || !strings.Contains(calls[2], runDir) {
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
	go func() { done <- w.Execute(core.RunOptions{Phases: []int{1}}) }()
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

	if proposed["decision"] != `refused: class "git" is not a remedy class` {
		t.Errorf("propose_remedy %v", proposed)
	}
	if restarted["accepted"] != false || restarted["reason"] != "run halted" {
		t.Errorf("restart_step %v", restarted)
	}
	if answered["accepted"] != false || answered["reason"] != "question q-none is not open" {
		t.Errorf("answer_question %v", answered)
	}
	if w.Watch.Router != w.Router || w.Router.Dog != w.Dog || w.Router.AnswerWindow != 5*time.Minute {
		t.Errorf("router %+v", w.Router)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run never halted")
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

	code := w.Execute(core.RunOptions{Phases: []int{1}})

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

func TestNoWatchdogStartsNothingKeepsTheChecksAndRecordsWatchdogSkippedOnce(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	w, err := f.preflight(f.todo, "--plain", "--phases", "1", "--no-watchdog")
	if err != nil {
		t.Fatal(err)
	}
	if w.Loop.RemedyWindow != 0 {
		t.Errorf("remedy window %s", w.Loop.RemedyWindow)
	}
	f.sim(w, newSim())
	dog := &dogHost{}
	w.Dog.Host = dog

	if code := w.Execute(core.RunOptions{Phases: []int{1}}); code != 0 {
		t.Fatalf("exit %d\n%s", code, f.out)
	}

	if calls := dog.Calls(); len(calls) != 0 {
		t.Errorf("watchdog touched: %q", calls)
	}
	if w.Loop.Watcher != w.Watch || len(w.Watch.Checks) != 5 || w.Watch.Dog != nil || w.Watch.Route(context.Background(), core.Question{ID: "q1"}) {
		t.Errorf("watch %+v", w.Watch)
	}
	if got := stepEvents(f.load(w.Loop.RunID), "watchdog-skipped"); len(got) != 1 {
		t.Errorf("watchdog-skipped events %+v", got)
	}
	report, _ := os.ReadFile(filepath.Join(w.Store.Dir(w.Loop.RunID), "report.md"))
	if strings.Count(string(report), "watchdog-skipped") != 1 {
		t.Errorf("report:\n%s", report)
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

	if code := w.Execute(core.RunOptions{Phases: []int{1}}); code != 0 {
		t.Fatalf("exit %d\n%s", code, f.out)
	}

	mcpPath := filepath.Join(w.Store.Dir(w.Loop.RunID), "watchdog.mcp.json")
	if calls := dog.Calls(); len(calls) < 2 || calls[1] != "Start wd-pane rloop-watchdog claude --model sonnet --cfg="+mcpPath {
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
	if _, err := f.preflight(f.todo, "--plain", "--no-watchdog"); err != nil {
		t.Fatal(err)
	}
}

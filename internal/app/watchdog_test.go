package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	mu         sync.Mutex
	calls      []string
	config     string
	configPath string
	configPerm os.FileMode
	startErr   error
	stale      map[string]string
	onPrompt   func(text string)
	question   func(id string)
	triage     func(text string)
	blocked    string
	state      core.AgentState
}

type lateDog struct {
	*dogHost
	store core.Store
	runID func() string
}

func (h *lateDog) ClosePane(pane string) error {
	err := h.store.Append(h.runID(), core.Record{Kind: core.RecordSignal, Signal: &core.Signal{Seq: 99, Kind: core.SignalWarn, Source: core.SourceWatchdog, Step: core.StepKey{Phase: "1", Kind: "implement"}, Reason: "late word"}})
	if err != nil {
		return err
	}
	return h.dogHost.ClosePane(pane)
}

type configAtStart struct {
	core.SessionHost
	mu      sync.Mutex
	mcp     map[string]string
	missing []string
}

func (h *configAtStart) Start(pane, name, kind string, args []string) (core.Agent, error) {
	h.mu.Lock()
	for i, arg := range args {
		if arg == "--mcp-config" && i+1 < len(args) {
			role := agentRole(name)
			h.mcp[role] = args[i+1]
			if _, err := os.Stat(args[i+1]); err != nil {
				h.missing = append(h.missing, role)
			}
		}
	}
	h.mu.Unlock()
	return h.SessionHost.Start(pane, name, kind, args)
}

type spacedSim struct {
	*configAtStart
	root, link string
}

func (h spacedSim) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	return h.configAtStart.Prompt(agent, strings.ReplaceAll(text, h.root, h.link), wait, timeout)
}

func TestAWatchdogAndAStepSessionWriteTheirMCPConfigUnderARepoRootWithASpace(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "repo with space")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	f := newFixtureIn(t, root)
	f.write("docs/topic/todo.md", resumeTodo)
	f.write(".r-loop/config.yaml", noReviewConfig)
	f.commit()
	f.env.PID, f.env.Pane = os.Getpid(), "driver-pane"
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, newSim())
	steps := &configAtStart{SessionHost: newSim(), mcp: map[string]string{}}
	w.Loop.Sessions.Host = spacedSim{configAtStart: steps, root: root, link: link}
	dogH := &dogHost{}
	dog := &configAtStart{SessionHost: dogH, mcp: map[string]string{}}
	w.Dog.Host = dog
	if code := w.Execute(core.RunOptions{Phases: []string{"1"}}); code != 0 {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	mcpPath, data, _ := dogH.startConfig()
	if mcpPath == "" || strings.HasPrefix(mcpPath, root) {
		t.Errorf("watchdog config %q is under the repo", mcpPath)
	}
	foundConfigArg := false
	for i, arg := range w.Dog.Provider.Args {
		if arg == "--mcp-config" && i+1 < len(w.Dog.Provider.Args) {
			foundConfigArg = true
			if w.Dog.Provider.Args[i+1] != mcpPath {
				t.Errorf("watchdog args %q, want config %q", w.Dog.Provider.Args, mcpPath)
			}
			break
		}
	}
	if !foundConfigArg {
		t.Errorf("watchdog args %q lack --mcp-config", w.Dog.Provider.Args)
	}
	if !strings.Contains(data, "/mcp/watchdog/") {
		t.Errorf("watchdog config %q", data)
	}
	if len(dog.missing) != 0 || len(steps.missing) != 0 || len(dog.mcp) != 1 {
		t.Errorf("dog paths=%v missing=%v step missing=%v", dog.mcp, dog.missing, steps.missing)
	}
	for _, path := range dog.mcp {
		if path != mcpPath {
			t.Errorf("watchdog config at start %q, want %q", path, mcpPath)
		}
	}
	stepPath := steps.mcp["rloop-p1-plan"]
	if !strings.HasPrefix(stepPath, root) || !strings.HasSuffix(stepPath, "-p1-plan.mcp.json") {
		t.Errorf("step path %q", stepPath)
	}
	if data, err := os.ReadFile(stepPath); err != nil || !strings.Contains(string(data), "/mcp/") {
		t.Errorf("step config %q: %v", data, err)
	}
}

func TestNoFileUnderTheRepoHoldsTheWatchdogTokenWhileTheRunIsLive(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, newSim())
	dog := &dogHost{}
	var once sync.Once
	var token string
	var leaks []string
	dog.onPrompt = func(string) {
		once.Do(func() {
			token = filepath.Base(w.Ask.WatchdogURL())
			if err := filepath.WalkDir(f.root, func(path string, entry os.DirEntry, err error) error {
				if err != nil || !entry.Type().IsRegular() {
					return err
				}
				data, err := os.ReadFile(path)
				if err == nil && (strings.Contains(string(data), token) || strings.Contains(string(data), "/mcp/watchdog/")) {
					leaks = append(leaks, path)
				}
				return err
			}); err != nil {
				t.Error(err)
			}
		})
	}
	w.Dog.Host = dog
	if code := w.Execute(core.RunOptions{Phases: []string{"1"}}); code != 0 {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if token == "" || len(leaks) != 0 {
		t.Errorf("token = %q, leaked files = %v", token, leaks)
	}
	path, _, _ := dog.startConfig()
	if path == "" || strings.HasPrefix(path, f.root) {
		t.Errorf("watchdog config path = %q", path)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("watchdog config remains: %v", err)
	}
}

func TestATempDirInsideTheRepoRefusesToStartTheWatchdog(t *testing.T) {
	for _, variant := range []string{"absolute", "relative", "case-variant", "symlink-to-subdirectory"} {
		t.Run(variant, func(t *testing.T) {
			f := newResumeFixture(t, noReviewConfig)
			w, err := f.preflight(f.todo, "--plain", "--phases", "1")
			if err != nil {
				t.Fatal(err)
			}
			f.sim(w, newSim())
			dog := &dogHost{}
			w.Dog.Host = dog
			base := filepath.Join(f.root, ".tmp")
			if err := os.Mkdir(base, 0o700); err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "absolute":
				t.Setenv("TMPDIR", base)
			case "relative":
				t.Chdir(f.root)
				t.Setenv("TMPDIR", ".tmp")
			case "case-variant":
				upper := strings.ToUpper(f.root)
				if _, err := os.Stat(upper); err != nil {
					t.Skip("case-sensitive filesystem")
				}
				t.Setenv("TMPDIR", filepath.Join(upper, ".tmp"))
			case "symlink-to-subdirectory":
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(base, link); err != nil {
					t.Fatal(err)
				}
				t.Setenv("TMPDIR", link)
			}
			if code := w.Execute(core.RunOptions{Phases: []string{"1"}}); code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if !strings.Contains(f.err.String(), "inside the repository") {
				t.Errorf("stderr %q", f.err.String())
			}
			for _, call := range dog.Calls() {
				if strings.HasPrefix(call, "Start ") {
					t.Errorf("watchdog started: %q", call)
				}
			}
			entries, err := os.ReadDir(base)
			if err != nil || len(entries) != 0 {
				t.Errorf("temp dir entries = %v, %v", entries, err)
			}
			if err := filepath.WalkDir(f.root, func(path string, entry os.DirEntry, err error) error {
				if err == nil && entry.Name() == "watchdog.mcp.json" {
					t.Errorf("watchdog config in repo: %s", path)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestARelativeTempDirOutsideTheRepoGivesTheWatchdogAnAbsoluteConfigPath(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, newSim())
	dog := &dogHost{}
	w.Dog.Host = dog
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	t.Setenv("TMPDIR", "tmp")
	if code := w.Execute(core.RunOptions{Phases: []string{"1"}}); code != 0 {
		t.Fatalf("exit %d\n%s", code, f.out)
	}
	if len(w.Dog.Provider.Args) < 2 {
		t.Fatalf("watchdog args %q", w.Dog.Provider.Args)
	}
	var mcpPath string
	for i, arg := range w.Dog.Provider.Args {
		if arg == "--mcp-config" && i+1 < len(w.Dog.Provider.Args) {
			mcpPath = w.Dog.Provider.Args[i+1]
		}
	}
	if !filepath.IsAbs(mcpPath) || !strings.HasPrefix(mcpPath, filepath.Join(cwd, "tmp")+string(os.PathSeparator)) {
		t.Fatalf("watchdog config arg %q, want absolute path under %s", mcpPath, filepath.Join(cwd, "tmp"))
	}
	path, data, _ := dog.startConfig()
	if path != mcpPath || !strings.Contains(data, w.Ask.WatchdogURL()) {
		t.Errorf("watchdog start config %q: %q", path, data)
	}
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
	h.mu.Lock()
	for _, arg := range args {
		if strings.HasSuffix(arg, "watchdog.mcp.json") {
			h.configPath = arg[strings.Index(arg, "/"):]
			data, _ := os.ReadFile(h.configPath)
			h.config = string(data)
			if info, err := os.Stat(h.configPath); err == nil {
				h.configPerm = info.Mode().Perm()
			}
		}
	}
	h.mu.Unlock()
	h.record("Start %s %s %s %s", pane, name, kind, strings.Join(args, " "))
	return core.Agent{Name: name, Pane: pane}, h.startErr
}

func (h *dogHost) startConfig() (path, data string, perm os.FileMode) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.configPath, h.config, h.configPerm
}
func (h *dogHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	h.record("Prompt %s %s", agent, text)
	if h.blocked != "" && strings.HasPrefix(text, h.blocked) {
		return errors.New("herdr: agent_blocked")
	}
	if h.onPrompt != nil {
		h.onPrompt(text)
	}
	if strings.HasPrefix(text, "triage ") {
		if h.triage != nil {
			go h.triage(text)
		} else if w, ok := triagers.Load(agent); ok {
			go autoTriage(w.(*Wiring), text)
		}
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
func (h *dogHost) Split(pane, direction, cwd string, env map[string]string) (string, error) {
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
	mcpPath, data, perm := dog.startConfig()
	calls := dog.Calls()
	if len(calls) < 3 || calls[0] != `Split "driver-pane" right `+f.root || calls[1] != "Start wd-pane "+core.WatchdogName(w.Loop.RunID)+" claude --model opus --effort high --mcp-config "+mcpPath || !strings.Contains(calls[2], runDir) {
		t.Errorf("watchdog calls %q", calls)
	}
	if !strings.Contains(data, w.Ask.WatchdogURL()) {
		t.Errorf("mcp config %s", data)
	}
	if perm != 0o600 || strings.HasPrefix(mcpPath, f.root) {
		t.Errorf("mcp config path %q, mode %v", mcpPath, perm)
	}
	if _, err := os.Stat(mcpPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("mcp config remains: %v", err)
	}
	if !dog.prompted("step ended phase-1/implement failed watchdog: off the plan") {
		t.Errorf("no step ended for implement: %q", calls)
	}
	if last := calls[len(calls)-1]; last != "ClosePane wd-pane" {
		t.Errorf("watchdog pane left open: %q", calls)
	}
}

func TestTheFinalReportHoldsARecordAppendedWhileTheWatchdogCloses(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.hang["rloop-p1-implement"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	dog := &lateDog{dogHost: &dogHost{}, store: w.Store, runID: func() string { return w.Loop.RunID }}
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "signal", Arguments: map[string]any{"kind": "halt", "step": "phase-1/implement", "reason": "off the plan", "evidence": "docs/topic/todo.md:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := res.StructuredContent.(map[string]any); out["accepted"] != true {
		t.Fatalf("signal result %v", res.StructuredContent)
	}
	select {
	case code := <-done:
		if code != 5 {
			t.Fatalf("exit %d, want 5\n%s", code, f.out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run never halted")
	}
	report, err := os.ReadFile(filepath.Join(w.Store.Dir(w.Loop.RunID), "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "warn from watchdog, phase 1 implement: late word") {
		t.Errorf("late watchdog signal missing from report:\n%s", report)
	}
}

func TestASignalCallIsHandledWhileAWarnHookRuns(t *testing.T) {
	dir := t.TempDir()
	hook := filepath.Join(dir, "hook.sh")
	entered := filepath.Join(dir, "entered")
	gate := filepath.Join(dir, "gate")
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o644) })
	script := fmt.Sprintf("touch %q\nwhile [ ! -e %q ]; do sleep 0.05; done\n", entered, gate)
	if err := os.WriteFile(hook, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newResumeFixture(t, noReviewConfig+"notify:\n  onWarn: sh "+hook+"\n")
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
	call := func(kind, reason string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "signal", Arguments: map[string]any{"kind": kind, "step": "phase-1/implement", "reason": reason, "evidence": "docs/topic/todo.md:1"}})
		if err != nil {
			t.Fatal(err)
		}
		if out, _ := res.StructuredContent.(map[string]any); out["accepted"] != true {
			t.Fatalf("signal %s result %v", reason, res.StructuredContent)
		}
	}
	call("warn", "slow")
	for deadline := time.Now().Add(2 * time.Second); ; {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("warn hook never entered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	call("warn", "slower")
	for deadline := time.Now().Add(2 * time.Second); ; {
		st, err := w.Store.Load(w.Loop.RunID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, event := range st.Events {
			if event.Kind == "warning" && event.Fields["reason"] == "slower" {
				found = true
				break
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second warning was not handled while hook ran")
		}
		time.Sleep(5 * time.Millisecond)
	}
	call("halt", "off the plan")
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 5 {
			t.Fatalf("exit %d, want 5\n%s", code, f.out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run never halted")
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
	t.Cleanup(func() { os.RemoveAll(w.dogDir) })
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

	mcpPath, data, _ := dog.startConfig()
	if calls := dog.Calls(); len(calls) < 2 || calls[1] != "Start wd-pane "+core.WatchdogName(w.Loop.RunID)+" claude --model opus --cfg="+mcpPath {
		t.Errorf("watchdog calls %q", calls)
	}
	if !strings.Contains(data, "/mcp/watchdog/") {
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
		"steps.plan.fallback":  "steps:\n  plan:\n    fallback:\n      provider: plainbot\n      model: m\n      effort: e\n",
		"steps.plan.reviewers": "steps:\n  plan:\n    reviewers:\n      - provider: plainbot\n        model: m\n        effort: e\n",
		"land.fix.provider":    "land:\n  fix:\n    provider: plainbot\n    model: m\n    effort: e\n",
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

var (
	triagers   sync.Map
	triageIDRe = regexp.MustCompile(`(?:phases|items) ([0-9a-z, ]+)\. Follow`)
)

func triageIDs(text string) []string {
	m := triageIDRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	return strings.Split(m[1], ", ")
}

func allBuild(text string) core.Triage {
	var t core.Triage
	if !strings.HasPrefix(text, "triage backlog") {
		for _, id := range triageIDs(text) {
			t.Phases = append(t.Phases, core.PhaseVerdict{Phase: id, Status: core.VerdictBuild})
		}
		return t
	}
	for _, id := range triageIDs(text) {
		t.Items = append(t.Items, fixItem(id))
		t.Groups = append(t.Groups, core.Group{ID: "g" + id, Items: []string{id}, Subsystem: "x"})
	}
	return t
}

func fixItem(id string) core.ItemVerdict {
	return core.ItemVerdict{ID: id, Title: "x", Verdict: core.VerdictFix, Category: "bug", Confidence: "high", RootCause: "x", Touches: []string{"x"}, Risk: core.RiskLocal}
}

func asksTheMaintainer(text string) bool {
	return strings.Contains(text, "ask the maintainer, then call submit_gate")
}

func autoTriage(w *Wiring, text string) {
	if ok, _, _ := w.submitTriage(allBuild(text)); ok && asksTheMaintainer(text) {
		w.submitGate(core.GateDecision{Decision: core.GateGo, MaintainerSaid: "go"})
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

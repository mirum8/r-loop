package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type scriptedHost struct {
	fakeSessionHost
	StartErr error
	StateErr error
	script   func(n int) AgentState
	polls    int
}

func (h *scriptedHost) Start(pane, name, kind string, args []string) (Agent, error) {
	a, err := h.fakeSessionHost.Start(pane, name, kind, args)
	if h.StartErr != nil {
		return a, h.StartErr
	}
	return a, err
}

func (h *scriptedHost) State(agent string) (AgentState, error) {
	h.record("SessionHost.State %s", agent)
	h.polls++
	if h.StateErr != nil {
		return AgentWorking, h.StateErr
	}
	if h.script == nil {
		return AgentWorking, nil
	}
	return h.script(h.polls), nil
}

type stepClock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(c.step)
	return c.t
}

type rig struct {
	shared  *callLog
	host    *scriptedHost
	repo    *fakeRepo
	store   *fakeStore
	prompts *fakePrompts
	sm      *SessionManager
	runDir  string
	resolve []string
}

func newRig(t *testing.T) *rig {
	shared := &callLog{}
	r := &rig{
		shared:  shared,
		host:    &scriptedHost{fakeSessionHost: fakeSessionHost{callLog: callLog{Shared: shared}}},
		repo:    &fakeRepo{callLog: callLog{Shared: shared}, RootDir: "/repo", SHA: "sha-start", Tree: "tree-start"},
		store:   &fakeStore{callLog: callLog{Shared: shared}},
		prompts: &fakePrompts{callLog: callLog{Shared: shared}, Texts: map[string]string{"implement": "do phase 3"}},
		runDir:  t.TempDir(),
	}
	clock := &stepClock{t: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC), step: time.Minute}
	r.sm = &SessionManager{
		Host:    r.host,
		Repo:    r.repo,
		Prompts: r.prompts,
		Store:   r.store,
		Resolve: func(provider, model, effort, askURL, mcpConfigPath string) (ProviderArgs, error) {
			r.resolve = []string{provider, model, effort, askURL, mcpConfigPath}
			return ProviderArgs{Kind: "codex", Args: []string{"-c", "model=" + model}, Ask: true}, nil
		},
		Now:        clock.Now,
		Poll:       time.Millisecond,
		StallGrace: 2 * time.Minute,
	}
	return r
}

func (r *rig) ref(attempt int) StepRef {
	return StepRef{
		Key:      StepKey{Run: "run-1", Phase: "3", Kind: "implement", Attempt: attempt},
		Kind:     StepKind{Name: "implement", Prompt: "implement", Check: "diff", Row: StepRow{Provider: "codex", Model: "gpt-5.6-sol", Effort: "medium", Timeout: time.Hour}},
		Phase:    Phase{ID: "3", Title: "Plan reader"},
		Worktree: "/repo/.r-loop/wt/phase-3",
		Branch:   "r-loop/phase-3",
		Base:     "main",
		RunDir:   r.runDir,
		Vars:     map[string]any{"PhaseNumber": "3"},
	}
}

func (r *rig) spawn(t *testing.T, attempt int) *Session {
	t.Helper()
	s, err := r.sm.Spawn(context.Background(), r.ref(attempt))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	return s
}

func (r *rig) writeSentinel(t *testing.T, s *Session, outcome, reason string) {
	t.Helper()
	body := `{"outcome":"` + outcome + `","reason":"` + reason + `"}`
	if err := os.WriteFile(s.Sentinel, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) writeRawSentinel(t *testing.T, s *Session, body string) {
	t.Helper()
	if err := os.WriteFile(s.Sentinel, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAnImplementStepThatChangedTheTodoFailsNamingIt(t *testing.T) {
	r := newRig(t)
	ref := r.ref(1)
	ref.Vars["TodoPath"] = "/repo/docs/todo.md"
	s, err := r.sm.Spawn(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	r.repo.TreeChanges = []string{"a.go", "docs/todo.md"}
	r.host.script = func(int) AgentState {
		r.writeSentinel(t, s, "ok", "")
		return AgentWorking
	}
	out := r.sm.Wait(context.Background(), s, &recObserver{})
	if out.State != StepFailed || out.Reason != "evidence missing: step changed docs/todo.md, the run's plan file; only the driver edits it" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestAHalfWrittenSentinelThatTurnsValidEndsTheStepByItsContent(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "truncated": `{"outcome":"o`} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			s := r.spawn(t, 1)
			r.repo.TreeChanges = []string{"a.go"}
			r.host.script = func(n int) AgentState {
				if n == 1 {
					r.writeRawSentinel(t, s, body)
				} else {
					r.writeSentinel(t, s, "ok", "")
				}
				return AgentWorking
			}
			out := r.sm.Wait(context.Background(), s, &recObserver{})
			if out.State != StepOK || out.Reason != "" {
				t.Fatalf("outcome = %+v", out)
			}
		})
	}
}

func TestASentinelStillMalformedAfterTheGraceFailsWithTheParseError(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.host.script = func(int) AgentState {
		r.writeRawSentinel(t, s, `{"outcome":`)
		return AgentWorking
	}
	out := r.sm.Wait(context.Background(), s, &recObserver{})
	if out.State != StepFailed || !strings.HasPrefix(out.Reason, "sentinel unreadable: ") || !strings.Contains(out.Reason, "unexpected end of JSON input") {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestAGoneAgentThatFinishedItsSentinelEndsByItsContent(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.repo.TreeChanges = []string{"a.go"}
	r.host.script = func(n int) AgentState {
		if n == 1 {
			r.writeRawSentinel(t, s, `{"outcome":"o`)
			return AgentWorking
		}
		r.writeSentinel(t, s, "ok", "")
		return AgentGone
	}
	out := r.sm.Wait(context.Background(), s, &recObserver{})
	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestAGoneAgentLeavingAHalfWrittenSentinelFailsWithTheParseError(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.host.script = func(n int) AgentState {
		if n == 1 {
			r.writeRawSentinel(t, s, `{"outcome":`)
			return AgentWorking
		}
		return AgentGone
	}
	out := r.sm.Wait(context.Background(), s, &recObserver{})
	if out.State != StepFailed || !strings.HasPrefix(out.Reason, "sentinel unreadable: ") {
		t.Fatalf("outcome = %+v", out)
	}
}

func (r *rig) steps() []StepState {
	var out []StepState
	for _, rec := range r.store.Records["run-1"] {
		if rec.Kind == RecordStep {
			out = append(out, rec.State)
		}
	}
	return out
}

func (r *rig) events(kind string) []Event {
	var out []Event
	for _, rec := range r.store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == kind {
			out = append(out, *rec.Event)
		}
	}
	return out
}

func (r *rig) count(prefix string) int {
	n := 0
	for _, c := range r.shared.Calls() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestATimedOutStateCallFailsTheStepNamingIt(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.host.StateErr = errors.New("herdr agent get x: timed out after 30s")
	out := r.sm.Wait(context.Background(), s, &recObserver{})
	if out.State != StepFailed || !strings.Contains(out.Reason, r.host.StateErr.Error()) || r.host.polls != 1 {
		t.Fatalf("outcome = %+v, polls = %d", out, r.host.polls)
	}
}

func TestATimedOutStateCallFailsAStepWaitingOnAnAnswer(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	s.OpenQuestion.Store(true)
	r.host.StateErr = errors.New("herdr agent get x: timed out after 30s")
	out := r.sm.Wait(context.Background(), s, &recObserver{})
	if out.State != StepFailed || !strings.Contains(out.Reason, r.host.StateErr.Error()) || r.host.polls != 1 {
		t.Fatalf("outcome = %+v, polls = %d", out, r.host.polls)
	}
}

func TestTheBackstopStillFiresWhenEveryHostStateCallFails(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.host.StateErr = errors.New("herdr agent get x: connection refused")
	out := r.sm.Wait(context.Background(), s, &recObserver{})
	if out.State != StepFailed || !strings.Contains(out.Reason, "backstop 1h0m0s") || r.host.polls != 61 {
		t.Fatalf("outcome = %+v, polls = %d", out, r.host.polls)
	}
}

func TestATimedOutHostCallAtSpawnFailsTheStepNamingIt(t *testing.T) {
	r := newRig(t)
	r.host.StartErr = errors.New("herdr agent start x: timed out after 30s")
	out := (singleRunner{sm: r.sm}).Run(context.Background(), r.ref(1), &recObserver{})
	if out.State != StepFailed || !strings.Contains(out.Reason, r.host.StartErr.Error()) {
		t.Fatalf("outcome = %+v", out)
	}
}

type recObserver struct {
	started, stalled, resumed int
	rounds                    []int
}

func (o *recObserver) Started(*Session) { o.started++ }
func (o *recObserver) Stalled(*Session) { o.stalled++ }
func (o *recObserver) Resumed(*Session) { o.resumed++ }

func (o *recObserver) Reviewing(_ *Session, round int) { o.rounds = append(o.rounds, round) }

func TestTwoRunsNameTheSameStepApart(t *testing.T) {
	r := newRig(t)
	first := r.spawn(t, 1)
	ref := r.ref(1)
	ref.Key.Run = "run-7"
	second, err := r.sm.Spawn(context.Background(), ref)
	if err != nil || first.Agent != "rloop-2kuxv-p3-implement" || second.Agent != "rloop-qih3p-p3-implement" {
		t.Fatalf("agents %q, %q; err %v", first.Agent, second.Agent, err)
	}
}

func TestTheSameRunIDInAnotherRepositoryIsNamedApart(t *testing.T) {
	r := newRig(t)
	r.repo.RootDir = "/other"
	if s := r.spawn(t, 1); s.Agent != "rloop-jkmip-p3-implement" {
		t.Fatalf("agent %q", s.Agent)
	}
}

func TestASpawnWhoseNameIsTakenMovesToTheNextToken(t *testing.T) {
	r := newRig(t)
	r.host.Panes = map[string]string{"rloop-2kuxv-p3-implement": "pane-9"}
	if s := r.spawn(t, 1); s.Agent != "rloop-uiz4e-p3-implement" {
		t.Fatalf("agent %q", s.Agent)
	}
}

func TestASpawnWithEveryAlternateNameTakenFails(t *testing.T) {
	r := newRig(t)
	r.host.Panes = map[string]string{"rloop-2kuxv-p3-implement": "pane-9", "rloop-uiz4e-p3-implement": "pane-8", "rloop-kjdff-p3-implement": "pane-7"}
	_, err := r.sm.Spawn(context.Background(), r.ref(1))
	if err == nil || err.Error() != "spawn: agent name rloop-2kuxv-p3-implement taken, and its alternates" || r.count("SessionHost.Open") != 0 || r.count("SessionHost.Start") != 0 || len(r.steps()) != 0 {
		t.Fatalf("err %v; calls %v; steps %v", err, r.shared.Calls(), r.steps())
	}
}

func TestASpawnFailsWhenHerdrCannotBeAskedForTheName(t *testing.T) {
	r := newRig(t)
	r.host.Err = errors.New("herdr down")
	_, err := r.sm.Spawn(context.Background(), r.ref(1))
	if err == nil || !strings.HasPrefix(err.Error(), "spawn: ") || !errors.Is(err, r.host.Err) || r.count("SessionHost.Start") != 0 {
		t.Fatalf("err %v; calls %v", err, r.shared.Calls())
	}
}

func TestSpawnRecordsTheAgentNameBeforeStartingIt(t *testing.T) {
	r := newRig(t)
	r.spawn(t, 1)
	events := r.events("agent-named")
	want := map[string]string{"attempt": "1", "agent": "rloop-2kuxv-p3-implement"}
	if len(events) != 1 || events[0].Phase != "3" || events[0].Step != "implement" || !reflect.DeepEqual(events[0].Fields, want) {
		t.Fatalf("events %+v", events)
	}
	calls := r.shared.Calls()
	start := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "SessionHost.Start pane-1") })
	if start < 1 || calls[start-1] != "Store.Append run-1 event" {
		t.Fatalf("calls %v", calls)
	}
}

func TestASpawnWhoseNameCannotBeRecordedNeverStartsTheAgent(t *testing.T) {
	r := newRig(t)
	r.sm.Store = failingEventStore{fakeStore: r.store, kind: "agent-named"}
	_, err := r.sm.Spawn(context.Background(), r.ref(1))
	if err == nil || !strings.Contains(err.Error(), "disk full") || r.count("SessionHost.Start") != 0 {
		t.Fatalf("err %v; calls %v", err, r.shared.Calls())
	}
}

func TestALongStepNameIsCutToTheLimitKeepingTheRunTokenAndAttempt(t *testing.T) {
	r := newRig(t)
	r.sm.Label = "test"
	ref := r.ref(2)
	ref.Key.Phase = "12"
	s, err := r.sm.Spawn(context.Background(), ref)
	if err != nil || s.Agent != "rloop-test-2kuxv-p12-im-4cdh5-a2" || len(s.Agent) != 32 {
		t.Fatalf("agent %q; err %v", s.Agent, err)
	}
}

func TestTwoRunsWithALongLabelStillNameTheStepApart(t *testing.T) {
	r := newRig(t)
	r.sm.Label = "a-very-long-label-name"
	first := r.spawn(t, 1)
	ref := r.ref(1)
	ref.Key.Run = "run-7"
	second, err := r.sm.Spawn(context.Background(), ref)
	if err != nil || first.Agent != "rloop-a-very-long-label-na-hykpv" || second.Agent != "rloop-a-very-long-label-na-bzyxw" {
		t.Fatalf("agents %q, %q; err %v", first.Agent, second.Agent, err)
	}
}

func TestAGateStepIsNamedWithTheRunToken(t *testing.T) {
	r := newRig(t)
	ref := r.ref(1)
	ref.Key.Phase, ref.Key.Kind, ref.Kind.Name = "1", "gate", "gate"
	if s, err := r.sm.Spawn(context.Background(), ref); err != nil || s.Agent != "rloop-2kuxv-p1-gate" {
		t.Fatalf("session %+v; err %v", s, err)
	}
}

func TestSpawnRecordsSpawnedBeforeOpenThenStartsPromptsAndRecordsRunning(t *testing.T) {
	r := newRig(t)

	s := r.spawn(t, 1)

	sentinel := filepath.Join(r.runDir, "phase-3", "implement-a1.sentinel")
	want := []string{
		"Repo.AddWorktree /repo/.r-loop/wt/phase-3 r-loop/phase-3 main",
		"Repo.HeadSHA /repo/.r-loop/wt/phase-3",
		"Repo.Snapshot /repo/.r-loop/wt/phase-3",
		"Store.Append run-1 event",
		"Repo.Root",
		"SessionHost.AgentPane rloop-2kuxv-p3-implement",
		"Store.Load run-1",
		"Store.Append run-1 step",
		"SessionHost.Open /repo/.r-loop/wt/phase-3 ◆ p3 implement map[GIT_COMMITTER_NAME:r-loop rloop-2kuxv-p3-implement R_LOOP_PHASE:3 R_LOOP_RUN:run-1 R_LOOP_SENTINEL:" + sentinel + " R_LOOP_STEP:implement]",
		"Store.Append run-1 event",
		"SessionHost.Start pane-1 rloop-2kuxv-p3-implement codex [-c model=gpt-5.6-sol]",
		"Prompts.Render implement",
		`SessionHost.Prompt rloop-2kuxv-p3-implement "do phase 3" false 0s`,
		"Store.Load run-1",
		"Store.Append run-1 step",
	}
	if got := r.shared.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls =\n%s", strings.Join(got, "\n"))
	}
	if got := r.steps(); !reflect.DeepEqual(got, []StepState{StepSpawned, StepRunning}) {
		t.Fatalf("steps = %v", got)
	}
	if s.StartSHA != "sha-start" || s.StartTree != "tree-start" || s.Dir != "/repo/.r-loop/wt/phase-3" {
		t.Fatalf("session = %+v", s)
	}
	if s.Workspace != "ws-1" || s.Agent != "rloop-2kuxv-p3-implement" || s.Sentinel != sentinel {
		t.Fatalf("session = %+v", s)
	}
	if !reflect.DeepEqual(r.resolve, []string{"codex", "gpt-5.6-sol", "medium", "", ""}) {
		t.Fatalf("resolve = %q", r.resolve)
	}
	if _, err := os.Stat(filepath.Dir(sentinel)); err != nil {
		t.Fatalf("sentinel dir: %v", err)
	}
}

func TestSpawnRendersTheStepVariablesPlusTheSentinel(t *testing.T) {
	r := newRig(t)
	var got map[string]any
	r.sm.Prompts = promptsFunc(func(name string, vars map[string]any) (string, string, error) {
		got = vars
		return "text", "embedded", nil
	})

	s := r.spawn(t, 1)

	want := map[string]any{"PhaseNumber": "3", "Sentinel": s.Sentinel}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("vars = %v", got)
	}
}

type promptsFunc func(name string, vars map[string]any) (string, string, error)

func (f promptsFunc) Render(name string, vars map[string]any) (string, string, error) {
	return f(name, vars)
}

func claudeLikeResolve(r *rig) func(provider, model, effort, askURL, mcpConfigPath string) (ProviderArgs, error) {
	return func(provider, model, effort, askURL, mcpConfigPath string) (ProviderArgs, error) {
		r.resolve = []string{provider, model, effort, askURL, mcpConfigPath}
		args := []string{"--model", model}
		if mcpConfigPath != "" {
			args = append(args, "--mcp-config", mcpConfigPath)
		}
		return ProviderArgs{Kind: "claude", Args: args, Ask: true}, nil
	}
}

type configCheckingHost struct {
	*scriptedHost
	path    string
	present bool
}

func (h *configCheckingHost) Start(pane, name, kind string, args []string) (Agent, error) {
	_, err := os.Stat(h.path)
	h.present = err == nil
	return h.scriptedHost.Start(pane, name, kind, args)
}

func TestAnAskingProviderGetsTheStepURLAndAnMCPConfigWrittenBeforeItStarts(t *testing.T) {
	r := newRig(t)
	r.sm.Ask = &fakeAskChannel{callLog: callLog{Shared: r.shared}, BaseURL: "http://127.0.0.1:7000/mcp/tok"}
	r.sm.Resolve = claudeLikeResolve(r)
	path := filepath.Join(r.runDir, "phase-3", "rloop-2kuxv-p3-implement.mcp.json")
	host := &configCheckingHost{scriptedHost: r.host, path: path}
	r.sm.Host = host
	var vars map[string]any
	r.sm.Prompts = promptsFunc(func(name string, v map[string]any) (string, string, error) {
		vars = v
		return "text", "embedded", nil
	})

	s := r.spawn(t, 1)

	url := "http://127.0.0.1:7000/mcp/tok/run-1/3/implement/1"
	if s.Ref.AskURL != url || vars["AskURL"] != url {
		t.Fatalf("ask url = %q, var = %v", s.Ref.AskURL, vars["AskURL"])
	}
	if !reflect.DeepEqual(r.resolve, []string{"codex", "gpt-5.6-sol", "medium", url, path}) {
		t.Fatalf("resolve = %q", r.resolve)
	}
	if !host.present {
		t.Fatal("the mcp config was not there when the agent started")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"mcpServers":{"r-loop":{"type":"http","url":"` + url + `"}}}`; string(data) != want {
		t.Fatalf("mcp config = %s", data)
	}
	if len(r.events("ask-none")) != 0 {
		t.Fatal("recorded ask-none for an asking provider")
	}
}

func TestLandStageSessionsHaveNoAskMCPConfig(t *testing.T) {
	for _, kind := range []string{"gatefix", "gate", "milestone", "plan", "implement"} {
		t.Run(kind, func(t *testing.T) {
			r := newRig(t)
			r.sm.Ask = &fakeAskChannel{callLog: callLog{Shared: r.shared}, BaseURL: "http://127.0.0.1:7000/mcp/tok"}
			r.sm.Resolve = claudeLikeResolve(r)
			r.sm.Prompts = promptsFunc(func(string, map[string]any) (string, string, error) {
				return "text", "embedded", nil
			})
			ref := r.ref(1)
			ref.Key.Kind = kind
			ref.Kind.Name = kind
			ref.Kind.Prompt = kind
			s, err := r.sm.Spawn(context.Background(), ref)
			if err != nil {
				t.Fatal(err)
			}
			matches, err := filepath.Glob(filepath.Join(r.runDir, "phase-3", "*.mcp.json"))
			if err != nil {
				t.Fatal(err)
			}
			land := kind == "gatefix" || kind == "gate" || kind == "milestone"
			if land {
				if s.Ref.AskURL != "" || len(matches) != 0 || r.count("AskChannel.StepURL") != 0 {
					t.Fatalf("ask URL = %q, configs = %v, calls = %v", s.Ref.AskURL, matches, r.shared.Calls())
				}
				if len(r.resolve) != 5 || r.resolve[3] != "" || r.resolve[4] != "" {
					t.Fatalf("resolve = %q", r.resolve)
				}
			} else if s.Ref.AskURL == "" || len(matches) != 1 || r.count("AskChannel.StepURL") != 1 {
				t.Fatalf("ask URL = %q, configs = %v, calls = %v", s.Ref.AskURL, matches, r.shared.Calls())
			}
		})
	}
}

func TestLandStageStallNudgeDoesNotOfferAskWatchdog(t *testing.T) {
	for _, kind := range []string{"gatefix", "gate", "milestone", "plan", "implement"} {
		t.Run(kind, func(t *testing.T) {
			r := newRig(t)
			r.sm.Ask = &fakeAskChannel{callLog: callLog{Shared: r.shared}, BaseURL: "http://127.0.0.1:7000/mcp/tok"}
			r.sm.Prompts = promptsFunc(func(string, map[string]any) (string, string, error) {
				return "text", "embedded", nil
			})
			ref := r.ref(1)
			ref.Key.Kind = kind
			ref.Kind.Name = kind
			ref.Kind.Prompt = kind
			s, err := r.sm.Spawn(context.Background(), ref)
			if err != nil {
				t.Fatal(err)
			}
			r.host.script = func(int) AgentState { return AgentBlocked }
			r.sm.Wait(context.Background(), s, &recObserver{})
			prompts := slices.DeleteFunc(r.shared.Calls(), func(c string) bool {
				return !strings.HasPrefix(c, "SessionHost.Prompt ")
			})
			if len(prompts) < 2 {
				t.Fatalf("prompts = %q", prompts)
			}
			got := strings.Contains(prompts[1], "ask_watchdog")
			want := kind == "plan" || kind == "implement"
			if got != want {
				t.Fatalf("nudge = %q; mentions ask_watchdog = %t, want %t", prompts[1], got, want)
			}
		})
	}
}

func TestAProviderTakingTheURLDirectlyGetsNoMCPConfigFile(t *testing.T) {
	r := newRig(t)
	r.sm.Ask = &fakeAskChannel{callLog: callLog{Shared: r.shared}, BaseURL: "http://127.0.0.1:7000/mcp/tok"}

	s := r.spawn(t, 1)

	url := "http://127.0.0.1:7000/mcp/tok/run-1/3/implement/1"
	if s.Ref.AskURL != url {
		t.Fatalf("ask url = %q", s.Ref.AskURL)
	}
	if got := r.count("SessionHost.Start pane-1 rloop-2kuxv-p3-implement codex [-c model=gpt-5.6-sol]"); got != 1 {
		t.Fatalf("calls =\n%s", strings.Join(r.shared.Calls(), "\n"))
	}
	matches, _ := filepath.Glob(filepath.Join(r.runDir, "phase-3", "*.mcp.json"))
	if len(matches) != 0 {
		t.Fatalf("wrote %v", matches)
	}
}

func TestAnAskNoneProviderGetsNoAskFlagAndRecordsAskNoneOnce(t *testing.T) {
	r := newRig(t)
	r.sm.Ask = &fakeAskChannel{callLog: callLog{Shared: r.shared}, BaseURL: "http://127.0.0.1:7000/mcp/tok"}
	r.sm.Resolve = func(provider, model, effort, askURL, mcpConfigPath string) (ProviderArgs, error) {
		r.resolve = []string{provider, model, effort, askURL, mcpConfigPath}
		return ProviderArgs{Kind: "codex", Args: []string{"-c", "model=" + model}}, nil
	}

	s := r.spawn(t, 1)

	if !reflect.DeepEqual(r.resolve, []string{"codex", "gpt-5.6-sol", "medium", "", ""}) {
		t.Fatalf("resolve = %q", r.resolve)
	}
	if s.Ref.AskURL != "" || r.count("AskChannel.StepURL") != 0 {
		t.Fatalf("ask url = %q", s.Ref.AskURL)
	}
	events := r.events("ask-none")
	if len(events) != 1 || events[0].Fields["provider"] != "codex" || events[0].Phase != "3" || events[0].Step != "implement" {
		t.Fatalf("ask-none events = %+v", events)
	}
}

func TestSpawnInPrimaryMakesNoWorktreeAndRunsInTheRepoRoot(t *testing.T) {
	r := newRig(t)
	ref := r.ref(1)
	ref.InPrimary = true
	ref.Worktree = ""

	s, err := r.sm.Spawn(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}

	if n := r.count("Repo.AddWorktree"); n != 0 {
		t.Fatalf("AddWorktree called %d times", n)
	}
	if s.Dir != "/repo" || r.host.Opened[0].CWD != "/repo" {
		t.Fatalf("dir = %q, opened = %+v", s.Dir, r.host.Opened)
	}
}

func TestSpawnFailureAfterOpenLeavesTheWorkspaceStandingAndNamesIt(t *testing.T) {
	r := newRig(t)
	r.host.StartErr = errors.New("herdr: pane_not_found: no pane pane-1")

	s, err := r.sm.Spawn(context.Background(), r.ref(1))

	if err == nil || err.Error() != "spawn: herdr: pane_not_found: no pane pane-1" {
		t.Fatalf("err = %v", err)
	}
	if s == nil || s.Workspace != "ws-1" {
		t.Fatalf("session = %+v", s)
	}
	if r.count("SessionHost.Close") != 0 || r.count("SessionHost.Start") != 1 || r.count("SessionHost.Prompt") != 0 {
		t.Fatalf("calls = %q", r.shared.Calls())
	}
	if got := r.steps(); !reflect.DeepEqual(got, []StepState{StepSpawned}) {
		t.Fatalf("steps = %v", got)
	}
}

func TestAttemptTwoGetsTheSuffixedNameAndSentinel(t *testing.T) {
	r := newRig(t)

	s := r.spawn(t, 2)

	if s.Agent != "rloop-2kuxv-p3-implement-a2" || r.host.Opened[0].Label != "◆ p3 implement·a2" {
		t.Fatalf("agent = %q, label = %q", s.Agent, r.host.Opened[0].Label)
	}
	if filepath.Base(s.Sentinel) != "implement-a2.sentinel" {
		t.Fatalf("sentinel = %q", s.Sentinel)
	}
}

func TestOkSentinelWithEvidenceIsOkAndCommitsOnlyInFinish(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.repo.TreeChanges = []string{"internal/plan/reader.go"}
	r.host.script = func(n int) AgentState {
		if n == 2 {
			r.writeSentinel(t, s, "ok", "done")
		}
		return AgentWorking
	}
	obs := &recObserver{}

	out := r.sm.Wait(context.Background(), s, obs)

	if out.State != StepOK || out.Reason != "" || out.Session != s {
		t.Fatalf("outcome = %+v", out)
	}
	if obs.started != 1 {
		t.Fatalf("started = %d", obs.started)
	}
	if r.count("Repo.CommitAll") != 0 {
		t.Fatal("committed before Finish")
	}
	if got := r.steps(); !reflect.DeepEqual(got, []StepState{StepSpawned, StepRunning}) {
		t.Fatalf("steps = %v", got)
	}

	fin := r.sm.Finish(s, out)

	if fin.State != StepOK {
		t.Fatalf("finish = %+v", fin)
	}
	commits := slices.DeleteFunc(r.shared.Calls(), func(c string) bool { return !strings.HasPrefix(c, "Repo.CommitAll") })
	if !reflect.DeepEqual(commits, []string{`Repo.CommitAll /repo/.r-loop/wt/phase-3 "r-loop: phase 3 implement"`}) {
		t.Fatalf("commits = %q", commits)
	}
	if got := r.steps(); !reflect.DeepEqual(got, []StepState{StepSpawned, StepRunning, StepOK}) {
		t.Fatalf("steps = %v", got)
	}
}

func TestFinishOfAnInPrimaryStepCommitsNothing(t *testing.T) {
	r := newRig(t)
	ref := r.ref(1)
	ref.InPrimary = true
	s, _ := r.sm.Spawn(context.Background(), ref)

	r.sm.Finish(s, Outcome{State: StepOK, Session: s})

	if r.count("Repo.CommitAll") != 0 {
		t.Fatal("committed an InPrimary step")
	}
	if got := r.steps(); got[len(got)-1] != StepOK {
		t.Fatalf("steps = %v", got)
	}
}

func TestFinishFailsTheStepWhenTheCommitFails(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.repo.Err = errors.New("index locked")

	fin := r.sm.Finish(s, Outcome{State: StepOK, Session: s})

	if fin.State != StepFailed || fin.Reason != "commit: index locked; snapshot: index locked" {
		t.Fatalf("finish = %+v", fin)
	}
	if got := r.steps(); got[len(got)-1] != StepFailed {
		t.Fatalf("steps = %v", got)
	}
}

func TestOkSentinelWithNoChangeFailsAndFinishRecordsASnapshotWithoutCommitting(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.host.script = func(n int) AgentState {
		r.writeSentinel(t, s, "ok", "done")
		return AgentWorking
	}

	out := r.sm.Wait(context.Background(), s, &recObserver{})

	if out.State != StepFailed || out.Reason != "evidence missing: no change since the step started" {
		t.Fatalf("outcome = %+v", out)
	}

	r.repo.Tree = "tree-left"
	r.sm.Finish(s, out)

	if r.count("Repo.CommitAll") != 0 {
		t.Fatal("committed a failed step")
	}
	recs := r.store.Records["run-1"]
	snap, last := recs[len(recs)-2], recs[len(recs)-1]
	if snap.Kind != RecordEvent || snap.Event.Kind != "snapshot" || !reflect.DeepEqual(snap.Event.Fields, map[string]string{"step": "implement-a1", "tree": "tree-left"}) {
		t.Fatalf("snapshot = %+v", snap.Event)
	}
	if last.Kind != RecordStep || last.State != StepFailed || last.Reason != out.Reason || *last.Step != s.Ref.Key {
		t.Fatalf("last = %+v", last)
	}
}

func TestAnAgentCommitFailsTheStep(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.repo.TreeChanges = []string{"a.go"}
	r.host.script = func(n int) AgentState {
		r.repo.SHA = "sha-agent"
		r.writeSentinel(t, s, "ok", "done")
		return AgentWorking
	}

	out := r.sm.Wait(context.Background(), s, &recObserver{})

	if out.State != StepFailed || out.Reason != "step committed before review" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestAnInPrimaryStepWhoseHeadLogFailsNamesTheLogError(t *testing.T) {
	r := newRig(t)
	ref := r.ref(1)
	ref.InPrimary = true
	r.repo.RunExit = 128
	r.repo.RunOutput = "fatal: bad revision\n"
	s, err := r.sm.Spawn(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	r.host.script = func(n int) AgentState {
		r.repo.SHA = "sha-moved"
		r.writeSentinel(t, s, "ok", "done")
		return AgentWorking
	}
	out := r.sm.Wait(context.Background(), s, &recObserver{})
	if out.State != StepFailed || out.Reason != "head log: exit 128: fatal: bad revision" {
		t.Fatalf("outcome = %+v", out)
	}
	var calls []string
	for _, call := range r.shared.Calls() {
		if strings.HasPrefix(call, "Repo.Run") {
			calls = append(calls, call)
		}
	}
	if len(calls) != 1 || !strings.Contains(calls[0], "sha-start..sha-moved") {
		t.Errorf("Repo.Run calls = %v", calls)
	}
}

func TestAWorktreeStepWithAMovedHeadFailsAsCommittedBeforeReviewWithoutReadingTheLog(t *testing.T) {
	r := newRig(t)
	r.repo.RunOutput = "abc1234\ttest\tmaintainer work\n"
	s := r.spawn(t, 1)
	r.host.script = func(n int) AgentState {
		r.repo.SHA = "sha-moved"
		r.writeSentinel(t, s, "ok", "done")
		return AgentWorking
	}
	out := r.sm.Wait(context.Background(), s, &recObserver{})
	if out.State != StepFailed || out.Reason != "step committed before review" || r.count("Repo.Run") != 0 {
		t.Fatalf("outcome = %+v; Repo.Run count = %d", out, r.count("Repo.Run"))
	}
}

func TestAFailedSentinelFailsWithItsReason(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.host.script = func(n int) AgentState {
		r.writeSentinel(t, s, "failed", "tests do not compile")
		return AgentWorking
	}

	out := r.sm.Wait(context.Background(), s, &recObserver{})

	if out.State != StepFailed || out.Reason != "tests do not compile" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestAGoneAgentFailsTheStep(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.host.script = func(n int) AgentState { return AgentGone }

	out := r.sm.Wait(context.Background(), s, &recObserver{})

	if out.State != StepFailed || out.Reason != "agent gone" {
		t.Fatalf("outcome = %+v", out)
	}
}

const nudgeText = "r-loop: no sentinel and no activity for 2m0s. If your work is done, write the sentinel now. If you are blocked, call ask_watchdog and end your turn to wait for its answer, or write a failed sentinel with the reason."

func TestIdleForTheGraceStallsAndNudgesOnceThenWorkingResumes(t *testing.T) {
	r := newRig(t)
	r.sm.Ask = &fakeAskChannel{callLog: callLog{Shared: r.shared}, BaseURL: "http://127.0.0.1:7000/mcp/tok"}
	s := r.spawn(t, 1)
	r.repo.TreeChanges = []string{"a.go"}
	r.host.script = func(n int) AgentState {
		switch {
		case n <= 3:
			return AgentIdle
		case n <= 5:
			return AgentWorking
		}
		r.writeSentinel(t, s, "ok", "done")
		return AgentWorking
	}
	obs := &recObserver{}

	out := r.sm.Wait(context.Background(), s, obs)

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	if obs.stalled != 1 || obs.resumed != 1 {
		t.Fatalf("observer = %+v", obs)
	}
	nudges := slices.DeleteFunc(r.shared.Calls(), func(c string) bool { return !strings.HasPrefix(c, "SessionHost.Prompt") })
	want := []string{
		`SessionHost.Prompt rloop-2kuxv-p3-implement "do phase 3" false 0s`,
		"SessionHost.Prompt rloop-2kuxv-p3-implement \"" + nudgeText + "\" false 0s",
	}
	if !reflect.DeepEqual(nudges, want) {
		t.Fatalf("prompts = %q", nudges)
	}
	if len(r.events("nudge")) != 1 {
		t.Fatalf("nudge events = %+v", r.events("nudge"))
	}
	if got := r.steps(); !reflect.DeepEqual(got, []StepState{StepSpawned, StepRunning, StepStalled, StepRunning}) {
		t.Fatalf("steps = %v", got)
	}
}

func TestStillIdleAfterTheNudgeFailsAsStalledAndLeavesTheSession(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.host.script = func(n int) AgentState { return AgentBlocked }
	obs := &recObserver{}

	out := r.sm.Wait(context.Background(), s, obs)

	if out.State != StepFailed || out.Reason != "stalled: no response to nudge" || !out.Stalled {
		t.Fatalf("outcome = %+v", out)
	}
	if obs.stalled != 1 || len(r.events("nudge")) != 1 {
		t.Fatalf("observer = %+v, nudges = %d", obs, len(r.events("nudge")))
	}
	if r.count("SessionHost.Close") != 0 || r.count("SessionHost.Interrupt") != 0 {
		t.Fatalf("calls = %q", r.shared.Calls())
	}
}

func TestAnAgentHerdrReportsDoneStallsAndIsNudgedLikeAnIdleOne(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.host.script = func(n int) AgentState { return AgentDone }
	obs := &recObserver{}

	out := r.sm.Wait(context.Background(), s, obs)

	if out.State != StepFailed || out.Reason != "stalled: no response to nudge" || !out.Stalled {
		t.Fatalf("outcome = %+v", out)
	}
	if obs.stalled != 1 || len(r.events("nudge")) != 1 {
		t.Fatalf("observer = %+v, nudges = %d", obs, len(r.events("nudge")))
	}
}

func TestTheBackstopFailsAStepThatKeepsWorking(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)

	out := r.sm.Wait(context.Background(), s, &recObserver{})

	if out.State != StepFailed || out.Reason != "backstop 1h0m0s" || out.Stalled {
		t.Fatalf("outcome = %+v", out)
	}
	if r.host.polls != 61 {
		t.Fatalf("polls = %d", r.host.polls)
	}
}

func TestAnOpenQuestionSuspendsTheStallClockAndTheBackstop(t *testing.T) {
	r := newRig(t)
	ref := r.ref(1)
	ref.Kind.Row.Timeout = 5 * time.Minute
	s, _ := r.sm.Spawn(context.Background(), ref)
	s.OpenQuestion.Store(true)
	r.repo.TreeChanges = []string{"a.go"}
	r.host.script = func(n int) AgentState {
		if n == 30 {
			r.writeSentinel(t, s, "ok", "done")
		}
		return AgentIdle
	}
	obs := &recObserver{}

	out := r.sm.Wait(context.Background(), s, obs)

	if out.State != StepOK || obs.stalled != 0 || len(r.events("nudge")) != 0 {
		t.Fatalf("outcome = %+v, observer = %+v", out, obs)
	}
}

func TestReviewingSuspendsTheStallClockAndTheBackstop(t *testing.T) {
	r := newRig(t)
	ref := r.ref(1)
	ref.Kind.Row.Timeout = 5 * time.Minute
	s, _ := r.sm.Spawn(context.Background(), ref)
	s.Reviewing.Store(true)
	r.repo.TreeChanges = []string{"a.go"}
	r.host.script = func(n int) AgentState {
		if n == 30 {
			s.Reviewing.Store(false)
		}
		return AgentWorking
	}

	out := r.sm.Wait(context.Background(), s, &recObserver{})

	if out.State != StepFailed || out.Reason != "backstop 5m0s" {
		t.Fatalf("outcome = %+v", out)
	}
	if r.host.polls != 35 {
		t.Fatalf("polls = %d", r.host.polls)
	}
}

func TestWaitEndsWhenTheContextIsCancelled(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	r.host.script = func(n int) AgentState {
		cancel()
		return AgentWorking
	}

	out := r.sm.Wait(ctx, s, &recObserver{})

	if out.State != StepFailed || out.Reason != "interrupted: context canceled" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestStopInterruptsTheAgentAndNeverClosesTheWorkspace(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)

	if err := r.sm.Stop(s); err != nil {
		t.Fatal(err)
	}

	if r.count("SessionHost.Interrupt rloop-2kuxv-p3-implement") != 1 || r.count("SessionHost.Close") != 0 {
		t.Fatalf("calls = %q", r.shared.Calls())
	}
}

func TestARelativeWorktreeRunsUnderTheRepoRoot(t *testing.T) {
	r := newRig(t)
	ref := r.ref(1)
	ref.Worktree = ".r-loop/wt/phase-3"

	s, err := r.sm.Spawn(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}

	if r.count("Repo.AddWorktree .r-loop/wt/phase-3 r-loop/phase-3 main") != 1 {
		t.Fatalf("calls = %q", r.shared.Calls())
	}
	if s.Dir != "/repo/.r-loop/wt/phase-3" || r.host.Opened[0].CWD != s.Dir {
		t.Fatalf("dir = %q", s.Dir)
	}
}

func TestFinishReportsFailureWhenTheOkStateCannotBeRecorded(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.store.Err = errors.New("disk full")

	fin := r.sm.Finish(s, Outcome{State: StepOK, Session: s})

	if fin.State != StepFailed || fin.Reason != "record: disk full" {
		t.Fatalf("finish = %+v", fin)
	}
}

func TestFinishNamesASnapshotThatCouldNotBeTaken(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	r.repo.Err = errors.New("index locked")

	fin := r.sm.Finish(s, Outcome{State: StepFailed, Reason: "agent gone", Session: s})

	if fin.State != StepFailed || fin.Reason != "agent gone; snapshot: index locked" {
		t.Fatalf("finish = %+v", fin)
	}
}

type failingPromptHost struct {
	*scriptedHost
	prompts int
}

func (h *failingPromptHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	h.prompts++
	if h.prompts > 1 {
		return errors.New("herdr: agent_busy: pane locked")
	}
	return nil
}

func TestAnUndeliveredNudgeFailsTheStepNamingTheHerdrError(t *testing.T) {
	r := newRig(t)
	r.host.script = func(n int) AgentState { return AgentIdle }
	r.sm.Host = &failingPromptHost{scriptedHost: r.host}
	s := r.spawn(t, 1)

	out := r.sm.Wait(context.Background(), s, &recObserver{})

	if out.State != StepFailed || out.Reason != "stalled: nudge not delivered: herdr: agent_busy: pane locked" || !out.Stalled {
		t.Fatalf("outcome = %+v", out)
	}
	if len(r.events("nudge")) != 0 {
		t.Fatal("recorded a nudge that was not delivered")
	}
}

func TestTheMCPConfigIsReadableOnlyByItsOwner(t *testing.T) {
	r := newRig(t)
	r.sm.Ask = &fakeAskChannel{callLog: callLog{Shared: r.shared}, BaseURL: "http://127.0.0.1:7000/mcp/secret"}
	r.sm.Resolve = claudeLikeResolve(r)

	r.spawn(t, 1)

	info, err := os.Stat(filepath.Join(r.runDir, "phase-3", "rloop-2kuxv-p3-implement.mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
}

func TestFinishRecordsNothingForAStepWhoseFailureIsAlreadyRecorded(t *testing.T) {
	r := newRig(t)
	s := r.spawn(t, 1)
	key := s.Ref.Key
	if err := r.store.Append(key.Run, Record{Kind: RecordStep, Step: &key, State: StepFailed, Reason: "watchdog: rewriting the spec"}); err != nil {
		t.Fatal(err)
	}

	r.sm.Finish(s, Outcome{State: StepFailed, Reason: "interrupted: context canceled", Session: s})

	var failed []string
	for _, rec := range r.store.Records[key.Run] {
		if rec.Kind == RecordStep && *rec.Step == key && rec.State == StepFailed {
			failed = append(failed, rec.Reason)
		}
	}
	if !reflect.DeepEqual(failed, []string{"watchdog: rewriting the spec"}) {
		t.Fatalf("failed records %q", failed)
	}
}

func TestALabelledRunNamesTheStepAgentAndWorkspaceWithTheLabel(t *testing.T) {
	r := newRig(t)
	r.sm.Label = "test"

	s := r.spawn(t, 1)

	if s.Agent != "rloop-test-2kuxv-p3-implement" || r.host.Opened[0].Label != "◆ test p3 implement" {
		t.Fatalf("agent = %q, label = %q", s.Agent, r.host.Opened[0].Label)
	}
}

func TestALabelledRetryKeepsTheAttemptSuffix(t *testing.T) {
	r := newRig(t)
	r.sm.Label = "test"

	s := r.spawn(t, 2)

	if s.Agent != "rloop-test-2kuxv-p3-implement-a2" || r.host.Opened[0].Label != "◆ test p3 implement·a2" {
		t.Fatalf("agent = %q, label = %q", s.Agent, r.host.Opened[0].Label)
	}
}

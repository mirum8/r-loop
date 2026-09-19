package askmcp

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"r-loop/internal/core"
)

type memStore struct {
	mu      sync.Mutex
	recs    []core.Record
	steps   map[core.StepKey]core.StepState
	loadErr error
}

func (m *memStore) Create(core.RunMeta) (string, error) { return "run-7", nil }
func (m *memStore) Append(runID string, rec core.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, rec)
	return nil
}
func (m *memStore) Load(runID string) (core.RunState, error) {
	return core.RunState{ID: runID, Steps: m.steps}, m.loadErr
}
func (m *memStore) Current() (string, int, bool) { return "", 0, false }
func (m *memStore) SetCurrent(string, int) error { return nil }
func (m *memStore) ClearCurrent() error          { return nil }
func (m *memStore) Aborted(string) bool          { return false }
func (m *memStore) MarkAbort(string) error       { return nil }
func (m *memStore) Dir(runID string) string      { return "" }
func (m *memStore) records() []core.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]core.Record(nil), m.recs...)
}

func serveWatchdog(t *testing.T, st *memStore) *Server {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run-7")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &Server{RunDir: dir, RunID: "run-7", Store: st}
	if _, err := s.Serve(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	m, _ := res.StructuredContent.(map[string]any)
	return m
}

func TestWatchdogURLHasItsOwnPrivateToken(t *testing.T) {
	s := serveWatchdog(t, &memStore{})

	data, err := os.ReadFile(filepath.Join(s.RunDir, "wd-token"))
	if err != nil {
		t.Fatal(err)
	}
	wd := string(data)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(wd) {
		t.Fatalf("wd token = %q", wd)
	}
	info, _ := os.Stat(filepath.Join(s.RunDir, "wd-token"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	step, _ := os.ReadFile(filepath.Join(s.RunDir, "token"))
	if wd == string(step) {
		t.Fatal("watchdog token equals the step token")
	}
	if !regexp.MustCompile(`^http://127\.0\.0\.1:\d+/mcp/watchdog/` + wd + `$`).MatchString(s.WatchdogURL()) {
		t.Fatalf("watchdog url = %q", s.WatchdogURL())
	}
}

func TestSignalOnTheWatchdogPathReachesItsHandlerAsAWatchdogSignalForTheLatestAttempt(t *testing.T) {
	st := &memStore{steps: map[core.StepKey]core.StepState{
		{Run: "run-7", Phase: 3, Kind: "implement", Attempt: 1}: core.StepFailed,
		{Run: "run-7", Phase: 3, Kind: "implement", Attempt: 2}: core.StepRunning,
		{Run: "run-7", Phase: 4, Kind: "implement", Attempt: 5}: core.StepRunning,
	}}
	s := serveWatchdog(t, st)
	var got core.Signal
	s.Handle(WatchdogHandlers{Signal: func(sig core.Signal) (bool, string) {
		got = sig
		return true, ""
	}})
	cs := connect(t, s.WatchdogURL())

	out := call(t, cs, "signal", map[string]any{"kind": "halt", "step": "phase-3/implement", "reason": "rewriting the plan", "evidence": "git diff"})

	if out["accepted"] != true {
		t.Fatalf("out = %+v", out)
	}
	want := core.Signal{Kind: core.SignalHalt, Source: core.SourceWatchdog, Step: core.StepKey{Run: "run-7", Phase: 3, Kind: "implement", Attempt: 2}, Reason: "rewriting the plan", Evidence: "git diff"}
	if got != want {
		t.Fatalf("signal = %+v", got)
	}
}

func TestPhaseCheckStepMapsToAttemptZero(t *testing.T) {
	st := &memStore{steps: map[core.StepKey]core.StepState{
		{Run: "run-7", Phase: 4, Kind: "check", Attempt: 3}: core.StepRunning,
	}}
	s := serveWatchdog(t, st)
	var got core.Signal
	s.Handle(WatchdogHandlers{Signal: func(sig core.Signal) (bool, string) {
		got = sig
		return false, "phase check may only warn"
	}})

	out := call(t, connect(t, s.WatchdogURL()), "signal", map[string]any{"kind": "warn", "step": "phase-4/check", "reason": "r", "evidence": "e"})

	if out["accepted"] != false || out["reason"] != "phase check may only warn" {
		t.Fatalf("out = %+v", out)
	}
	if got.Step != (core.StepKey{Run: "run-7", Phase: 4, Kind: "check", Attempt: 0}) {
		t.Fatalf("step = %+v", got.Step)
	}
}

func TestAMalformedStepGoesThroughTheSignalHandlerAsAnUnknownStep(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	var got []core.Signal
	s.Handle(WatchdogHandlers{Signal: func(sig core.Signal) (bool, string) {
		got = append(got, sig)
		return false, `step "phase3/implement" is not phase-<N>/<kind>`
	}})

	out := call(t, connect(t, s.WatchdogURL()), "signal", map[string]any{"kind": "halt", "step": "phase3/implement", "reason": "r", "evidence": "e"})

	if out["accepted"] != false || !strings.Contains(out["reason"].(string), "phase-<N>/<kind>") {
		t.Fatalf("out = %+v", out)
	}
	want := core.Signal{Kind: core.SignalHalt, Source: core.SourceWatchdog, Step: core.StepKey{Run: "run-7", Kind: "phase3/implement"}, Reason: "r", Evidence: "e"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("handler got %+v", got)
	}
}

func TestAProposalRefusedWithAnExplanationKeepsTheDecisionExactAndCarriesTheReason(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	s.Handle(WatchdogHandlers{Propose: func(class, command, why string) (string, string) {
		return "refused", "no step to remedy"
	}})

	out := call(t, connect(t, s.WatchdogURL()), "propose_remedy", map[string]any{"class": "deps", "command": "c", "why": "w"})

	if out["decision"] != "refused" || out["reason"] != "no step to remedy" {
		t.Fatalf("out = %+v", out)
	}
}

func TestEachToolDelegatesToItsHandler(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	var calls []string
	s.Handle(WatchdogHandlers{
		Propose: func(class, command, why string) (string, string) {
			calls = append(calls, "propose "+class+"|"+command+"|"+why)
			return "authorised", ""
		},
		Restart: func(step, addendum, provider string) (bool, string) {
			calls = append(calls, "restart "+step+"|"+addendum+"|"+provider)
			return false, "no authorised remedy"
		},
		Answer: func(id, answer, citation string) (bool, string) {
			calls = append(calls, "answer "+id+"|"+answer+"|"+citation)
			return true, ""
		},
	})
	cs := connect(t, s.WatchdogURL())

	if out := call(t, cs, "propose_remedy", map[string]any{"class": "deps", "command": "go mod download", "why": "missing module"}); out["decision"] != "authorised" {
		t.Fatalf("propose = %+v", out)
	}
	if out := call(t, cs, "restart_step", map[string]any{"step": "phase-3/implement", "addendum": "use jsonl", "provider": "codex"}); out["accepted"] != false || out["reason"] != "no authorised remedy" {
		t.Fatalf("restart = %+v", out)
	}
	if out := call(t, cs, "restart_step", map[string]any{"step": "phase-3/plan"}); out["accepted"] != false {
		t.Fatalf("restart = %+v", out)
	}
	if out := call(t, cs, "answer_question", map[string]any{"id": "q2", "answer": "jsonl", "citation": "spec.md:12"}); out["accepted"] != true {
		t.Fatalf("answer = %+v", out)
	}
	want := "propose deps|go mod download|missing module,restart phase-3/implement|use jsonl|codex,restart phase-3/plan||,answer q2|jsonl|spec.md:12"
	if strings.Join(calls, ",") != want {
		t.Fatalf("calls = %v", calls)
	}
}

func TestANilHandlerAnswersNotAvailable(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	cs := connect(t, s.WatchdogURL())

	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"signal", map[string]any{"kind": "halt", "step": "phase-3/implement", "reason": "r", "evidence": "e"}},
		{"restart_step", map[string]any{"step": "phase-3/implement"}},
		{"answer_question", map[string]any{"id": "q1", "answer": "a", "citation": "spec.md:1"}},
	} {
		if out := call(t, cs, c.tool, c.args); out["accepted"] != false || out["reason"] != "not available" {
			t.Fatalf("%s = %+v", c.tool, out)
		}
	}
	if out := call(t, cs, "propose_remedy", map[string]any{"class": "deps", "command": "c", "why": "w"}); out["decision"] != "refused" {
		t.Fatalf("propose = %+v", out)
	}
}

func TestEveryCallIsRecordedBeforeItsHandlerRuns(t *testing.T) {
	st := &memStore{}
	s := serveWatchdog(t, st)
	var seen []core.Record
	s.Handle(WatchdogHandlers{Propose: func(class, command, why string) (string, string) {
		seen = st.records()
		return "refused", ""
	}})
	cs := connect(t, s.WatchdogURL())

	call(t, cs, "propose_remedy", map[string]any{"class": "ports", "command": "kill 4242", "why": "port busy"})
	call(t, cs, "signal", map[string]any{"kind": "warn", "step": "phase-2/plan", "reason": "slow", "evidence": "log"})

	if len(seen) != 1 {
		t.Fatalf("records seen by the handler = %+v", seen)
	}
	ev := seen[0].Event
	if seen[0].Kind != core.RecordEvent || ev == nil || ev.Kind != "watchdog-call" || ev.Fields["tool"] != "propose_remedy" || ev.Fields["command"] != "kill 4242" || ev.Fields["class"] != "ports" || ev.Fields["why"] != "port busy" {
		t.Fatalf("record = %+v", seen[0])
	}
	all := st.records()
	if len(all) != 2 || all[1].Event.Fields["tool"] != "signal" || all[1].Event.Fields["step"] != "phase-2/plan" || all[1].Event.Fields["kind"] != "warn" {
		t.Fatalf("records = %+v", all)
	}
}

func TestWatchdogPathAlsoServesAskUser(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	done := ask(connect(t, s.WatchdogURL()), map[string]any{"question": "Which remedy?"})

	q := next(t, s)
	if q.Text != "Which remedy?" || q.Step.Kind != "watchdog" {
		t.Fatalf("question = %+v", q)
	}
	if err := s.Answer(q.ID, "deps", "person", ""); err != nil {
		t.Fatal(err)
	}
	if got := answerText(t, <-done); got != "deps" {
		t.Fatalf("answer = %q", got)
	}
}

func TestWatchdogToolsOnAStepPathAre404(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	called := false
	s.Handle(WatchdogHandlers{Signal: func(core.Signal) (bool, string) {
		called = true
		return true, ""
	}})
	stepURL := s.StepURL(core.StepKey{Run: "run-7", Phase: 3, Kind: "implement", Attempt: 1})
	cs := connect(t, stepURL)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "ask_user" {
		t.Fatalf("step tools = %+v", tools.Tools)
	}
	for _, name := range []string{"signal", "propose_remedy", "restart_step", "answer_question"} {
		_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"kind": "halt", "step": "phase-3/implement"}})
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "not found") {
			t.Fatalf("%s on a step path: err = %v", name, err)
		}
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
		resp, err := http.Post(stepURL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s on a step path: status = %d", name, resp.StatusCode)
		}
	}
	if called {
		t.Fatal("the handler ran for a step-path call")
	}
}

func TestAWrongWatchdogTokenIs404(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	wrong := strings.Replace(s.WatchdogURL(), "/watchdog/", "/watchdog/0", 1)
	step, _ := os.ReadFile(filepath.Join(s.RunDir, "token"))
	stepToken := strings.TrimSuffix(s.WatchdogURL(), filepath.Base(s.WatchdogURL())) + string(step)

	for _, url := range []string{wrong, stepToken, s.WatchdogURL() + "/extra"} {
		resp, err := http.Post(url, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"signal"}}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status = %d", url, resp.StatusCode)
		}
	}
}

func TestAnOversizedBodyOnAStepPathIsRefusedWithoutBeingReadWhole(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	stepURL := s.StepURL(core.StepKey{Run: "run-7", Phase: 3, Kind: "implement", Attempt: 1})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"` + strings.Repeat("x", 5<<20) + `"}}}`

	resp, err := http.Post(stepURL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestASignalWhoseAttemptCannotBeResolvedIsNotAccepted(t *testing.T) {
	s := serveWatchdog(t, &memStore{loadErr: errors.New("events.jsonl: corrupt")})
	called := false
	s.Handle(WatchdogHandlers{Signal: func(core.Signal) (bool, string) {
		called = true
		return true, ""
	}})

	out := call(t, connect(t, s.WatchdogURL()), "signal", map[string]any{"kind": "halt", "step": "phase-3/implement", "reason": "r", "evidence": "e"})

	if out["accepted"] != false || !strings.Contains(out["reason"].(string), "corrupt") || called {
		t.Fatalf("out = %+v, called = %v", out, called)
	}
}

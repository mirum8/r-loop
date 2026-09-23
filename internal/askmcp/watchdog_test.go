package askmcp

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
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
func (m *memStore) Current() (string, int, bool)   { return "", 0, false }
func (m *memStore) SetCurrent(string, int) error   { return nil }
func (m *memStore) ClearCurrent(string, int) error { return nil }
func (m *memStore) Aborted(string) bool            { return false }
func (m *memStore) MarkAbort(string) error         { return nil }
func (m *memStore) Dir(runID string) string        { return "" }
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
		{Run: "run-7", Phase: "3", Kind: "implement", Attempt: 1}: core.StepFailed,
		{Run: "run-7", Phase: "3", Kind: "implement", Attempt: 2}: core.StepRunning,
		{Run: "run-7", Phase: "4", Kind: "implement", Attempt: 5}: core.StepRunning,
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
	want := core.Signal{Kind: core.SignalHalt, Source: core.SourceWatchdog, Step: core.StepKey{Run: "run-7", Phase: "3", Kind: "implement", Attempt: 2}, Reason: "rewriting the plan", Evidence: "git diff"}
	if got != want {
		t.Fatalf("signal = %+v", got)
	}
}

func TestPhaseCheckStepMapsToAttemptZero(t *testing.T) {
	st := &memStore{steps: map[core.StepKey]core.StepState{
		{Run: "run-7", Phase: "4", Kind: "check", Attempt: 3}: core.StepRunning,
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
	if got.Step != (core.StepKey{Run: "run-7", Phase: "4", Kind: "check", Attempt: 0}) {
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
	s.Handle(WatchdogHandlers{Propose: func(class, command, why, maintainerSaid string) (string, string) {
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
		Propose: func(class, command, why, maintainerSaid string) (string, string) {
			calls = append(calls, "propose "+class+"|"+command+"|"+why)
			return "authorised", ""
		},
		Restart: func(step, addendum, provider, maintainerSaid string) (bool, string) {
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
		{"ask_maintainer", map[string]any{"question": "q"}},
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
	s.Handle(WatchdogHandlers{Propose: func(class, command, why, maintainerSaid string) (string, string) {
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

func TestWatchdogPathListsNoAskTool(t *testing.T) {
	s := serveWatchdog(t, &memStore{})

	tools, err := connect(t, s.WatchdogURL()).ListTools(context.Background(), nil)

	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if want := []string{"answer_question", "ask_maintainer", "propose_remedy", "restart_step", "signal", "submit_gate", "submit_triage"}; !slices.Equal(names, want) {
		t.Fatalf("watchdog tools = %v, want %v", names, want)
	}
}

func TestMaintainerSaidReachesTheHandlersAndIsRecorded(t *testing.T) {
	st := &memStore{}
	s := serveWatchdog(t, st)
	var said []string
	s.Handle(WatchdogHandlers{
		Propose: func(class, command, why, maintainerSaid string) (string, string) {
			said = append(said, maintainerSaid)
			return "authorised", ""
		},
		Restart: func(step, addendum, provider, maintainerSaid string) (bool, string) {
			said = append(said, maintainerSaid)
			return true, ""
		},
	})
	cs := connect(t, s.WatchdogURL())

	call(t, cs, "propose_remedy", map[string]any{"class": "deps", "command": "go mod download", "why": "missing module", "maintainer_said": "yes, download it"})
	call(t, cs, "restart_step", map[string]any{"step": "phase-3/implement", "provider": "gemini", "maintainer_said": "use gemini"})

	if !slices.Equal(said, []string{"yes, download it", "use gemini"}) {
		t.Fatalf("handlers got %q", said)
	}
	recs := st.records()
	if len(recs) != 2 || recs[0].Event.Fields["maintainer_said"] != "yes, download it" || recs[1].Event.Fields["maintainer_said"] != "use gemini" {
		t.Fatalf("records = %+v", recs)
	}
}

func TestWatchdogToolsOnAStepPathAre404(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	called := false
	s.Handle(WatchdogHandlers{Signal: func(core.Signal) (bool, string) {
		called = true
		return true, ""
	}})
	stepURL := s.StepURL(core.StepKey{Run: "run-7", Phase: "3", Kind: "implement", Attempt: 1})
	cs := connect(t, stepURL)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "ask_watchdog" {
		t.Fatalf("step tools = %+v", tools.Tools)
	}
	for _, name := range []string{"signal", "propose_remedy", "restart_step", "answer_question", "ask_maintainer"} {
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
	stepURL := s.StepURL(core.StepKey{Run: "run-7", Phase: "3", Kind: "implement", Attempt: 1})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_watchdog","arguments":{"question":"` + strings.Repeat("x", 5<<20) + `"}}}`

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

func TestAskMaintainerIsRecordedThenDelegatedAndReturnsAtOnce(t *testing.T) {
	st := &memStore{}
	s := serveWatchdog(t, st)
	var seen []core.Record
	var got []string
	s.Handle(WatchdogHandlers{AskMaintainer: func(question string, options []string, recommended string) error {
		seen = st.records()
		got = append(append([]string{question}, options...), recommended)
		return nil
	}})

	out := call(t, connect(t, s.WatchdogURL()), "ask_maintainer", map[string]any{"question": "retry with the helper renamed?", "options": []string{"yes", "no"}, "recommended": "yes"})

	if out["accepted"] != true {
		t.Fatalf("out = %+v", out)
	}
	if !slices.Equal(got, []string{"retry with the helper renamed?", "yes", "no", "yes"}) {
		t.Fatalf("handler got %q", got)
	}
	if len(seen) != 1 {
		t.Fatalf("records seen by the handler = %+v", seen)
	}
	ev := seen[0].Event
	if ev.Kind != "watchdog-call" || ev.Fields["tool"] != "ask_maintainer" || ev.Fields["question"] != "retry with the helper renamed?" || ev.Fields["options"] != "yes; no" || ev.Fields["recommended"] != "yes" {
		t.Fatalf("record = %+v", ev)
	}
}

func TestAskMaintainerWithoutAQuestionIsRefused(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	called := false
	s.Handle(WatchdogHandlers{AskMaintainer: func(string, []string, string) error {
		called = true
		return nil
	}})

	out := call(t, connect(t, s.WatchdogURL()), "ask_maintainer", map[string]any{"question": "  "})

	if out["accepted"] != false || out["reason"] != "question is empty" || called {
		t.Fatalf("out = %+v, called = %v", out, called)
	}
}

func TestEveryOtherWatchdogCallResumesBeforeItsHandlerRuns(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	var calls []string
	s.Handle(WatchdogHandlers{
		Resume: func() error {
			calls = append(calls, "resume")
			return nil
		},
		AskMaintainer: func(string, []string, string) error {
			calls = append(calls, "ask")
			return nil
		},
		Signal: func(core.Signal) (bool, string) {
			calls = append(calls, "signal")
			return true, ""
		},
		Propose: func(string, string, string, string) (string, string) {
			calls = append(calls, "propose")
			return "ask", ""
		},
		Restart: func(string, string, string, string) (bool, string) {
			calls = append(calls, "restart")
			return true, ""
		},
		Answer: func(string, string, string) (bool, string) {
			calls = append(calls, "answer")
			return true, ""
		},
	})
	cs := connect(t, s.WatchdogURL())

	call(t, cs, "ask_maintainer", map[string]any{"question": "q"})
	call(t, cs, "answer_question", map[string]any{"id": "q1", "answer": "a", "citation": "maintainer"})
	call(t, cs, "propose_remedy", map[string]any{"class": "retry", "command": "c", "why": "w"})
	call(t, cs, "restart_step", map[string]any{"step": "phase-3/implement"})
	call(t, cs, "signal", map[string]any{"kind": "warn", "step": "phase-3/implement", "reason": "r", "evidence": "e"})

	want := "ask,resume,answer,resume,propose,resume,restart,resume,signal"
	if strings.Join(calls, ",") != want {
		t.Fatalf("calls = %v", calls)
	}
}

func TestACallWhoseResumeCannotBeRecordedIsNotHandled(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	called := false
	s.Handle(WatchdogHandlers{
		Resume: func() error { return errors.New("record watchdog-resumed: disk full") },
		Answer: func(string, string, string) (bool, string) {
			called = true
			return true, ""
		},
	})

	out := call(t, connect(t, s.WatchdogURL()), "answer_question", map[string]any{"id": "q1", "answer": "a", "citation": "maintainer"})

	if out["accepted"] != false || !strings.Contains(out["reason"].(string), "disk full") || called {
		t.Fatalf("out = %+v, called = %v", out, called)
	}
}

func TestSubmitTriageHandsThePayloadToItsHandlerAndReturnsTheTable(t *testing.T) {
	st := &memStore{}
	s := serveWatchdog(t, st)
	var got []core.Triage
	s.Handle(WatchdogHandlers{SubmitTriage: func(tr core.Triage) (bool, string, string) {
		got = append(got, tr)
		if len(got) == 1 {
			return false, "item 4 has no verdict", ""
		}
		return true, "", "| Group | Phase |"
	}})
	cs := connect(t, s.WatchdogURL())
	args := map[string]any{
		"items":  []any{map[string]any{"id": "3", "title": "t", "verdict": "fix", "category": "bug", "confidence": "high", "root_cause_or_scope": "c", "touches": []any{"a.go"}, "risk": "local"}},
		"groups": []any{map[string]any{"group_id": "G1", "items": []any{"3"}, "subsystem": "store"}},
	}

	refused := call(t, cs, "submit_triage", args)
	accepted := call(t, cs, "submit_triage", args)

	if refused["accepted"] != false || refused["reason"] != "item 4 has no verdict" {
		t.Fatalf("refused = %+v", refused)
	}
	if accepted["accepted"] != true || accepted["table"] != "| Group | Phase |" {
		t.Fatalf("accepted = %+v", accepted)
	}
	want := core.Triage{
		Items:  []core.ItemVerdict{{ID: "3", Title: "t", Verdict: "fix", Category: "bug", Confidence: "high", RootCause: "c", Touches: []string{"a.go"}, Risk: "local"}},
		Groups: []core.Group{{ID: "G1", Items: []string{"3"}, Subsystem: "store"}},
	}
	if len(got) != 2 || !reflect.DeepEqual(got[1], want) {
		t.Fatalf("handler got %+v", got)
	}
	recs := st.records()
	if len(recs) != 2 || recs[0].Event.Fields["tool"] != "submit_triage" || !strings.Contains(recs[0].Event.Fields["triage"], `"group_id":"G1"`) {
		t.Fatalf("records = %+v", recs)
	}
}

func TestSubmitGateHandsTheDecisionToItsHandler(t *testing.T) {
	st := &memStore{}
	s := serveWatchdog(t, st)
	var got core.GateDecision
	s.Handle(WatchdogHandlers{SubmitGate: func(g core.GateDecision) (bool, string, string) {
		got = g
		return true, "", "new table"
	}})

	out := call(t, connect(t, s.WatchdogURL()), "submit_gate", map[string]any{
		"decision": "revise", "drop": []any{"4"}, "split": []any{map[string]any{"group": "G1", "into": []any{[]any{"1"}, []any{"2"}}}},
		"merge": []any{[]any{"G2", "G3"}}, "maintainer_said": "drop 4 and split G1",
	})

	if out["accepted"] != true || out["table"] != "new table" {
		t.Fatalf("out = %+v", out)
	}
	want := core.GateDecision{Decision: "revise", Drop: []string{"4"}, Split: []core.Split{{Group: "G1", Into: [][]string{{"1"}, {"2"}}}}, Merge: [][]string{{"G2", "G3"}}, MaintainerSaid: "drop 4 and split G1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("handler got %+v", got)
	}
	if f := st.records()[0].Event.Fields; f["tool"] != "submit_gate" || f["decision"] != "revise" || f["maintainer_said"] != "drop 4 and split G1" {
		t.Fatalf("record = %+v", f)
	}
}

func TestTriageToolsWithoutAHandlerSayNoTriageIsOpen(t *testing.T) {
	s := serveWatchdog(t, &memStore{})
	cs := connect(t, s.WatchdogURL())

	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"submit_triage", map[string]any{"phases": []any{map[string]any{"phase": "3", "status": "build"}}}},
		{"submit_gate", map[string]any{"decision": "go", "maintainer_said": "go"}},
	} {
		if out := call(t, cs, c.tool, c.args); out["accepted"] != false || out["reason"] != "no triage is open" {
			t.Fatalf("%s = %+v", c.tool, out)
		}
	}
}

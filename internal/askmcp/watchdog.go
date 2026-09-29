package askmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"r-loop/internal/core"
)

const notAvailable = "not available"

var errMalformedStep = errors.New("not phase-<N>/<kind>")

type WatchdogHandlers struct {
	Signal  func(core.Signal) (bool, string)
	Propose func(class, command, why, maintainerSaid string) (string, string)
	Restart func(step, addendum, provider, model, effort, maintainerSaid string) (bool, string)
	Answer  func(id, answer, citation string) (bool, string)

	AnswerDialog   func(id string, keys []string, rule, maintainerSaid string) (string, string)
	ResolveBlocker func(id, action, rule, addendum string, keys []string, provider, model, effort, maintainerSaid string) (string, string)

	AskMaintainer func(question string, options []string, recommended string) error
	Resume        func() error

	SubmitTriage func(core.Triage) (bool, string, string)
	SubmitGate   func(core.GateDecision) (bool, string, string)

	RunStatus func() (core.RunStatusView, error)
	StepInfo  func(step string) (core.StepInfo, bool)
}

type askMaintainerInput struct {
	Question    string   `json:"question"`
	Options     []string `json:"options,omitempty"`
	Recommended string   `json:"recommended,omitempty"`
}

type signalInput struct {
	Kind     string `json:"kind"`
	Step     string `json:"step"`
	Reason   string `json:"reason"`
	Evidence string `json:"evidence"`
}

type proposeInput struct {
	Class          string `json:"class"`
	Command        string `json:"command"`
	Why            string `json:"why"`
	MaintainerSaid string `json:"maintainer_said,omitempty"`
}

type restartInput struct {
	Step           string `json:"step"`
	Addendum       string `json:"addendum,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	MaintainerSaid string `json:"maintainer_said,omitempty"`
}

type answerInput struct {
	ID       string `json:"id"`
	Answer   string `json:"answer"`
	Citation string `json:"citation"`
}

type answerDialogInput struct {
	ID             string   `json:"id"`
	Keys           []string `json:"keys"`
	Rule           string   `json:"rule,omitempty"`
	MaintainerSaid string   `json:"maintainer_said,omitempty"`
}

type resolveBlockerInput struct {
	ID             string   `json:"id"`
	Action         string   `json:"action"`
	Rule           string   `json:"rule,omitempty"`
	Addendum       string   `json:"addendum,omitempty"`
	Keys           []string `json:"keys,omitempty"`
	Provider       string   `json:"provider,omitempty"`
	Model          string   `json:"model,omitempty"`
	Effort         string   `json:"effort,omitempty"`
	MaintainerSaid string   `json:"maintainer_said,omitempty"`
}

type acceptedOutput struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

type triageInput struct {
	Phases []core.PhaseVerdict `json:"phases,omitempty"`
	Items  []core.ItemVerdict  `json:"items,omitempty"`
	Groups []core.Group        `json:"groups,omitempty"`
}

type tableOutput struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
	Table    string `json:"table,omitempty"`
}

func (s *Server) tableResult(out tableOutput) (*mcp.CallToolResult, tableOutput, error) {
	text := "Refused: " + out.Reason
	switch {
	case out.Accepted && out.Table != "":
		text = out.Table + "\n\nPrint this table to the maintainer verbatim, exactly as it is above; never summarise it. When you ask them about it, name " + filepath.Join(s.RunDir, "triage.md") + ", where the table is saved."
	case out.Accepted:
		text = "Accepted."
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
}

const noTriage = "no triage is open"

type stepStatusOutput struct {
	Step        string `json:"step"`
	Attempt     int    `json:"attempt"`
	State       string `json:"state"`
	RetriesLeft int    `json:"retries_left"`
}

type openOutput struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Step        string   `json:"step"`
	Text        string   `json:"text"`
	Options     []string `json:"options,omitempty"`
	Recommended string   `json:"recommended,omitempty"`
}

type fallbackOutput struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

type runStatusOutput struct {
	Reason    string                    `json:"reason,omitempty"`
	Run       string                    `json:"run,omitempty"`
	Status    string                    `json:"status,omitempty"`
	Live      string                    `json:"live,omitempty"`
	Steps     []stepStatusOutput        `json:"steps,omitempty"`
	Landed    []string                  `json:"landed,omitempty"`
	Open      []openOutput              `json:"open,omitempty"`
	Fallbacks map[string]fallbackOutput `json:"fallbacks,omitempty"`
}

type stepInfoInput struct {
	Step string `json:"step"`
}

type stepInfoOutput struct {
	Found    bool   `json:"found"`
	Reason   string `json:"reason,omitempty"`
	Step     string `json:"step,omitempty"`
	Attempt  int    `json:"attempt,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Base     string `json:"base,omitempty"`
	StartSHA string `json:"start_sha,omitempty"`
}

type decisionOutput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

var watchdogTools = map[string]bool{"signal": true, "propose_remedy": true, "restart_step": true, "answer_question": true, "answer_dialog": true, "resolve_blocker": true, "ask_maintainer": true, "submit_triage": true, "submit_gate": true, "run_status": true, "step_info": true}

func (s *Server) Handle(h WatchdogHandlers) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers = h
}

func (s *Server) handlersNow() WatchdogHandlers {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handlers
}

func (s *Server) watchdogServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "r-loop-watchdog", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "signal",
		Description: "Report a step going the wrong way: kind warn or halt, step phase-<N>/<kind>, with the reason and the evidence.",
	}, trackTool(s, func(_ context.Context, _ *mcp.CallToolRequest, in signalInput) (*mcp.CallToolResult, acceptedOutput, error) {
		if err := s.record("signal", in.Step, map[string]string{"kind": in.Kind, "step": in.Step, "reason": in.Reason, "evidence": in.Evidence}); err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		if err := s.resume(); err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		key, err := s.resolve(in.Step)
		if errors.Is(err, errMalformedStep) {
			key, err = core.StepKey{Run: s.RunID, Kind: in.Step}, nil
		}
		if err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		h := s.handlersNow().Signal
		if h == nil {
			return nil, acceptedOutput{Reason: notAvailable}, nil
		}
		ok, reason := h(core.Signal{Kind: core.SignalKind(in.Kind), Source: core.SourceWatchdog, Step: key, Reason: in.Reason, Evidence: in.Evidence})
		return nil, acceptedOutput{Accepted: ok, Reason: reason}, nil
	}))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "propose_remedy",
		Description: "Propose the exact command that would unblock the step, with its class and why. Run it only when the decision is authorised. A class off the allow-list needs maintainer_said: the maintainer's reply, quoted, after you asked them in your own session.",
	}, trackTool(s, func(_ context.Context, _ *mcp.CallToolRequest, in proposeInput) (*mcp.CallToolResult, decisionOutput, error) {
		if err := s.record("propose_remedy", "", map[string]string{"class": in.Class, "command": in.Command, "why": in.Why, "maintainer_said": in.MaintainerSaid}); err != nil {
			return nil, decisionOutput{Decision: "refused", Reason: err.Error()}, nil
		}
		if err := s.resume(); err != nil {
			return nil, decisionOutput{Decision: "refused", Reason: err.Error()}, nil
		}
		h := s.handlersNow().Propose
		if h == nil {
			return nil, decisionOutput{Decision: "refused", Reason: notAvailable}, nil
		}
		decision, reason := h(in.Class, in.Command, in.Why, in.MaintainerSaid)
		return nil, decisionOutput{Decision: decision, Reason: reason}, nil
	}))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "restart_step",
		Description: "Restart a failed or stalled step phase-<N>/<kind> as a new attempt, optionally with an addendum or another provider. A provider that is not the row's fallback needs model, effort and maintainer_said: the maintainer's reply, quoted, after you asked them in your own session.",
	}, trackTool(s, func(_ context.Context, _ *mcp.CallToolRequest, in restartInput) (*mcp.CallToolResult, acceptedOutput, error) {
		if err := s.record("restart_step", in.Step, map[string]string{"step": in.Step, "addendum": in.Addendum, "provider": in.Provider, "model": in.Model, "effort": in.Effort, "maintainer_said": in.MaintainerSaid}); err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		if err := s.resume(); err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		h := s.handlersNow().Restart
		if h == nil {
			return nil, acceptedOutput{Reason: notAvailable}, nil
		}
		ok, reason := h(in.Step, in.Addendum, in.Provider, in.Model, in.Effort, in.MaintainerSaid)
		return nil, acceptedOutput{Accepted: ok, Reason: reason}, nil
	}))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "answer_question",
		Description: "Answer an open step question. Cite a path:line that holds the answer, or maintainer when the maintainer answered it in your own session. An empty or invalid citation is refused and the question stays open.",
	}, trackTool(s, func(_ context.Context, _ *mcp.CallToolRequest, in answerInput) (*mcp.CallToolResult, acceptedOutput, error) {
		if err := s.record("answer_question", "", map[string]string{"id": in.ID, "answer": in.Answer, "citation": in.Citation}); err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		if err := s.resume(); err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		h := s.handlersNow().Answer
		if h == nil {
			return nil, acceptedOutput{Reason: notAvailable}, nil
		}
		ok, reason := h(in.ID, in.Answer, in.Citation)
		return nil, acceptedOutput{Accepted: ok, Reason: reason}, nil
	}))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "answer_dialog",
		Description: "Answer an open dialog in a step's pane with the herdr keys that select your choice, in order: enter, esc, up, down, tab, or a single digit or letter. Give rule: the text of one configured dialog rule, exactly; decline, with keys [\"esc\"] only; or no rule and maintainer_said: the maintainer's reply, quoted, after you asked them in your own session. The driver presses the keys only when the decision is authorised.",
	}, trackTool(s, func(_ context.Context, _ *mcp.CallToolRequest, in answerDialogInput) (*mcp.CallToolResult, decisionOutput, error) {
		if err := s.record("answer_dialog", "", map[string]string{"id": in.ID, "keys": strings.Join(in.Keys, " "), "rule": in.Rule, "maintainer_said": in.MaintainerSaid}); err != nil {
			return nil, decisionOutput{Decision: "refused", Reason: err.Error()}, nil
		}
		if err := s.resume(); err != nil {
			return nil, decisionOutput{Decision: "refused", Reason: err.Error()}, nil
		}
		h := s.handlersNow().AnswerDialog
		if h == nil {
			return nil, decisionOutput{Decision: "refused", Reason: notAvailable}, nil
		}
		decision, reason := h(in.ID, in.Keys, in.Rule, in.MaintainerSaid)
		return nil, decisionOutput{Decision: decision, Reason: reason}, nil
	}))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "resolve_blocker",
		Description: "Clear an open blocker the driver holds the run on, with one of its actions: retry, keys, switch, skip, block or stop. retry takes an addendum for the next attempt; keys takes the herdr keys to press in its pane and rule: the text of one configured dialog rule, exactly; switch takes provider, and model and effort for a provider that is not the row's fallback. Anything not authorised on its own comes back ask: ask the maintainer in your own session, then call again with maintainer_said: their reply, quoted. block and stop are always authorised.",
	}, trackTool(s, func(_ context.Context, _ *mcp.CallToolRequest, in resolveBlockerInput) (*mcp.CallToolResult, decisionOutput, error) {
		if err := s.record("resolve_blocker", "", map[string]string{"id": in.ID, "action": in.Action, "rule": in.Rule, "addendum": in.Addendum, "keys": strings.Join(in.Keys, " "), "provider": in.Provider, "model": in.Model, "effort": in.Effort, "maintainer_said": in.MaintainerSaid}); err != nil {
			return nil, decisionOutput{Decision: "refused", Reason: err.Error()}, nil
		}
		if err := s.resume(); err != nil {
			return nil, decisionOutput{Decision: "refused", Reason: err.Error()}, nil
		}
		h := s.handlersNow().ResolveBlocker
		if h == nil {
			return nil, decisionOutput{Decision: "refused", Reason: notAvailable}, nil
		}
		decision, reason := h(in.ID, in.Action, in.Rule, in.Addendum, in.Keys, in.Provider, in.Model, in.Effort, in.MaintainerSaid)
		return nil, decisionOutput{Decision: decision, Reason: reason}, nil
	}))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ask_maintainer",
		Description: "Call this first whenever you need the maintainer: it shows them that you are waiting for them, with the question, and returns at once. Then ask them in your own session and wait for the reply. Your next call of any other tool except run_status and step_info marks the wait over.",
	}, trackTool(s, func(_ context.Context, _ *mcp.CallToolRequest, in askMaintainerInput) (*mcp.CallToolResult, acceptedOutput, error) {
		if err := s.record("ask_maintainer", "", map[string]string{"question": in.Question, "options": strings.Join(in.Options, "; "), "recommended": in.Recommended}); err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		if strings.TrimSpace(in.Question) == "" {
			return nil, acceptedOutput{Reason: "question is empty"}, nil
		}
		h := s.handlersNow().AskMaintainer
		if h == nil {
			return nil, acceptedOutput{Reason: notAvailable}, nil
		}
		if err := h(in.Question, in.Options, in.Recommended); err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		return nil, acceptedOutput{Accepted: true}, nil
	}))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "submit_triage",
		Description: "Submit your triage before the run starts: one verdict per phase for a plan, or one per item plus the groups for a backlog. A refusal carries the reason; fix it and submit again. An accepted call returns the table the driver built.",
	}, trackTool(s, func(_ context.Context, _ *mcp.CallToolRequest, in triageInput) (*mcp.CallToolResult, tableOutput, error) {
		if err := s.record("submit_triage", "", map[string]string{"triage": marshal(in)}); err != nil {
			return s.tableResult(tableOutput{Reason: err.Error()})
		}
		if err := s.resume(); err != nil {
			return s.tableResult(tableOutput{Reason: err.Error()})
		}
		h := s.handlersNow().SubmitTriage
		if h == nil {
			return s.tableResult(tableOutput{Reason: noTriage})
		}
		ok, reason, table := h(core.Triage{Phases: in.Phases, Items: in.Items, Groups: in.Groups})
		return s.tableResult(tableOutput{Accepted: ok, Reason: reason, Table: table})
	}))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "submit_gate",
		Description: "Submit the maintainer's decision on the table: go, revise (drop, split, merge; returns the new table) or abort, with maintainer_said quoted.",
	}, trackTool(s, func(_ context.Context, _ *mcp.CallToolRequest, in core.GateDecision) (*mcp.CallToolResult, tableOutput, error) {
		if err := s.record("submit_gate", "", map[string]string{"gate": marshal(in), "decision": in.Decision, "maintainer_said": in.MaintainerSaid}); err != nil {
			return s.tableResult(tableOutput{Reason: err.Error()})
		}
		if err := s.resume(); err != nil {
			return s.tableResult(tableOutput{Reason: err.Error()})
		}
		h := s.handlersNow().SubmitGate
		if h == nil {
			return s.tableResult(tableOutput{Reason: noTriage})
		}
		ok, reason, table := h(in)
		return s.tableResult(tableOutput{Accepted: ok, Reason: reason, Table: table})
	}))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "run_status",
		Description: "Read the run as the driver holds it now: the live step, each step's latest attempt with its state and retries left, the landed phases, every open question, dialog and blocker with its id, and each step kind's fallback. Use it to catch up after you lost track. It changes nothing.",
	}, trackTool(s, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, runStatusOutput, error) {
		h := s.handlersNow().RunStatus
		if h == nil {
			return nil, runStatusOutput{Reason: notAvailable}, nil
		}
		view, err := h()
		if err != nil {
			return nil, runStatusOutput{Reason: err.Error()}, nil
		}
		return nil, statusOutput(view), nil
	}))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "step_info",
		Description: "Read a started step phase-<N>/<kind>: its latest attempt, the herdr agent that runs it, its worktree, the base branch and the commit it started from. It changes nothing.",
	}, trackTool(s, func(_ context.Context, _ *mcp.CallToolRequest, in stepInfoInput) (*mcp.CallToolResult, stepInfoOutput, error) {
		if _, _, ok := core.ParseStepName(in.Step); !ok {
			return nil, stepInfoOutput{Reason: fmt.Sprintf("step %q: %v", in.Step, errMalformedStep)}, nil
		}
		h := s.handlersNow().StepInfo
		if h == nil {
			return nil, stepInfoOutput{Reason: notAvailable}, nil
		}
		info, ok := h(in.Step)
		if !ok {
			return nil, stepInfoOutput{Reason: in.Step + " has not started"}, nil
		}
		return nil, stepInfoOutput{Found: true, Step: info.Step, Attempt: info.Attempt, Agent: info.Agent, Worktree: info.Worktree, Base: info.Base, StartSHA: info.StartSHA}, nil
	}))
	return srv
}

func statusOutput(v core.RunStatusView) runStatusOutput {
	out := runStatusOutput{Run: v.Run, Status: string(v.Status), Live: v.Live, Landed: v.Landed}
	for _, st := range v.Steps {
		out.Steps = append(out.Steps, stepStatusOutput{Step: st.Step, Attempt: st.Attempt, State: string(st.State), RetriesLeft: st.RetriesLeft})
	}
	for _, q := range v.Open {
		kind := q.Kind
		if kind == "" {
			kind = "question"
		}
		out.Open = append(out.Open, openOutput{ID: q.ID, Kind: kind, Step: stepName(q.Step), Text: q.Text, Options: q.Options, Recommended: q.Recommended})
	}
	if len(v.Fallbacks) > 0 {
		out.Fallbacks = map[string]fallbackOutput{}
		for kind, fb := range v.Fallbacks {
			if fb.Provider == "" {
				continue
			}
			out.Fallbacks[kind] = fallbackOutput{Provider: fb.Provider, Model: fb.Model, Effort: fb.Effort}
		}
	}
	return out
}

func stepName(key core.StepKey) string {
	if key.Phase == "" {
		return key.Kind
	}
	return fmt.Sprintf("phase-%s/%s", key.Phase, key.Kind)
}

func marshal(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func (s *Server) resume() error {
	if h := s.handlersNow().Resume; h != nil {
		return h()
	}
	return nil
}

func (s *Server) record(tool, step string, fields map[string]string) error {
	if s.Store == nil {
		return nil
	}
	fields["tool"] = tool
	now := time.Now()
	ev := &core.Event{At: now, Kind: "watchdog-call", Step: step, Fields: fields}
	if err := s.Store.Append(s.RunID, core.Record{Kind: core.RecordEvent, At: now, Event: ev}); err != nil {
		return fmt.Errorf("record %s: %w", tool, err)
	}
	return nil
}

func (s *Server) resolve(step string) (core.StepKey, error) {
	phase, kind, ok := core.ParseStepName(step)
	if !ok {
		return core.StepKey{}, fmt.Errorf("step %q: %w", step, errMalformedStep)
	}
	key := core.StepKey{Run: s.RunID, Phase: phase, Kind: kind}
	if key.Kind == "check" || s.Store == nil {
		return key, nil
	}
	if g, ok := s.Store.(*core.RecordGuard); ok {
		if n, following := g.Latest(key.Phase, key.Kind); following {
			key.Attempt = n
			return key, nil
		}
	}
	st, err := s.Store.Load(s.RunID)
	if err != nil {
		return core.StepKey{}, fmt.Errorf("resolve %s: %w", step, err)
	}
	for k := range st.Steps {
		if k.Phase == key.Phase && k.Kind == key.Kind && k.Attempt > key.Attempt {
			key.Attempt = k.Attempt
		}
	}
	return key, nil
}

func callsWatchdogTool(body []byte) bool {
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &msg) != nil {
		return false
	}
	return msg.Method == "tools/call" && watchdogTools[msg.Params.Name]
}

package askmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"r-loop/internal/core"
)

const notAvailable = "not available"

type WatchdogHandlers struct {
	Signal  func(core.Signal) (bool, string)
	Propose func(class, command, why string) string
	Restart func(step, addendum, provider string) (bool, string)
	Answer  func(id, answer, citation string) (bool, string)
}

type signalInput struct {
	Kind     string `json:"kind"`
	Step     string `json:"step"`
	Reason   string `json:"reason"`
	Evidence string `json:"evidence"`
}

type proposeInput struct {
	Class   string `json:"class"`
	Command string `json:"command"`
	Why     string `json:"why"`
}

type restartInput struct {
	Step     string `json:"step"`
	Addendum string `json:"addendum,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type answerInput struct {
	ID       string `json:"id"`
	Answer   string `json:"answer"`
	Citation string `json:"citation"`
}

type acceptedOutput struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

type decisionOutput struct {
	Decision string `json:"decision"`
}

var watchdogTools = map[string]bool{"signal": true, "propose_remedy": true, "restart_step": true, "answer_question": true}

var stepName = regexp.MustCompile(`^phase-(\d+)/([^/\s]+)$`)

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
	s.addAskUser(srv, core.StepKey{Run: s.RunID, Kind: "watchdog"})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "signal",
		Description: "Report a step going the wrong way: kind warn or halt, step phase-<N>/<kind>, with the reason and the evidence.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in signalInput) (*mcp.CallToolResult, acceptedOutput, error) {
		if err := s.record("signal", in.Step, map[string]string{"kind": in.Kind, "step": in.Step, "reason": in.Reason, "evidence": in.Evidence}); err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		key, err := s.resolve(in.Step)
		if err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		h := s.handlersNow().Signal
		if h == nil {
			return nil, acceptedOutput{Reason: notAvailable}, nil
		}
		ok, reason := h(core.Signal{Kind: core.SignalKind(in.Kind), Source: core.SourceWatchdog, Step: key, Reason: in.Reason, Evidence: in.Evidence})
		return nil, acceptedOutput{Accepted: ok, Reason: reason}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "propose_remedy",
		Description: "Propose the exact command that would unblock the step, with its class and why. Run it only when the decision is authorised.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in proposeInput) (*mcp.CallToolResult, decisionOutput, error) {
		if err := s.record("propose_remedy", "", map[string]string{"class": in.Class, "command": in.Command, "why": in.Why}); err != nil {
			return nil, decisionOutput{Decision: "refused"}, nil
		}
		h := s.handlersNow().Propose
		if h == nil {
			return nil, decisionOutput{Decision: "refused"}, nil
		}
		return nil, decisionOutput{Decision: h(in.Class, in.Command, in.Why)}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "restart_step",
		Description: "Restart a failed or stalled step phase-<N>/<kind> as a new attempt, optionally with an addendum or another provider.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in restartInput) (*mcp.CallToolResult, acceptedOutput, error) {
		if err := s.record("restart_step", in.Step, map[string]string{"step": in.Step, "addendum": in.Addendum, "provider": in.Provider}); err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		h := s.handlersNow().Restart
		if h == nil {
			return nil, acceptedOutput{Reason: notAvailable}, nil
		}
		ok, reason := h(in.Step, in.Addendum, in.Provider)
		return nil, acceptedOutput{Accepted: ok, Reason: reason}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "answer_question",
		Description: "Answer an open question with a path:line citation; an empty citation escalates it to the person.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in answerInput) (*mcp.CallToolResult, acceptedOutput, error) {
		if err := s.record("answer_question", "", map[string]string{"id": in.ID, "answer": in.Answer, "citation": in.Citation}); err != nil {
			return nil, acceptedOutput{Reason: err.Error()}, nil
		}
		h := s.handlersNow().Answer
		if h == nil {
			return nil, acceptedOutput{Reason: notAvailable}, nil
		}
		ok, reason := h(in.ID, in.Answer, in.Citation)
		return nil, acceptedOutput{Accepted: ok, Reason: reason}, nil
	})
	return srv
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
	m := stepName.FindStringSubmatch(step)
	if m == nil {
		return core.StepKey{}, fmt.Errorf("step %q is not phase-<N>/<kind>", step)
	}
	phase, err := strconv.Atoi(m[1])
	if err != nil {
		return core.StepKey{}, fmt.Errorf("step %q is not phase-<N>/<kind>", step)
	}
	key := core.StepKey{Run: s.RunID, Phase: phase, Kind: m[2]}
	if key.Kind == "check" || s.Store == nil {
		return key, nil
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

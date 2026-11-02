package sessionhost

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"r-loop/internal/core"
)

type fakeHost struct {
	name   string
	agents map[string]string
	calls  []string
}

func (f *fakeHost) record(call string) { f.calls = append(f.calls, call) }

func (f *fakeHost) known(agent string) error {
	if _, ok := f.agents[agent]; ok {
		return nil
	}
	return fmt.Errorf("%s: agent %s: %w", f.name, agent, core.ErrNotFound)
}

func (f *fakeHost) Reachable() error { f.record("Reachable"); return nil }
func (f *fakeHost) Open(core.OpenSpec) (core.Workspace, error) {
	f.record("Open")
	return core.Workspace{}, core.ErrNotFound
}
func (f *fakeHost) Start(string, string, string, []string) (core.Agent, error) {
	f.record("Start")
	return core.Agent{}, core.ErrNotFound
}
func (f *fakeHost) Prompt(agent, _ string, _ bool, _ time.Duration) error {
	f.record("Prompt " + agent)
	return f.known(agent)
}
func (f *fakeHost) Submit(agent, _ string) error { f.record("Submit " + agent); return f.known(agent) }
func (f *fakeHost) State(agent string) (core.AgentState, error) {
	f.record("State " + agent)
	if f.known(agent) != nil {
		return core.AgentGone, nil
	}
	return core.AgentWorking, nil
}
func (f *fakeHost) StateSeq(agent string) (core.AgentState, int64, error) {
	f.record("StateSeq " + agent)
	if f.known(agent) != nil {
		return core.AgentGone, 0, nil
	}
	return core.AgentIdle, 7, nil
}
func (f *fakeHost) AgentPane(agent string) (string, error) {
	f.record("AgentPane " + agent)
	return f.agents[agent], nil
}
func (f *fakeHost) Screen(agent string) (string, error) {
	f.record("Screen " + agent)
	return f.name + " screen", f.known(agent)
}
func (f *fakeHost) Read(agent string, lines int) (string, error) {
	f.record("Read " + agent)
	return fmt.Sprintf("%s history %d", f.name, lines), f.known(agent)
}
func (f *fakeHost) SendKeys(agent string, _ ...string) error {
	f.record("SendKeys " + agent)
	return f.known(agent)
}
func (f *fakeHost) Interrupt(agent string) error {
	f.record("Interrupt " + agent)
	return f.known(agent)
}
func (f *fakeHost) Close(ws string) error { f.record("Close " + ws); return f.known(ws) }
func (f *fakeHost) Tag(ws string, _ map[string]string) error {
	f.record("Tag " + ws)
	return f.known(ws)
}
func (f *fakeHost) Split(string, string, string, float64, map[string]string) (string, error) {
	f.record("Split")
	return "", core.ErrNotFound
}
func (f *fakeHost) PaneInfo(pane string) (core.PaneInfo, error) {
	f.record("PaneInfo " + pane)
	return core.PaneInfo{Workspace: f.name}, f.known(pane)
}
func (f *fakeHost) OpenTab(string, core.OpenSpec) (string, error) {
	f.record("OpenTab")
	return "", core.ErrNotFound
}
func (f *fakeHost) ClosePane(pane string) error { f.record("ClosePane " + pane); return f.known(pane) }

func newRouter(primary, alt *fakeHost, altUp bool) (*router, *int) {
	probes := 0
	return &router{primary: primary, probe: func() (core.SessionHost, bool) {
		probes++
		return alt, altUp
	}}, &probes
}

func TestRouterFallsThroughOnEachNotFoundShape(t *testing.T) {
	primary := &fakeHost{name: "herdr", agents: map[string]string{}}
	alt := &fakeHost{name: "app", agents: map[string]string{"a": "pn1", "ws": "", "pn1": ""}}
	r, probes := newRouter(primary, alt, true)

	if state, err := r.State("a"); err != nil || state != core.AgentWorking {
		t.Fatalf("State = %v, %v", state, err)
	}
	if state, seq, err := r.StateSeq("a"); err != nil || state != core.AgentIdle || seq != 7 {
		t.Fatalf("StateSeq = %v %d %v", state, seq, err)
	}
	if pane, err := r.AgentPane("a"); err != nil || pane != "pn1" {
		t.Fatalf("AgentPane = %q, %v", pane, err)
	}
	if screen, err := r.Screen("a"); err != nil || screen != "app screen" {
		t.Fatalf("Screen = %q, %v", screen, err)
	}
	if history, err := r.Read("a", 200); err != nil || history != "app history 200" {
		t.Fatalf("Read = %q, %v", history, err)
	}
	if info, err := r.PaneInfo("pn1"); err != nil || info.Workspace != "app" {
		t.Fatalf("PaneInfo = %+v, %v", info, err)
	}
	for name, call := range map[string]func() error{
		"SendKeys":  func() error { return r.SendKeys("a", "enter") },
		"Interrupt": func() error { return r.Interrupt("a") },
		"Prompt":    func() error { return r.Prompt("a", "hi", false, 0) },
		"Submit":    func() error { return r.Submit("a", "hi") },
		"Close":     func() error { return r.Close("ws") },
		"Tag":       func() error { return r.Tag("ws", nil) },
		"ClosePane": func() error { return r.ClosePane("pn1") },
		"PromptCtx": func() error { return r.PromptContext(t.Context(), "a", "hi", false, 0) },
	} {
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if *probes != 1 {
		t.Fatalf("other host probed %d times, want once", *probes)
	}
}

func TestRouterAnswersFromThePrimaryWhenItKnowsTheAgent(t *testing.T) {
	primary := &fakeHost{name: "herdr", agents: map[string]string{"a": "w1:p1"}}
	alt := &fakeHost{name: "app", agents: map[string]string{"a": "pn1"}}
	r, probes := newRouter(primary, alt, true)

	pane, err := r.AgentPane("a")
	_ = r.Interrupt("a")

	if err != nil || pane != "w1:p1" || len(alt.calls) != 0 || *probes != 0 {
		t.Fatalf("pane=%q err=%v alt=%v probes=%d", pane, err, alt.calls, *probes)
	}
}

func TestRouterKeepsThePrimaryAnswerWhenTheOtherHostIsDown(t *testing.T) {
	primary := &fakeHost{name: "herdr", agents: map[string]string{}}
	alt := &fakeHost{name: "app", agents: map[string]string{"a": "pn1"}}
	r, probes := newRouter(primary, alt, false)

	state, err := r.State("a")
	ierr := r.Interrupt("a")
	_, _ = r.AgentPane("a")

	if err != nil || state != core.AgentGone || !errors.Is(ierr, core.ErrNotFound) || len(alt.calls) != 0 || *probes != 1 {
		t.Fatalf("state=%v err=%v ierr=%v alt=%v probes=%d", state, err, ierr, alt.calls, *probes)
	}
}

func TestRouterDoesNotFallThroughOnOtherErrors(t *testing.T) {
	primary := &failingHost{fakeHost{name: "herdr"}}
	alt := &fakeHost{name: "app", agents: map[string]string{"a": "pn1"}}
	r := &router{primary: primary, probe: func() (core.SessionHost, bool) { return alt, true }}

	if err := r.Interrupt("a"); err == nil || len(alt.calls) != 0 {
		t.Fatalf("err=%v alt=%v", err, alt.calls)
	}
}

type failingHost struct{ fakeHost }

func (f *failingHost) Interrupt(string) error { return errors.New("herdr: timed out") }

func TestRouterNeverSendsCreatingCallsToTheOtherHost(t *testing.T) {
	primary := &fakeHost{name: "herdr"}
	alt := &fakeHost{name: "app"}
	r, probes := newRouter(primary, alt, true)

	_ = r.Reachable()
	_, _ = r.Open(core.OpenSpec{})
	_, _ = r.OpenTab("w", core.OpenSpec{})
	_, _ = r.Split("p", "right", "/", 0.5, nil)
	_, _ = r.Start("p", "a", "claude", nil)

	if len(primary.calls) != 5 || len(alt.calls) != 0 || *probes != 0 {
		t.Fatalf("primary=%v alt=%v probes=%d", primary.calls, alt.calls, *probes)
	}
}

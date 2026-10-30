package sessionhost

import (
	"context"
	"errors"
	"sync"
	"time"

	"r-loop/internal/core"
)

type router struct {
	primary core.SessionHost
	probe   func() (core.SessionHost, bool)

	once  sync.Once
	alt   core.SessionHost
	altOK bool
}

func (r *router) other() (core.SessionHost, bool) {
	r.once.Do(func() { r.alt, r.altOK = r.probe() })
	return r.alt, r.altOK
}

func notFound(err error) bool {
	return errors.Is(err, core.ErrNotFound)
}

func fallErr(r *router, call func(core.SessionHost) error) error {
	err := call(r.primary)
	if !notFound(err) {
		return err
	}
	if alt, ok := r.other(); ok {
		return call(alt)
	}
	return err
}

func (r *router) Reachable() error { return r.primary.Reachable() }

func (r *router) Open(spec core.OpenSpec) (core.Workspace, error) { return r.primary.Open(spec) }

func (r *router) OpenTab(workspace string, spec core.OpenSpec) (string, error) {
	return r.primary.OpenTab(workspace, spec)
}

func (r *router) Split(pane, direction, cwd string, ratio float64, env map[string]string) (string, error) {
	return r.primary.Split(pane, direction, cwd, ratio, env)
}

func (r *router) Start(pane, name, kind string, args []string) (core.Agent, error) {
	return r.primary.Start(pane, name, kind, args)
}

func gone(state core.AgentState, err error) bool {
	return (err == nil && state == core.AgentGone) || notFound(err)
}

func (r *router) State(agent string) (core.AgentState, error) {
	state, err := r.primary.State(agent)
	if !gone(state, err) {
		return state, err
	}
	if alt, ok := r.other(); ok {
		return alt.State(agent)
	}
	return state, err
}

func (r *router) StateSeq(agent string) (core.AgentState, int64, error) {
	state, seq, err := r.primary.StateSeq(agent)
	if !gone(state, err) {
		return state, seq, err
	}
	if alt, ok := r.other(); ok {
		return alt.StateSeq(agent)
	}
	return state, seq, err
}

func (r *router) AgentPane(agent string) (string, error) {
	pane, err := r.primary.AgentPane(agent)
	if (err == nil && pane != "") || (err != nil && !notFound(err)) {
		return pane, err
	}
	if alt, ok := r.other(); ok {
		return alt.AgentPane(agent)
	}
	return pane, err
}

func (r *router) Screen(agent string) (string, error) {
	var out string
	err := fallErr(r, func(h core.SessionHost) (err error) { out, err = h.Screen(agent); return })
	return out, err
}

func (r *router) SendKeys(agent string, keys ...string) error {
	return fallErr(r, func(h core.SessionHost) error { return h.SendKeys(agent, keys...) })
}

func (r *router) Interrupt(agent string) error {
	return fallErr(r, func(h core.SessionHost) error { return h.Interrupt(agent) })
}

func (r *router) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	return fallErr(r, func(h core.SessionHost) error { return h.Prompt(agent, text, wait, timeout) })
}

func (r *router) PromptContext(ctx context.Context, agent, text string, wait bool, timeout time.Duration) error {
	return fallErr(r, func(h core.SessionHost) error {
		if p, ok := h.(interface {
			PromptContext(context.Context, string, string, bool, time.Duration) error
		}); ok {
			return p.PromptContext(ctx, agent, text, wait, timeout)
		}
		return h.Prompt(agent, text, wait, timeout)
	})
}

func (r *router) Submit(agent, text string) error {
	return fallErr(r, func(h core.SessionHost) error { return h.Submit(agent, text) })
}

func (r *router) ClosePane(pane string) error {
	return fallErr(r, func(h core.SessionHost) error { return h.ClosePane(pane) })
}

func (r *router) Close(workspaceID string) error {
	return fallErr(r, func(h core.SessionHost) error { return h.Close(workspaceID) })
}

func (r *router) PaneInfo(pane string) (core.PaneInfo, error) {
	var out core.PaneInfo
	err := fallErr(r, func(h core.SessionHost) (err error) { out, err = h.PaneInfo(pane); return })
	return out, err
}

func (r *router) Tag(workspaceID string, tokens map[string]string) error {
	return fallErr(r, func(h core.SessionHost) error { return h.Tag(workspaceID, tokens) })
}

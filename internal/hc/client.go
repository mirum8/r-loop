package hc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"r-loop/internal/core"
)

var (
	callTimeout     = 30 * time.Second
	submitTimeout   = 10 * time.Second
	paneBusyBudget  = 20 * time.Second
	paneBusyBackoff = 250 * time.Millisecond
	trustSettle     = 1500 * time.Millisecond
)

type Client struct {
	HC, App string
}

type Error struct {
	Kind, Message string
}

func (e Error) Error() string { return "hc: " + e.Kind + ": " + e.Message }

func (e Error) Unwrap() error {
	switch e.Kind {
	case "not_found":
		return core.ErrNotFound
	case "blocked":
		return core.ErrAgentBlocked
	}
	return nil
}

func withoutTerminalID(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "HC_TERMINAL_ID=") {
			out = append(out, e)
		}
	}
	return out
}

func run(parent context.Context, limit time.Duration, name, bin string, args ...string) ([]byte, []byte, int, error) {
	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = withoutTerminalID(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, nil, 0, fmt.Errorf("%s %s: timed out after %s", name, strings.Join(args[:min(len(args), 3)], " "), limit)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil, nil, 0, ctx.Err()
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return nil, nil, 0, err
	}
	return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
}

func (c Client) hc(ctx context.Context, limit time.Duration, out any, args ...string) error {
	stdout, stderr, exit, err := run(ctx, limit, "hc", c.HC, args...)
	if err != nil {
		return err
	}
	if exit != 0 {
		var body struct {
			Error *struct {
				Kind    string `json:"kind"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(stderr, &body) == nil && body.Error != nil {
			return Error{Kind: body.Error.Kind, Message: body.Error.Message}
		}
		kind := "unknown"
		switch exit {
		case 2:
			kind = "usage"
		case 3:
			kind = "not_found"
		case 9:
			kind = "blocked"
		}
		return Error{Kind: kind, Message: strings.TrimSpace(string(stderr))}
	}
	if err := json.Unmarshal(stdout, out); err != nil {
		return fmt.Errorf("hc %s: %w", args[0], err)
	}
	return nil
}

func (c Client) app(out any, args ...string) error {
	stdout, stderr, exit, err := run(context.Background(), callTimeout, "herdr-app", c.App, args...)
	if err != nil {
		return err
	}
	if exit != 0 {
		var body struct {
			Error *string `json:"error"`
		}
		if json.Unmarshal(stdout, &body) == nil && body.Error != nil {
			return fmt.Errorf("herdr-app: %s", *body.Error)
		}
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(stdout))
		}
		return fmt.Errorf("herdr-app: %s", msg)
	}
	if err := json.Unmarshal(stdout, out); err != nil {
		return fmt.Errorf("herdr-app %s: %w", args[0], err)
	}
	return nil
}

type focus struct {
	Space string `json:"space"`
	Tab   string `json:"tab"`
	Pane  string `json:"focused_pane"`
}

type layout struct {
	Spaces []struct {
		ID   string `json:"id"`
		Tabs []struct {
			Terminals []struct {
				Terminal string `json:"terminal"`
				Pane     string `json:"pane"`
			} `json:"terminals"`
		} `json:"tabs"`
	} `json:"spaces"`
}

func (c Client) focus() (focus, error) {
	var f focus
	err := c.app(&f, "status")
	return f, err
}

func (c Client) layout() (layout, error) {
	var l layout
	err := c.app(&l, "ls")
	return l, err
}

func (l layout) pane(p string) (space, terminal string, ok bool) {
	for _, s := range l.Spaces {
		for _, tab := range s.Tabs {
			for _, t := range tab.Terminals {
				if t.Pane == p {
					return s.ID, t.Terminal, true
				}
			}
		}
	}
	return "", "", false
}

func (l layout) terminal(id string) (pane string, ok bool) {
	for _, s := range l.Spaces {
		for _, tab := range s.Tabs {
			for _, t := range tab.Terminals {
				if t.Terminal == id {
					return t.Pane, true
				}
			}
		}
	}
	return "", false
}

func (l layout) space(id string) (terminals []string, ok bool) {
	for _, s := range l.Spaces {
		if s.ID != id {
			continue
		}
		terminals = []string{}
		for _, tab := range s.Tabs {
			for _, t := range tab.Terminals {
				terminals = append(terminals, t.Terminal)
			}
		}
		return terminals, true
	}
	return nil, false
}

func paneNotFound(p string) error {
	return fmt.Errorf("herdr-app: pane %s: %w", p, core.ErrNotFound)
}

func spaceNotFound(id string) error {
	return fmt.Errorf("herdr-app: space %s: %w", id, core.ErrNotFound)
}

func terminalNotFound(t string) error {
	return fmt.Errorf("herdr-app: terminal %s: %w", t, core.ErrNotFound)
}

func shellArgv(cwd string, env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	argv := []string{"env"}
	for _, k := range keys {
		argv = append(argv, k+"="+env[k])
	}
	argv = append(argv, "/bin/sh", "-c", `cd "$1" && exec "$SHELL" -l`, "sh", cwd)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(argv)
	return strings.TrimSuffix(buf.String(), "\n")
}

func (c Client) restore(f focus) error {
	var out struct{}
	for _, args := range [][]string{
		{"do", "space.select", "--id", f.Space},
		{"do", "tab.select", "--id", f.Tab},
		{"do", "pane.focus", "--pane", f.Pane},
	} {
		if err := c.app(&out, args...); err != nil {
			return fmt.Errorf("restore focus: %w", err)
		}
	}
	return nil
}

func (c Client) Reachable() error {
	var out struct{}
	if err := c.hc(context.Background(), callTimeout, &out, "server", "status", "--json"); err != nil {
		return err
	}
	_, err := c.focus()
	return err
}

type created struct {
	Space    string `json:"space"`
	Terminal string `json:"terminal"`
}

func (c Client) create(args ...string) (created, string, error) {
	f, err := c.focus()
	if err != nil {
		return created{}, "", err
	}
	var out created
	if err := c.app(&out, args...); err != nil {
		return created{}, "", err
	}
	if err := c.restore(f); err != nil {
		return created{}, "", err
	}
	l, err := c.layout()
	if err != nil {
		return created{}, "", err
	}
	pane, ok := l.terminal(out.Terminal)
	if !ok {
		return created{}, "", terminalNotFound(out.Terminal)
	}
	return out, pane, nil
}

func (c Client) Open(spec core.OpenSpec) (core.Workspace, error) {
	out, pane, err := c.create("do", "space.new", "--name", spec.Label, "--folder", spec.CWD, "--argv", shellArgv(spec.CWD, spec.Env))
	if err != nil {
		return core.Workspace{}, err
	}
	return core.Workspace{ID: out.Space, RootPane: pane}, nil
}

func (c Client) OpenTab(workspace string, spec core.OpenSpec) (string, error) {
	l, err := c.layout()
	if err != nil {
		return "", err
	}
	if _, ok := l.space(workspace); !ok {
		return "", spaceNotFound(workspace)
	}
	_, pane, err := c.create("do", "tab.new", "--name", spec.Label, "--space", workspace, "--argv", shellArgv(spec.CWD, spec.Env))
	return pane, err
}

func (c Client) Split(pane, direction, cwd string, ratio float64, env map[string]string) (string, error) {
	if pane == "" {
		f, err := c.focus()
		if err != nil {
			return "", err
		}
		pane = f.Pane
	} else {
		l, err := c.layout()
		if err != nil {
			return "", err
		}
		if _, _, ok := l.pane(pane); !ok {
			return "", paneNotFound(pane)
		}
	}
	var out struct {
		Pane string `json:"pane"`
	}
	if err := c.app(&out, "do", "pane.split", "--direction", direction, "--pane", pane, "--argv", shellArgv(cwd, env)); err != nil {
		return "", err
	}
	return out.Pane, nil
}

func (c Client) ClosePane(pane string) error {
	l, err := c.layout()
	if err != nil {
		return err
	}
	if _, _, ok := l.pane(pane); !ok {
		return paneNotFound(pane)
	}
	var out struct{}
	return c.app(&out, "do", "pane.close", "--pane", pane)
}

func (c Client) Close(id string) error {
	l, err := c.layout()
	if err != nil {
		return err
	}
	if _, ok := l.space(id); !ok {
		return spaceNotFound(id)
	}
	var out struct{}
	return c.app(&out, "do", "space.close", "--id", id, "--confirmed", "true")
}

func (c Client) PaneInfo(pane string) (core.PaneInfo, error) {
	l, err := c.layout()
	if err != nil {
		return core.PaneInfo{}, err
	}
	space, t, ok := l.pane(pane)
	if !ok {
		return core.PaneInfo{}, paneNotFound(pane)
	}
	term, err := c.get(t)
	if err != nil {
		return core.PaneInfo{}, err
	}
	return core.PaneInfo{Workspace: space, Cols: term.Cols, Rows: term.Rows}, nil
}

type terminal struct {
	State      string            `json:"state"`
	Labels     map[string]string `json:"labels"`
	Foreground *struct {
		Argv []string `json:"argv"`
	} `json:"foreground"`
	Status *struct {
		State string `json:"state"`
	} `json:"status"`
	StatusMS int64 `json:"status_ms"`
	Cols     int   `json:"cols"`
	Rows     int   `json:"rows"`
}

func (c Client) get(t string) (terminal, error) {
	var out terminal
	err := c.hc(context.Background(), callTimeout, &out, "get", "--json", t)
	return out, err
}

func (c Client) find(ctx context.Context, agent string) (string, error) {
	var out struct {
		Terminals []struct {
			Address string `json:"address"`
		} `json:"terminals"`
	}
	if err := c.hc(ctx, callTimeout, &out, "ls", "--json", "--label", "rloop.agent="+agent); err != nil {
		return "", err
	}
	switch len(out.Terminals) {
	case 0:
		return "", Error{Kind: "not_found", Message: "no live terminal labelled rloop.agent=" + agent}
	case 1:
		return out.Terminals[0].Address, nil
	}
	addresses := make([]string, 0, len(out.Terminals))
	for _, t := range out.Terminals {
		addresses = append(addresses, t.Address)
	}
	return "", fmt.Errorf("hc: agent %s is on more than one terminal: %s", agent, strings.Join(addresses, ", "))
}

func execLine(kind string, args []string) string {
	words := make([]string, 0, len(args)+1)
	for _, w := range append([]string{kind}, args...) {
		words = append(words, "'"+strings.ReplaceAll(w, "'", `'\''`)+"'")
	}
	return "exec " + strings.Join(words, " ")
}

func (c Client) Start(pane, name, kind string, args []string) (core.Agent, error) {
	l, err := c.layout()
	if err != nil {
		return core.Agent{}, err
	}
	_, t, ok := l.pane(pane)
	if !ok {
		return core.Agent{}, paneNotFound(pane)
	}
	if _, err := c.find(context.Background(), name); err == nil {
		return core.Agent{}, fmt.Errorf("hc: agent %s already exists", name)
	} else if !errors.Is(err, core.ErrNotFound) {
		return core.Agent{}, err
	}
	var out struct{}
	if err := c.hc(context.Background(), callTimeout, &out, "label", "--json", t, "rloop.agent="+name); err != nil {
		return core.Agent{}, err
	}
	base, err := c.get(t)
	if err != nil {
		return core.Agent{}, err
	}
	if err := c.hc(context.Background(), callTimeout, &out, "send", "--json", "--force", t, execLine(kind, args)); err != nil {
		return core.Agent{}, err
	}
	if err := c.awaitProgram(t, name, kind, base); err != nil {
		return core.Agent{}, err
	}
	if err := c.awaitReady(name); err != nil {
		return core.Agent{}, err
	}
	return core.Agent{Name: name, Pane: pane}, nil
}

func (c Client) awaitProgram(t, name, kind string, base terminal) error {
	exited := fmt.Errorf("hc: agent %s exited before it started", name)
	if base.State == "finished" {
		return exited
	}
	var baseArgv []string
	if base.Foreground != nil {
		baseArgv = base.Foreground.Argv
	}
	deadline := time.Now().Add(paneBusyBudget)
	for {
		term, err := c.get(t)
		if err != nil {
			return err
		}
		if term.State == "finished" {
			return exited
		}
		if term.Foreground != nil && !slices.Equal(term.Foreground.Argv, baseArgv) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("hc: agent %s never started %s", name, kind)
		}
		time.Sleep(paneBusyBackoff)
	}
}

var trustDialogs = []struct {
	marker string
	keys   []string
}{
	{"Yes, I trust this folder", []string{"down", "enter"}},
	{"Trust this folder?", []string{"enter"}},
	{"Do you trust the contents of this directory?", []string{"enter"}},
}

func asksTrust(text string) ([]string, bool) {
	for _, d := range trustDialogs {
		if contains(text, d.marker) {
			return d.keys, true
		}
	}
	return nil, false
}

func (c Client) trustDialog(agent string) ([]string, bool, error) {
	screen, err := c.Screen(agent)
	if err != nil {
		return nil, false, err
	}
	if keys, ok := asksTrust(screen); ok {
		return keys, true, nil
	}
	history, err := c.Read(agent, 200)
	if err != nil {
		return nil, false, err
	}
	keys, ok := asksTrust(history)
	return keys, ok, nil
}

func (c Client) awaitReady(agent string) error {
	answered := false
	var quietSince time.Time
	var quietSeq int64
	deadline := time.Now().Add(paneBusyBudget)
	for {
		var asks bool
		var st core.AgentState
		if answered {
			screen, err := c.Screen(agent)
			if err != nil {
				return err
			}
			_, asks = asksTrust(screen)
			if !asks {
				var seq int64
				if st, seq, err = c.StateSeq(agent); err != nil {
					return err
				}
				switch {
				case st != core.AgentIdle && st != core.AgentDone:
					quietSince = time.Time{}
				case quietSince.IsZero() || seq != quietSeq:
					quietSince, quietSeq = time.Now(), seq
				}
				if !quietSince.IsZero() && time.Since(quietSince) >= trustSettle {
					return nil
				}
			} else {
				quietSince = time.Time{}
			}
		} else {
			keys, ok, err := c.trustDialog(agent)
			if err != nil {
				return err
			}
			if ok {
				if err := c.SendKeys(agent, keys...); err != nil {
					return err
				}
				answered = true
				deadline = time.Now().Add(paneBusyBudget)
				continue
			}
			if st, err = c.State(agent); err != nil {
				return err
			}
			if st == core.AgentIdle || st == core.AgentDone {
				return nil
			}
		}
		if time.Now().After(deadline) {
			switch {
			case asks:
				return fmt.Errorf("hc: agent %s still asks to trust its directory", agent)
			case answered && st == core.AgentBlocked:
				return fmt.Errorf("hc: agent %s stays blocked after the trust dialog", agent)
			case answered:
				return fmt.Errorf("hc: agent %s never became ready after the trust dialog (state %s)", agent, st)
			}
			return fmt.Errorf("hc: agent %s never became ready (state %s)", agent, st)
		}
		time.Sleep(paneBusyBackoff)
	}
}

func contains(text, marker string) bool {
	return strings.Contains(squash(text), squash(marker))
}

func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

func (c Client) State(agent string) (core.AgentState, error) {
	st, _, err := c.StateSeq(agent)
	return st, err
}

func (c Client) StateSeq(agent string) (core.AgentState, int64, error) {
	t, err := c.find(context.Background(), agent)
	if errors.Is(err, core.ErrNotFound) {
		return core.AgentGone, 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	term, err := c.get(t)
	if errors.Is(err, core.ErrNotFound) {
		return core.AgentGone, 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	switch {
	case term.State == "finished":
		return core.AgentGone, term.StatusMS, nil
	case term.Status == nil:
		return core.AgentUnknown, term.StatusMS, nil
	}
	switch s := core.AgentState(term.Status.State); s {
	case core.AgentIdle, core.AgentWorking, core.AgentBlocked, core.AgentDone:
		return s, term.StatusMS, nil
	}
	return core.AgentUnknown, term.StatusMS, nil
}

func (c Client) AgentPane(agent string) (string, error) {
	t, err := c.find(context.Background(), agent)
	if errors.Is(err, core.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	l, err := c.layout()
	if err != nil {
		return "", err
	}
	pane, _ := l.terminal(t)
	return pane, nil
}

func (c Client) read(agent string, args ...string) (string, error) {
	t, err := c.find(context.Background(), agent)
	if err != nil {
		return "", err
	}
	var out struct {
		Text string `json:"text"`
	}
	err = c.hc(context.Background(), callTimeout, &out, append(append([]string{"read", "--json"}, args...), t)...)
	return out.Text, err
}

func (c Client) Screen(agent string) (string, error) {
	return c.read(agent)
}

func (c Client) Read(agent string, lines int) (string, error) {
	return c.read(agent, "--lines", strconv.Itoa(lines))
}

func (c Client) SendKeys(agent string, keys ...string) error {
	t, err := c.find(context.Background(), agent)
	if err != nil {
		return err
	}
	var out struct{}
	for _, key := range keys {
		if err := c.hc(context.Background(), callTimeout, &out, "keys", "--json", t, key); err != nil {
			return err
		}
	}
	return nil
}

func (c Client) Interrupt(agent string) error {
	t, err := c.find(context.Background(), agent)
	if err != nil {
		return err
	}
	var out struct{}
	if err := c.hc(context.Background(), callTimeout, &out, "interrupt", "--json", t); err != nil {
		return err
	}
	return c.hc(context.Background(), callTimeout, &out, "keys", "--json", t, "ctrl+c")
}

func (c Client) Tag(id string, tokens map[string]string) error {
	l, err := c.layout()
	if err != nil {
		return err
	}
	terminals, ok := l.space(id)
	if !ok {
		return spaceNotFound(id)
	}
	keys := make([]string, 0, len(tokens))
	for k := range tokens {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+tokens[k])
	}
	for _, t := range terminals {
		term, err := c.get(t)
		if err != nil {
			return err
		}
		if _, agent := term.Labels["rloop.agent"]; term.State != "live" || !agent {
			continue
		}
		var out struct{}
		if err := c.hc(context.Background(), callTimeout, &out, append([]string{"label", "--json", t}, pairs...)...); err != nil {
			return err
		}
	}
	return nil
}

type sendResult struct {
	Started *bool       `json:"started"`
	Turn    uint64      `json:"turn"`
	Wait    *waitResult `json:"wait"`
}

type waitResult struct {
	Ended  bool    `json:"ended"`
	Reason string  `json:"reason"`
	Status *string `json:"status"`
	Turn   uint64  `json:"turn"`
}

func seconds(d time.Duration) string { return strconv.Itoa(max(1, int(math.Ceil(d.Seconds())))) }

func (c Client) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	return c.PromptContext(context.Background(), agent, text, wait, timeout)
}

func (c Client) PromptContext(ctx context.Context, agent, text string, wait bool, timeout time.Duration) error {
	t, err := c.find(ctx, agent)
	if err != nil {
		return err
	}
	args := []string{"send", "--json", t, "--", text}
	limit := callTimeout
	if wait {
		args = []string{"send", "--json", "--wait", "--timeout", seconds(timeout), t, "--", text}
		limit = callTimeout + timeout
	}
	var sent sendResult
	if err := c.hc(ctx, limit, &sent, args...); err != nil {
		return err
	}
	timedOut := Error{Kind: "timeout", Message: agent + " still working after " + seconds(timeout) + "s"}
	stalled := (sent.Started != nil && !*sent.Started) || (sent.Wait != nil && sent.Wait.Reason == "not_started")
	if !stalled {
		if wait && sent.Wait != nil && !sent.Wait.Ended && sent.Wait.Reason == "timeout" {
			return timedOut
		}
		return nil
	}
	var out struct{}
	if err := c.hc(ctx, callTimeout, &out, "keys", "--json", t, "enter"); err != nil {
		return err
	}
	retry := submitTimeout
	if wait {
		retry = timeout
	}
	var w waitResult
	if err := c.hc(ctx, callTimeout+retry, &w, "wait", "--json", "--timeout", seconds(retry), t); err != nil {
		return err
	}
	submitted := w.Turn > sent.Turn || (w.Status != nil && (*w.Status == "working" || *w.Status == "blocked"))
	if !submitted {
		return fmt.Errorf("hc: prompt to %s not submitted", agent)
	}
	if wait && !w.Ended && w.Reason == "timeout" {
		return timedOut
	}
	return nil
}

func (c Client) Submit(agent, text string) error {
	t, err := c.find(context.Background(), agent)
	if err != nil {
		return err
	}
	var sent sendResult
	if err := c.hc(context.Background(), callTimeout, &sent, "send", "--json", t, "--", text); err != nil {
		return err
	}
	if sent.Started != nil && !*sent.Started {
		return fmt.Errorf("%w: hc: prompt to %s did not start", core.ErrPromptStalled, agent)
	}
	return nil
}

package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"r-loop/internal/core"
)

var ErrNoBinary = errors.New("herdr binary not found")

var (
	callTimeout     = 30 * time.Second
	submitTimeout   = 10 * time.Second
	paneBusyBudget  = 20 * time.Second
	paneBusyBackoff = 250 * time.Millisecond
)

type Error struct {
	Code, Message string
}

func (e Error) Error() string {
	return "herdr: " + e.Code + ": " + e.Message
}

type Client struct {
	Bin string
}

func (c Client) exec(args ...string) ([]byte, error) {
	return c.execWithin(callTimeout, args...)
}

func (c Client) execWithin(limit time.Duration, args ...string) ([]byte, error) {
	return c.execWithinContext(context.Background(), limit, args...)
}

func (c Client) execWithinContext(parent context.Context, limit time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("herdr %s: timed out after %s", strings.Join(args[:min(len(args), 3)], " "), limit)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil, ctx.Err()
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNoBinary, c.Bin)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return nil, err
	}
	msg := strings.TrimSpace(stderr.String())
	if exitErr.ExitCode() == 2 {
		return nil, Error{Code: "usage", Message: msg}
	}
	var body struct {
		Error *Error `json:"error"`
	}
	if json.Unmarshal(stderr.Bytes(), &body) == nil && body.Error != nil {
		return nil, *body.Error
	}
	return nil, Error{Code: "unknown", Message: msg}
}

func (c Client) call(out any, args ...string) error {
	return c.callWithin(callTimeout, out, args...)
}

func (c Client) callWithin(limit time.Duration, out any, args ...string) error {
	return c.callWithinContext(context.Background(), limit, out, args...)
}

func (c Client) callWithinContext(ctx context.Context, limit time.Duration, out any, args ...string) error {
	data, err := c.execWithinContext(ctx, limit, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("herdr %s: %w", strings.Join(args[:2], " "), err)
	}
	return nil
}

func (c Client) Reachable() error {
	var out struct{}
	return c.call(&out, "workspace", "list")
}

func (c Client) Open(spec core.OpenSpec) (core.Workspace, error) {
	args := []string{"workspace", "create", "--cwd", spec.CWD, "--label", spec.Label}
	args = append(args, envFlags(spec.Env)...)
	args = append(args, "--no-focus")
	var out struct {
		Result struct {
			Workspace struct {
				ID string `json:"workspace_id"`
			} `json:"workspace"`
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := c.call(&out, args...); err != nil {
		return core.Workspace{}, err
	}
	return core.Workspace{ID: out.Result.Workspace.ID, RootPane: out.Result.RootPane.ID}, nil
}

func envFlags(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	flags := make([]string, 0, 2*len(env))
	for _, k := range keys {
		flags = append(flags, "--env", k+"="+env[k])
	}
	return flags
}

func (c Client) Split(pane, direction, cwd string, ratio float64, env map[string]string) (string, error) {
	args := []string{"pane", "split"}
	if pane == "" {
		args = append(args, "--current")
	} else {
		args = append(args, "--pane", pane)
	}
	args = append(args, "--direction", direction)
	if ratio != 0 {
		args = append(args, "--ratio", strconv.FormatFloat(ratio, 'f', -1, 64))
	}
	args = append(args, "--cwd", cwd)
	args = append(args, envFlags(env)...)
	args = append(args, "--no-focus")
	var out struct {
		Result struct {
			Pane struct {
				ID string `json:"pane_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := c.call(&out, args...); err != nil {
		return "", err
	}
	return out.Result.Pane.ID, nil
}

func (c Client) PaneSize(pane string) (int, int, error) {
	var out struct {
		Result struct {
			Layout struct {
				Panes []struct {
					ID   string `json:"pane_id"`
					Rect struct {
						Width  int `json:"width"`
						Height int `json:"height"`
					} `json:"rect"`
				} `json:"panes"`
			} `json:"layout"`
		} `json:"result"`
	}
	if err := c.call(&out, "pane", "layout", "--pane", pane); err != nil {
		return 0, 0, err
	}
	for _, p := range out.Result.Layout.Panes {
		if p.ID == pane {
			return p.Rect.Width, p.Rect.Height, nil
		}
	}
	return 0, 0, fmt.Errorf("herdr: pane %s is not in its own layout", pane)
}

func (c Client) OpenTab(workspace string, spec core.OpenSpec) (string, error) {
	args := []string{"tab", "create", "--workspace", workspace, "--cwd", spec.CWD, "--label", spec.Label}
	args = append(args, envFlags(spec.Env)...)
	args = append(args, "--no-focus")
	var out struct {
		Result struct {
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := c.call(&out, args...); err != nil {
		return "", err
	}
	return out.Result.RootPane.ID, nil
}

type agentResult struct {
	Result struct {
		Agent struct {
			Name   string `json:"name"`
			Pane   string `json:"pane_id"`
			Status string `json:"agent_status"`
			Seq    int64  `json:"state_change_seq"`
		} `json:"agent"`
	} `json:"result"`
}

func (c Client) Start(pane, name, kind string, args []string) (core.Agent, error) {
	argv := append([]string{"agent", "start", name, "--kind", kind, "--pane", pane, "--"}, args...)
	var out agentResult
	deadline := time.Now().Add(paneBusyBudget)
	for {
		err := c.call(&out, argv...)
		var herr Error
		if errors.As(err, &herr) && herr.Code == "agent_pane_busy" && time.Now().Before(deadline) {
			time.Sleep(paneBusyBackoff)
			continue
		}
		if errors.As(err, &herr) && herr.Code == "agent_not_ready" {
			out.Result.Agent.Name, out.Result.Agent.Pane = name, pane
			break
		}
		if err != nil {
			return core.Agent{}, err
		}
		break
	}
	if err := c.awaitReady(name); err != nil {
		return core.Agent{}, err
	}
	return core.Agent{Name: out.Result.Agent.Name, Pane: out.Result.Agent.Pane}, nil
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
	deadline := time.Now().Add(paneBusyBudget)
	for {
		var asks bool
		if answered {
			screen, err := c.Screen(agent)
			if err != nil {
				return err
			}
			_, asks = asksTrust(screen)
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
		}
		var st core.AgentState
		if !asks {
			var err error
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
				return fmt.Errorf("herdr: agent %s still asks to trust its directory", agent)
			case answered && st == core.AgentBlocked:
				return fmt.Errorf("herdr: agent %s stays blocked after the trust dialog", agent)
			case answered:
				return fmt.Errorf("herdr: agent %s never became ready after the trust dialog (state %s)", agent, st)
			}
			return fmt.Errorf("herdr: agent %s never became ready (state %s)", agent, st)
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

func (c Client) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	return c.PromptContext(context.Background(), agent, text, wait, timeout)
}

func (c Client) PromptContext(ctx context.Context, agent, text string, wait bool, timeout time.Duration) error {
	started := []string{"--until", "working", "--until", "blocked", "--timeout", strconv.FormatInt(submitTimeout.Milliseconds(), 10)}
	finished := []string{"--timeout", strconv.FormatInt(timeout.Milliseconds(), 10)}
	args := append([]string{"agent", "prompt", agent, text, "--wait"}, started...)
	limit := callTimeout + submitTimeout
	if wait {
		args = append([]string{"agent", "prompt", agent, text, "--wait"}, finished...)
		limit = callTimeout + timeout
	}
	var out struct{}
	err := c.callWithinContext(ctx, limit, &out, args...)
	var herr Error
	if !errors.As(err, &herr) || herr.Code != "agent_prompt_stalled" {
		return err
	}
	if err := c.callWithinContext(ctx, callTimeout, &out, "agent", "send-keys", agent, "enter"); err != nil {
		return err
	}
	if err := c.callWithinContext(ctx, callTimeout+submitTimeout, &out, append([]string{"agent", "wait", agent}, started...)...); err != nil {
		return fmt.Errorf("herdr: prompt to %s not submitted: %w", agent, err)
	}
	if !wait {
		return nil
	}
	return c.callWithinContext(ctx, callTimeout+timeout, &out, append([]string{"agent", "wait", agent}, finished...)...)
}

func (c Client) State(agent string) (core.AgentState, error) {
	st, _, err := c.StateSeq(agent)
	return st, err
}

func (c Client) StateSeq(agent string) (core.AgentState, int64, error) {
	var out agentResult
	err := c.call(&out, "agent", "get", agent)
	var herr Error
	if errors.As(err, &herr) && strings.HasSuffix(herr.Code, "not_found") {
		return core.AgentGone, 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	seq := out.Result.Agent.Seq
	switch s := core.AgentState(out.Result.Agent.Status); s {
	case core.AgentIdle, core.AgentWorking, core.AgentBlocked, core.AgentDone:
		return s, seq, nil
	}
	return core.AgentUnknown, seq, nil
}

func (c Client) AgentPane(agent string) (string, error) {
	var out agentResult
	err := c.call(&out, "agent", "get", agent)
	var herr Error
	if errors.As(err, &herr) && strings.HasSuffix(herr.Code, "not_found") {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return out.Result.Agent.Pane, nil
}

func (c Client) Read(agent string, lines int) (string, error) {
	data, err := c.exec("agent", "read", agent, "--source", "recent-unwrapped", "--lines", strconv.Itoa(lines))
	return string(data), err
}

func (c Client) Screen(agent string) (string, error) {
	data, err := c.exec("agent", "read", agent, "--source", "visible")
	return string(data), err
}

func (c Client) SendKeys(agent string, keys ...string) error {
	var out struct{}
	for _, key := range keys {
		if err := c.call(&out, "agent", "send-keys", agent, key); err != nil {
			return err
		}
	}
	return nil
}

func (c Client) SendText(agent, text string) error {
	pane, err := c.AgentPane(agent)
	if err != nil {
		return err
	}
	if pane == "" {
		return fmt.Errorf("agent %s has no pane", agent)
	}
	_, err = c.exec("pane", "send-text", pane, text)
	return err
}

func (c Client) Interrupt(agent string) error {
	var out struct{}
	if err := c.call(&out, "agent", "send-keys", agent, "esc"); err != nil {
		return err
	}
	return c.call(&out, "agent", "send-keys", agent, "ctrl+c")
}

func (c Client) Focus(agent string) error {
	var out struct{}
	return c.call(&out, "agent", "focus", agent)
}

func (c Client) ClosePane(pane string) error {
	var out struct{}
	return c.call(&out, "pane", "close", pane)
}

func (c Client) Tag(workspaceID string, tokens map[string]string) error {
	args := []string{"workspace", "report-metadata", workspaceID, "--source", "r-loop"}
	keys := make([]string, 0, len(tokens))
	for k := range tokens {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if tokens[k] == "" {
			args = append(args, "--clear-token", k)
		} else {
			args = append(args, "--token", k+"="+tokens[k])
		}
	}
	_, err := c.exec(args...)
	return err
}

func (c Client) Close(workspaceID string) error {
	var out struct{}
	return c.call(&out, "workspace", "close", workspaceID)
}

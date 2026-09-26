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

func (c Client) Split(pane, direction, cwd string, env map[string]string) (string, error) {
	args := []string{"pane", "split"}
	if pane == "" {
		args = append(args, "--current")
	} else {
		args = append(args, "--pane", pane)
	}
	args = append(args, "--direction", direction, "--cwd", cwd)
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

type agentResult struct {
	Result struct {
		Agent struct {
			Name   string `json:"name"`
			Pane   string `json:"pane_id"`
			Status string `json:"agent_status"`
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
		if errors.As(err, &herr) && herr.Code == "agent_not_ready" && kind == "claude" {
			accepted, terr := c.acceptTrust(name, claudeTrustAnswer, "down", "enter")
			if terr != nil {
				return core.Agent{}, terr
			}
			if !accepted {
				return core.Agent{}, err
			}
			if err := c.awaitClaudeBanner(name); err != nil {
				return core.Agent{}, err
			}
			if err := c.awaitUnblocked(name); err != nil {
				return core.Agent{}, err
			}
			return core.Agent{Name: name, Pane: pane}, nil
		}
		if err != nil {
			return core.Agent{}, err
		}
		break
	}
	if kind == "codex" {
		if err := c.awaitCodexPrompt(name); err != nil {
			return core.Agent{}, err
		}
		if err := c.awaitSettled(name); err != nil {
			return core.Agent{}, err
		}
	} else if _, err := c.acceptTrust(name, codexTrustQuestion, "enter"); err != nil {
		return core.Agent{}, err
	}
	return core.Agent{Name: out.Result.Agent.Name, Pane: out.Result.Agent.Pane}, nil
}

const (
	codexTrustQuestion = "Do you trust the contents of this directory?"
	codexTrustFolder   = "Trust this folder?"
	codexBanner        = ">_ OpenAI Codex"
	claudeTrustAnswer  = "Yes, I trust this folder"
	claudeBanner       = "Claude Code v"
)

func (c Client) awaitCodexPrompt(agent string) error {
	trusted := false
	deadline := time.Now().Add(paneBusyBudget)
	for {
		screen, err := c.Screen(agent)
		if err != nil {
			return err
		}
		asks := strings.Contains(screen, codexTrustQuestion) || strings.Contains(screen, codexTrustFolder)
		switch {
		case asks && !trusted:
			if err := c.SendKeys(agent, "enter"); err != nil {
				return err
			}
			trusted = true
			deadline = time.Now().Add(paneBusyBudget)
			continue
		case !asks && strings.Contains(screen, codexBanner):
			return nil
		}
		if time.Now().After(deadline) {
			if trusted {
				return fmt.Errorf("herdr: agent %s never showed codex's prompt after the trust dialog", agent)
			}
			return fmt.Errorf("herdr: agent %s never showed codex's prompt", agent)
		}
		time.Sleep(paneBusyBackoff)
	}
}

func (c Client) awaitSettled(agent string) error {
	deadline := time.Now().Add(paneBusyBudget)
	for {
		st, err := c.State(agent)
		if err != nil || st == core.AgentIdle || st == core.AgentDone {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("herdr: agent %s never settled after start", agent)
		}
		time.Sleep(paneBusyBackoff)
	}
}

func (c Client) acceptTrust(agent, marker string, keys ...string) (bool, error) {
	asks := func() (bool, error) {
		screen, err := c.Screen(agent)
		return strings.Contains(screen, marker), err
	}
	ask, err := asks()
	if err != nil || !ask {
		return false, err
	}
	if err := c.SendKeys(agent, keys...); err != nil {
		return true, err
	}
	deadline := time.Now().Add(paneBusyBudget)
	for {
		if ask, err = asks(); err != nil || !ask {
			return true, err
		}
		if time.Now().After(deadline) {
			return true, fmt.Errorf("herdr: agent %s still asks to trust its directory", agent)
		}
		time.Sleep(paneBusyBackoff)
	}
}

func (c Client) awaitClaudeBanner(agent string) error {
	deadline := time.Now().Add(paneBusyBudget)
	for {
		screen, err := c.Screen(agent)
		if err != nil || strings.Contains(screen, claudeBanner) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("herdr: agent %s never showed claude's prompt after the trust dialog", agent)
		}
		time.Sleep(paneBusyBackoff)
	}
}

func (c Client) awaitUnblocked(agent string) error {
	deadline := time.Now().Add(paneBusyBudget)
	for {
		st, err := c.State(agent)
		if err != nil || st != core.AgentBlocked {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("herdr: agent %s stays blocked after the trust dialog", agent)
		}
		time.Sleep(paneBusyBackoff)
	}
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
	var out agentResult
	err := c.call(&out, "agent", "get", agent)
	var herr Error
	if errors.As(err, &herr) && strings.HasSuffix(herr.Code, "not_found") {
		return core.AgentGone, nil
	}
	if err != nil {
		return "", err
	}
	switch s := core.AgentState(out.Result.Agent.Status); s {
	case core.AgentIdle, core.AgentWorking, core.AgentBlocked, core.AgentDone:
		return s, nil
	}
	return core.AgentUnknown, nil
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

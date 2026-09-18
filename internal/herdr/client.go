package herdr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"r-loop/internal/core"
)

var ErrNoBinary = errors.New("herdr binary not found")

var (
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
	cmd := exec.Command(c.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
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
	data, err := c.exec(args...)
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
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+spec.Env[k])
	}
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

func (c Client) Split(pane, direction, cwd string) (string, error) {
	args := []string{"pane", "split"}
	if pane == "" {
		args = append(args, "--current")
	} else {
		args = append(args, "--pane", pane)
	}
	args = append(args, "--direction", direction, "--cwd", cwd, "--no-focus")
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
		if err != nil {
			return core.Agent{}, err
		}
		break
	}
	return core.Agent{Name: out.Result.Agent.Name, Pane: out.Result.Agent.Pane}, nil
}

func (c Client) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	args := []string{"agent", "prompt", agent, text}
	if wait {
		args = append(args, "--wait", "--timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	}
	var out struct{}
	return c.call(&out, args...)
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

func (c Client) Read(agent string, lines int) (string, error) {
	data, err := c.exec("agent", "read", agent, "--source", "recent-unwrapped", "--lines", strconv.Itoa(lines))
	return string(data), err
}

func (c Client) Interrupt(agent string) error {
	var out struct{}
	if err := c.call(&out, "agent", "send-keys", agent, "esc"); err != nil {
		return err
	}
	return c.call(&out, "agent", "send-keys", agent, "ctrl+c")
}

func (c Client) ClosePane(pane string) error {
	var out struct{}
	return c.call(&out, "pane", "close", pane)
}

func (c Client) Close(workspaceID string) error {
	var out struct{}
	return c.call(&out, "workspace", "close", workspaceID)
}

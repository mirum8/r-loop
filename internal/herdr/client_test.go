package herdr

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
)

var _ core.SessionHost = Client{}

func fake(t *testing.T, stdout string) (Client, func() []string) {
	t.Helper()
	return fakeExit(t, stdout, "", 0)
}

func fakeExit(t *testing.T, stdout, stderr string, exit int) (Client, func() []string) {
	t.Helper()
	bin, err := filepath.Abs("testdata/herdr")
	if err != nil {
		t.Fatal(err)
	}
	argvFile := filepath.Join(t.TempDir(), "argv")
	t.Setenv("HERDR_ARGV", argvFile)
	t.Setenv("HERDR_STDOUT", stdout)
	t.Setenv("HERDR_STDERR", stderr)
	t.Setenv("HERDR_EXIT", string(rune('0'+exit)))
	return Client{Bin: bin}, func() []string {
		data, err := os.ReadFile(argvFile)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	}
}

func assertArgv(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
}

func TestReachableRunsWorkspaceList(t *testing.T) {
	c, argv := fake(t, `{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[]}}`)

	if err := c.Reachable(); err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	assertArgv(t, argv(), []string{"workspace", "list"})
}

func TestReachableReturnsErrorCodeWhenServerIsDown(t *testing.T) {
	c, _ := fakeExit(t, "", `{"error":{"code":"server_unreachable","message":"no server"},"id":"cli:workspace:list"}`, 1)

	err := c.Reachable()

	var herr Error
	if !errors.As(err, &herr) || herr.Code != "server_unreachable" || herr.Message != "no server" {
		t.Fatalf("got %#v", err)
	}
}

func TestOpenCreatesWorkspaceAndParsesIDs(t *testing.T) {
	c, argv := fake(t, `{"id":"cli:workspace:create","result":{"root_pane":{"pane_id":"w3A:p1","workspace_id":"w3A"},"type":"workspace_created","workspace":{"label":"phase-3","workspace_id":"w3A"}}}`)

	ws, err := c.Open(core.OpenSpec{CWD: "/repo/wt", Label: "phase-3", Env: map[string]string{"R_LOOP_RUN": "r1", "A": "b"}})

	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if ws != (core.Workspace{ID: "w3A", RootPane: "w3A:p1"}) {
		t.Fatalf("workspace %+v", ws)
	}
	assertArgv(t, argv(), []string{"workspace", "create", "--cwd", "/repo/wt", "--label", "phase-3", "--env", "A=b", "--env", "R_LOOP_RUN=r1", "--no-focus"})
}

func TestSplitByPaneID(t *testing.T) {
	c, argv := fake(t, `{"id":"cli:pane:split","result":{"pane":{"pane_id":"w3A:p2","workspace_id":"w3A"},"type":"pane_info"}}`)

	pane, err := c.Split("w3A:p1", "right", "/repo/wt")

	if err != nil || pane != "w3A:p2" {
		t.Fatalf("got %q, %v", pane, err)
	}
	assertArgv(t, argv(), []string{"pane", "split", "--pane", "w3A:p1", "--direction", "right", "--cwd", "/repo/wt", "--no-focus"})
}

func TestSplitCurrentPaneWhenPaneIsEmpty(t *testing.T) {
	c, argv := fake(t, `{"id":"cli:pane:split","result":{"pane":{"pane_id":"w3A:p3"},"type":"pane_info"}}`)

	pane, err := c.Split("", "down", "/repo")

	if err != nil || pane != "w3A:p3" {
		t.Fatalf("got %q, %v", pane, err)
	}
	assertArgv(t, argv(), []string{"pane", "split", "--current", "--direction", "down", "--cwd", "/repo", "--no-focus"})
}

func TestStartRunsAgentStartWithArgsAfterDashes(t *testing.T) {
	c, argv := fake(t, `{"id":"cli:agent:start","result":{"agent":{"agent":"claude","agent_status":"idle","name":"phase-3-implement","pane_id":"w3A:p2"},"argv":["claude","--model","haiku"],"type":"agent_started"}}`)

	agent, err := c.Start("w3A:p2", "phase-3-implement", "claude", []string{"--model", "haiku"})

	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if agent != (core.Agent{Name: "phase-3-implement", Pane: "w3A:p2"}) {
		t.Fatalf("agent %+v", agent)
	}
	assertArgv(t, argv(), []string{"agent", "start", "phase-3-implement", "--kind", "claude", "--pane", "w3A:p2", "--", "--model", "haiku"})
}

func TestStartReturnsAgentNotReadyWithoutRetry(t *testing.T) {
	c, argv := fakeExit(t, "", `{"error":{"code":"agent_not_ready","message":"agent x is blocked during startup"},"id":"cli:agent:start"}`, 1)

	_, err := c.Start("w3A:p2", "x", "codex", nil)

	var herr Error
	if !errors.As(err, &herr) || herr.Code != "agent_not_ready" {
		t.Fatalf("got %#v", err)
	}
	assertArgv(t, argv(), []string{"agent", "start", "x", "--kind", "codex", "--pane", "w3A:p2", "--"})
}

func TestPromptWithoutWait(t *testing.T) {
	c, argv := fake(t, `{"id":"cli:agent:prompt","result":{"type":"ok"}}`)

	if err := c.Prompt("a1", "do it; rm -rf $HOME\n'quoted' \"too\"", false, time.Minute); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	assertArgv(t, argv(), []string{"agent", "prompt", "a1", "do it; rm -rf $HOME\n'quoted' \"too\""})
}

func TestPromptWithWaitPassesTimeoutInMilliseconds(t *testing.T) {
	c, argv := fake(t, `{"id":"cli:agent:prompt","result":{"type":"ok"}}`)

	if err := c.Prompt("a1", "go", true, 90*time.Second); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	assertArgv(t, argv(), []string{"agent", "prompt", "a1", "go", "--wait", "--timeout", "90000"})
}

func TestPromptErrorCodes(t *testing.T) {
	for _, code := range []string{"agent_blocked", "agent_prompt_stalled"} {
		t.Run(code, func(t *testing.T) {
			c, _ := fakeExit(t, "", `{"error":{"code":"`+code+`","message":"m"},"id":"cli:agent:prompt"}`, 1)

			err := c.Prompt("a1", "go", true, time.Second)

			var herr Error
			if !errors.As(err, &herr) || herr.Code != code {
				t.Fatalf("got %#v", err)
			}
		})
	}
}

func TestStateMapsLifecycleStates(t *testing.T) {
	cases := map[string]core.AgentState{
		"idle":    core.AgentIdle,
		"working": core.AgentWorking,
		"blocked": core.AgentBlocked,
		"done":    core.AgentDone,
		"unknown": core.AgentUnknown,
	}
	for status, want := range cases {
		t.Run(status, func(t *testing.T) {
			c, argv := fake(t, `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"`+status+`","name":"a1"},"type":"agent_info"}}`)

			got, err := c.State("a1")

			if err != nil || got != want {
				t.Fatalf("got %q, %v", got, err)
			}
			assertArgv(t, argv(), []string{"agent", "get", "a1"})
		})
	}
}

func TestStateOfMissingAgentIsGone(t *testing.T) {
	c, _ := fakeExit(t, "", `{"error":{"code":"agent_not_found","message":"agent target a1 not found"},"id":"cli:agent:get"}`, 1)

	got, err := c.State("a1")

	if err != nil || got != core.AgentGone {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReadReturnsRecentUnwrappedOutput(t *testing.T) {
	c, argv := fake(t, "line one\nline two\n")

	out, err := c.Read("a1", 40)

	if err != nil || out != "line one\nline two\n" {
		t.Fatalf("got %q, %v", out, err)
	}
	assertArgv(t, argv(), []string{"agent", "read", "a1", "--source", "recent-unwrapped", "--lines", "40"})
}

func TestInterruptSendsEscThenCtrlC(t *testing.T) {
	bin, err := filepath.Abs("testdata/herdr")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	wrapper := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\n\"" + bin + "\" \"$@\" || exit $?\ncat \"$HERDR_ARGV\" >> \"" + log + "\"\nprintf '\\n' >> \"" + log + "\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	fake(t, `{"id":"cli:agent:send-keys","result":{"type":"ok"}}`)

	if err := (Client{Bin: wrapper}).Interrupt("a1"); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "agent\x00send-keys\x00a1\x00esc\x00\nagent\x00send-keys\x00a1\x00ctrl+c\x00\n"
	if string(data) != want {
		t.Fatalf("calls\n got %q\nwant %q", data, want)
	}
}

func TestCloseRunsWorkspaceCloseWithoutGroup(t *testing.T) {
	c, argv := fake(t, `{"id":"cli:workspace:close","result":{"type":"ok"}}`)

	if err := c.Close("w3A"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertArgv(t, argv(), []string{"workspace", "close", "w3A"})
}

func TestExitTwoIsUsageError(t *testing.T) {
	c, _ := fakeExit(t, "", "error: unexpected argument '--bogus'", 2)

	err := c.Close("w3A")

	var herr Error
	if !errors.As(err, &herr) || herr.Code != "usage" || !strings.Contains(herr.Message, "--bogus") {
		t.Fatalf("got %#v", err)
	}
}

func TestExitOneWithoutJSONKeepsStderr(t *testing.T) {
	c, _ := fakeExit(t, "", "boom", 1)

	err := c.Close("w3A")

	var herr Error
	if !errors.As(err, &herr) || herr.Message != "boom" {
		t.Fatalf("got %#v", err)
	}
}

func TestUnparsableStdoutIsAnError(t *testing.T) {
	c, _ := fake(t, "not json")

	if _, err := c.Split("w3A:p1", "right", "/repo"); err == nil {
		t.Fatal("want error")
	}
}

func TestMissingBinaryIsErrNoBinary(t *testing.T) {
	c := Client{Bin: filepath.Join(t.TempDir(), "no-such-herdr")}

	if err := c.Reachable(); !errors.Is(err, ErrNoBinary) {
		t.Fatalf("got %v", err)
	}
}

func TestMissingBinaryOnPathIsErrNoBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if err := (Client{Bin: "herdr"}).Reachable(); !errors.Is(err, ErrNoBinary) {
		t.Fatalf("got %v", err)
	}
}

package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

	pane, err := c.Split("w3A:p1", "right", "/repo/wt", nil)

	if err != nil || pane != "w3A:p2" {
		t.Fatalf("got %q, %v", pane, err)
	}
	assertArgv(t, argv(), []string{"pane", "split", "--pane", "w3A:p1", "--direction", "right", "--cwd", "/repo/wt", "--no-focus"})
}

func TestSplitPassesEnvAsSortedEnvFlagsBeforeNoFocus(t *testing.T) {
	c, argv := fake(t, `{"id":"cli:pane:split","result":{"pane":{"pane_id":"w3A:p2","workspace_id":"w3A"},"type":"pane_info"}}`)
	pane, err := c.Split("w3A:p1", "right", "/repo/wt", map[string]string{"R_LOOP_STEP": "implement", "R_LOOP_RUN": "r1"})
	if err != nil || pane != "w3A:p2" {
		t.Fatalf("got %q, %v", pane, err)
	}
	assertArgv(t, argv(), []string{"pane", "split", "--pane", "w3A:p1", "--direction", "right", "--cwd", "/repo/wt", "--env", "R_LOOP_RUN=r1", "--env", "R_LOOP_STEP=implement", "--no-focus"})
}

func TestSplitCurrentPaneWhenPaneIsEmpty(t *testing.T) {
	c, argv := fake(t, `{"id":"cli:pane:split","result":{"pane":{"pane_id":"w3A:p3"},"type":"pane_info"}}`)

	pane, err := c.Split("", "down", "/repo", nil)

	if err != nil || pane != "w3A:p3" {
		t.Fatalf("got %q, %v", pane, err)
	}
	assertArgv(t, argv(), []string{"pane", "split", "--current", "--direction", "down", "--cwd", "/repo", "--no-focus"})
}

func TestStartRunsAgentStartWithArgsAfterDashes(t *testing.T) {
	c, calls := scripted(t, chatScreen, chatScreen)

	agent, err := c.Start("w3A:p2", "phase-3-implement", "claude", []string{"--model", "haiku"})

	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if agent != (core.Agent{Name: "phase-3-implement", Pane: "w3A:p2"}) {
		t.Fatalf("agent %+v", agent)
	}
	assertCalls(t, calls(), [][]string{
		{"agent", "start", "phase-3-implement", "--kind", "claude", "--pane", "w3A:p2", "--", "--model", "haiku"},
		{"agent", "read", "phase-3-implement", "--source", "visible"},
	})
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

func TestAWedgedHerdrCallFailsAfterTheCallTimeoutNamingTheCommand(t *testing.T) {
	c, _ := fake(t, `{"id":"cli:agent:get","result":{"agent":{"agent_status":"idle"},"type":"agent_info"}}`)
	t.Setenv("HERDR_SLEEP", "300")
	old := callTimeout
	callTimeout = 200 * time.Millisecond
	t.Cleanup(func() { callTimeout = old })
	start := time.Now()
	_, err := c.State("a1")
	if err == nil || !strings.Contains(err.Error(), "herdr agent get a1") || !strings.Contains(err.Error(), "timed out after 200ms") || time.Since(start) > 5*time.Second {
		t.Fatalf("State = %v after %v", err, time.Since(start))
	}
}

func TestAWaitingPromptGetsItsWaitOnTopOfTheCallTimeout(t *testing.T) {
	c, _ := fake(t, `{"id":"cli:agent:prompt","result":{"type":"ok"}}`)
	t.Setenv("HERDR_SLEEP", "0.5")
	old := callTimeout
	callTimeout = 200 * time.Millisecond
	t.Cleanup(func() { callTimeout = old })
	if err := c.Prompt("a1", "x", true, 2*time.Second); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
}

func TestCancelWaitingPromptStopsTheHerdrCLI(t *testing.T) {
	c, _ := fake(t, `{"id":"cli:agent:prompt","result":{"type":"ok"}}`)
	t.Setenv("HERDR_SLEEP", "300")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.PromptContext(ctx, "a1", "check phase 1", true, 10*time.Minute) }()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("PromptContext error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("PromptContext still running %s after cancellation", time.Since(start))
	}
}

func TestAgentPaneNamesThePaneAnAgentRunsIn(t *testing.T) {
	c, argv := fake(t, `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"done","name":"rloop-watchdog","pane_id":"w2X:p3"},"type":"agent_info"}}`)

	got, err := c.AgentPane("rloop-watchdog")

	if err != nil || got != "w2X:p3" {
		t.Fatalf("got %q, %v", got, err)
	}
	assertArgv(t, argv(), []string{"agent", "get", "rloop-watchdog"})
}

func TestAgentPaneOfMissingAgentIsEmpty(t *testing.T) {
	c, _ := fakeExit(t, "", `{"error":{"code":"agent_not_found","message":"agent target a1 not found"},"id":"cli:agent:get"}`, 1)

	got, err := c.AgentPane("a1")

	if err != nil || got != "" {
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

func TestTagReportsTokensAndClearsEmptyOnes(t *testing.T) {
	c, argv := fake(t, "")

	if err := c.Tag("w3A", map[string]string{"rloop_wait": "", "rloop": "◆ review r1/2"}); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	assertArgv(t, argv(), []string{"workspace", "report-metadata", "w3A", "--source", "r-loop", "--token", "rloop=◆ review r1/2", "--clear-token", "rloop_wait"})
}

func TestClosePaneRunsPaneClose(t *testing.T) {
	c, argv := fake(t, `{"id":"cli:pane:close","result":{"type":"ok"}}`)

	if err := c.ClosePane("w3A:p2"); err != nil {
		t.Fatalf("ClosePane: %v", err)
	}
	assertArgv(t, argv(), []string{"pane", "close", "w3A:p2"})
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

	if _, err := c.Split("w3A:p1", "right", "/repo", nil); err == nil {
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

func busyThenStarted(t *testing.T, failures int) (Client, string) {
	t.Helper()
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	bin := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\n" +
		"if [ \"$2\" = read ]; then exit 0; fi\n" +
		"n=$(cat \"" + count + "\" 2>/dev/null || echo 0)\n" +
		"n=$((n+1))\n" +
		"echo $n > \"" + count + "\"\n" +
		"if [ $n -le " + string(rune('0'+failures)) + " ]; then\n" +
		"  printf '%s' '{\"error\":{\"code\":\"agent_pane_busy\",\"message\":\"agent target pane w1:p1 is not an available shell\"}}' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"printf '%s' '{\"result\":{\"agent\":{\"name\":\"a1\",\"pane_id\":\"w1:p1\",\"agent_status\":\"idle\"}}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return Client{Bin: bin}, count
}

func shrinkPaneBusyWait(t *testing.T, budget time.Duration) {
	t.Helper()
	oldBudget, oldBackoff := paneBusyBudget, paneBusyBackoff
	paneBusyBudget, paneBusyBackoff = budget, time.Millisecond
	t.Cleanup(func() { paneBusyBudget, paneBusyBackoff = oldBudget, oldBackoff })
}

func TestStartRetriesWhilePaneShellIsNotReady(t *testing.T) {
	shrinkPaneBusyWait(t, 5*time.Second)
	c, count := busyThenStarted(t, 3)

	agent, err := c.Start("w1:p1", "a1", "claude", nil)

	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if agent != (core.Agent{Name: "a1", Pane: "w1:p1"}) {
		t.Fatalf("agent %+v", agent)
	}
	if data, _ := os.ReadFile(count); strings.TrimSpace(string(data)) != "4" {
		t.Fatalf("calls %q", data)
	}
}

func TestStartGivesUpOnBusyPaneAfterBudget(t *testing.T) {
	shrinkPaneBusyWait(t, 20*time.Millisecond)
	c, _ := busyThenStarted(t, 9)

	_, err := c.Start("w1:p1", "a1", "claude", nil)

	var herr Error
	if !errors.As(err, &herr) || herr.Code != "agent_pane_busy" {
		t.Fatalf("got %#v", err)
	}
}

const (
	trustScreen = "> You are in /repo/.r-loop/wt/phase-1\n\n  Do you trust the contents of this directory? Working with untrusted contents comes with higher risk of prompt\n  injection.\n\n› 1. Yes, continue\n  2. No, quit\n\n  Press enter to continue\n"
	chatScreen  = "╭──╮\n│ >_ OpenAI Codex (v0.155.0) │\n╰──╯\n\n› Ask Codex to do anything\n"
)

func scripted(t *testing.T, before, after string) (Client, func() [][]string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	entered := filepath.Join(dir, "entered")
	for name, screen := range map[string]string{"before": before, "after": after} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(screen), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\n" +
		"printf '%s\\0' \"$@\" >> \"" + log + "\"\n" +
		"printf '\\n' >> \"" + log + "\"\n" +
		"case \"$2\" in\n" +
		"start) printf '%s' '{\"result\":{\"agent\":{\"name\":\"'\"$3\"'\",\"pane_id\":\"'\"$7\"'\",\"agent_status\":\"idle\"}}}' ;;\n" +
		"send-keys) touch \"" + entered + "\"; printf '%s' '{\"result\":{\"type\":\"ok\"}}' ;;\n" +
		"read) if [ -e \"" + entered + "\" ]; then cat \"" + filepath.Join(dir, "after") + "\"; else cat \"" + filepath.Join(dir, "before") + "\"; fi ;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return Client{Bin: bin}, func() [][]string {
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			calls = append(calls, strings.Split(strings.TrimSuffix(line, "\x00"), "\x00"))
		}
		return calls
	}
}

func assertCalls(t *testing.T, got, want [][]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls\n got %q\nwant %q", got, want)
	}
}

func TestStartAcceptsDirectoryTrustDialogBeforeReturning(t *testing.T) {
	shrinkPaneBusyWait(t, 5*time.Second)
	c, calls := scripted(t, trustScreen, chatScreen)

	agent, err := c.Start("w4M:p2", "rloop-p1-plan-rv-codex-r1", "codex", nil)

	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if agent != (core.Agent{Name: "rloop-p1-plan-rv-codex-r1", Pane: "w4M:p2"}) {
		t.Fatalf("agent %+v", agent)
	}
	assertCalls(t, calls(), [][]string{
		{"agent", "start", "rloop-p1-plan-rv-codex-r1", "--kind", "codex", "--pane", "w4M:p2", "--"},
		{"agent", "read", "rloop-p1-plan-rv-codex-r1", "--source", "visible"},
		{"agent", "send-keys", "rloop-p1-plan-rv-codex-r1", "enter"},
		{"agent", "read", "rloop-p1-plan-rv-codex-r1", "--source", "visible"},
	})
}

func TestStartFailsWhenTrustDialogDoesNotClear(t *testing.T) {
	shrinkPaneBusyWait(t, 20*time.Millisecond)
	c, _ := scripted(t, trustScreen, trustScreen)

	_, err := c.Start("w4M:p2", "a1", "codex", nil)

	if err == nil || !strings.Contains(err.Error(), "trust") {
		t.Fatalf("got %v", err)
	}
}

const claudeReadyScreen = " ▐▛███▛█   Claude Code v2.1.280\n❯\n  -- INSERT --\n"

const claudeTrustScreen = " Accessing workspace:\n /repo\n Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team).\n Claude Code'll be able to read, edit, and execute files here.\n ❯ No, exit\n   Yes, I trust this folder\n Enter to confirm · Esc to cancel\n"

func notReadyThenTrusted(t *testing.T, before, after string) (Client, func() [][]string) {
	return notReadyThenTrustedBlocked(t, before, after, 0)
}

func notReadyThenTrustedBlocked(t *testing.T, before, after string, blockedGets int) (Client, func() [][]string) {
	t.Helper()
	dir := t.TempDir()
	gets := filepath.Join(dir, "gets")
	log := filepath.Join(dir, "calls")
	entered := filepath.Join(dir, "entered")
	for name, screen := range map[string]string{"before": before, "after": after} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(screen), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\n" +
		"printf '%s\\0' \"$@\" >> \"" + log + "\"\n" +
		"printf '\\n' >> \"" + log + "\"\n" +
		"case \"$2\" in\n" +
		"start) printf '%s' '{\"error\":{\"code\":\"agent_not_ready\",\"message\":\"agent '\"$3\"' is blocked during startup and is not ready for prompts\"}}' >&2; exit 1 ;;\n" +
		"send-keys) if [ \"$4\" = enter ]; then touch \"" + entered + "\"; fi; printf '%s' '{\"result\":{\"type\":\"ok\"}}' ;;\n" +
		"get) n=$(cat \"" + gets + "\" 2>/dev/null || echo 0); n=$((n+1)); echo $n > \"" + gets + "\"; if [ $n -le " + strconv.Itoa(blockedGets) + " ]; then s=blocked; else s=idle; fi; printf '%s' '{\"result\":{\"agent\":{\"agent_status\":\"'$s'\"}}}' ;;\n" +
		"read) if [ -e \"" + entered + "\" ]; then cat \"" + filepath.Join(dir, "after") + "\"; else cat \"" + filepath.Join(dir, "before") + "\"; fi ;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return Client{Bin: bin}, func() [][]string {
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			calls = append(calls, strings.Split(strings.TrimSuffix(line, "\x00"), "\x00"))
		}
		return calls
	}
}

func TestStartAcceptsClaudesTrustDialogThatHerdrReportsNotReady(t *testing.T) {
	shrinkPaneBusyWait(t, 5*time.Second)
	c, calls := notReadyThenTrusted(t, claudeTrustScreen, claudeReadyScreen)

	agent, err := c.Start("w8P:p1", "rloop-wd-run-1", "claude", []string{"--model", "opus"})

	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if agent != (core.Agent{Name: "rloop-wd-run-1", Pane: "w8P:p1"}) {
		t.Fatalf("agent %+v", agent)
	}
	assertCalls(t, calls(), [][]string{
		{"agent", "start", "rloop-wd-run-1", "--kind", "claude", "--pane", "w8P:p1", "--", "--model", "opus"},
		{"agent", "read", "rloop-wd-run-1", "--source", "visible"},
		{"agent", "send-keys", "rloop-wd-run-1", "down"},
		{"agent", "send-keys", "rloop-wd-run-1", "enter"},
		{"agent", "read", "rloop-wd-run-1", "--source", "visible"},
		{"agent", "read", "rloop-wd-run-1", "--source", "visible"},
		{"agent", "get", "rloop-wd-run-1"},
	})
}

func TestStartStillFailsWhenAnAgentIsNotReadyForAnotherReason(t *testing.T) {
	shrinkPaneBusyWait(t, 20*time.Millisecond)
	c, calls := notReadyThenTrusted(t, "  ✨ Update available!\n› 1. Update now\n  2. Skip\n", "")

	_, err := c.Start("w8P:p1", "a1", "claude", nil)

	var herr Error
	if !errors.As(err, &herr) || herr.Code != "agent_not_ready" {
		t.Fatalf("got %#v", err)
	}
	for _, call := range calls() {
		if call[1] == "send-keys" {
			t.Fatalf("pressed keys on an unknown startup screen: %q", call)
		}
	}
}

func TestStartFailsWhenClaudesTrustDialogDoesNotClear(t *testing.T) {
	shrinkPaneBusyWait(t, 20*time.Millisecond)
	c, _ := notReadyThenTrusted(t, claudeTrustScreen, claudeTrustScreen)

	_, err := c.Start("w8P:p1", "a1", "claude", nil)

	if err == nil || !strings.Contains(err.Error(), "trust") {
		t.Fatalf("got %v", err)
	}
}

func TestStartWaitsUntilHerdrNoLongerReportsTheTrustedClaudeBlocked(t *testing.T) {
	shrinkPaneBusyWait(t, 5*time.Second)
	c, calls := notReadyThenTrustedBlocked(t, claudeTrustScreen, claudeReadyScreen, 2)

	if _, err := c.Start("w8P:p1", "rloop-wd-run-1", "claude", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	gets := 0
	for _, call := range calls() {
		if call[1] == "get" {
			gets++
		}
	}
	if gets != 3 {
		t.Fatalf("agent get called %d times, want Start to wait through 2 blocked states until idle", gets)
	}
	if st, err := c.State("rloop-wd-run-1"); err != nil || st != core.AgentIdle {
		t.Fatalf("state after Start = %s, %v", st, err)
	}
}

func TestStartFailsWhenTheTrustedClaudeStaysBlocked(t *testing.T) {
	shrinkPaneBusyWait(t, 20*time.Millisecond)
	c, _ := notReadyThenTrustedBlocked(t, claudeTrustScreen, claudeReadyScreen, 1<<30)

	_, err := c.Start("w8P:p1", "a1", "claude", nil)

	if err == nil || err.Error() != "herdr: agent a1 stays blocked after the trust dialog" {
		t.Fatalf("got %v", err)
	}
}

func TestStartWaitsForClaudesBannerAfterItsTrustDialog(t *testing.T) {
	shrinkPaneBusyWait(t, 5*time.Second)
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	state := filepath.Join(dir, "state")
	screens := []string{claudeTrustScreen, "/repo claude --model sonnet\n", "/repo claude --model sonnet\n", " ▐▛███▛█   Claude Code v2.1.280\n❯\n  -- INSERT --\n"}
	for i, s := range screens {
		if err := os.WriteFile(filepath.Join(dir, "screen"+strconv.Itoa(i)), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\n" +
		"printf '%s\\0' \"$@\" >> \"" + log + "\"\n" +
		"printf '\\n' >> \"" + log + "\"\n" +
		"n=$(cat \"" + state + "\" 2>/dev/null || echo 0)\n" +
		"case \"$2\" in\n" +
		"start) printf '%s' '{\"error\":{\"code\":\"agent_not_ready\",\"message\":\"blocked during startup\"}}' >&2; exit 1 ;;\n" +
		"send-keys) if [ \"$4\" = enter ]; then echo 1 > \"" + state + "\"; fi; printf '%s' '{\"result\":{\"type\":\"ok\"}}' ;;\n" +
		"get) printf '%s' '{\"result\":{\"agent\":{\"agent_status\":\"idle\"}}}' ;;\n" +
		"read) cat \"" + dir + "/screen$n\"; if [ $n -gt 0 ] && [ $n -lt 3 ]; then echo $((n+1)) > \"" + state + "\"; fi ;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Client{Bin: bin}

	if _, err := c.Start("w8P:p1", "rloop-wd-run-1", "claude", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	screen, err := c.exec("agent", "read", "rloop-wd-run-1", "--source", "visible")
	if err != nil || !strings.Contains(string(screen), "Claude Code v") {
		t.Fatalf("Start returned before claude drew its prompt; screen now %q, %v", screen, err)
	}
	data, _ := os.ReadFile(state)
	if strings.TrimSpace(string(data)) != "3" {
		t.Fatalf("Start returned while claude was still restarting (state %q)", data)
	}
}

func TestStartFailsWhenClaudeNeverShowsItsBannerAfterItsTrustDialog(t *testing.T) {
	shrinkPaneBusyWait(t, 20*time.Millisecond)
	c, _ := notReadyThenTrusted(t, claudeTrustScreen, "/repo claude --model sonnet\n")

	_, err := c.Start("w8P:p1", "a1", "claude", nil)

	if err == nil || err.Error() != "herdr: agent a1 never showed claude's prompt after the trust dialog" {
		t.Fatalf("got %v", err)
	}
}

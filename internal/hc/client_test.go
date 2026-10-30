package hc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"r-loop/internal/core"
)

var _ core.SessionHost = Client{}

var _ interface {
	PromptContext(context.Context, string, string, bool, time.Duration) error
} = Client{}

const (
	codexTrustScreen    = "> You are in /repo/.r-loop/wt/phase-1\n\n  Do you trust the contents of this directory? Working with untrusted contents comes with higher risk of prompt\n  injection.\n\n› 1. Yes, continue\n  2. No, quit\n\n  Press enter to continue\n"
	codexNewTrustScreen = "  Folder access\n  /repo/.r-loop/wt/phase-1\n\n  Trust this folder? Codex can read, edit, and run files here, subject to\n  your permission settings. Your trust decision will be saved.\n\n› 1. Trust and continue\n  2. Quit\n\n  enter continue · esc quit\n"
	claudeTrustScreen   = " Accessing workspace:\n /repo\n Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team).\n Claude Code'll be able to read, edit, and execute files here.\n ❯ No, exit\n   Yes, I trust this folder\n Enter to confirm · Esc to cancel\n"
	claudeReadyScreen   = " ▐▛███▛█   Claude Code v2.1.280\n❯\n  -- INSERT --\n"
)

type reply struct {
	out, err string
	exit     int
	sleep    string
}

type call struct {
	argv       []string
	terminalID string
}

const fakeScript = `#!/bin/sh
[ -n "$HC_FAKE_WARM" ] && exit 0
d=${0%/*}
prog=${0##*/}
{ printf '%s\0' "$prog" "$@"; printf 'HC_TERMINAL_ID=%s\n' "${HC_TERMINAL_ID-unset}"; } >> "$d/calls"
if [ "$prog" = hc ]; then key="hc_$1"; elif [ "$1" = do ]; then key="herdr-app_do_$2"; else key="herdr-app_$1"; fi
[ "$key" = hc_keys ] && : > "$d/keyed"
if [ -e "$d/keyed" ] && [ -e "$d/replies/${key}_after_keys.total" ]; then key="${key}_after_keys"; fi
if [ ! -e "$d/replies/$key.total" ]; then echo "fake: no reply for $key" >&2; exit 99; fi
n=0; [ -e "$d/$key.count" ] && read n < "$d/$key.count"; n=$((n+1)); echo $n > "$d/$key.count"
read total < "$d/replies/$key.total"; i=$n; [ $i -gt $total ] && i=$total
f="$d/replies/$key.$i"
if [ -s "$f.sleep" ]; then read s < "$f.sleep"; sleep "$s" & echo $! > "$d/child"; wait; fi
read code < "$f.exit"
while IFS= read -r line || [ -n "$line" ]; do printf '%s\n' "$line"; done < "$f.out"
while IFS= read -r line || [ -n "$line" ]; do printf '%s\n' "$line" >&2; done < "$f.err"
exit "$code"
`

func fakeHosts(t *testing.T, replies map[string][]reply) (Client, func() []call) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "replies"), 0o755); err != nil {
		t.Fatal(err)
	}
	for key, rs := range replies {
		file := filepath.Join("replies", strings.ReplaceAll(key, " ", "_"))
		for i, r := range rs {
			n := file + "." + strconv.Itoa(i+1)
			write(n+".out", r.out, 0o644)
			write(n+".err", r.err, 0o644)
			write(n+".exit", strconv.Itoa(r.exit), 0o644)
			write(n+".sleep", r.sleep, 0o644)
		}
		write(file+".total", strconv.Itoa(len(rs)), 0o644)
	}
	for _, name := range []string{"hc", "herdr-app"} {
		write(name, fakeScript, 0o755)
		warm := exec.Command(filepath.Join(dir, name))
		warm.Env = append(os.Environ(), "HC_FAKE_WARM=1")
		if err := warm.Run(); err != nil {
			t.Fatal(err)
		}
	}
	return Client{HC: filepath.Join(dir, "hc"), App: filepath.Join(dir, "herdr-app")}, func() []call {
		data, err := os.ReadFile(filepath.Join(dir, "calls"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		var calls []call
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			parts := strings.Split(line, "\x00")
			calls = append(calls, call{argv: parts[:len(parts)-1], terminalID: strings.TrimPrefix(parts[len(parts)-1], "HC_TERMINAL_ID=")})
		}
		return calls
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func ok(out string) reply {
	return reply{out: out}
}

func failed(exit int, stderr string) reply {
	return reply{err: stderr, exit: exit}
}

func getJSON(state, status string, argv ...string) string {
	body := map[string]any{
		"address":   "t1d",
		"state":     state,
		"labels":    map[string]string{"rloop.agent": "rl-p1-plan"},
		"status":    nil,
		"status_ms": nil,
		"cols":      120,
		"rows":      40,
	}
	if status != "" {
		body["status"] = map[string]string{"app": "claude-code", "state": status}
		body["status_ms"] = int64(1793345420081)
	}
	if len(argv) > 0 {
		body["foreground"] = map[string]any{"argv": argv, "name": argv[0], "pid": 19860}
	}
	data, _ := json.Marshal(body)
	return string(data)
}

func readJSON(text string) string {
	data, _ := json.Marshal(map[string]any{"complete": true, "how": "screen", "text": text})
	return string(data)
}

func agentFound(t *testing.T) []reply {
	t.Helper()
	return []reply{ok(fixture(t, "ls-one.json"))}
}

func assertEqual[T any](t *testing.T, name string, actual, expected T) {
	t.Helper()
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("%s\nactual   %#v\nexpected %#v", name, actual, expected)
	}
}

func argvs(actual []call) [][]string {
	out := [][]string{}
	for _, c := range actual {
		out = append(out, c.argv)
	}
	return out
}

func assertCalls(t *testing.T, actual []call, expected [][]string) {
	t.Helper()
	assertEqual(t, "calls", argvs(actual), expected)
}

func callsOf(actual []call, prefix ...string) [][]string {
	out := [][]string{}
	for _, c := range actual {
		if len(c.argv) >= len(prefix) && reflect.DeepEqual(c.argv[:len(prefix)], prefix) {
			out = append(out, c.argv)
		}
	}
	return out
}

func assertErrorText(t *testing.T, err error, expected string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error nil, expected %q", expected)
	}
	assertEqual(t, "error", err.Error(), expected)
}

func shrink(t *testing.T, v *time.Duration, d time.Duration) {
	t.Helper()
	old := *v
	*v = d
	t.Cleanup(func() { *v = old })
}

func servedAt(t *testing.T, c Client, key string, n int) func() time.Time {
	t.Helper()
	file := filepath.Join(filepath.Dir(c.HC), strings.ReplaceAll(key, " ", "_")+".count")
	at := make(chan time.Time, 1)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			data, _ := os.ReadFile(file)
			if got, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && got >= n {
				at <- time.Now()
				return
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	return func() time.Time {
		t.Helper()
		select {
		case when := <-at:
			return when
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never served %d times", key, n)
			return time.Time{}
		}
	}
}

func childPID(t *testing.T, c Client) int {
	t.Helper()
	file := filepath.Join(filepath.Dir(c.HC), "child")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(file)
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the fake never started a sleeping child")
	return 0
}

func assertExits(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child %d still running", pid)
}

func TestEveryCallRunsWithoutHCTerminalID(t *testing.T) {
	// given
	t.Setenv("HC_TERMINAL_ID", "t9")
	c, calls := fakeHosts(t, map[string][]reply{
		"hc server":        {ok(fixture(t, "server-status.json"))},
		"herdr-app status": {ok(fixture(t, "app-status.json"))},
	})

	// when
	err := c.Reachable()

	// then
	assertEqual(t, "error", err, nil)
	actual := calls()
	assertEqual(t, "terminal ids", []string{actual[0].terminalID, actual[1].terminalID}, []string{"unset", "unset"})
}

func TestReachableAsksTheServerThenTheWindow(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc server":        {ok(fixture(t, "server-status.json"))},
		"herdr-app status": {ok(fixture(t, "app-status.json"))},
	})

	// when
	err := c.Reachable()

	// then
	assertEqual(t, "error", err, nil)
	assertCalls(t, calls(), [][]string{{"hc", "server", "status", "--json"}, {"herdr-app", "status"}})
}

func TestReachableReturnsTheServerErrorWithoutAskingTheWindow(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc server":        {failed(1, `{"error":{"kind":"unavailable","message":"no server","hint":""}}`)},
		"herdr-app status": {ok(fixture(t, "app-status.json"))},
	})

	// when
	err := c.Reachable()

	// then
	assertErrorText(t, err, "hc: unavailable: no server")
	assertCalls(t, calls(), [][]string{{"hc", "server", "status", "--json"}})
}

func TestAnHCErrorReadsAsKindAndMessage(t *testing.T) {
	// given
	c, _ := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc read": {failed(1, `{"error":{"kind":"spawn_failed","message":"boom","hint":""}}`)},
	})

	// when
	_, err := c.Screen("rl-p1-plan")

	// then
	assertErrorText(t, err, "hc: spawn_failed: boom")
	var actual Error
	assertEqual(t, "errors.As", errors.As(err, &actual), true)
	assertEqual(t, "hc error", actual, Error{Kind: "spawn_failed", Message: "boom"})
	assertEqual(t, "wraps ErrNotFound", errors.Is(err, core.ErrNotFound), false)
}

func TestExitThreeWrapsErrNotFound(t *testing.T) {
	cases := []struct {
		name, stderr, expected string
	}{
		{"json", `{"error":{"kind":"not_found","message":"no terminal \"t1d\""}}`, `hc: not_found: no terminal "t1d"`},
		{"bare", "gone", "hc: not_found: gone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			c, _ := fakeHosts(t, map[string][]reply{
				"hc ls":   agentFound(t),
				"hc read": {failed(3, tc.stderr)},
			})

			// when
			_, err := c.Screen("rl-p1-plan")

			// then
			assertErrorText(t, err, tc.expected)
			assertEqual(t, "wraps ErrNotFound", errors.Is(err, core.ErrNotFound), true)
		})
	}
}

func TestExitNineIsABlockedErrorWrappingErrAgentBlocked(t *testing.T) {
	// given
	c, _ := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc send": {failed(9, `{"error":{"kind":"blocked","message":"t1d is blocked","hint":""}}`)},
	})

	// when
	err := c.Prompt("rl-p1-plan", "go", false, 0)

	// then
	assertErrorText(t, err, "hc: blocked: t1d is blocked")
	assertEqual(t, "wraps ErrAgentBlocked", errors.Is(err, core.ErrAgentBlocked), true)
}

func TestAHerdrAppErrorReadsAsItsMessage(t *testing.T) {
	// given
	c, _ := fakeHosts(t, map[string][]reply{
		"herdr-app ls":            {ok(fixture(t, "app-ls.json"))},
		"herdr-app do pane.close": {{out: `{"error":"no pane \"pSPLIT\""}`, exit: 1}},
	})

	// when
	err := c.ClosePane("pSPLIT")

	// then
	assertErrorText(t, err, `herdr-app: no pane "pSPLIT"`)
}

func TestANonWaitingSendGetsOnlyTheCallTimeout(t *testing.T) {
	// given
	shrink(t, &callTimeout, 200*time.Millisecond)
	c, _ := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc send": {{out: `{"started":true,"turn":1}`, sleep: "30"}},
	})

	// when
	err := c.Submit("rl-p1-plan", "go")

	// then
	assertErrorText(t, err, "hc send --json t1d: timed out after 200ms")
	assertExits(t, childPID(t, c))
}

func TestAWedgedCallFailsAfterTheCallTimeoutNamingTheCommand(t *testing.T) {
	// given
	shrink(t, &callTimeout, 200*time.Millisecond)
	c, _ := fakeHosts(t, map[string][]reply{
		"hc server": {{out: fixture(t, "server-status.json"), sleep: "30"}},
	})

	// when
	err := c.Reachable()

	// then
	assertErrorText(t, err, "hc server status --json: timed out after 200ms")
	assertExits(t, childPID(t, c))
}

func TestAWaitingPromptGetsItsWaitOnTopOfTheCallTimeout(t *testing.T) {
	// given
	shrink(t, &callTimeout, 200*time.Millisecond)
	c, _ := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc send": {{out: `{"started":true,"turn":1,"wait":{"ended":true,"reason":"status","status":"idle","turn":1}}`, sleep: "0.5"}},
	})

	// when
	err := c.Prompt("rl-p1-plan", "go", true, 2*time.Second)

	// then
	assertEqual(t, "error", err, nil)
}

func TestCancellingAPromptStopsTheCLIAtEveryStage(t *testing.T) {
	notStarted := ok(`{"started":false,"turn":1}`)
	cases := []struct {
		name    string
		replies func(t *testing.T) map[string][]reply
	}{
		{"lookup", func(t *testing.T) map[string][]reply {
			return map[string][]reply{"hc ls": {{out: fixture(t, "ls-one.json"), sleep: "30"}}}
		}},
		{"send", func(t *testing.T) map[string][]reply {
			return map[string][]reply{"hc ls": agentFound(t), "hc send": {{out: `{"started":true,"turn":1}`, sleep: "30"}}}
		}},
		{"retry enter", func(t *testing.T) map[string][]reply {
			return map[string][]reply{"hc ls": agentFound(t), "hc send": {notStarted}, "hc keys": {{out: `{"ok":true}`, sleep: "30"}}}
		}},
		{"retry wait", func(t *testing.T) map[string][]reply {
			return map[string][]reply{"hc ls": agentFound(t), "hc send": {notStarted}, "hc keys": {ok(`{"ok":true}`)}, "hc wait": {{out: `{"ended":true,"reason":"status","status":"working","turn":2}`, sleep: "30"}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			c, _ := fakeHosts(t, tc.replies(t))
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- c.PromptContext(ctx, "rl-p1-plan", "go", false, 0) }()
			pid := childPID(t, c)

			// when
			cancel()

			// then
			select {
			case err := <-done:
				assertEqual(t, "canceled", errors.Is(err, context.Canceled), true)
			case <-time.After(5 * time.Second):
				t.Fatal("PromptContext still running after cancellation")
			}
			assertExits(t, pid)
		})
	}
}

func TestUnparsableHCOutputIsAnError(t *testing.T) {
	// given
	c, _ := fakeHosts(t, map[string][]reply{
		"hc server": {ok("not json")},
	})

	// when
	err := c.Reachable()

	// then
	assertEqual(t, "prefix", err != nil && strings.HasPrefix(err.Error(), "hc server: "), true)
}

const shellArgvRepo = `["env","A=b","R_LOOP_RUN=r1","/bin/sh","-c","cd \"$1\" && exec \"$SHELL\" -l","sh","/repo/wt"]`

const shellArgvPlain = `["env","/bin/sh","-c","cd \"$1\" && exec \"$SHELL\" -l","sh","/repo/wt"]`

func restores() map[string][]reply {
	return map[string][]reply{
		"herdr-app do space.select": {ok(`{"ok":true}`)},
		"herdr-app do tab.select":   {ok(`{"ok":true}`)},
		"herdr-app do pane.focus":   {ok(`{"ok":true}`)},
	}
}

func with(base map[string][]reply, more map[string][]reply) map[string][]reply {
	for k, v := range more {
		base[k] = v
	}
	return base
}

func TestOpenCreatesASpaceThenPutsTheFocusBack(t *testing.T) {
	// given
	c, calls := fakeHosts(t, with(restores(), map[string][]reply{
		"herdr-app status":       {ok(fixture(t, "app-status.json"))},
		"herdr-app do space.new": {ok(fixture(t, "space-new.json"))},
		"herdr-app ls":           {ok(fixture(t, "app-ls.json"))},
	}))

	// when
	actual, err := c.Open(core.OpenSpec{CWD: "/repo/wt", Label: "phase-3", Env: map[string]string{"R_LOOP_RUN": "r1", "A": "b"}})

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "workspace", actual, core.Workspace{ID: "spRUN", RootPane: "pROOT"})
	assertCalls(t, calls(), [][]string{
		{"herdr-app", "status"},
		{"herdr-app", "do", "space.new", "--name", "phase-3", "--folder", "/repo/wt", "--argv", shellArgvRepo},
		{"herdr-app", "do", "space.select", "--id", "spUSER"},
		{"herdr-app", "do", "tab.select", "--id", "tbUSER"},
		{"herdr-app", "do", "pane.focus", "--pane", "pUSER"},
		{"herdr-app", "ls"},
	})
}

func TestOpenTabCreatesATabThenPutsTheFocusBack(t *testing.T) {
	// given
	c, calls := fakeHosts(t, with(restores(), map[string][]reply{
		"herdr-app status":     {ok(fixture(t, "app-status.json"))},
		"herdr-app do tab.new": {ok(fixture(t, "tab-new.json"))},
		"herdr-app ls":         {ok(fixture(t, "app-ls.json"))},
	}))

	// when
	actual, err := c.OpenTab("spRUN", core.OpenSpec{CWD: "/repo/wt", Label: "review"})

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "pane", actual, "pTAB")
	assertCalls(t, calls(), [][]string{
		{"herdr-app", "ls"},
		{"herdr-app", "status"},
		{"herdr-app", "do", "tab.new", "--name", "review", "--space", "spRUN", "--argv", shellArgvPlain},
		{"herdr-app", "do", "space.select", "--id", "spUSER"},
		{"herdr-app", "do", "tab.select", "--id", "tbUSER"},
		{"herdr-app", "do", "pane.focus", "--pane", "pUSER"},
		{"herdr-app", "ls"},
	})
}

func TestOpenTabOfASpaceMissingFromLsWrapsErrNotFound(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"herdr-app ls": {ok(fixture(t, "app-ls.json"))},
	})

	// when
	_, err := c.OpenTab("spGONE", core.OpenSpec{CWD: "/repo/wt", Label: "review"})

	// then
	assertErrorText(t, err, "herdr-app: space spGONE: not found")
	assertEqual(t, "wraps ErrNotFound", errors.Is(err, core.ErrNotFound), true)
	assertCalls(t, calls(), [][]string{{"herdr-app", "ls"}})
}

func TestOpenFailsWhenTheFocusCannotBePutBack(t *testing.T) {
	// given
	c, calls := fakeHosts(t, with(restores(), map[string][]reply{
		"herdr-app status":          {ok(fixture(t, "app-status.json"))},
		"herdr-app do space.new":    {ok(fixture(t, "space-new.json"))},
		"herdr-app do space.select": {{out: `{"error":"no such space"}`, exit: 1}},
		"herdr-app ls":              {ok(fixture(t, "app-ls.json"))},
	}))

	// when
	_, err := c.Open(core.OpenSpec{CWD: "/repo/wt", Label: "phase-3"})

	// then
	assertErrorText(t, err, "restore focus: herdr-app: no such space")
	assertEqual(t, "ls calls", callsOf(calls(), "herdr-app", "ls"), [][]string{})
}

func TestOpenOfATerminalMissingFromLsWrapsErrNotFound(t *testing.T) {
	// given
	c, _ := fakeHosts(t, with(restores(), map[string][]reply{
		"herdr-app status":       {ok(fixture(t, "app-status.json"))},
		"herdr-app do space.new": {ok(`{"space":"spRUN","tab":"tbRUN1","terminal":"t9z"}`)},
		"herdr-app ls":           {ok(fixture(t, "app-ls.json"))},
	}))

	// when
	_, err := c.Open(core.OpenSpec{CWD: "/repo/wt", Label: "phase-3"})

	// then
	assertErrorText(t, err, "herdr-app: terminal t9z: not found")
	assertEqual(t, "wraps ErrNotFound", errors.Is(err, core.ErrNotFound), true)
}

func splitReplies(t *testing.T) map[string][]reply {
	return map[string][]reply{
		"herdr-app ls":            {ok(fixture(t, "app-ls.json"))},
		"herdr-app do pane.split": {ok(fixture(t, "pane-split.json"))},
	}
}

func TestShellArgvSortsTheVariablesAfterEnv(t *testing.T) {
	// given
	c, calls := fakeHosts(t, splitReplies(t))

	// when
	_, err := c.Split("pROOT", "right", "/repo/wt", 0, map[string]string{"R_LOOP_STEP": "implement", "R_LOOP_RUN": "r1"})

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "argv", callsOf(calls(), "herdr-app", "do", "pane.split")[0][8], `["env","R_LOOP_RUN=r1","R_LOOP_STEP=implement","/bin/sh","-c","cd \"$1\" && exec \"$SHELL\" -l","sh","/repo/wt"]`)
}

func TestShellArgvStartsWithEnvWithoutVariables(t *testing.T) {
	// given
	c, calls := fakeHosts(t, splitReplies(t))

	// when
	_, err := c.Split("pROOT", "down", "/repo", 0, nil)

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "argv", callsOf(calls(), "herdr-app", "do", "pane.split")[0][8], `["env","/bin/sh","-c","cd \"$1\" && exec \"$SHELL\" -l","sh","/repo"]`)
}

func TestSplitRunsPaneSplitAndReturnsTheNewPane(t *testing.T) {
	// given
	c, calls := fakeHosts(t, splitReplies(t))

	// when
	actual, err := c.Split("pROOT", "right", "/repo/wt", 0, nil)

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "pane", actual, "pSPLIT")
	assertCalls(t, calls(), [][]string{
		{"herdr-app", "ls"},
		{"herdr-app", "do", "pane.split", "--direction", "right", "--pane", "pROOT", "--argv", shellArgvPlain},
	})
}

func TestSplitOfAPaneMissingFromLsWrapsErrNotFound(t *testing.T) {
	// given
	c, calls := fakeHosts(t, splitReplies(t))

	// when
	_, err := c.Split("pGONE", "right", "/repo/wt", 0, nil)

	// then
	assertErrorText(t, err, "herdr-app: pane pGONE: not found")
	assertEqual(t, "wraps ErrNotFound", errors.Is(err, core.ErrNotFound), true)
	assertCalls(t, calls(), [][]string{{"herdr-app", "ls"}})
}

func TestSplitOfAnEmptyPaneSplitsTheFocusedPane(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"herdr-app status":        {ok(fixture(t, "app-status.json"))},
		"herdr-app do pane.split": {ok(fixture(t, "pane-split.json"))},
	})

	// when
	actual, err := c.Split("", "down", "/repo/wt", 0, nil)

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "pane", actual, "pSPLIT")
	assertCalls(t, calls(), [][]string{
		{"herdr-app", "status"},
		{"herdr-app", "do", "pane.split", "--direction", "down", "--pane", "pUSER", "--argv", shellArgvPlain},
	})
}

func TestSplitIgnoresTheRatio(t *testing.T) {
	// given
	c, calls := fakeHosts(t, splitReplies(t))

	// when
	_, err := c.Split("pROOT", "right", "/repo/wt", 0.3, nil)

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "split", callsOf(calls(), "herdr-app", "do", "pane.split"), [][]string{
		{"herdr-app", "do", "pane.split", "--direction", "right", "--pane", "pROOT", "--argv", shellArgvPlain},
	})
}

func TestClosePaneClosesAPaneInLs(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"herdr-app ls":            {ok(fixture(t, "app-ls.json"))},
		"herdr-app do pane.close": {ok(fixture(t, "ok.json"))},
	})

	// when
	err := c.ClosePane("pSPLIT")

	// then
	assertEqual(t, "error", err, nil)
	assertCalls(t, calls(), [][]string{{"herdr-app", "ls"}, {"herdr-app", "do", "pane.close", "--pane", "pSPLIT"}})
}

func TestClosePaneOfAPaneMissingFromLsWrapsErrNotFound(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"herdr-app ls": {ok(fixture(t, "app-ls.json"))},
	})

	// when
	err := c.ClosePane("pGONE")

	// then
	assertEqual(t, "wraps ErrNotFound", errors.Is(err, core.ErrNotFound), true)
	assertCalls(t, calls(), [][]string{{"herdr-app", "ls"}})
}

func TestCloseClosesTheSpaceConfirmed(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"herdr-app ls":             {ok(fixture(t, "app-ls.json"))},
		"herdr-app do space.close": {ok(fixture(t, "ok.json"))},
	})

	// when
	err := c.Close("spRUN")

	// then
	assertEqual(t, "error", err, nil)
	assertCalls(t, calls(), [][]string{{"herdr-app", "ls"}, {"herdr-app", "do", "space.close", "--id", "spRUN", "--confirmed", "true"}})
}

func TestCloseOfASpaceMissingFromLsWrapsErrNotFound(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"herdr-app ls": {ok(fixture(t, "app-ls.json"))},
	})

	// when
	err := c.Close("spGONE")

	// then
	assertEqual(t, "wraps ErrNotFound", errors.Is(err, core.ErrNotFound), true)
	assertCalls(t, calls(), [][]string{{"herdr-app", "ls"}})
}

func TestPaneInfoTakesTheSpaceFromLsAndTheSizeFromHC(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"herdr-app ls": {ok(fixture(t, "app-ls.json"))},
		"hc get":       {ok(fixture(t, "get-shell.json"))},
	})

	// when
	actual, err := c.PaneInfo("pSPLIT")

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "info", actual, core.PaneInfo{Workspace: "spRUN", Cols: 120, Rows: 40})
	assertCalls(t, calls(), [][]string{{"herdr-app", "ls"}, {"hc", "get", "--json", "t1d"}})
}

func TestPaneInfoOfAPaneMissingFromLsWrapsErrNotFound(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"herdr-app ls": {ok(fixture(t, "app-ls.json"))},
		"hc get":       {ok(fixture(t, "get-shell.json"))},
	})

	// when
	_, err := c.PaneInfo("pGONE")

	// then
	assertEqual(t, "wraps ErrNotFound", errors.Is(err, core.ErrNotFound), true)
	assertCalls(t, calls(), [][]string{{"herdr-app", "ls"}})
}

func TestAnAgentOnOneLiveTerminalIsThatTerminal(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc read": {ok(fixture(t, "read-shell.json"))},
	})

	// when
	actual, err := c.Screen("rl-p1-plan")

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "screen", actual, "/repo/wt % ")
	assertCalls(t, calls(), [][]string{
		{"hc", "ls", "--json", "--label", "rloop.agent=rl-p1-plan"},
		{"hc", "read", "--json", "t1d"},
	})
}

func TestAnAgentOnNoTerminalIsNotFound(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   {ok(fixture(t, "ls-none.json"))},
		"hc read": {ok(fixture(t, "read-shell.json"))},
	})

	// when
	_, err := c.Screen("rl-p1-plan")

	// then
	assertErrorText(t, err, "hc: not_found: no live terminal labelled rloop.agent=rl-p1-plan")
	assertEqual(t, "wraps ErrNotFound", errors.Is(err, core.ErrNotFound), true)
	assertEqual(t, "reads", callsOf(calls(), "hc", "read"), [][]string{})
}

func TestAnAgentOnTwoTerminalsIsAnErrorNamingThem(t *testing.T) {
	// given
	c, _ := fakeHosts(t, map[string][]reply{
		"hc ls": {ok(fixture(t, "ls-two.json"))},
	})

	// when
	_, err := c.Screen("rl-p1-plan")

	// then
	assertErrorText(t, err, "hc: agent rl-p1-plan is on more than one terminal: t3, t7")
	assertEqual(t, "wraps ErrNotFound", errors.Is(err, core.ErrNotFound), false)
}

func startReplies(t *testing.T, gets ...string) map[string][]reply {
	t.Helper()
	getReplies := []reply{}
	for _, g := range gets {
		getReplies = append(getReplies, ok(g))
	}
	return map[string][]reply{
		"herdr-app ls": {ok(fixture(t, "app-ls.json"))},
		"hc ls":        {ok(fixture(t, "ls-none.json")), ok(fixture(t, "ls-one.json"))},
		"hc label":     {ok(fixture(t, "ok.json"))},
		"hc send":      {ok(`{"started":null,"turn":0}`)},
		"hc keys":      {ok(fixture(t, "ok.json"))},
		"hc get":       getReplies,
		"hc read":      {ok(readJSON(claudeReadyScreen))},
	}
}

func TestStartLabelsTheTerminalTypesExecAndReturnsOnceTheAgentIsIdle(t *testing.T) {
	// given
	shell := fixture(t, "get-shell.json")
	c, calls := fakeHosts(t, startReplies(t, shell, shell, fixture(t, "get-claude-idle.json")))

	// when
	actual, err := c.Start("pSPLIT", "rl-p1-plan", "claude", []string{"--model", "haiku"})

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "agent", actual, core.Agent{Name: "rl-p1-plan", Pane: "pSPLIT"})
	assertCalls(t, calls()[:5], [][]string{
		{"herdr-app", "ls"},
		{"hc", "ls", "--json", "--label", "rloop.agent=rl-p1-plan"},
		{"hc", "label", "--json", "t1d", "rloop.agent=rl-p1-plan"},
		{"hc", "get", "--json", "t1d"},
		{"hc", "send", "--json", "--force", "t1d", "exec 'claude' '--model' 'haiku'"},
	})
}

func TestStartQuotesEachWordForAPOSIXShell(t *testing.T) {
	// given
	c, calls := fakeHosts(t, startReplies(t, fixture(t, "get-shell.json"), getJSON("live", "idle", "codex")))

	// when
	_, err := c.Start("pSPLIT", "rl-p1-plan", "codex", []string{"-c", "it's a test"})

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "send text", callsOf(calls(), "hc", "send")[0][5], `exec 'codex' '-c' 'it'\''s a test'`)
}

func TestStartFailsWhenTheNameIsAlreadyOnALiveTerminal(t *testing.T) {
	// given
	c, calls := fakeHosts(t, with(startReplies(t, fixture(t, "get-shell.json")), map[string][]reply{
		"hc ls": agentFound(t),
	}))

	// when
	_, err := c.Start("pSPLIT", "rl-p1-plan", "claude", nil)

	// then
	assertErrorText(t, err, "hc: agent rl-p1-plan already exists")
	assertCalls(t, calls(), [][]string{
		{"herdr-app", "ls"},
		{"hc", "ls", "--json", "--label", "rloop.agent=rl-p1-plan"},
	})
}

func TestStartOfAPaneMissingFromLsWrapsErrNotFound(t *testing.T) {
	// given
	c, calls := fakeHosts(t, startReplies(t, fixture(t, "get-shell.json")))

	// when
	_, err := c.Start("pGONE", "rl-p1-plan", "claude", nil)

	// then
	assertEqual(t, "wraps ErrNotFound", errors.Is(err, core.ErrNotFound), true)
	assertCalls(t, calls(), [][]string{{"herdr-app", "ls"}})
}

func TestStartFailsWhenTheProgramNeverReplacesTheShell(t *testing.T) {
	// given
	shrink(t, &paneBusyBudget, 300*time.Millisecond)
	shrink(t, &paneBusyBackoff, 50*time.Millisecond)
	c, _ := fakeHosts(t, startReplies(t, fixture(t, "get-shell.json")))

	// when
	_, err := c.Start("pSPLIT", "rl-p1-plan", "claude", nil)

	// then
	assertErrorText(t, err, "hc: agent rl-p1-plan never started claude")
}

func TestStartFailsWhenTheTerminalFinishesBeforeTheProgramStarts(t *testing.T) {
	// given
	shrink(t, &paneBusyBackoff, 50*time.Millisecond)
	c, _ := fakeHosts(t, startReplies(t, fixture(t, "get-shell.json"), getJSON("finished", "")))

	// when
	_, err := c.Start("pSPLIT", "rl-p1-plan", "claude", nil)

	// then
	assertErrorText(t, err, "hc: agent rl-p1-plan exited before it started")
}

func TestStartKeepsWaitingWhileALiveTerminalShowsNoForeground(t *testing.T) {
	// given
	shrink(t, &paneBusyBackoff, 50*time.Millisecond)
	c, _ := fakeHosts(t, startReplies(t, getJSON("live", ""), getJSON("live", ""), fixture(t, "get-claude-idle.json")))

	// when
	actual, err := c.Start("pSPLIT", "rl-p1-plan", "claude", nil)

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "agent", actual, core.Agent{Name: "rl-p1-plan", Pane: "pSPLIT"})
}

func TestStartAnswersEachTrustDialogWithItsKeys(t *testing.T) {
	cases := []struct {
		name, screen string
		expected     [][]string
	}{
		{"claude", claudeTrustScreen, [][]string{{"hc", "keys", "--json", "t1d", "down"}, {"hc", "keys", "--json", "t1d", "enter"}}},
		{"codex trust this folder", codexNewTrustScreen, [][]string{{"hc", "keys", "--json", "t1d", "enter"}}},
		{"codex trust the contents", codexTrustScreen, [][]string{{"hc", "keys", "--json", "t1d", "enter"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			shrink(t, &trustSettle, 50*time.Millisecond)
			shrink(t, &paneBusyBackoff, 20*time.Millisecond)
			c, calls := fakeHosts(t, with(startReplies(t, fixture(t, "get-shell.json"), fixture(t, "get-claude-idle.json")), map[string][]reply{
				"hc read":            {ok(readJSON(tc.screen))},
				"hc read after keys": {ok(readJSON(claudeReadyScreen))},
			}))

			// when
			_, err := c.Start("pSPLIT", "rl-p1-plan", "claude", nil)

			// then
			assertEqual(t, "error", err, nil)
			assertEqual(t, "keys", callsOf(calls(), "hc", "keys"), tc.expected)
		})
	}
}

func TestStartWaitsAQuietWindowAfterTheTrustAnswer(t *testing.T) {
	// given
	shrink(t, &trustSettle, 700*time.Millisecond)
	c, _ := fakeHosts(t, with(startReplies(t, fixture(t, "get-shell.json"), fixture(t, "get-claude-idle.json")), map[string][]reply{
		"hc read":            {ok(readJSON(claudeTrustScreen))},
		"hc read after keys": {ok(readJSON(claudeReadyScreen))},
		"hc get after keys":  {ok(fixture(t, "get-claude-idle.json"))},
	}))
	answered := servedAt(t, c, "hc keys", 2)

	// when
	_, err := c.Start("pSPLIT", "rl-p1-plan", "claude", nil)

	// then
	returned := time.Now()
	assertEqual(t, "error", err, nil)
	assertEqual(t, "quiet window held", returned.Sub(answered()) >= 650*time.Millisecond, true)
}

func TestStartRestartsTheQuietWindowWhenTheSequenceMoves(t *testing.T) {
	// given
	shrink(t, &trustSettle, 700*time.Millisecond)
	idleAt := func(seq int) reply {
		return ok(`{"address":"t1d","state":"live","labels":{"rloop.agent":"rl-p1-plan"},"foreground":{"argv":["claude"]},"status":{"app":"claude-code","state":"idle"},"status_ms":` + strconv.Itoa(seq) + `,"cols":120,"rows":40}`)
	}
	c, _ := fakeHosts(t, with(startReplies(t, fixture(t, "get-shell.json"), fixture(t, "get-claude-idle.json")), map[string][]reply{
		"hc read":            {ok(readJSON(claudeTrustScreen))},
		"hc read after keys": {ok(readJSON(claudeReadyScreen))},
		"hc get after keys":  {idleAt(1), idleAt(1), idleAt(2)},
	}))
	moved := servedAt(t, c, "hc get after keys", 3)

	// when
	_, err := c.Start("pSPLIT", "rl-p1-plan", "claude", nil)

	// then
	returned := time.Now()
	assertEqual(t, "error", err, nil)
	assertEqual(t, "quiet window restarted", returned.Sub(moved()) >= 650*time.Millisecond, true)
}

func TestStartReturnsOnTheFirstIdleWithoutADialog(t *testing.T) {
	// given
	shrink(t, &paneBusyBackoff, 20*time.Millisecond)
	c, calls := fakeHosts(t, startReplies(t, fixture(t, "get-shell.json"), fixture(t, "get-claude-idle.json")))

	// when
	_, err := c.Start("pSPLIT", "rl-p1-plan", "claude", nil)

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "keys", callsOf(calls(), "hc", "keys"), [][]string{})
	assertEqual(t, "gets", callsOf(calls(), "hc", "get"), [][]string{
		{"hc", "get", "--json", "t1d"},
		{"hc", "get", "--json", "t1d"},
		{"hc", "get", "--json", "t1d"},
	})
}

func TestStartAcceptsDoneAsReady(t *testing.T) {
	// given
	shrink(t, &paneBusyBackoff, 20*time.Millisecond)
	c, calls := fakeHosts(t, startReplies(t, fixture(t, "get-shell.json"), getJSON("live", "done", "claude")))

	// when
	_, err := c.Start("pSPLIT", "rl-p1-plan", "claude", nil)

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "keys", callsOf(calls(), "hc", "keys"), [][]string{})
}

func TestStartAnswersATrustDialogFoundOnlyInHistory(t *testing.T) {
	// given
	shrink(t, &trustSettle, 50*time.Millisecond)
	shrink(t, &paneBusyBackoff, 20*time.Millisecond)
	c, calls := fakeHosts(t, with(startReplies(t, fixture(t, "get-shell.json"), fixture(t, "get-claude-idle.json")), map[string][]reply{
		"hc read":            {ok(readJSON(claudeReadyScreen)), ok(readJSON(codexTrustScreen))},
		"hc read after keys": {ok(readJSON(claudeReadyScreen))},
	}))

	// when
	_, err := c.Start("pSPLIT", "rl-p1-plan", "codex", nil)

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "history read", callsOf(calls(), "hc", "read", "--json", "--lines"), [][]string{{"hc", "read", "--json", "--lines", "200", "t1d"}})
	assertEqual(t, "keys", callsOf(calls(), "hc", "keys"), [][]string{{"hc", "keys", "--json", "t1d", "enter"}})
}

func TestStartFailsWhenTheTrustDialogDoesNotClear(t *testing.T) {
	// given
	shrink(t, &paneBusyBudget, 500*time.Millisecond)
	shrink(t, &paneBusyBackoff, 50*time.Millisecond)
	c, _ := fakeHosts(t, with(startReplies(t, fixture(t, "get-shell.json"), fixture(t, "get-claude-idle.json")), map[string][]reply{
		"hc read": {ok(readJSON(codexTrustScreen))},
	}))

	// when
	_, err := c.Start("pSPLIT", "rl-p1-plan", "codex", nil)

	// then
	assertErrorText(t, err, "hc: agent rl-p1-plan still asks to trust its directory")
}

func TestStartFailsWhenTheTrustedAgentStaysBlocked(t *testing.T) {
	// given
	shrink(t, &paneBusyBudget, 500*time.Millisecond)
	shrink(t, &paneBusyBackoff, 50*time.Millisecond)
	c, _ := fakeHosts(t, with(startReplies(t, fixture(t, "get-shell.json"), fixture(t, "get-claude-idle.json")), map[string][]reply{
		"hc read":            {ok(readJSON(codexTrustScreen))},
		"hc read after keys": {ok(readJSON(claudeReadyScreen))},
		"hc get after keys":  {ok(getJSON("live", "blocked", "codex"))},
	}))

	// when
	_, err := c.Start("pSPLIT", "rl-p1-plan", "codex", nil)

	// then
	assertErrorText(t, err, "hc: agent rl-p1-plan stays blocked after the trust dialog")
}

func TestStartNeverPressesKeysOnAnUnrecognisedBlockedScreen(t *testing.T) {
	// given
	shrink(t, &paneBusyBudget, 500*time.Millisecond)
	shrink(t, &paneBusyBackoff, 50*time.Millisecond)
	c, calls := fakeHosts(t, with(startReplies(t, fixture(t, "get-shell.json"), getJSON("live", "blocked", "claude")), map[string][]reply{
		"hc read": {ok(readJSON("Allow this tool call? [y/n]\n"))},
	}))

	// when
	_, err := c.Start("pSPLIT", "rl-p1-plan", "claude", nil)

	// then
	assertErrorText(t, err, "hc: agent rl-p1-plan never became ready (state blocked)")
	assertEqual(t, "keys", callsOf(calls(), "hc", "keys"), [][]string{})
}

func TestStateSeqMapsHCStatus(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		expectedState core.AgentState
		expectedSeq   int64
	}{
		{"idle", getJSON("live", "idle", "claude"), core.AgentIdle, 1793345420081},
		{"working", getJSON("live", "working", "claude"), core.AgentWorking, 1793345420081},
		{"blocked", getJSON("live", "blocked", "claude"), core.AgentBlocked, 1793345420081},
		{"done", getJSON("live", "done", "claude"), core.AgentDone, 1793345420081},
		{"error", getJSON("live", "error", "claude"), core.AgentUnknown, 1793345420081},
		{"null status", getJSON("live", "", "zsh"), core.AgentUnknown, 0},
		{"finished", getJSON("finished", ""), core.AgentGone, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			c, _ := fakeHosts(t, map[string][]reply{
				"hc ls":  agentFound(t),
				"hc get": {ok(tc.body)},
			})

			// when
			actualState, actualSeq, err := c.StateSeq("rl-p1-plan")

			// then
			assertEqual(t, "error", err, nil)
			assertEqual(t, "state", actualState, tc.expectedState)
			assertEqual(t, "seq", actualSeq, tc.expectedSeq)
		})
	}
}

func TestStateOfAnAgentOnNoTerminalIsGone(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":  {ok(fixture(t, "ls-none.json"))},
		"hc get": {ok(fixture(t, "get-claude-idle.json"))},
	})

	// when
	actual, err := c.State("rl-p1-plan")

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "state", actual, core.AgentGone)
	assertEqual(t, "gets", callsOf(calls(), "hc", "get"), [][]string{})
}

func TestStateOfATerminalHCNoLongerKnowsIsGone(t *testing.T) {
	// given
	c, _ := fakeHosts(t, map[string][]reply{
		"hc ls":  agentFound(t),
		"hc get": {failed(3, `{"error":{"kind":"not_found","message":"no terminal \"t1d\""}}`)},
	})

	// when
	actualState, actualSeq, err := c.StateSeq("rl-p1-plan")

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "state", actualState, core.AgentGone)
	assertEqual(t, "seq", actualSeq, int64(0))
}

func TestAgentPaneIsThePaneOfTheAgentsTerminal(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":        agentFound(t),
		"herdr-app ls": {ok(fixture(t, "app-ls.json"))},
	})

	// when
	actual, err := c.AgentPane("rl-p1-plan")

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "pane", actual, "pSPLIT")
	assertCalls(t, calls(), [][]string{{"hc", "ls", "--json", "--label", "rloop.agent=rl-p1-plan"}, {"herdr-app", "ls"}})
}

func TestAgentPaneOfAnAgentOnNoTerminalIsEmpty(t *testing.T) {
	// given
	c, _ := fakeHosts(t, map[string][]reply{
		"hc ls":        {ok(fixture(t, "ls-none.json"))},
		"herdr-app ls": {ok(fixture(t, "app-ls.json"))},
	})

	// when
	actual, err := c.AgentPane("rl-p1-plan")

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "pane", actual, "")
}

func TestAgentPaneOfATerminalWithoutAPaneIsEmpty(t *testing.T) {
	// given
	c, _ := fakeHosts(t, map[string][]reply{
		"hc ls":        {ok(`{"hidden_finished":0,"terminals":[{"address":"t9z","state":"live"}]}`)},
		"herdr-app ls": {ok(fixture(t, "app-ls.json"))},
	})

	// when
	actual, err := c.AgentPane("rl-p1-plan")

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "pane", actual, "")
}

func TestPromptWithoutWaitSendsTheText(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc send": {ok(`{"started":true,"status":"idle","turn":0}`)},
	})

	// when
	err := c.Prompt("rl-p1-plan", "review the plan", false, 0)

	// then
	assertEqual(t, "error", err, nil)
	assertCalls(t, calls(), [][]string{
		{"hc", "ls", "--json", "--label", "rloop.agent=rl-p1-plan"},
		{"hc", "send", "--json", "t1d", "--", "review the plan"},
	})
}

func TestPromptTextStartingWithADashFollowsTheEndOfOptions(t *testing.T) {
	cases := []struct {
		name     string
		wait     bool
		expected []string
	}{
		{"without wait", false, []string{"hc", "send", "--json", "t1d", "--", "- item"}},
		{"with wait", true, []string{"hc", "send", "--json", "--wait", "--timeout", "90", "t1d", "--", "- item"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			c, calls := fakeHosts(t, map[string][]reply{
				"hc ls":   agentFound(t),
				"hc send": {ok(`{"started":true,"turn":1,"wait":{"ended":true,"reason":"status","status":"idle","turn":1}}`)},
			})

			// when
			err := c.Prompt("rl-p1-plan", "- item", tc.wait, 90*time.Second)

			// then
			assertEqual(t, "error", err, nil)
			assertEqual(t, "send", callsOf(calls(), "hc", "send"), [][]string{tc.expected})
		})
	}
}

func TestSubmitTextStartingWithADashFollowsTheEndOfOptions(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc send": {ok(`{"started":true,"turn":1}`)},
	})

	// when
	err := c.Submit("rl-p1-plan", "---")

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "send", callsOf(calls(), "hc", "send"), [][]string{{"hc", "send", "--json", "t1d", "--", "---"}})
}

func TestPromptWithWaitPassesTheTimeoutInWholeSeconds(t *testing.T) {
	cases := []struct {
		name     string
		timeout  time.Duration
		expected string
	}{
		{"whole", 90 * time.Second, "90"},
		{"fraction", 1500 * time.Millisecond, "2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			c, calls := fakeHosts(t, map[string][]reply{
				"hc ls":   agentFound(t),
				"hc send": {ok(`{"started":true,"turn":1,"wait":{"ended":true,"reason":"status","status":"idle","turn":1}}`)},
			})

			// when
			err := c.Prompt("rl-p1-plan", "go", true, tc.timeout)

			// then
			assertEqual(t, "error", err, nil)
			assertEqual(t, "send", callsOf(calls(), "hc", "send"), [][]string{{"hc", "send", "--json", "--wait", "--timeout", tc.expected, "t1d", "--", "go"}})
		})
	}
}

func TestPromptThatDidNotStartIsSubmittedWithOneMoreEnter(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc send": {ok(`{"started":false,"turn":1}`)},
		"hc keys": {ok(fixture(t, "ok.json"))},
		"hc wait": {ok(`{"ended":true,"reason":"status","status":"idle","turn":2}`)},
	})

	// when
	err := c.Prompt("rl-p1-plan", "go", false, 0)

	// then
	assertEqual(t, "error", err, nil)
	assertCalls(t, calls()[1:], [][]string{
		{"hc", "send", "--json", "t1d", "--", "go"},
		{"hc", "keys", "--json", "t1d", "enter"},
		{"hc", "wait", "--json", "--timeout", "10", "t1d"},
	})
}

func TestWaitingPromptThatDidNotStartWaitsForTheTurnAfterOneMoreEnter(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc send": {ok(`{"started":false,"turn":1,"wait":{"ended":false,"reason":"not_started","status":"idle","turn":1}}`)},
		"hc keys": {ok(fixture(t, "ok.json"))},
		"hc wait": {ok(`{"ended":true,"reason":"status","status":"idle","turn":2}`)},
	})

	// when
	err := c.Prompt("rl-p1-plan", "go", true, 90*time.Second)

	// then
	assertEqual(t, "error", err, nil)
	assertCalls(t, calls()[1:], [][]string{
		{"hc", "send", "--json", "--wait", "--timeout", "90", "t1d", "--", "go"},
		{"hc", "keys", "--json", "t1d", "enter"},
		{"hc", "wait", "--json", "--timeout", "90", "t1d"},
	})
}

func TestPromptWhoseRetryWaitReportsNoStatusIsNotSubmitted(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc send": {ok(`{"started":false,"turn":1}`)},
		"hc keys": {ok(fixture(t, "ok.json"))},
		"hc wait": {ok(`{"ended":true,"reason":"exit","status":null,"turn":1}`)},
	})

	// when
	err := c.Prompt("rl-p1-plan", "go", false, 0)

	// then
	assertErrorText(t, err, "hc: prompt to rl-p1-plan not submitted")
	assertCalls(t, calls()[1:], [][]string{
		{"hc", "send", "--json", "t1d", "--", "go"},
		{"hc", "keys", "--json", "t1d", "enter"},
		{"hc", "wait", "--json", "--timeout", "10", "t1d"},
	})
}

func TestPromptThatStillDoesNotStartAfterOneMoreEnterFails(t *testing.T) {
	// given
	c, _ := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc send": {ok(`{"started":false,"turn":1}`)},
		"hc keys": {ok(fixture(t, "ok.json"))},
		"hc wait": {ok(`{"ended":false,"reason":"timeout","status":"idle","turn":1}`)},
	})

	// when
	err := c.Prompt("rl-p1-plan", "go", false, 0)

	// then
	assertErrorText(t, err, "hc: prompt to rl-p1-plan not submitted")
}

func TestWaitingPromptThatTimesOutIsATimeoutError(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc send": {ok(`{"started":true,"turn":1,"wait":{"ended":false,"reason":"timeout","status":"working","turn":1}}`)},
		"hc keys": {ok(fixture(t, "ok.json"))},
	})

	// when
	err := c.Prompt("rl-p1-plan", "go", true, 90*time.Second)

	// then
	assertErrorText(t, err, "hc: timeout: rl-p1-plan still working after 90s")
	assertEqual(t, "keys", callsOf(calls(), "hc", "keys"), [][]string{})
}

func TestSubmitSendsTheTextAndReturnsOnceItStarts(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc send": {ok(`{"started":true,"turn":1}`)},
	})

	// when
	err := c.Submit("rl-p1-plan", "go")

	// then
	assertEqual(t, "error", err, nil)
	assertCalls(t, calls(), [][]string{
		{"hc", "ls", "--json", "--label", "rloop.agent=rl-p1-plan"},
		{"hc", "send", "--json", "t1d", "--", "go"},
	})
}

func TestSubmitReportsAStalledPromptWithoutPressingEnter(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc send": {ok(`{"started":false,"turn":1}`)},
		"hc keys": {ok(fixture(t, "ok.json"))},
	})

	// when
	err := c.Submit("rl-p1-plan", "go")

	// then
	assertEqual(t, "wraps ErrPromptStalled", errors.Is(err, core.ErrPromptStalled), true)
	assertEqual(t, "keys", callsOf(calls(), "hc", "keys"), [][]string{})
}

func TestReadPassesTheLines(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc read": {ok(fixture(t, "read-shell.json"))},
	})

	// when
	actual, err := c.Read("rl-p1-plan", 200)

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "text", actual, "/repo/wt % ")
	assertEqual(t, "reads", callsOf(calls(), "hc", "read"), [][]string{{"hc", "read", "--json", "--lines", "200", "t1d"}})
}

func TestSendKeysPressesEachKeyInOrder(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc keys": {ok(fixture(t, "ok.json"))},
	})

	// when
	err := c.SendKeys("rl-p1-plan", "down", "enter")

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "keys", callsOf(calls(), "hc", "keys"), [][]string{
		{"hc", "keys", "--json", "t1d", "down"},
		{"hc", "keys", "--json", "t1d", "enter"},
	})
}

func TestSendKeysStopsAtTheFirstError(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":   agentFound(t),
		"hc keys": {failed(1, `{"error":{"kind":"invalid_params","message":"bad key"}}`)},
	})

	// when
	err := c.SendKeys("rl-p1-plan", "down", "enter")

	// then
	assertErrorText(t, err, "hc: invalid_params: bad key")
	assertEqual(t, "keys", callsOf(calls(), "hc", "keys"), [][]string{{"hc", "keys", "--json", "t1d", "down"}})
}

func TestInterruptPressesTheStopKeyThenCtrlC(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"hc ls":        agentFound(t),
		"hc interrupt": {ok(fixture(t, "ok.json"))},
		"hc keys":      {ok(fixture(t, "ok.json"))},
	})

	// when
	err := c.Interrupt("rl-p1-plan")

	// then
	assertEqual(t, "error", err, nil)
	assertCalls(t, calls()[1:], [][]string{
		{"hc", "interrupt", "--json", "t1d"},
		{"hc", "keys", "--json", "t1d", "ctrl+c"},
	})
}

func TestTagLabelsEveryLiveAgentTerminalOfTheSpace(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"herdr-app ls": {ok(fixture(t, "app-ls.json"))},
		"hc get": {
			ok(getJSON("live", "idle", "claude")),
			ok(`{"address":"t1d","state":"live","labels":{"herdr.space":"spRUN"},"foreground":null,"status":null,"status_ms":null,"cols":120,"rows":40}`),
			ok(getJSON("finished", "")),
		},
		"hc label": {ok(fixture(t, "ok.json"))},
	})

	// when
	err := c.Tag("spRUN", map[string]string{"rloop_wait": "", "rloop": "◆ working"})

	// then
	assertEqual(t, "error", err, nil)
	assertEqual(t, "gets", callsOf(calls(), "hc", "get"), [][]string{
		{"hc", "get", "--json", "t1b"},
		{"hc", "get", "--json", "t1d"},
		{"hc", "get", "--json", "t1c"},
	})
	assertEqual(t, "labels", callsOf(calls(), "hc", "label"), [][]string{{"hc", "label", "--json", "t1b", "rloop=◆ working", "rloop_wait="}})
}

func TestTagOfASpaceMissingFromLsWrapsErrNotFound(t *testing.T) {
	// given
	c, calls := fakeHosts(t, map[string][]reply{
		"herdr-app ls": {ok(fixture(t, "app-ls.json"))},
	})

	// when
	err := c.Tag("spGONE", map[string]string{"rloop": "◆ working"})

	// then
	assertEqual(t, "wraps ErrNotFound", errors.Is(err, core.ErrNotFound), true)
	assertCalls(t, calls(), [][]string{{"herdr-app", "ls"}})
}

func TestTagReturnsTheLabelError(t *testing.T) {
	// given
	c, _ := fakeHosts(t, map[string][]reply{
		"herdr-app ls": {ok(fixture(t, "app-ls.json"))},
		"hc get":       {ok(getJSON("live", "idle", "claude"))},
		"hc label":     {failed(1, `{"error":{"kind":"invalid_params","message":"bad label"}}`)},
	})

	// when
	err := c.Tag("spRUN", map[string]string{"rloop": "◆ working"})

	// then
	assertErrorText(t, err, "hc: invalid_params: bad label")
}

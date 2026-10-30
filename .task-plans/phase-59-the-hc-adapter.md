status: planned

# Phase 59 — The hc adapter

## Summary

The phase adds `internal/hc`, a second `core.SessionHost` that drives herdr-app: terminals through `hc`, the window (spaces, tabs, panes, focus) through `herdr-app`. Every later call finds an agent by the label `rloop.agent=<name>` that `Start` puts on its terminal. It also adds two sentinels to `ports.go`. `core.ErrNotFound` lets the phase-60 router tell "this host does not know that id" from a real failure. `core.ErrAgentBlocked` lets the core recognise a blocked agent on either host (the watchdog's answer to q1). Each adapter's error type wraps both through an `Unwrap` method.

The choices and their winners:

- **Where the sentinels attach.** Option 1: an `Unwrap() error` method on `herdr.Error` and `hc.Error`. Option 2: `fmt.Errorf("%w: %w")` at each call site. Option 1 wins: one place per adapter, `errors.As(err, &herdr.Error{})` keeps working, and the message text stays byte-identical. Existing herdr tests compare that text.
- **How `blocked()` in the watchdog recognises a refusal.** Option 1: `errors.Is(err, ErrAgentBlocked)` only, with the core and app test fakes updated to wrap the sentinel. Option 2: keep the herdr string match beside `errors.Is`. Option 1 wins: the core keeps no knowledge of a herdr error code, the fakes then behave like the real adapters, and the watchdog asked for exactly this.
- **The trust-dialog and readiness loop.** Option 1: a copy in `internal/hc`. Option 2: one shared loop exported from `internal/herdr` or `internal/core`. Option 1 wins. Sharing would make `hc` import `herdr`, or add a core file and interface outside the phase. Each copy's messages carry its own adapter prefix, and each package's tests shrink its own timing vars. The two starts also differ before the loop (herdr's `agent_not_ready` against hc's foreground wait).
- **How `Start` knows the typed `exec` took effect.** Option 1: poll `hc get` until the foreground argv differs from the argv read just before typing. Option 2: wait for a foreground argv whose basename equals `kind`. Option 1 wins. `exec` keeps the pid (probed: pid 19860 before and after), and a script-based agent's foreground is its interpreter (`node …/codex`), which option 2 misses.
- **Not found for a pane or space.** Option 1: resolve the id in `herdr-app ls` first. Option 2: string-match herdr-app's `no such space` / `no pane "x"` errors. Option 1 wins: the spec names it ("missing from `herdr-app ls`"), and the error text is not a contract. It applies to every method that takes a pane or space id: `Start`, `Split` (with an explicit pane), `OpenTab`, `PaneInfo`, `Tag`, `ClosePane`, `Close`, and `AgentPane`'s terminal-to-pane step. tech-design.md:1614-1616 requires the wrap for any id the host does not know.
- **herdr-app's error shape.** herdr-app prints `{"error": "<text>"}` on **stdout** and exits 1 (probed). It is surfaced as a plain error `herdr-app: <text>`, with no type, because nothing branches on it.
- **A stalled prompt's retry.** After a send that did not start, `hc keys <t> enter` and then `hc wait --timeout <s> <t>`. Probed: such a wait follows the send's turn (it returns once that turn ends, or times out with the turn unchanged). The retry counts as submitted when the wait's `turn` is past the send's `turn` or its `status` is `working` or `blocked`.
- **A waiting prompt that times out.** `send --wait` returns exit 0 with `wait.ended: false, reason: "timeout"`. The adapter turns that into `hc: timeout: …`, as herdr's `agent prompt --wait` errors on a timeout (see the fake at `internal/herdr/client_test.go:198`).

## Why this approach

The phase proposed changing `ports.go`, `internal/herdr/client.go` and its test, and creating `internal/hc`. The plan follows that proposal and the tech design (`docs/task-loop-driver/tech-design.md:1611-1659`) call by call, with these differences and additions:

- **`core.ErrAgentBlocked` and `internal/core/watchdog.go`, the one production file beyond `Files:`** (the watchdog's answer to q1). `blocked()` at `internal/core/watchdog.go:526-528` recognises a blocked agent only by the herdr text `agent_blocked`. It drives the watchdog's wait-and-retry at `internal/core/watchdog.go:300` and answer delivery at `internal/core/loop.go:1573`. hc's exit 9 reads `hc: blocked: …` (`tech-design.md:1648`), so on herdr-app a blocked watchdog would have failed its prompt instead of waiting. The retry is required on any host (`tech-design.md:729`).
- **Test files beyond `Files:`.** Five fakes return a blocked refusal as plain text, and `errors.Is` would stop recognising them, so each gains `%w` of `core.ErrAgentBlocked`: `internal/core/watchdog_test.go:499`, `:766`, `:967`, `internal/core/loop_events_test.go:310`, and `internal/app/watchdog_test.go:346`. Without this they would no longer model the adapters, and the watchdog and delivery tests that depend on them would fail. `internal/core/hooks_test.go:2104` is left alone: `SessionManager.prompt` never calls `blocked()` and that test asserts only the error text.
- **`PromptContext` on `hc.Client`.** The watchdog type-asserts `PromptContext(ctx, …)` at `internal/core/watchdog.go:261` and `:497` to cancel a waiting prompt and to drain on stop. `herdr.Client` has it (`internal/herdr/client.go:389`). Without it the hc host would quietly lose cancellation, so `hc.Client` gets the same method and `Prompt` delegates to it, as in `internal/herdr/client.go:371-373`.
- **No `hc.ErrNoBinary`.** `herdr.ErrNoBinary` is used only inside `internal/herdr`, and nothing in `internal/app` maps it. Phase 60 detects a missing binary with `LookPath` (todo.md, Phase 60, first item).
- **`--json` on every hc call.** `hc read` prints text without it (`hc --skill`: "Piped output is JSON except run, exec, read"). With it, `read` returns `{"text": …}`, so every hc call parses JSON, as the item requires.

Evidence from live probes of hc 0.1.14 and herdr-app 0.1.9:

- `space.new` and `tab.new` return `{"space","tab","terminal"}`, and `pane.split` returns `{"pane","terminal"}`. `do` succeeds with `{"ok": true}`.
- `herdr-app status` carries `space`, `tab` and `focused_pane`. `herdr-app ls` nests `spaces[].tabs[].terminals[]{terminal, pane}`.
- `hc ls --json` lists live terminals as `{address, …}` without labels. `hc get --json` carries `labels`, `foreground{name, argv, pid}`, `status{state, app}` or `null`, `status_ms`, `state` (`live` or `finished`), `cols` and `rows`.
- hc errors are `{"error": {"kind", "message", "hint"}}` on stderr: exit 3 is `not_found`, exit 11 is `finished`.
- `send` without `--wait` returns `{"started": true|false|null, "turn"}`, `false` after about 5 s. `send --wait` with an agent that never starts returns `wait.reason: "not_started"`.
- A plain program reports no status. `printf '\033]7501;state=idle:app=x\007'` makes it report `idle`.

## Changes

### 1. `internal/core/ports.go` (modify)

- Beside `ErrPromptStalled` (`internal/core/ports.go:11`), add:
  ```go
  var ErrNotFound = errors.New("not found")

  var ErrAgentBlocked = errors.New("agent blocked")
  ```
- Serves: `core.ErrNotFound` (item 1), the blocked retry on herdr-app (q1).

### 2. `internal/core/watchdog.go` (modify)

- Replace `blocked` at `internal/core/watchdog.go:526-528` with:
  ```go
  func blocked(err error) bool {
  	return errors.Is(err, ErrAgentBlocked)
  }
  ```
  `errors` is already imported; `strings` stays imported for its other uses in the file.
- Serves: the watchdog's retry and answer delivery recognise a blocked agent on both hosts (q1).

### 3. Core and app test fakes (modify): `internal/core/watchdog_test.go`, `internal/core/loop_events_test.go`, `internal/app/watchdog_test.go`

Each `errors.New("<text>")` below becomes `fmt.Errorf("<text>: %w", ErrAgentBlocked)`. In the app test the sentinel is spelled `core.ErrAgentBlocked`. Add `"fmt"` to the two core test files' imports; the app test already imports it.

- `internal/core/watchdog_test.go:499`: `"herdr agent prompt: herdr: agent_blocked: waiting on a permission"`
- `internal/core/watchdog_test.go:766`: `"herdr agent prompt: herdr: agent_blocked: agent " + dogName + " is blocked and requires interactive input"`. Write it as `fmt.Errorf("herdr agent prompt: herdr: agent_blocked: agent %s is blocked and requires interactive input: %w", dogName, ErrAgentBlocked)`.
- `internal/core/watchdog_test.go:967`: same text as `:499`.
- `internal/core/loop_events_test.go:310`: `"herdr agent prompt: agent_blocked"`
- `internal/app/watchdog_test.go:346`: `"herdr: agent_blocked"`

The assertion at `internal/core/watchdog_test.go:515` (`Contains "agent_blocked"`) still holds.

Serves: the existing watchdog and delivery tests keep proving the blocked retry through the sentinel (q1).

### 4. `internal/herdr/client.go` (modify)

- Add, after `Error.Error` (`internal/herdr/client.go:35-37`):
  ```go
  func (e Error) Unwrap() error {
  	switch {
  	case strings.HasSuffix(e.Code, "not_found"):
  		return core.ErrNotFound
  	case e.Code == "agent_blocked":
  		return core.ErrAgentBlocked
  	}
  	return nil
  }
  ```
  Every error the CLI reports with such a code passes through `Error` (`internal/herdr/client.go:85-87`), so this covers every call.
- `StateSeq` (`:421-437`) and `AgentPane` (`:439-450`): replace `var herr Error; errors.As(err, &herr) && strings.HasSuffix(herr.Code, "not_found")` with `errors.Is(err, core.ErrNotFound)`. Behaviour is unchanged: `AgentGone, 0, nil` and `"", nil`. `State` delegates to `StateSeq`.
- Serves: item 1, and q1 for herdr.

### 5. `internal/hc/client.go` (create)

Package `hc`. Imports: `bytes`, `context`, `encoding/json`, `errors`, `fmt`, `math`, `os`, `os/exec`, `slices`, `sort`, `strconv`, `strings`, `syscall`, `time`, `unicode`, `r-loop/internal/core`.

**Tunables**, package vars as in `internal/herdr/client.go:23-29`, so tests can shrink them:
```go
var (
	callTimeout     = 30 * time.Second
	submitTimeout   = 10 * time.Second
	paneBusyBudget  = 20 * time.Second
	paneBusyBackoff = 250 * time.Millisecond
	trustSettle     = 1500 * time.Millisecond
)
```

**Types**
```go
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
```

**Process runner.** One function follows `execWithinContext` (`internal/herdr/client.go:51-89`):
```go
func run(ctx context.Context, limit time.Duration, name, bin string, args ...string) (stdout, stderr []byte, exit int, err error)
```
- `name` is `"hc"` or `"herdr-app"`, used only in messages; `bin` is `c.HC` or `c.App`.
- `exec.CommandContext` with `context.WithTimeout(ctx, limit)`.
- `SysProcAttr{Setpgid: true}`, `Cancel` kills `-pid` with `SIGKILL`, `WaitDelay = time.Second`.
- `cmd.Env = withoutTerminalID(os.Environ())`, where `withoutTerminalID` drops every entry with the prefix `HC_TERMINAL_ID=`.
- On deadline it returns `fmt.Errorf("%s %s: timed out after %s", name, strings.Join(args[:min(len(args), 3)], " "), limit)` On `context.Canceled` it returns `ctx.Err()`. A non-`ExitError` start failure (a missing binary) is returned as is. An `ExitError` returns `stdout, stderr, code, nil`.

The two wrappers call `run`:

```go
func (c Client) hc(ctx context.Context, limit time.Duration, out any, args ...string) error
func (c Client) app(out any, args ...string) error
```

- `hc` runs `c.HC` with `args`. Callers always include `--json`, at the position given per method below.
  - Exit 0: `json.Unmarshal(stdout, out)`. A parse failure is `fmt.Errorf("hc %s: %w", args[0], err)`.
  - Exit ≠ 0 with stderr `{"error":{"kind","message"}}`: return `Error{Kind, Message}`.
  - Exit ≠ 0 without that JSON: `Error{Kind: k, Message: strings.TrimSpace(string(stderr))}`, with `k` = `"not_found"` on exit 3, `"blocked"` on exit 9, `"usage"` on exit 2, otherwise `"unknown"`.
- `app` runs `c.App` with `args`, limit `callTimeout`, background context.
  - Exit 0: unmarshal into `out`. A parse failure is `fmt.Errorf("herdr-app %s: %w", args[0], err)`.
  - Exit ≠ 0 with stdout `{"error": "<text>"}`: `fmt.Errorf("herdr-app: %s", text)`.
  - Otherwise `fmt.Errorf("herdr-app: %s", strings.TrimSpace(stderr))`, or of stdout when stderr is empty.

**herdr-app reads**
```go
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
```
- `func (c Client) focus() (focus, error)`: `herdr-app status`.
- `func (c Client) layout() (layout, error)`: `herdr-app ls`.
- `func (l layout) pane(p string) (space, terminal string, ok bool)` and `func (l layout) terminal(t string) (pane string, ok bool)` walk the nesting.
- `func (l layout) space(id string) (terminals []string, ok bool)` returns the space's terminals in `ls` order.
- `func paneNotFound(p string) error { return fmt.Errorf("herdr-app: pane %s: %w", p, core.ErrNotFound) }` and `spaceNotFound(id)`, the same with `space`.

**Shell argv**
```go
func shellArgv(cwd string, env map[string]string) string
```
Returns the JSON text of `["env", "K=V"… (keys sorted), "/bin/sh", "-c", "cd \"$1\" && exec \"$SHELL\" -l", "sh", cwd]` via `json.Marshal`. `env` stays first with no variables. Key sorting follows `envFlags` (`internal/herdr/client.go:135-149`).

**Focus restore**
```go
func (c Client) restore(f focus) error
```
Runs `herdr-app do space.select --id <f.Space>`, `herdr-app do tab.select --id <f.Tab>` and `herdr-app do pane.focus --pane <f.Pane>`, each into `struct{}`, and stops at the first error, returned as `fmt.Errorf("restore focus: %w", err)`.

**Window methods**

- `Reachable()`: `hc server status --json`, then `herdr-app status`. The first error is returned.
- `Open(spec)`:
  1. `f := focus()`.
  2. `herdr-app do space.new --name <spec.Label> --folder <spec.CWD> --argv <shellArgv(spec.CWD, spec.Env)>`, into `{space, tab, terminal}`.
  3. `restore(f)`.
  4. `layout()`, then the pane of the returned terminal. A terminal missing from `ls` returns `fmt.Errorf("herdr-app: terminal %s: %w", t, core.ErrNotFound)`.
  5. Returns `core.Workspace{ID: space, RootPane: pane}`.
- `OpenTab(workspace, spec)`: first `layout()`; a space missing from `ls` returns `spaceNotFound(workspace)` with no other call. Then the same steps as `Open` with `herdr-app do tab.new --name <spec.Label> --space <workspace> --argv <shellArgv(spec.CWD, spec.Env)>`. Returns the pane.
- `Split(pane, direction, cwd, ratio, env)`:
  - When `pane == ""`, `pane = focus().Pane`.
  - Otherwise `layout()`; a pane missing from `ls` returns `paneNotFound(pane)` with no other call.
  - Runs `herdr-app do pane.split --direction <direction> --pane <pane> --argv <shellArgv(cwd, env)>` and returns `.pane`.
  - `ratio` is not read.
- `ClosePane(pane)`: `layout()`. A pane missing from `ls` returns `paneNotFound(pane)`. Otherwise `herdr-app do pane.close --pane <pane>`.
- `Close(id)`: `layout()`. A space missing from `ls` returns `spaceNotFound(id)`. Otherwise `herdr-app do space.close --id <id> --confirmed true`.
- `PaneInfo(pane)`: `layout()`. A missing pane returns `paneNotFound(pane)`. Otherwise `hc get --json <terminal>` into `{cols, rows}`, and the method returns `core.PaneInfo{Workspace: space, Cols, Rows}`.

**hc terminal reads**
```go
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
```
- `func (c Client) get(t string) (terminal, error)`: `hc get --json <t>`.

**Agent lookup**
```go
func (c Client) find(ctx context.Context, agent string) (string, error)
```
- Runs `hc ls --json --label rloop.agent=<agent>` under `ctx`, limit `callTimeout`, into `{terminals: [{address}]}`. `PromptContext` passes its own `ctx`; every other method passes `context.Background()`.
- None: `Error{Kind: "not_found", Message: "no live terminal labelled rloop.agent=" + agent}`.
- Two or more: `fmt.Errorf("hc: agent %s is on more than one terminal: %s", agent, strings.Join(addresses, ", "))`.
- Exactly one: its address.

**Start**
1. `layout()`, then the pane's terminal `t`. A missing pane returns `paneNotFound(pane)`.
2. `find(name)`. `nil` error returns `fmt.Errorf("hc: agent %s already exists", name)`. An error that does not wrap `core.ErrNotFound` is returned as is.
3. `hc label --json <t> rloop.agent=<name>`.
4. `base := get(t)`.
5. `hc send --json --force <t> <execLine(kind, args)>` into `struct{}`, limit `callTimeout`.
6. `awaitProgram(t, name, kind, base)`: every `paneBusyBackoff`, until `paneBusyBudget` passes, `get(t)`.
   - `State == "finished"` returns `fmt.Errorf("hc: agent %s exited before it started", name)`.
   - A live sample with `Foreground == nil` keeps polling.
   - A sample whose `Foreground.Argv` differs (`!slices.Equal`) from the baseline argv returns nil. The baseline argv is `base.Foreground.Argv`, or `nil` when `base.Foreground == nil`.
   - At the deadline it returns `fmt.Errorf("hc: agent %s never started %s", name, kind)`.
   - A baseline with `State == "finished"` returns the "exited before it started" error at once.
7. `awaitReady(name)`.
8. Returns `core.Agent{Name: name, Pane: pane}`.

`execLine(kind string, args []string) string` returns `"exec " + strings.Join(quoted words, " ")`, where every word, `kind` included, is `"'" + strings.ReplaceAll(w, "'", `'\''`) + "'"`.

**Trust and readiness.** Copy `trustDialogs`, `asksTrust`, `trustDialog`, `awaitReady`, `contains` and `squash` from `internal/herdr/client.go:258-369` unchanged, except that the four failure messages start `hc: ` instead of `herdr: `. They run over this package's `Screen`, `Read`, `SendKeys`, `State` and `StateSeq`.

**Agent methods.** Each first calls `find(context.Background(), agent)`; a lookup error is returned as is, except where noted.

- `StateSeq(agent)`:
  - A `find` error wrapping `core.ErrNotFound` returns `core.AgentGone, 0, nil`.
  - Otherwise `get(t)`; a `get` error wrapping `core.ErrNotFound` also returns `AgentGone, 0, nil`.
  - `State == "finished"` returns `AgentGone, StatusMS, nil`.
  - `Status == nil` returns `AgentUnknown, StatusMS, nil`.
  - `Status.State` `idle`, `working`, `blocked` or `done` returns that state. Anything else, `error` included, returns `AgentUnknown`. The sequence is `StatusMS`.
- `State(agent)`: `StateSeq` without the sequence, as in `internal/herdr/client.go:416-419`.
- `AgentPane(agent)`:
  - A `find` error wrapping `core.ErrNotFound` returns `"", nil`.
  - Otherwise `layout()`; a terminal missing from `ls` returns `"", nil`, and a present one returns its pane.
- `Screen(agent)`: `hc read --json <t>` into `{text}`, returns `text`.
- `Read(agent, lines)`: `hc read --json --lines <lines> <t>`, returns `text`.
- `SendKeys(agent, keys...)`: one `hc keys --json <t> <key>` per key, in order, stopping at the first error.
- `Interrupt(agent)`: `hc interrupt --json <t>`, then `hc keys --json <t> ctrl+c`.
- `Tag(id, tokens)`:
  1. `layout()`. A missing space returns `spaceNotFound(id)`.
  2. For each terminal of the space, in `ls` order, `get(t)`. One with `State == "live"` and an `rloop.agent` key in `Labels` gets `hc label --json <t> k=v…`, with keys sorted and an empty value written `k=`.
  3. The first error is returned. A space with no such terminal returns nil.

**Prompting**
```go
type sendResult struct {
	Started *bool  `json:"started"`
	Turn    uint64 `json:"turn"`
	Wait    *waitResult `json:"wait"`
}

type waitResult struct {
	Ended  bool    `json:"ended"`
	Reason string  `json:"reason"`
	Status *string `json:"status"`
	Turn   uint64  `json:"turn"`
}

func seconds(d time.Duration) string { return strconv.Itoa(max(1, int(math.Ceil(d.Seconds())))) }
```

- `Prompt(agent, text, wait, timeout)` returns `c.PromptContext(context.Background(), …)`.
- `PromptContext(ctx, agent, text, wait, timeout)`:
  1. `t := find(ctx, agent)`.
  2. Without `wait`: `hc send --json <t> <text>`, limit `callTimeout` (hc's own start check, about 5 s, fits inside it). With `wait`: `hc send --json --wait --timeout <seconds(timeout)> <t> <text>`, limit `callTimeout + timeout`. Both run under `ctx`.
  3. The send stalled when `Started != nil && !*Started`, or `Wait != nil && Wait.Reason == "not_started"`.
  4. Not stalled:
     - With `wait`, `Wait != nil && !Wait.Ended && Wait.Reason == "timeout"` returns `Error{Kind: "timeout", Message: agent + " still working after " + seconds(timeout) + "s"}`.
     - Otherwise nil.
  5. Stalled:
     - `hc keys --json <t> enter`, limit `callTimeout`, under `ctx`.
     - Then `hc wait --json --timeout <s> <t>`, where `s` is `seconds(submitTimeout)` without `wait` and `seconds(timeout)` with it, limit `callTimeout` plus that duration, under `ctx`.
     - Submitted when `w.Turn > send.Turn`, or `w.Status != nil` and `*w.Status` is `working` or `blocked`. A `null` status with an unchanged turn is not submitted. Otherwise it returns `fmt.Errorf("hc: prompt to %s not submitted", agent)`.
     - Submitted with `wait` and `!w.Ended && w.Reason == "timeout"` returns the same timeout `Error` as step 4. Otherwise nil.
- `Submit(agent, text)`: `find`, then `hc send --json <t> <text>`, limit `callTimeout`. `Started != nil && !*Started` returns `fmt.Errorf("%w: hc: prompt to %s did not start", core.ErrPromptStalled, agent)` and presses nothing.

Serves: items 2–8, plus watchdog cancellation (`internal/core/watchdog.go:261`).

### 6. `internal/hc/testdata/*.json` (create)

Canned output copied from the probes, with fixed ids:

- `app-status.json`: `{"space":"spUSER","tab":"tbUSER","focused_pane":"pUSER","errors":[]}`
- `app-ls.json`:
  - space `spUSER` with tab `tbUSER` and terminal `t1`/`pUSER`.
  - space `spRUN` with tab `tbRUN1`, terminals `t1b`/`pROOT` and `t1d`/`pSPLIT`.
  - in `spRUN`, tab `tbRUN2` with terminal `t1c`/`pTAB`.
  - Shape exactly as `herdr-app ls` prints it: `id`, `name`, `folder`, `machine`, `active`, `tabs[].id/name/active/settled/terminals[].terminal/pane/status/app/name/sub_agents`.
- `space-new.json`: `{"space":"spRUN","tab":"tbRUN1","terminal":"t1b"}`
- `tab-new.json`: `{"space":"spRUN","tab":"tbRUN2","terminal":"t1c"}`
- `pane-split.json`: `{"pane":"pSPLIT","terminal":"t1d"}`
- `ok.json`: `{"ok":true}`
- `ls-none.json`: `{"hidden_finished":0,"terminals":[]}`
- `ls-one.json`: `{"hidden_finished":0,"terminals":[{"address":"t1d","agent":"env","cwd":"/repo/wt","name":null,"state":"live","turn":0,"where":"run / 1"}]}`
- `ls-two.json`: the same with entries `t3` and `t7`.
- `get-shell.json`: a full `hc get` body.
  - `state: "live"`, `foreground: {"argv":["/bin/zsh","-l"],"cwd":"/repo/wt","name":"zsh","pid":19860}`, `status: null`, `status_ms: null`.
  - `labels: {"herdr.space":"spRUN","herdr.tab":"tbRUN1","rloop.agent":"rl-p1-plan"}`.
  - `cols: 120`, `rows: 40`.
- `get-claude-idle.json`: as `get-shell.json` with `foreground.argv ["claude","--model","haiku"]`, `name "2.1.314"`, `status {"app":"claude-code","state":"idle"}` and `status_ms 1793345420081`.
- `read-shell.json`: `{"complete":true,"how":"screen","text":"/repo/wt % "}`
- `server-status.json`: the probed `hc server status --json` body with `version "0.1.14"`.

Variants that differ in one field (a state, a status, a label set) are written inline in the test as short JSON strings.

### 7. `internal/hc/client_test.go` (create)

`package hc`, starting with `var _ core.SessionHost = Client{}` and `var _ interface{ PromptContext(context.Context, string, string, bool, time.Duration) error } = Client{}`.

**Fake.** One shell script, written by the helper into `t.TempDir()` as both `hc` and `herdr-app` (two copies), follows `scripted` (`internal/herdr/client_test.go:603-637`):

```go
type reply struct {
	out, err string
	exit     int
	sleep    string
}

func fakeHosts(t *testing.T, replies map[string][]reply) (Client, func() []call)

type call struct {
	argv       []string
	terminalID string
}
```

- **Keys.** `hc <sub>` for hc, `herdr-app <sub>` for `status` and `ls`, and `herdr-app do <command>` for `do`. A key suffixed ` after keys` is served instead of the plain key once any `hc keys` call has run, so a test models a screen or state that changes after a key press.
- **Order.** Each key's replies are served in order and the last one repeats. A key with no reply exits 99 with `fake: no reply for <key>` on stderr, so an unexpected call fails the test.
- **What the script does.** It writes each reply's parts to files `<dir>/<key>.<n>.{out,err,exit,sleep}` and records `basename "$0"`, then argv NUL-separated, then `HC_TERMINAL_ID=${HC_TERMINAL_ID-unset}`, one line per call. It keeps a per-key counter file `<dir>/<key>.count`, spaces in the key written `_`, holding the number of calls served so far, and touches `<dir>/keyed` on `hc keys`. When `sleep` is set it runs `sleep <s> &`, writes `$!` to `<dir>/child`, and `wait`s, so a test can find the child and prove it was killed.
- **Return.** `fakeHosts` returns `Client{HC: <dir>/hc, App: <dir>/herdr-app}`.

**Helpers**, each taking the values its test depends on:
- `fixture(t, name) string` reads `testdata/<name>`.
- `ok(out string) reply`.
- `failed(exit int, stderr string) reply`.
- `getJSON(state, status string, argv ...string) string` builds a `hc get` body. `status ""` means `null`; no `argv` means `foreground: null`.
- `agentFound()` returns the `hc ls` reply list `[ok(fixture("ls-one.json"))]`.
- `assertEqual[T comparable](t, name string, actual, expected T)`.
- `assertCalls(t, actual []call, expected [][]string)` compares `basename` plus argv.
- `shrink(t, v *time.Duration, d time.Duration)` restores the var through `t.Cleanup`.
- `servedAt(t, c Client, key string, n int) func() time.Time` starts a goroutine that polls `<dir>/<key>.count` every 5 ms until it reads at least `n`, and records `time.Now()` then. The returned func blocks until that time is recorded, failing the test after 5 s.
- `childPID(t, c Client) int` polls `<dir>/child` every 10 ms for up to 2 s until it holds a pid (`<dir>` is `filepath.Dir(c.HC)`).
- `assertExits(t, pid int)` polls `syscall.Kill(pid, 0)` every 10 ms for up to 2 s until it returns `syscall.ESRCH`, and fails the test otherwise.

Every test below has `// given`, `// when`, `// then` blocks.

### 8. `internal/herdr/client_test.go` (modify)

Add the two tests listed under `## Tests`. No existing test changes.

### 9. `internal/hc/live_test.go` (create)

`TestLiveHC` skips with `t.Skip("set HC_TERMINAL_ID (run inside herdr-app) to run against the installed hc")` unless `HC_TERMINAL_ID` is set. It mirrors `TestLiveHerdr` (`internal/herdr/live_test.go:36`):

1. `c := Client{HC: "hc", App: "herdr-app"}` and `Reachable()`.
2. `dir := t.TempDir()` and `suffix := strconv.FormatInt(time.Now().UnixMilli()%1e8, 10)`. The live test is exempt from fixed data because names must be unique per run.
3. `Open(core.OpenSpec{CWD: dir, Label: "r-loop-live-" + suffix, Env: map[string]string{"R_LOOP_LIVE": "1"}})`, with `t.Cleanup(func() { _ = c.Close(ws.ID) })`. Asserts non-empty `ws.ID` and `ws.RootPane`.
4. `pane := Split(ws.RootPane, "right", dir, 0, map[string]string{"R_LOOP_LIVE": "1"})`. Asserts non-empty.
5. `Start(pane, "rloop-live-"+suffix, "sh", []string{"-c", "printf '\\033]7501;state=idle:app=rloop-live\\007'; echo rloop-live-ready; exec cat"})`. Asserts `core.Agent{Name, Pane: pane}`. The single quotes inside the script exercise `execLine`'s quoting against a real shell.
6. Polls `Screen(name)` every 250 ms for up to 10 s until it contains `rloop-live-ready`.
7. `Tag(ws.ID, map[string]string{"rloop": "◆ live", "rloop_wait": ""})` returns nil.
8. `Close(ws.ID)` returns nil. Then `State(name)` is polled every 250 ms for up to 10 s until `core.AgentGone`.

## Tests

Write these first. Every hc test uses `fakeHosts` and testdata from Change 6.

### `internal/herdr/client_test.go`

- `TestErrorCodesEndingNotFoundWrapErrNotFound`: table over `fakeExit` errors from `Close("w1")`. The codes `workspace_not_found`, `pane_not_found` and `agent_not_found` make `errors.Is(err, core.ErrNotFound)` true; `server_unreachable` and `agent_blocked` make it false. Each row also asserts `err.Error()` is `herdr: <code>: m`. Covers item 1 and keeps the text unchanged.
- `TestAgentBlockedWrapsErrAgentBlocked`: `Prompt` fails with code `agent_blocked`. `errors.Is(err, core.ErrAgentBlocked)` is true and `errors.Is(err, core.ErrPromptStalled)` is false. Covers q1 on herdr.
- The existing `TestStateOfMissingAgentIsGone`, `TestStateSeqOfMissingAgentIsGone` and `TestAgentPaneOfMissingAgentIsEmpty` keep pinning `gone` and `""` for an unknown agent (item 1).

### `internal/core` and `internal/app`

- The existing watchdog and delivery tests, whose fakes now wrap `ErrAgentBlocked` (Change 3), pin that `blocked()` recognises the sentinel:
  - `TestNotifyRecordsWatchdogUnreachableWhenABlockedWatchdogIsGone`.
  - The `askingHost`, `goneHost` and `eventsHost` tests.
  - The app watchdog test using `dogHost.blocked`.

  Covers q1.

### `internal/hc/client_test.go`: plumbing

- `TestEveryCallRunsWithoutHCTerminalID`: with `t.Setenv("HC_TERMINAL_ID", "t9")`, `Reachable()` records two calls (`hc server status --json`, `herdr-app status`), both with `terminalID == "unset"`. Covers item 2's stripped environment.
- `TestReachableAsksTheServerThenTheWindow`: the calls are exactly `[hc server status --json]` and `[herdr-app status]`, and the result is nil.
- `TestReachableReturnsTheServerErrorWithoutAskingTheWindow`: `hc server` fails exit 1 with `{"error":{"kind":"unavailable","message":"no server","hint":""}}`. The error text is `hc: unavailable: no server`, and the calls hold no `herdr-app status`.
- `TestAnHCErrorReadsAsKindAndMessage`: `hc read` fails exit 1 with kind `spawn_failed`, message `boom`. `Screen` returns an error with text `hc: spawn_failed: boom` that `errors.As` into `Error{Kind: "spawn_failed", Message: "boom"}`, and it does not wrap `core.ErrNotFound`. Covers item 2's error surfacing.
- `TestExitThreeWrapsErrNotFound`, a table:
  - exit 3 with `{"error":{"kind":"not_found","message":"no terminal \"t1d\""}}` reads `hc: not_found: no terminal "t1d"`.
  - exit 3 with bare stderr `gone` reads `hc: not_found: gone`.

  Both wrap `core.ErrNotFound`, via `Screen`. Covers item 2's exit 3.
- `TestExitNineIsABlockedErrorWrappingErrAgentBlocked`: `hc send` fails exit 9 with kind `blocked`, message `t1d is blocked`. `Prompt` returns text `hc: blocked: t1d is blocked` and `errors.Is(err, core.ErrAgentBlocked)`. Covers tech-design's exit 9 and q1.
- `TestAHerdrAppErrorReadsAsItsMessage`: `ls` succeeds, `do pane.close` exits 1 with stdout `{"error":"no pane \"pSPLIT\""}`. `ClosePane("pSPLIT")` returns `herdr-app: no pane "pSPLIT"`.
- `TestANonWaitingSendGetsOnlyTheCallTimeout`: `callTimeout` 200 ms, `hc ls` returns `ls-one.json`, `hc send` sleeps 30 s. `Submit("rl-p1-plan", "go")` returns `hc send --json t1d: timed out after 200ms`, and `assertExits(t, childPID(t, c))`. Covers item 2's 30 s limit for a send without `--wait`.
- `TestAWedgedCallFailsAfterTheCallTimeoutNamingTheCommand`: `callTimeout` shrunk to 200 ms, `hc server` sleeps 30 s. `Reachable` returns `hc server status --json: timed out after 200ms`, and `assertExits(t, childPID(t, c))` proves the fake's `sleep` child died with its process group. Covers item 2's timeout and own process group.
- `TestAWaitingPromptGetsItsWaitOnTopOfTheCallTimeout`: `callTimeout` 200 ms, `hc send` sleeps 0.5 s and replies a finished wait. `Prompt(name, "go", true, 2*time.Second)` returns nil. Covers "a waiting prompt gets its wait on top".
- `TestCancellingAPromptStopsTheCLIAtEveryStage`, a table. Each row makes one stage's command sleep 30 s:
  - the lookup: `hc ls`.
  - the send: `hc send`.
  - the retry's enter: `hc send` replies `started:false`, then `hc keys`.
  - the retry's wait: `hc send` replies `started:false` and `hc keys` succeeds, then `hc wait`.

  `PromptContext(ctx, "rl-p1-plan", "go", false, 0)` runs in a goroutine, and the test cancels `ctx` once `childPID` finds the sleeping child. The returned error is `context.Canceled` and `assertExits` proves the child died. Covers watchdog cancellation (`internal/core/watchdog.go:261`, `:497`) and the process group kill.
- `TestUnparsableHCOutputIsAnError`: `hc server` prints `not json`. `Reachable` returns an error starting `hc server: `.

### Window

- `TestOpenCreatesASpaceThenPutsTheFocusBack`: the calls are exactly, in order:
  - `herdr-app status`
  - `herdr-app do space.new --name phase-3 --folder /repo/wt --argv ["env","A=b","R_LOOP_RUN=r1","/bin/sh","-c","cd \"$1\" && exec \"$SHELL\" -l","sh","/repo/wt"]`
  - `herdr-app do space.select --id spUSER`
  - `herdr-app do tab.select --id tbUSER`
  - `herdr-app do pane.focus --pane pUSER`
  - `herdr-app ls`

  The result is `core.Workspace{ID: "spRUN", RootPane: "pROOT"}`. Covers item 3 and the shell argv.
- `TestOpenTabCreatesATabThenPutsTheFocusBack`: the calls are `ls`, `status`, `do tab.new --name review --space spRUN --argv […"/repo/wt"]`, the three restores and `ls`. Returns `pTAB`. Covers item 3.
- `TestOpenTabOfASpaceMissingFromLsWrapsErrNotFound`: `OpenTab("spGONE", …)` returns an error wrapping `core.ErrNotFound` that reads `herdr-app: space spGONE: not found`, and the calls are only `herdr-app ls`: no status, no `tab.new`. Covers tech-design.md:1614-1616.
- `TestOpenFailsWhenTheFocusCannotBePutBack`: `do space.select` exits 1 `{"error":"no such space"}`. `Open` returns `restore focus: herdr-app: no such space`, and no `ls` call follows.
- `TestOpenOfATerminalMissingFromLsWrapsErrNotFound`: `space.new` returns terminal `t9z`. The error wraps `core.ErrNotFound` and reads `herdr-app: terminal t9z: not found`.
- `TestShellArgvSortsTheVariablesAfterEnv`: given `herdr-app ls` → `app-ls.json` and `do pane.split` → `pane-split.json`, `Split("pROOT", "right", "/repo/wt", 0, {"R_LOOP_STEP": "implement", "R_LOOP_RUN": "r1"})` passes `--argv ["env","R_LOOP_RUN=r1","R_LOOP_STEP=implement","/bin/sh","-c","cd \"$1\" && exec \"$SHELL\" -l","sh","/repo/wt"]`. Covers item 4.
- `TestShellArgvStartsWithEnvWithoutVariables`: given the same two replies, `Split("pROOT", "down", "/repo", 0, nil)` passes `--argv ["env","/bin/sh","-c","cd \"$1\" && exec \"$SHELL\" -l","sh","/repo"]`. Covers item 4.
- `TestSplitRunsPaneSplitAndReturnsTheNewPane`: the calls are exactly `herdr-app ls`, then `herdr-app do pane.split --direction right --pane pROOT --argv […]`, and the result is `pSPLIT`. Covers item 4.
- `TestSplitOfAPaneMissingFromLsWrapsErrNotFound`: `Split("pGONE", "right", "/repo/wt", 0, nil)` returns an error wrapping `core.ErrNotFound` that reads `herdr-app: pane pGONE: not found`, and the calls are only `herdr-app ls`. Covers tech-design.md:1614-1616.
- `TestSplitOfAnEmptyPaneSplitsTheFocusedPane`: the calls are `herdr-app status`, then `do pane.split --direction down --pane pUSER --argv […]`. Covers item 4.
- `TestSplitIgnoresTheRatio`: given the same two replies, `Split("pROOT", "right", "/repo/wt", 0.3, nil)` records the same argv as the ratio-0 call. Covers item 4.
- `TestClosePaneClosesAPaneInLs`: the calls are `herdr-app ls`, `herdr-app do pane.close --pane pSPLIT`, and the result is nil. Covers item 7.
- `TestClosePaneOfAPaneMissingFromLsWrapsErrNotFound`: `ClosePane("pGONE")` wraps `core.ErrNotFound`, and the calls are only `herdr-app ls`. Covers item 2.
- `TestCloseClosesTheSpaceConfirmed`: the calls are `ls` and `do space.close --id spRUN --confirmed true`, and the result is nil. Covers item 7.
- `TestCloseOfASpaceMissingFromLsWrapsErrNotFound`: `Close("spGONE")` wraps `core.ErrNotFound`, with only `ls` called. Covers item 2.
- `TestPaneInfoTakesTheSpaceFromLsAndTheSizeFromHC`: the calls are `herdr-app ls`, `hc get --json t1d`, and the result is `core.PaneInfo{Workspace: "spRUN", Cols: 120, Rows: 40}`. Covers item 7.
- `TestPaneInfoOfAPaneMissingFromLsWrapsErrNotFound`: `PaneInfo("pGONE")` wraps `core.ErrNotFound`, with no `hc get`. Covers item 2.

### Agent lookup

- `TestAnAgentOnOneLiveTerminalIsThatTerminal`: `hc ls` returns `ls-one.json`. `Screen("rl-p1-plan")` returns `/repo/wt % `, and the calls are `hc ls --json --label rloop.agent=rl-p1-plan`, then `hc read --json t1d`. Covers item 6 and `Screen`.
- `TestAnAgentOnNoTerminalIsNotFound`: `ls-none.json`. `Screen` returns an error wrapping `core.ErrNotFound`, reading `hc: not_found: no live terminal labelled rloop.agent=rl-p1-plan`, with no `hc read`. Covers item 6.
- `TestAnAgentOnTwoTerminalsIsAnErrorNamingThem`: `ls-two.json`. `Screen` returns `hc: agent rl-p1-plan is on more than one terminal: t3, t7`, which does not wrap `core.ErrNotFound`. Covers item 6.

### Start

- `TestStartLabelsTheTerminalTypesExecAndReturnsOnceTheAgentIsIdle`. Given `ls`, `hc ls` none then one, `hc get` [shell, shell, claude-idle], `hc read` the ready screen, and `label`/`send` ok. `Start("pSPLIT", "rl-p1-plan", "claude", ["--model", "haiku"])` returns `core.Agent{Name: "rl-p1-plan", Pane: "pSPLIT"}`, and the first five calls are exactly:
  - `herdr-app ls`
  - `hc ls --json --label rloop.agent=rl-p1-plan`
  - `hc label --json t1d rloop.agent=rl-p1-plan`
  - `hc get --json t1d`
  - `hc send --json --force t1d exec 'claude' '--model' 'haiku'`

  Covers item 5.
- `TestStartQuotesEachWordForAPOSIXShell`: with `kind "codex"` and `args ["-c", "it's a test"]`, the send's text argv element is `exec 'codex' '-c' 'it'\''s a test'`. Covers items 4 and 5's quoting.
- `TestStartFailsWhenTheNameIsAlreadyOnALiveTerminal`: `hc ls` returns one terminal. The error is exactly `hc: agent rl-p1-plan already exists`, and the calls are only `herdr-app ls` and `hc ls …`: no label, no send. Covers item 5.
- `TestStartOfAPaneMissingFromLsWrapsErrNotFound`: `Start("pGONE", …)` wraps `core.ErrNotFound`, with only `herdr-app ls` called. Covers item 2.
- `TestStartFailsWhenTheProgramNeverReplacesTheShell`: `paneBusyBudget` 300 ms and `hc get` always shell. The error is `hc: agent rl-p1-plan never started claude`. Covers item 5's foreground wait.
- `TestStartFailsWhenTheTerminalFinishesBeforeTheProgramStarts`: `hc get` [shell, `getJSON("finished", "")`]. The error is `hc: agent rl-p1-plan exited before it started`.
- `TestStartKeepsWaitingWhileALiveTerminalShowsNoForeground`: `hc get` [live with `foreground: null`, live with `foreground: null`, claude-idle]. `Start` returns `core.Agent{Name: "rl-p1-plan", Pane: "pSPLIT"}` and nil, with no error about an exit.
- `TestStartAnswersEachTrustDialogWithItsKeys`, a table. Each row: `hc read` returns the dialog screen before keys and the ready screen after keys, and `hc get` returns idle. The recorded `hc keys --json t1d <key>` calls are exactly:
  - claude's `Yes, I trust this folder` screen (`claudeTrustScreen` text copied from `internal/herdr/client_test.go:861`): `down`, then `enter`.
  - codex's `Trust this folder?` screen (copied from `:673`): `enter`.
  - codex's `Do you trust the contents of this directory?` screen (copied from `:599`): `enter`.

  Covers item 5's dialogs.
- `TestStartWaitsAQuietWindowAfterTheTrustAnswer`: `trustSettle` 700 ms, the claude dialog, and `hc get after keys` idle with a fixed `status_ms`. `answered := servedAt(t, c, "hc keys", 2)` marks the `enter` press. `Start` returns nil, and the time from `answered()` to its return is at least 650 ms (700 ms less the 5 ms polling slack, rounded down). Covers item 5's settle after the trust answer.
- `TestStartRestartsTheQuietWindowWhenTheSequenceMoves`: `trustSettle` 700 ms, the claude dialog, and `hc get after keys` replies [idle `status_ms` 1, idle `status_ms` 1, idle `status_ms` 2], the last repeated. `moved := servedAt(t, c, "hc get after keys", 3)` marks the first sample of sequence 2. `Start` returns nil, and the time from `moved()` to its return is at least 650 ms. A loop that kept the window from the first quiet sample would return about 200 ms after the change, because two 250 ms backoffs had already passed. Covers item 5's "unchanged sequence for 1.5 s".
- `TestStartReturnsOnTheFirstIdleWithoutADialog`: the ready screen and idle. The result is nil and no `hc keys` call is recorded.
- `TestStartAcceptsDoneAsReady`: `hc get` returns status `done`. The result is nil and no keys are pressed. Covers "idle or done".
- `TestStartAnswersATrustDialogFoundOnlyInHistory`: `hc read --json t1d` returns the ready screen, and `hc read --json --lines 200 t1d` returns codex's trust screen before keys. One `enter` is pressed and the result is nil.
- `TestStartFailsWhenTheTrustDialogDoesNotClear`: `paneBusyBudget` 500 ms and the dialog both before and after keys. The error is `hc: agent rl-p1-plan still asks to trust its directory`.
- `TestStartFailsWhenTheTrustedAgentStaysBlocked`: `paneBusyBudget` 500 ms, then the ready screen and `blocked` after keys. The error is `hc: agent rl-p1-plan stays blocked after the trust dialog`.
- `TestStartNeverPressesKeysOnAnUnrecognisedBlockedScreen`: `paneBusyBudget` 500 ms, status `blocked`, and a screen without any marker. The error is `hc: agent rl-p1-plan never became ready (state blocked)` and no `hc keys` call is recorded.

### State

- `TestStateSeqMapsHCStatus`, a table over `hc get` bodies. Each row asserts the state and sequence:
  - `idle`, `working`, `blocked` and `done` map to the same state with seq `1793345420081`.
  - `error` maps to `unknown`.
  - a `null` status maps to `unknown` with seq `0`.
  - `state "finished"` maps to `gone`.

  Covers item 6.
- `TestStateOfAnAgentOnNoTerminalIsGone`: `ls-none.json`. `State` returns `core.AgentGone, nil`, with no `hc get`. Covers item 6 and the router's not-found answer.
- `TestStateOfATerminalHCNoLongerKnowsIsGone`: `hc get` fails exit 3. `StateSeq` returns `core.AgentGone, 0, nil`.
- `TestAgentPaneIsThePaneOfTheAgentsTerminal`: the calls are `hc ls …` and `herdr-app ls`, and the result is `pSPLIT`. Covers item 7.
- `TestAgentPaneOfAnAgentOnNoTerminalIsEmpty`: returns `"", nil`. Covers item 7.
- `TestAgentPaneOfATerminalWithoutAPaneIsEmpty`: `hc ls` lists `t9z`, absent from `app-ls.json`. Returns `"", nil`.

### Prompting

- `TestPromptWithoutWaitSendsTheText`: the calls are `hc ls …` and `hc send --json t1d review the plan`, with reply `{"started":true,"status":"idle","turn":0}`. The result is nil. Covers item 7.
- `TestPromptWithWaitPassesTheTimeoutInWholeSeconds`, a table over the send argv: `90*time.Second` gives `--wait --timeout 90`, and `1500*time.Millisecond` gives `--wait --timeout 2`. The reply carries a finished wait. Covers item 7.
- `TestPromptThatDidNotStartIsSubmittedWithOneMoreEnter`: send replies `started:false, turn 1`, and `hc wait` replies `ended:true, reason status, status idle, turn 2`. The calls after the lookup are `hc send --json t1d go`, `hc keys --json t1d enter`, `hc wait --json --timeout 10 t1d`, and the result is nil. Covers item 7's retry.
- `TestWaitingPromptThatDidNotStartWaitsForTheTurnAfterOneMoreEnter`: `send --wait --timeout 90` replies `wait.reason not_started, turn 1`, and `hc wait --json --timeout 90 t1d` replies `turn 2`. The result is nil.
- `TestPromptWhoseRetryWaitReportsNoStatusIsNotSubmitted`: send replies `started:false, turn 1`, and the wait replies `ended:true, reason exit, status null, turn 1`. The error is exactly `hc: prompt to rl-p1-plan not submitted`, and the calls after the lookup are exactly `hc send --json t1d go`, `hc keys --json t1d enter`, `hc wait --json --timeout 10 t1d`: one retry, no panic.
- `TestPromptThatStillDoesNotStartAfterOneMoreEnterFails`: the wait replies `ended:false, reason timeout, status idle, turn 1`. The error is `hc: prompt to rl-p1-plan not submitted`.
- `TestWaitingPromptThatTimesOutIsATimeoutError`: send replies `wait.ended false, reason timeout, status working`. The error is `hc: timeout: rl-p1-plan still working after 90s`, and no `hc keys` call is recorded.
- `TestSubmitSendsTheTextAndReturnsOnceItStarts`: the calls are `hc ls …` and `hc send --json t1d go`, with reply `started:true`. The result is nil. Covers item 7.
- `TestSubmitReportsAStalledPromptWithoutPressingEnter`: reply `started:false`. `errors.Is(err, core.ErrPromptStalled)`, and no `hc keys` call is recorded. Covers item 7.

### Keys, reads, tags

- `TestReadPassesTheLines`: `Read("rl-p1-plan", 200)` calls `hc read --json --lines 200 t1d` and returns the reply's `text`. Covers item 7.
- `TestSendKeysPressesEachKeyInOrder`: `SendKeys(name, "down", "enter")` records `hc keys --json t1d down`, then `hc keys --json t1d enter`, and returns nil. Covers item 7.
- `TestSendKeysStopsAtTheFirstError`: the first `hc keys` exits 1. The error is returned and only one `hc keys` call is recorded.
- `TestInterruptPressesTheStopKeyThenCtrlC`: the calls after the lookup are `hc interrupt --json t1d`, then `hc keys --json t1d ctrl+c`, and the result is nil. Covers item 7.
- `TestTagLabelsEveryLiveAgentTerminalOfTheSpace`. Given `app-ls.json` and `hc get` bodies in `ls` order:
  - `t1b`: live, with `rloop.agent`.
  - `t1d`: live, without it.
  - `t1c`: finished, with it.

  `Tag("spRUN", {"rloop_wait": "", "rloop": "◆ working"})` records exactly one label call, `hc label --json t1b rloop=◆ working rloop_wait=`, and returns nil. Covers item 8: the removal, sorted keys, and only live agent terminals.
- `TestTagOfASpaceMissingFromLsWrapsErrNotFound`: `Tag("spGONE", …)` wraps `core.ErrNotFound`, with no hc call. Covers item 2.
- `TestTagReturnsTheLabelError`: `hc label` exits 1 with kind `invalid_params`. The error reads `hc: invalid_params: <message>`. Covers "its error is the caller's warning".

### `internal/hc/live_test.go`

- `TestLiveHC`: as in Change 9. It runs only inside herdr-app and covers item 9's live round trip (open, split, start a shell agent, read its screen, tag, close).

## Left out

- **A shared trust/readiness package**: its only benefit is sharing three marker lines and one loop. It would add a package and an import between the adapters, which ADR-99 keeps apart; the copy is pinned by tests on both sides.
- **`hc.ErrNoBinary`**: nothing maps herdr's to an exit code, and phase 60 checks binaries with `LookPath`.
- **Closing a new space or pane when the focus restore fails**: the ids come from `herdr-app status` a moment earlier, so a restore failure means the window went away, and a close would fail the same way. The restore error is returned.
- **Labelling terminals without `rloop.agent` in `Tag`**: ADR-99 and tech-design limit labels to "the space's r-loop terminals".
- **A guard for an empty `space`, `tab` or `focused_pane` in `herdr-app status`**: no caller can reach it. r-loop opens spaces only into a running window, which always shows a space and a focused pane, and a missing one surfaces as herdr-app's own `no such space` error.
- **Forwarding `PromptContext` through the phase-60 router**: that is phase 60's file.

## Assumptions

- `hc --timeout` takes whole seconds. A sub-second or fractional timeout is rounded up to at least 1 s, because hc's help documents "Seconds" and r-loop's timeouts are whole minutes in config.
- The foreground wait counts any change of the foreground argv as the program having started. When `Start` reads the baseline while the pane is still starting (`env …` or `/bin/sh -c …`), the later change to the login shell passes it early. The readiness loop still waits for the agent's own `idle`/`done`, so the early pass costs nothing.
- The watchdog tags its new space right after `Open`, before `Start` labels a terminal (`internal/core/watchdog.go:115-118`). On herdr-app that one tag finds no `rloop.agent` terminal and is a no-op returning nil. The step workspaces' tags, all sent after `Start`, are unaffected. This follows ADR-99's "the space's r-loop terminals".
- The ready and trust screen texts in the hc tests are copied from the herdr tests' constants (`internal/herdr/client_test.go:599`, `:673`, `:859`, `:861`). hc's `read` returns the same terminal text.

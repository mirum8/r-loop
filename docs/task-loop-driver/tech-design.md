# r-loop — Tech design contracts

Read beside `todo.md`. Nothing here reaches an implementer: the leaf items repeat whatever they
need, because `/r:task-run` sees one leaf block and nothing else. Every contract below is shared by
at least two leaves of its milestone; what one leaf alone needs is in that leaf's items.

Stack, fixed by the spec and never re-decided here: Go 1.25.14 · module `r-loop` (no host in the
path) · Bubble Tea v1.3.10 · Lip Gloss v1.1.0 · MCP go-sdk v1.8.0
(`github.com/modelcontextprotocol/go-sdk`) · yaml.v3 v3.0.1 · herdr 0.9.0 · git 2.50.1. Nothing
from the skill-pack is read at run time; `todo.md` and `.task-plans/` are the two shared paths.

Four choices the spec left open were put to the maintainer on 2026-09-18 and the plan is built on
the answers: **the driver commits a step's work itself, once, when the step ends `ok` after its
last review round** (revised in round 20; an agent never commits, and nothing is committed before
its review completes); **a failed step waits a bounded `watchdog.remedyWindow` for a remedy before
the run halts**; **a `--plain` run reads an escalated answer from stdin when stdin is a terminal**,
and otherwise leaves the question open; **a reviewer entry is either a provider name or a block
with `provider`, `model` and `effort`**.

Round 20 (2026-09-18) redesigned two things the milestones below carry: the **plan step** writes a
four-section phase plan judged by a stricter `plan-file` check (ADR-59), and **review is a half of
every step** rather than a step of its own — reviewer panes beside the author, the author verifying
and applying the findings, in configurable rounds (ADR-56, ADR-57), committed once at the end
(ADR-58).

Round 21 (2026-09-18) made unattended completion a goal: a stalled step is nudged once and then
fails (ADR-62); a halt stops only the failed phase and the phases that depend on it (ADR-65); a red
land gate gets a bounded fix round (ADR-64); and `--unattended` pre-authorises the routine
remedies, bounds restarts, and restarts a step on its row's fallback provider (ADR-61, ADR-66).
Step agents ask the watchdog, and only the watchdog asks the maintainer, in its own session
(ADR-73, which removed ADR-63's question timeout and ADR-67's `r-loop answer`). Every automatic decision opens the run report. Every session
the driver starts — a step, a reviewer, a fallback, the gate fix, the watchdog — names its own
provider, model and effort, and `--model` and `--effort` override one row for one run (ADR-68).

## Milestone 1 — Core, plan file, config and state

- **Layout** — `cmd/r-loop` (main) · `internal/core` (loop, state machines, step kinds, evidence
  checks, runners, session manager, review half, land gate, watch, remedies; **imports only the
  standard library and itself**) · adapters, one package each: `internal/plan` (PlanSource) ·
  `internal/config` (LoopConfig) · `internal/store` (Store) · `internal/providers`
  (ProviderRegistry) · `internal/prompts` (Prompts) · `internal/herdr` (SessionHost) ·
  `internal/gitrepo` (Repo) · `internal/askmcp` (AskChannel and the watchdog surface) ·
  `internal/face/plain` and `internal/face/tui` (Face) · `internal/notify` (Notifier) ·
  `internal/app` (wiring, preflight, status, resume). A test in `internal/core` runs `go list
  -deps ./internal/core/...` and fails on any `r-loop/internal/` package other than
  `internal/core`.
- **Enums** — `StepState`: `queued · spawned · running · ok · failed · stalled · waiting-input`;
  `PhaseState`: `unticked · planned · implemented · landed · blocked` (`blocked` — the phase
  halted, or depends on one that did; resumable); `RunStatus`: `created · running ·
  halted · finished`. Legal step transitions: queued→spawned→running; running→ok | failed |
  stalled | waiting-input; waiting-input→running; stalled→running | failed. `ok` and `failed` are
  terminal. **A re-run — by resume or by a watchdog restart — is a new attempt**:
  `StepKey.Attempt` increments and the new key starts at `queued`; the old attempt's record is
  never reopened. Any other transition returns `ErrIllegalTransition`. A step's review rounds are
  not states: a step stays `running` through them, and each round is recorded as events.
- **Types** — `StepKey{Run string; Phase string; Kind string; Attempt int}` ·
  `Phase{ID string; Title string; Implements []string; DependsOn []string; Files []string; Risk
  string; Items []Item; DoneWhen string; Milestone int; Block string}` (`ID` is the heading label,
  `\d+[a-z]?` lowercased with leading zeros dropped — `10`, or `10a` for a phase inserted after 10;
  every phase reference below is that label, so the run dir is `phase-10a/`. Labels run in
  document order, fail-closed: no duplicate, the first is `1`, a bare number is the previous
  number + 1, a lettered label shares the previous number and sorts after it — `1, 2, 2a, 2b, 3`;
  `Block` is the raw heading-to-next-heading text, followed — when any ticked `## Resolve first` entry's `Blocks:`
  names that phase or all — by a blank line, `Resolved first:`, a blank line and the full text of
  each such entry; the `plan` and `implement` prompts tell the agent to follow each `Resolved:`
  line and never ask about it again) · `Item{Text string; Done bool}` · `Milestone{Number int; Name
  string; Phases []string}` · `Entry{Name, Body string; Ticked, HasBox bool; Owner, Blocks, Timebox,
  Output, Resolved string; BlocksAll bool; BlocksPhases []string; Malformed []string}` ·
  `Signal{Seq int; Kind SignalKind; Source SignalSource; Step StepKey; Reason, Evidence string; At
  time.Time; Rejected bool; RejectReason string}` · `Question{ID string; Step StepKey; Text
  string; Options []string; Recommended string; AskedAt time.Time; Answer, AnsweredBy, Citation string; AnsweredAt
  time.Time}` · `Remedy{ID string; Step StepKey; Class, Command, Why, Consent string; ProposedAt,
  DecidedAt time.Time}` · `Landing{Phase string; MergeSHA string; GateSkipped bool; GateOutput
  string; Added, Deleted int}` — the merge's diff size, measured by the land gate before the gate
  runs; a failed measurement aborts the merge · `Event{At time.Time; Kind string; Phase string; Step string; Fields
  map[string]string}` — the one stream both faces render · `RunMeta{Todo string; ResolvedConfig
  []byte; Started time.Time}` · `Record{Kind string; At time.Time; Step *StepKey; State
  StepState; Run RunStatus; Reason string; Question *Question; Signal *Signal; Remedy *Remedy; Landing
  *Landing; Event *Event}` · `RunState{ID, Todo string; Started time.Time; Status RunStatus; Steps map[StepKey]StepState;
  LastStep *StepKey; Landed []Landing; Questions []Question; Signals []Signal; Remedies []Remedy;
  Events []Event; Warnings []string; Spans map[StepKey]StepSpan}` · `StepSpan{Started, Ended
  time.Time}` — a step's first `running` record (the moment `Watch.StepStarted` fires) to its `ok`/`failed` record, replayed from the step log.
- **Ports** (interfaces in `internal/core`, each with a fake in `internal/core/fakes_test.go`):
  - `PlanSource`: `Read(path) (Plan, error)` · `Tick(path, phase string) error` (the `Stamp` method was removed
    with ADR-72: the watchdog writes the stamp); `Plan{Path, Topic string; Phases []Phase; Milestones
    []Milestone; ResolveFirst []Entry}`.
  - `SessionHost`: `Reachable() error` · `Open(OpenSpec{CWD, Label string; Env
    map[string]string}) (Workspace{ID, RootPane string}, error)` · `Start(pane, name, kind
    string, args []string) (Agent{Name, Pane string}, error)` (the herdr adapter retries
    `agent_pane_busy` every 250 ms within a 20 s budget and accepts codex's "Do you trust the
    contents of this directory?" dialog with `enter`, and — when a claude start fails
    `agent_not_ready` on a screen showing "Yes, I trust this folder" — claude's trust dialog
    with `down` then `enter`, then reads the visible screen every 250 ms within the 20 s budget
    until it shows claude's banner `Claude Code v` (failing `herdr: agent <name> never showed
    claude's prompt after the trust dialog` — herdr reports claude `idle` while it re-initialises
    and a prompt typed then is lost), then polls `State` the same way until herdr
    no longer reports the agent `blocked` (failing `herdr: agent <name> stays blocked after the
    trust dialog`), returning `Agent{name, pane}` from its own arguments; either fails
    when the dialog has not cleared in 20 s, and any other not-ready screen returns the
    `agent_not_ready` error with no key pressed — the driver arranging trust for the sessions it
    opens, spec ADR-1) · `Prompt(agent, text string, wait
    bool, timeout time.Duration) error` · `State(agent) (AgentState, error)` with `AgentState ∈
    {idle, working, blocked, done, unknown, gone}` · `Read(agent string, lines int) (string,
    error)` · `Interrupt(agent) error` · `Close(workspaceID) error` · `Split(pane, direction,
    cwd string) (pane string, error)` (an empty `pane` splits the pane herdr calls current) ·
    `AgentPane(agent) (string, error)` (the pane the named agent runs in, `""` when herdr knows no
    such agent) · `ClosePane(pane) error` (closes one pane; used only for the watchdog's own — a
    stale one of the same run at its start, and its own at the run's end) · `Tag(workspaceID,
    tokens map[string]string) error` (display-only sidebar tokens via herdr `workspace
    report-metadata --source r-loop`; an empty value clears one: `rloop` carries the step's live
    state, `rloop_wait` is set only while the step waits for input; a failure is a warning, never
    a step failure).
  - `Repo`: `Root() string` · `Clean() ([]string, error)` · `HeadBranch() (string, error)` ·
    `HeadSHA(dir) (string, error)` · `AddWorktree(dir, branch, base string) error` ·
    `RemoveWorktree(dir) error` · `DeleteBranch(branch) error` · `Dirty(dir) ([]string, error)` · `CommitAll(dir, message)
    (string, error)` · `DiffNonEmpty(dir, ref) (bool, error)` · `ChangedFiles(dir, ref)
    ([]string, error)` · `DiffStat(dir, ref) (added, deleted int, err error)` — **all three
    compare the working tree, untracked files included, against `ref`** · `Snapshot(dir)
    (tree string, err error)` — a tree object of the working tree, untracked files included,
    written through a throwaway index so no branch, commit or real index changes ·
    `TreeDiff(from, to string) ([]string, error)` — the paths that differ between two trees ·
    `MergeNoFF(branch) error` (`--no-commit`; a failure that leaves unmerged paths returns
    `ErrMergeConflict` naming them after `git merge --abort`; any other failure returns git's
    error) · `AbortMerge() error` · `Commit(message) (string, error)` (`git add -A`
    then commit, in the primary tree) · `CommitTouches(sha) ([]string, error)` · `ResetHard(ref)
    error` · `Run(dir, command string, timeout) (exit int, output string, err error)` (via `sh
    -c`).
  - `Store`: `Create(RunMeta) (runID string, err error)` · `Append(runID, Record) error` (a
    transition is appended **before** the action it describes) · `Load(runID) (RunState,
    error)` (reads every file of the run) · `Current() (runID string, pid int, ok bool)` ·
    `SetCurrent(runID, pid) error` · `ClearCurrent() error` · `Aborted(runID) bool` ·
    `MarkAbort(runID) error` · `Dir(runID) string`. The `store` adapter also has
    `ClearAbort(runID) error`, outside the port, which `r-loop resume` calls to remove the abort
    marker before it re-runs.
  - `Prompts`: `Render(name string, vars map[string]any) (text, source string, err error)`.
  - `AskChannel`: `Serve(ctx) (baseURL string, err error)` · `StepURL(StepKey) string` ·
    `Questions() <-chan Question` · `Answer(id, answer, by, citation string) error`.
  - `Face`: `Emit(Event)` · `Close()`. A face never asks anything (ADR-73).
  - `Notifier`: `Fire(hook string, env map[string]string)` (never returns an error to the loop; a
    non-zero exit is an `Event{Kind: "notify-failed"}`).
- **Run directory** (`Store` adapter) — `.r-loop/runs/<runID>/` with `runID =
  <yyyymmdd-HHMMSS>`; files: `config.resolved.yaml`, `events.jsonl` (step, run, landing and
  display events, including `baseline`, `snapshot` and `review-round` events), `questions.jsonl`,
  `signals.jsonl`, `remedies.jsonl`, `report.md`, and `phase-<N>/` holding sentinels, step logs,
  findings and verdicts. A question's answer is a second line for the same id; `Load` keeps the
  last. `.r-loop/runs/current` holds `<runID> <pid>`; `.r-loop/runs/<runID>/abort` is the abort
  marker. `.r-loop/runs/` and `.r-loop/wt/` are appended to `<git-common-dir>/info/exclude` when
  absent, never to `.gitignore`.
- **Sentinel** — JSON in `.r-loop/runs/<runID>/phase-<N>/`:
  `{"outcome":"ok"|"failed","reason":"<text>"}`; the driver timestamps every record itself, and an
  unknown field (an old sentinel's `at`) is ignored. The author half writes
  `<kind>-a<attempt>.sentinel`; a reviewer `<kind>-rv-<name>-r<round>-a<attempt>.sentinel`;
  the author's verify-and-apply half `<kind>-fix-r<round>-a<attempt>.sentinel`. Unreadable or
  malformed → the step is `failed` with reason `sentinel unreadable`.
- **Config resolution** — every key resolves CLI override → `.r-loop/config.yaml` →
  `~/.config/r-loop/config.yaml` → the embedded defaults, with provenance `<file>:<key>`,
  `flag:--provider`, `flag:--model`, `flag:--effort` or `default`. Step row keys: `prompt, check,
  provider, model, effort, timeout`, `fallback`, plus the review half on any row: `reviewers`,
  `rounds`, `reviewTimeout`. **A reviewer entry and a `fallback` are each a scalar provider name
  (model and effort left to the provider, flag omitted, banner prints `provider default`) or a
  block with `provider`, `model`, `effort`; a reviewer block also takes `name`, `prompt` and
  `requires` (Milestone 8).** `--provider`, `--model` and `--effort` each take
  `<step>=<value>` and override only that row's own key, never its reviewers; a key
  `land.fix` inherits from the implement row is the overridden value. **A `--provider
  <step>=<p>` naming that row's fallback provider swaps the fallback for the run** (spec ADR-66,
  amended 2026-09-19): the fallback becomes the row's configured provider, model and effort — the
  values before any flag — with provenance `flag:--provider`, and the banner adds `override:
  steps.<step>.fallback swapped to <provider> (--provider) replaces <old provider model effort>
  (<source>)`; `--model` and `--effort` never touch the fallback. A config file's `fallback` naming
  its row's own provider is still rejected. `land.fix` is an optional block with `provider`, `model`, `effort`, all optional,
  for the `gatefix` step: absent, the implement row's three; a missing key is the implement
  row's unless the block names another provider, in which case it stays empty (that provider's
  default) and takes `land.fix.provider`'s provenance. The reader resolves `land.fix` at load, so every later reader sees three plain
  values. `watchdog.effort` sits beside `watchdog.provider` and `watchdog.model`. `rounds: 0`
  or an empty `reviewers` list means the step has no review half. Rows: `plan`, `implement`,
  `milestone`. Defaults: pipeline `[plan, implement]`; plan `claude/opus/high/1h/plan-file`,
  reviewers `[codex]`, `rounds 2`, `reviewTimeout 20m`; implement
  `codex/gpt-5.6-sol/medium/4h/diff`, reviewers `[claude, ui]` (`ui` = `claude/opus/high`,
  `prompt review-ui`, `requires .claude/skills/test-app/SKILL.md`), `rounds 3`, `reviewTimeout 45m`;
  fallback plan `codex`, implement `claude` (scalars: provider defaults for model and effort);
  milestone `claude/opus/medium/1h/report`, no review; `land.fixRounds 1`, `land.gateTimeout 30m`, no `land.fix`;
  `unattended.allow [deps, ports, locks, restart, retry, provider]`, applied only with
  `--unattended`;
  `watchdog.maxRestarts 2`; `watchdog.provider claude`, `watchdog.model opus`,
  `watchdog.effort high`, `watchdog.unblockTimeout 2h`, `watchdog.allow []`, `watchdog.remedyWindow 10m`,
  `watchdog.checkTimeout 10m`, `watchdog.stallGrace 2m`,
  `watchdog.overtimeFactor 2`, `watchdog.diffFactor 3`; `notify.onHalt/onWarn/onDone ""`. A
  flow-style YAML node is rejected naming the line; an unknown key is rejected naming the key and
  file; a negative `rounds` is rejected; each is exit `2`.

## Milestone 2 — Sessions and providers

- **Provider block** — `providers.<name>`: `kind` (required), `flags` (fixed, no
  placeholder, passed first on every start), `modelFlag` (`{model}`),
  `effortFlag` (`{effort}`, may be empty → banner `effort n/a`), `askFlag` (`{url}` or
  `{mcpConfig}`), `doneSignal ∈ {sentinel}`, `ask ∈ {mcp, none}`, `review` (may be empty; `{args}` is replaced with `Args(p, model, effort, "", "")`, and `{output}` with the shell-quoted `<ArtifactsDir>/native-review.txt`).
  **Precedence is whole-block**: the project config's block, else
  `~/.config/r-loop/providers/<name>.yaml`, else the shipped block. `Args(p, model, effort, askURL,
  mcpConfigPath)` expands the templates and omits a flag whose template or value is empty.
  Shipped: `claude` (`--model {model}`, `--effort {effort}`, `--mcp-config {mcpConfig}`, `review: /code-review`) and
  `codex` (`flags: -c check_for_update_on_startup=false`, `-c model={model}`, `-c model_reasoning_effort={effort}`, `-c
  mcp_servers.r-loop.url={url}`, `review: codex exec review --uncommitted {args} -o {output}`).
  `{mcpConfig}` is a per-agent file
  `{"mcpServers":{"r-loop":{"type":"http","url":"<url>"}}}`. Neither sets an MCP tool timeout:
  every call returns at once (ADR-76), so the clients' defaults are enough. The core sees a provider only as
  `ProviderArgs{Kind string; Args []string; Ask bool; Review string}`.
- **Step kind** — `StepKind{Name, Prompt, Check string; Row StepRow}`; `StepRow{Provider, Model, Effort string; Fallback Fallback; Timeout time.Duration; Reviewers []Reviewer; Rounds int; ReviewTimeout time.Duration}`; `Reviewer{Provider, Model, Effort string}`, `Fallback{Provider, Model, Effort string}` and `GateFix{Provider, Model, Effort string}` — one type per role, the same three fields; an empty `Model` or `Effort` is the provider's default. A row's `check ∈ {plan-file,
  diff, report}` or one added with `RegisterCheck`; `findings` and `verdict` are the review
  half's own checks and are never named on a row. Evidence predicates take
  `EvidenceContext{Repo; Worktree, StartSHA, StartTree, PlanPath string; FindingsFiles []string;
  VerdictPath, RoundTree, ReportPath string; FS fs.FS}`. **`StartTree` is the step's baseline:
  `Snapshot(worktree)` taken when its first attempt spawns (ADR-58, amended 2026-09-19), so every
  check measures the step's cumulative change across its attempts, never the phase base or an
  earlier step's leftovers. A restart or a resume reuses the recorded baseline; an attempt after
  the step's `ok` attempt takes a fresh one.**
- **Shipped checks** — `plan-file`: the plan exists; one of its first five lines is `status:
  planned`; it carries the four headings `## Summary`, `## Changes`, `## Tests`, `## Assumptions`;
  `## Tests` holds at least one list item (`- `, `* `, `+ `, `N.` or `N)`) or markdown table data
  row (below the header and its `|---|` separator); and `TreeDiff(StartTree, Snapshot(worktree))`
  names the plan and no other path — `plan step changed <path>` for another path, `plan step did
  not change <PlanPath>` for none. `diff`: `TreeDiff(StartTree, Snapshot(worktree))` is non-empty. `report`: the
  report exists and is non-empty. `findings` and `verdict`: Milestone 4.
- **Phase plan** — `.task-plans/phase-<N>-<kebab title>.md` (≤ 60 characters), r-loop's own
  format; the `/r:task-run` format is not read or written:

  ```markdown
  status: planned

  ## Summary       what the phase does and the approach, in a few lines
  ## Changes       per file: create or modify, what changes, which existing code it reuses
  ## Tests         the tests to write first, covering every open item of the phase
  ## Assumptions   choices made without asking, or "none"
  ```

  The driver reads `## Assumptions` after the plan step ends `ok` and emits each list item (never a
  table row, never a `none` item) as an `Event{Kind: "assumption"}`; the run report lists them per phase.
- **Two-signal rule** — `ok` only when the sentinel says `ok` **and** the evidence predicate
  passes **and** `HeadSHA(worktree)` still equals `StartSHA` (an agent commit is
  `failed(step committed before review)`); sentinel `failed` → `failed(<reason>)`; ok with
  evidence missing → `failed(evidence missing: <what>)`; `blocked`/`idle`/`done` (herdr's `done`
  is idle with output nobody has looked at, reported until a client focuses the pane, which r-loop
  never does) for
  `watchdog.stallGrace` (default 2 min), no sentinel, no open question → `stalled`, and the
  driver sends one fixed nudge (below); `working` again → `running`; still quiet a further
  `stallGrace` after the nudge → `failed(stalled: no response to nudge)`; elapsed > timeout →
  `failed(backstop)`. The driver never kills a session: a failed step's session is left
  standing. For a step with a review
  half the same rule applies per half and per round.
- **Nudge** — fixed text in `internal/core`, never model output: `r-loop: no sentinel and no
  activity for <grace>. If your work is done, write the sentinel now. If you are blocked, call
  ask_watchdog, or write a failed sentinel with the reason.` — `call ask_watchdog, or` only when the
  session has an ask URL. Sent once per stall, to step agents and reviewers alike, and recorded as
  `Event{Kind: "nudge"}`; a nudge that cannot be delivered fails the step `stalled: nudge not
  delivered: <err>`.
- **Names and paths** — phase branch `r-loop/phase-<N>`; worktree `.r-loop/wt/phase-<N>/` from the
  primary tree's HEAD branch (`base`); slug `phase-<N>-<kebab title>` (≤ 60 chars); workspace label
  `◆ [<label> ]p<N> <kind>[·a<attempt>]`; author, gate and milestone agent
  `rloop-[<label>-]<token>-p<N>-<kind>[-a<attempt>]`; reviewer agent adds
  `-rv-<name>-r<round>[-a<attempt>]`. The token is five base36 characters of
  `fnv32a(<repo root>\n<runID>[\n<salt>])` modulo 36⁵. A taken name uses salt 1 or 2.
  All these agents use one 32-character cap: the suffix stays, while an overlong prefix is cut
  and receives a five-character hash of its full value. An `agent-named` event with
  `attempt`, `agent`, and for reviewers `reviewer` and `round`, is appended before each `Host.Start`.
  Watchdog `rloop-wd-<runID>`
  (`core.WatchdogName`: the run id lowercased, every character outside `[a-z0-9_-]` turned into
  `-`, and a name longer than 32 cut to fit with a `-<8-hex fnv32a of the run id>` suffix), so a
  run never touches another run's watchdog — all within `[a-z][a-z0-9_-]{0,31}`. Findings `phase-<N>/<kind>-findings-<name>-r<round>.json`; verdict
  `phase-<N>/<kind>-verdict-r<round>.json`; milestone report
  `docs/<topic>/reports/milestone-<M>-<slug>.md`.
- **Spawn** — `SessionManager{Host; Repo; Prompts; Store; Ask AskChannel; Resolve; Now; Poll,
  StallGrace}`; `StepRef{Key StepKey; Kind StepKind; Phase Phase; InPrimary bool; Worktree, Branch,
  Base, RunDir, AskURL string; Vars map[string]any; ReviewFrom int; PrevRoundTree string;
  KeepUncommitted bool}` — the last three only from a resume: `ReviewFrom`/`PrevRoundTree` restart
  the review half at a recorded round (the agent starts without the work prompt), and
  `KeepUncommitted` marks a `--replan` plan attempt that commits nothing. Unless `InPrimary`, ensure
  the worktree; record `StartSHA = HeadSHA(dir)`; `StartTree` is the tree of the last `Event{Kind:
  "baseline", Fields{step, tree}}` recorded for this phase and step kind when the previous attempt
  did not end `ok`, else `Snapshot(dir)`, appended as that `baseline` event; append `spawned`;
  `Open` with `Env{R_LOOP_SENTINEL, R_LOOP_RUN, R_LOOP_PHASE, R_LOOP_STEP}`; `Start` in the root
  pane; render; `Prompt` without wait; append `running`. A failure from `Open` on returns `spawn:
  herdr: <code>: <message>`, leaves whatever opened standing, and is never retried by the session
  manager. `InPrimary` spawns in `Repo.Root()` with no worktree and no review half (the milestone
  report). `Wait(ctx, s, Observer)` reports through `Observer{Started, Stalled, Resumed,
  Reviewing(s, round)}` — `Reviewing` a review round's find half; an observer that also has
  `Fixing(s, round)` is told when the fix half starts — and returns on a sentinel, a gone agent, the
  backstop, a stall unanswered after its nudge (`failed(stalled: no response to nudge)`, ADR-62) or
  the context ending (`failed(interrupted: <ctx error>)`). `Stop` is `Interrupt`. A step's workspace
  stays standing while its phase is blocked or aborted, for inspection; the loop closes it when a
  later attempt of the same step starts (resume or watchdog restart), and closes every workspace
  the phase's steps opened once the phase lands or its item is skipped. Each close appends a
  `workspace-closed` event before the `Close` call, so no workspace is closed twice; a failed close
  is a warning, never a failure. After a land, and after its workspaces close, the loop appends `worktree-removed`
  and runs `RemoveWorktree(.r-loop/wt/phase-<N>)` then `DeleteBranch(r-loop/phase-<N>)`
  (`git branch -d`, which refuses an unmerged branch); either failing is a warning. A skipped item
  never merged, so it keeps its worktree and branch.
- **One commit per step** — after the author half (and, when configured, every review round) ends
  `ok`, the driver runs `CommitAll(worktree, "r-loop: phase <N> <kind>")` once, and only then
  records the step `ok`. A step that ends any other way commits nothing: its work stays
  uncommitted in the worktree, and the driver appends `Event{Kind: "snapshot", Fields{step,
  tree}}` with `Snapshot(worktree)` before it records the terminal state, so a resume can tell the
  step's own leftovers from someone else's.
- **Prompt variables** — `PhaseNumber, PhaseTitle, PhaseBlock, Criteria, TodoPath, SpecDir,
  PlanPath, Branch, Base, Worktree, Sentinel, RunDir, AskURL, PhaseWarnings, ReviewedKind, Round,
  Rounds, ReviewCommand, FindingsPath, FindingsFiles, PriorFindings, PriorVerdicts, RoundTree,
  VerdictPath, ReportPath, MilestoneName, MilestonePhases, Addendum`; override
  `.r-loop/prompts/<name>.md`, else embedded. Templates: `plan`, `implement`, `review`, `fix`,
  `milestone`, `watchdog`, `gatefix`; `gatefix` adds `GateCommand` and `GateOutput`; `watchdog`
  receives only `TodoPath`, `SpecDir`, `RunDir` and `Allow` (the allow-listed remedy classes).

## Milestone 3 — The serial loop, landing and the plain face

- **Runners** — `StepRunner.Run(ctx, StepRef, Observer) Outcome`, run in a goroutine; the loop
  holds the live session from `Observer.Started`. `core.DefaultRunners(sm, kinds)` is keyed by a
  step kind's `Check`; any check without an entry uses the single-session runner: `Spawn`,
  `Wait`, then the review half when `Row.Reviewers` is non-empty and `Row.Rounds > 0`, then the
  one commit. Until Milestone 4 lands the review half is a no-op, so wiring never changes when it
  does. `core.StepVars` fills every template variable, empty where unused.
- **Run list** — unticked phases in document order, narrowed by `--from N` (that label and every
  phase after it) or `--phases n,n`, both taking labels such as `10a`; a
  listed phase ticked or absent is exit `2`. Phase state advances `planned → implemented →
  landed`.
- **Outcomes** — `failed` → wait `watchdog.remedyWindow` for a `Restart` (skipped with no
  watchdog; at most `watchdog.maxRestarts` restarts per step), then the phase is **blocked**:
  `Event{Kind: "phase-blocked", Fields{phase, reason}}` and `notify.onWarn` with
  `R_LOOP_STATUS=blocked`; every phase that depends on it, directly or through others, is
  `blocked` with `Event{Kind: "phase-skipped", Fields{phase, because}}`; the loop continues with
  the next phase that can run. A watchdog `halt` → `failed(watchdog: <reason>)` appended first,
  then `Interrupt` — the attempt's only `failed` record — and the phase is blocked the same way,
  with no remedy window; a halt for a step that just ended blocks that step's phase and closes its
  remedy window. An abort likewise records the run `halted` before the live step is cancelled. When nothing is left to run: all landed → `finished`,
  `notify.onDone`, exit `0`; any phase blocked → `halted`, `notify.onHalt`, `r-loop resume`
  printed, and the exit code of the **first** block — `1` failed, `3` a failure that began as a
  stall, `5` a watchdog halt. Abort marker → exit `1` at once; preflight refusal `4`; usage, git
  state, config `2`; missing binary `127`.
- **Resume** — skips landed phases and `ok` steps, and re-runs the stopped step of **every**
  phase that halted, in phase order, as a new attempt on its own worktree; a phase blocked only
  because of a dependency simply runs. Before claiming, resume asks herdr for the state of the
  phase's last attempt's recorded step and reviewer agents from `agent-named` events; for a run
  without these events it rebuilds the old step and reviewer names. When an agent is `working` or
  `blocked` — a driver killed mid-step leaves it running — resume appends `Event{Kind:
  "stale-interrupted", Phase, Step, Fields{agent, state}}`, then `Interrupt`s it and prints
  `interrupted previous session <agent>: still <state>`. After the claim and before the re-run, every
  step of a halted phase still in a non-terminal state is recorded `failed(interrupted: driver died)`
  (a resume-only close from any non-terminal state; attempt+1 is the re-run). The worktree is **claimed** when it is
  clean, when `Snapshot(worktree)` equals the last `snapshot` (or `review-round`) event recorded
  for that step, or when that step never reached `ok` or `failed` and a `baseline` event exists
  for it — the changes are its own leftovers, and the new attempt reuses that baseline, so
  evidence counts them (spec ADR-16, amended 2026-09-19); anything else — a tree under a terminal
  step that differs from its snapshot, or under a step with no baseline — is an unclaimed tree,
  exit `2`. A step whose author half had passed resumes at its recorded round
  with a fresh author session. `--replan` re-runs the phase's `plan` step as a new attempt, with
  the failed step's reason as its addendum, before the step that failed. The watchdog is always
  started (`--no-watchdog` was removed, ADR-71). A Resolve-first entry answered during resume is recorded in the resumed
  run as it is at startup (Report, below).
- **Gate fix** — a red gate with `land.fixRounds` left runs one `gatefix` step in the phase
  worktree on the config's resolved `land.fix` provider, model and effort (the implement row's
  unless `land.fix` names its own), with `GateCommand` and `GateOutput`, `check: diff`, the
  implement row's timeout and its reviewers for one round; the gate command itself runs under
  `land.gateTimeout` (default 30m); its `ok` commits `r-loop: phase <N> gatefix`, and the
  landing starts again from the merge. A red gate with no fix round left blocks the phase.
- **Land** — in the primary tree: `MergeNoFF(r-loop/phase-<N>)` (`--no-commit`) → the merged tree
  is on disk, uncommitted → `Run(root, gate command, gate timeout)`, the gate command being the
  `Done when:` line's inline code spans joined with ` && `, or its trimmed text when it has none
  (the gate fix's `GateCommand` is the same string); exit ≠ 0 → `AbortMerge()`, halt
  with the output, nothing ticked, a gate-fix round when one is left; no command → `gate-skipped` recorded, never a halt → `Tick`
  → `Commit("phase <N>: <title>")` → `CommitTouches` must include the todo and one other path, else
  `ResetHard("HEAD~1")` and halt. A conflict aborts before the gate. The gate therefore proves the
  phase's code, and no merge commit exists unless it passed.
- **Milestone boundary** — `LandGate` calls its `Boundary` after a landing; when phase N closed
  `## Milestone M`, one `InPrimary` session on the `milestone` row writes the report; `ok` →
  commit `docs(report): milestone <M>`; anything else → `report-skipped`, never a halt.
- **Report** — `report.md` opens with `human touches: <n>` (every answer a person gave, every
  consent, every resume, every Resolve-first answer) and an **Automatic decisions** section: restarts with their remedy,
  fallback restarts with the provider, model and effort used, gate-fix rounds,
  round-limit warnings, nudges, and blocked and skipped phases. A Resolve-first answer — at startup
  or during resume — is an `Event{Kind: "human", Step: "resolve first", Fields{what: answer, id:
  r<n>, entry, answer, by}}`, appended as soon as the run exists, and listed first under Questions
  as `r<n> resolve first: <entry> → <answer> (<by>)`.
- **Banner** — one line per pipeline row and the milestone row, `<step> <provider> <model>
  <effort> <timeout> <check> ← <provenance>`; under a row with a review half, `review rounds <n>
  <reviewTimeout>` and one `reviewer <provider> <model|provider default> <effort|provider
  default>` line per reviewer and `fallback <provider> <model|provider default> <effort|provider
  default>` per row that has one; `gatefix <provider> <model|provider default> <effort|provider
  default>`; watchdog on/off with `<provider> <model> <effort|provider default>`. Every step,
  reviewer, fallback, gatefix and watchdog line ends `← <provenance>`: one source when its
  provider, model and effort share it, else `provider <src> model <src> effort <src>`. Overrides
  with the value replaced; prompt source per step; `mode: unattended` with the added allow-list,
  or `mode: attended`.
- **Plain lines / status / report / notify env** — as written in Phases 15–16: `HH:MM:SS phase
  <N> <kind> <state> <provider> <detail>` (during a review, `<detail>` is `review r<round>/<rounds>
  <half>`, `<half>` `find` or `fix`); `HH:MM:SS phase <N> <kind> nudge` when a stalled step is
  nudged; `r-loop status --plain` lines
  — `run <id> running (driver pid <pid> not alive — r-loop resume)` and no `live` line when
  `current` names this run with a dead pid, `phase <N> not in this run` for an unticked phase
  outside the recorded run list; `report.md` rewritten on every
  transition; hook env `R_LOOP_RUN, R_LOOP_STATUS, R_LOOP_PHASE, R_LOOP_STEP, R_LOOP_REASON,
  R_LOOP_TODO, R_LOOP_REPORT`, with `R_LOOP_STATUS ∈ {halted, finished, warning, blocked}`,
  `sh -c`, 60 s.

## Milestone 4 — The review half

- **Shape** — a review half runs after a step's author half ends `ok` and before the step's
  commit, inside the step's own workspace: the author stays in the root pane, and each reviewer
  gets a pane split to its right (`Split(rootPane, "right", worktree)`, stacked when there are several).
  **Each round starts a fresh reviewer agent** in a fresh pane: from round 2 on, the previous
  round's reviewer panes are closed (`ClosePane`) and split again in the same places, since an
  interrupted agent (Claude Code at an idle prompt) need not exit and would leave its pane busy. The author is the same agent session through
  every round. A reviewer gets its own ask URL
  (`<base>/<phase>/<kind>-rv-<name>/<attempt>`) and, for a `{mcpConfig}` flag,
  `<RunDir>/phase-<N>/<kind>-rv-<name>-a<attempt>.mcp.json`. Preflight refuses a provider with
  `ask: none` in any session role (ADR-73), so every reviewer has the channel.
- **A round** — (1) `RoundTree = Snapshot(worktree)`, appended as `Event{Kind: "review-round",
  Fields{step, round, tree}}` before any reviewer starts; (2) resolve every reviewer (one with no
  `review` command fails the step before any pane opens); start every reviewer agent, then prompt
  each with the `review` template; (3) join on every reviewer sentinel; any `failed` or `stalled`
  reviewer fails the step naming it; every findings file must pass the `findings` check; a native reviewer's `<ArtifactsDir>/native-review.txt` must exist and be non-empty, else ``evidence missing: native review `<cmd>` produced no output``; then
  `TreeDiff(RoundTree, Snapshot(worktree))` must be empty, else `failed(reviewer modified the
  tree: <paths>)`; (4) no findings in any file → the review half ends clean; (5) prompt the author
  with the `fix` template; join on its sentinel; run the `verdict` check against `RoundTree`, then
  the step's own evidence check again; (6) no verdict `real` at P1 or P2 → the review half ends
  clean; otherwise the next round.
- **Clocks** — while reviewers run, the author's stall clock and backstop are suspended; each
  round's reviewers and the author's fix half share `Row.ReviewTimeout`. An open question
  suspends clocks as everywhere else, and a reviewer's clocks also hold while its step has an open
  question.
- **Round limit** — reaching `Row.Rounds` after a round that fixed something ends the review half
  `ok` with a `warn` Signal from the driver: `review round limit reached; round <n> fixes
  unreviewed`. It is never a halt.
- **What a reviewer sees** — for `plan`: the plan file against the phase block and the code; for
  `implement` and any other kind: the uncommitted changes in the worktree. Round 1 reviews all of
  it; later rounds review all of it with the delta since `RoundTree` of the previous round named,
  and receive every earlier round's findings and verdicts — a finding dismissed with evidence is
  not raised again without new evidence.
- **Findings file** — fixed JSON, written by the reviewer from its native output:
  `{"reviewer":"<provider>","findings":[{"id":"<provider>-r<round>-<n>","title":"…","detail":"…","files":["…"]}]}`.
- **Verdict file** — written by the author: `{"findings":[{"id":"<provider>-r<round>-<n>",
  "reviewer":"<provider>","title":"…","verdict":"real"|"not-real"|"out-of-scope",
  "severity":"P1"|"P2"|"P3"|"P4","fixed":true|false,"files":["…"],"evidence":"<path:line>"}]}`.
- **`verdict` check** — verdict ids equal the round's finding ids across every findings file;
  every entry has a verdict and a severity; every `not-real` entry carries `evidence` matching
  `^[^\s:]+:\d+$` whose path exists in the worktree; every `fixed: true` entry is `real` at P1/P2
  and each of its `files` is in `TreeDiff(RoundTree, Snapshot(worktree))`; every `real` P1/P2 is
  `fixed: true`. Every finding is emitted as `Event{Kind: "finding", Fields{step, round,
  reviewer, id, title, verdict, severity, fixed, evidence}}`; the report lists dismissals with
  their evidence.

## Milestone 5 — The ask channel

- **Server** — MCP go-sdk streamable HTTP on `127.0.0.1:<free port>`, base `/mcp/<runToken>`
  (32 hex chars, stored mode 0600). A step URL is `<base>/<phase>/<kind>/<attempt>`, a reviewer's
  `<base>/<phase>/<kind>-rv-<name>/<attempt>` — the path identifies the asking agent. Tool
  `ask_watchdog(question, options?, recommended?) → {id, status: "asked"}` returns at once
  (ADR-76); ids `q<seq>`. Its text tells the agent the answer will arrive as its next message, to
  end its turn now and do nothing else until it arrives. The question is sent on `Questions()`
  before the call returns; a call that ends before the driver takes it drops the question. One
  open question per `StepKey`: a second `ask_watchdog` while one is open is a tool error naming
  the open id and saying to end the turn and wait. `Server.Answer(id, …)` closes the question, so
  the step may ask again; an unknown or already-closed id is an error. No call waits, so there is
  no keepalive and no MCP tool timeout on any client.
- **waiting-input** — a question moves the step `running → waiting-input`, freezes its backstop
  and its stall nudge, and is recorded; the answer, once typed into the asking pane, returns it to
  `running`. A backstop firing in `waiting-input` halts
  with `invariant: a question never expires`.
- **Routing** — the loop freezes the step first, records and emits the question, then hands it
  to `Watcher.Route` (the watchdog, Milestone 7). No face ever asks (ADR-73). The driver never
  answers a question itself: when the watchdog is gone, `Watch.Route` halts the run instead.
- **Delivery** — `RunLoop.Deliver(id, answer, by, citation)` records the answer, emits
  `question-answered`, closes it with `AskChannel.Answer`, and then, off the caller's goroutine,
  types `r-loop: answer to <id> (by watchdog, citing <path:line>): <answer>` — or `(by maintainer)`
  — into the agent that asked with `Host.Prompt(agent, text, false, 0)`. The agent is named when
  the question is admitted: the step's own session for `<kind>`, the round's reviewer pane for
  `<kind>-rv-<name>`. It types only once `Host.State` reports the agent not `working`, checking at
  the session poll interval and retrying on `agent_blocked`; then the step returns to `running`.
  An agent found gone first withdraws the question (`Answer: agent gone`) and releases the step.
- **Withdrawal** — when a step ends (`ok`, `failed`, `stalled`, a halt or an abort) its open
  questions, answered but not yet typed included, are withdrawn: each is recorded `AnsweredBy:
  withdrawn`, `Answer: step <state>`, and closed on the server; nothing is typed. `r-loop resume`
  withdraws every question left open by a driver that died (`Answer: step failed`), since its step
  ended with the driver. A question that arrives for a step that
  has already ended is withdrawn at once (`Answer: step ended`) and never moves the step to
  `waiting-input`. An answer to a withdrawn question or an ended step's question is dropped with
  `Event{Kind: "note", Fields{reason: "answer dropped: question <id> is not open"}}` — not
  delivered, not a human touch, and never moving a step out of `ok` or `failed`.

## Milestone 6 — The TUI

- **Model** — one Bubble Tea model fed by `Event`s; header, phase rail (24 columns), live-step
  panel, warnings. Instrument tokens from `DESIGN.md`: surface `#0F1115`, raised
  `#171A20`, text `#D6DAE0`, dim `#8A929E`, primary `#6E9FC4`, secondary `#E0A458`, tertiary
  `#8FA87F`, error `#E0736A`, outline `#2E343D`. Both faces consume the same event log. During a
  review the live step reads `<kind> · review r<round>/<rounds>`. The TUI runs only without
  `--plain` and with a terminal on both stdin and stdout. `error` events and the halt banner draw
  in `error`, warnings in `secondary`; with `NO_COLOR` the loud states (a blocked phase, the halt
  banner) fall back to inverse. The TUI shows no questions: the maintainer answers in the
  watchdog's pane (ADR-73).
- **Stop** — `ctrl+c` on a live run asks `stop the run? … [y/n]`; `y` marks the run aborted,
  exactly as `r-loop abort` does (the live step's session and worktree are left for resume); any
  other key cancels. Before a run exists `y` only says `stopping before the run starts`.
- **Dry run** — after the run list, each open `## Resolve first` entry that blocks a phase in it
  is named: `open ## Resolve first: "<name>" blocks phase <n> — the run will ask for it` (TUI) or
  `… the run refuses until it is resolved: /r:plan-unblock <todo>` (`--plain`).

## Milestone 7 — The watchdog

- **Session** — agent `rloop-wd-<runID>` (`core.WatchdogName`, Milestone 2 names). `Start` first
  asks `AgentPane` for that name; a stale watchdog of the same run (left by a killed driver) is
  recorded as `Event{Kind: "watchdog-stale-closed", Fields{pane}}` and closed with `ClosePane`.
  It then records `Event{Kind: "watchdog-start"}` and splits a pane to the right of the driver's
  own pane — `HERDR_PANE_ID`, passed in as `Env.Pane` and `Watchdog.Pane`; with no driver pane it
  opens its own workspace labelled with the watchdog name. `Stop` closes that workspace, or else
  the pane. If `Start` fails on its row, the run is blocked with exit 4 (`watchdog did not start:
  <reason>`); there is no fallback provider for the watchdog.
- **Delivery** — step notices go through `Watchdog.Post`, an ordered outbox one goroutine drains
  with `Notify`; questions, phase checks and the unblock walk call `Notify` directly, after any
  queued notices. A prompt refused with `agent_blocked` is retried every 30 s while the agent
  exists; the first refusal records and emits `Event{Kind: "watchdog-waiting"}`, the next
  accepted prompt `Event{Kind: "watchdog-resumed"}`, and one `Host.State` of `gone` records
  `Event{Kind: "watchdog-unreachable"}` and fires `OnGone` (wired to `Watch.WatchdogGone`): the
  live step gets one driver `halt` `the watchdog is gone`, and with none live the loop starts no
  further step or phase, so the run halts with exit `5` at once. `Stop` drains the outbox, giving
  up on a blocked prompt, and never fires `OnGone`.
- **Second MCP surface** — `<base>/watchdog/<wdToken>`; tools `signal(kind, step, reason,
  evidence) → {accepted, reason?}` · `propose_remedy(class, command, why, maintainer_said?) →
  {decision: authorised|refused|ask, reason?}` ·
  `restart_step(step, addendum?, provider?, maintainer_said?) → {accepted, reason?}` ·
  `answer_question(id, answer, citation) → {accepted, reason?}` ·
  `ask_maintainer(question, options?, recommended?) → {accepted, reason?}`; `step` is `phase-<N>/<kind>`,
  resolved to the latest attempt. None of these is reachable from a step path, and this path
  serves no `ask_watchdog`. Every call is recorded as `watchdog-call` before its handler runs.
- **Waiting for the maintainer (ADR-73, amended 2026-09-23)** — `ask_maintainer` returns at once;
  an empty question is refused. It calls `Watchdog.AskMaintainer`, which records and emits
  `Event{Kind: "watchdog-waiting", Fields: {question, options?, recommended?}}` (`options` joined
  with `; `). `Remedies.Propose` returning `ask`, and `Remedies.Restart` refusing a provider that
  is not the row's fallback without `maintainer_said`, mark the same wait with the remedy as the
  question. Every other watchdog tool first calls `Watchdog.Resume`, which records and emits
  `watchdog-resumed` once; a resume that cannot be recorded refuses the call. The wait is one
  flag shared with the `agent_blocked` wait above, so neither path double-emits; an accepted
  prompt ends it only when herdr reported the watchdog blocked during it, since a codex watchdog
  asking in plain text accepts prompts while it waits. The TUI adds an amber feed line
  `watchdog asks you: <question>`; the plain face prints `!  watchdog waiting for you: <question>`.
- **Acceptance** — `Watch.Accept`: `warn` or `halt` naming the live step, or one that ended within
  the last poll tick, is accepted; anything else is recorded `Rejected` with its reason. A
  `signal` whose `step` is not `phase-<N>/<kind>` still goes through `Accept` and is rejected. A
  rejected watchdog `signal` halts the run (exit `5`). **During a phase check** the only
  accepted step is `phase-<N>/check`: `warn` is accepted, `halt` is rejected with `phase check
  may only warn` and does **not** halt the run.
- **Routing** — `warn` → event + `notify.onWarn`; `halt` → `failed(watchdog: <reason>)` appended,
  then `Interrupt`, exit `5`; an accepted `halt` for a step that just ended blocks its phase, exit
  `5`, and closes its remedy window. Driver checks
  emit the same `Signal` with `Source: driver`, all `warn`.
- **Phase check into the plan** — the accepted warnings of `phase-<N>/check` become the plan
  prompt's `PhaseWarnings`, so the planner addresses each one.
- **Remedies** — classes `deps · ports · containers · locks · restart · retry · provider`;
  allow-listed → authorised at once; otherwise an empty `maintainer_said` returns `ask` and records
  nothing, and a non-empty one — the maintainer's reply, quoted, from the watchdog's own session —
  is authorised with `Consent: maintainer` (the `watchdog-call` event keeps the quote); the record
  is the command as proposed with `Consent ∈ {allow-list, maintainer, refused}`; the driver never
  runs it.
  `restart_step` is accepted only for a `failed` or `stalled` step inside its remedy window, after
  an authorised remedy for it (or an allow-listed restart class), and while the step has had fewer
  than `watchdog.maxRestarts` restarts; it queues a new attempt. With `--unattended`,
  `unattended.allow` is added to `watchdog.allow`, and an allow-listed `provider` restart may name
  only the row's `fallback` — any other provider needs `maintainer_said`, unless a `provider`
  remedy the maintainer authorised names it, which authorises the restart without asking again. A restart naming the fallback runs
  the new attempt on the fallback's provider, model and effort; one naming any other provider
  runs on that provider's defaults; neither carries the row's own model or effort.
- **Citations** — `path:line`, the path relative to the repository root, existing in the
  **primary tree** and not under `.r-loop/`, or the literal `maintainer` when the watchdog asked
  the maintainer in its own session. An empty or invalid citation is refused and the question
  stays open with the watchdog. The primary tree holds the spec, the tech design, the
  todo, the committed phase plans and every landed phase, and never the current phase's
  uncommitted worktree.

## Milestone 8 — The UI test reviewer

Spec ADR-74. One more reviewer on implement, not a step kind: it runs the project's own
`/test-app` skill and reports through the same findings, verdict and round machinery as a code
reviewer.

- **Reviewer block** — `{provider, model, effort, name, prompt, requires}`, all but `provider`
  optional. `core.Reviewer{Provider, Model, Effort, Name, Prompt, Requires}`; `ID()` is `Name` or
  else `Provider`, `Template()` is `Prompt` or else `review`. `name` matches `[a-z0-9][a-z0-9-]*`;
  two reviewers of one row with the same `ID()` are a config error (exit 2); `requires` is a local
  path (`filepath.IsLocal`). A `fallback` or `land.fix` block rejects the three new keys.
- **Identity** — `ID()` keys the reviewer's `StepKey.Kind` suffix `-rv-<name>`, agent, sentinel,
  MCP config, ask URL path, `FindingsPath`, the findings file's `reviewer` and id prefix, and the
  `review-find` (including its `command` field) and `finding` events. `Provider` alone picks the CLI, model and effort.
- **Native command** — only a reviewer whose `Template()` is `review` needs `ProviderArgs.Review`,
  in `ReviewHalf.Run` and in preflight.
- **Requirement** — once, before the first round (or the resumed round), each reviewer with
  `requires` is looked up in the worker's worktree, then in `Repo.Root()`. Absent from both, it is
  left out of every round and `Event{Kind: "reviewer-skipped", Fields{step, reviewer, reason:
  "reviewer <name>: no <requires>"}}` is appended; the report's skip lines and the TUI feed show
  it. Another stat error fails the step `reviewer <name>: <err>`. No reviewer left: the half is
  `ok` with no round and no event beyond the skips.
- **Prompt vars** — two keys join `StepVars` as empty strings and are set per reviewer:
  `ArtifactsDir` = `<RunDir>/phase-<N>/<kind>-rv-<name>-r<round>-a<attempt>` (every reviewer) and
  `RequiredPath` = the absolute path the requirement resolved to (else empty).
- **`review-ui` prompt** — embedded, overridable as `.r-loop/prompts/review-ui.md`. It keeps
  `review.md`'s findings contract, earlier-rounds block and sentinel partial, and tells the agent
  to: read the `<!-- test-app-surface: web|tui|cli -->` marker in `RequiredPath`; write empty
  findings when nothing the app renders, prints or accepts changed; invoke the real `/test-app`
  (or follow `RequiredPath` when the Skill tool does not list it), which builds, deploys and tears
  down on its own; check the changed flows; for web capture the two most-changed pages at
  1280x800, 768x1024 and iPhone 14 (≤ 6) and judge them with `frontend-design`, for a terminal UI
  capture 160x50, the default size and 80x24 (≤ 6); save captures under `ArtifactsDir`; name the
  source files behind each defect in `files`; report a check that could not run as a finding.
- **Preflight** — renders each distinct reviewer prompt (`prompt review-ui: <source>`) and prints
  `reviewer <name> requires <path>: found | missing, reviewer skipped`, resolved against the
  primary tree.
- **Rounds** — unchanged: the `ui` reviewer runs every round beside the code reviewer, both
  findings files go to the one fix half, a real P1/P2 from either opens the next round, and real
  P3/P4 UI findings are reported only (`finding` events). `gatefix` inherits the implement row's
  reviewers, the `ui` reviewer with them.

## Milestone 9 — Free-text start

Spec ADR-75. A run can start from free text. A short intake session turns the text into an argv,
and the driver validates that argv before anything of a run exists.

- **Trigger** — `parseFlags(args) (Options, []string, error)` returns the positionals. `Main`
  starts the intake when there is no flag error and `freeForm(positional)` is true: more than one
  positional, or one that does not end in `.md`. Otherwise `ParseArgs` runs as before.
- **Row** — `config.Intake{Provider, Model, Effort}` comes from `intake.*` (default `claude sonnet
  medium`) and shares the role schema with `land.fix`. `applyOverrides` takes the row name
  `config.IntakeRow` (`intake`), and its provenance path is `intake.<key>`. The banner line is
  `intake: <provider> <model> <effort>  ← <sources>`. `validateProviders` adds
  `role{field: "intake.provider"}`, and `newIntake` runs the same `checkRole` before herdr is
  touched.
- **Port use** — `core.Intake{Host, Prompts, Provider, Name, Root, Pane, Vars, Poll}`.
  `Run(ctx, accepted <-chan []string) ([]string, error)` opens (splits `Pane` right, or opens a
  `◆ intake` workspace at `Root`), then `Start`s, renders `intake`, and `Prompt`s. It then returns
  on the first of: an accepted argv, `ctx.Done()`, or `State == AgentGone` (`ErrIntakeGone`). It
  always closes what it opened.
- **MCP** — `askmcp.Intake{Submit func([]string) (bool, string)}.Serve(ctx)` serves one loopback
  URL `/mcp/intake/<token>` whose token lives only in memory. It has one tool,
  `submit_args{argv}` → `{accepted, reason}`. For a provider that reads a file, the MCP config is
  `<tmp>/intake.mcp.json`, in a temp dir that is removed afterwards.
- **Validation** — in app, deterministic: `ParseArgs(argv)`, the plan path ends in `.md`,
  `plan.Reader.Read`, `core.RunList`, `config.Load(root, home, overrides)`. The accepted argv goes
  on a 1-buffered channel, and a second accept is refused. Exit codes: Ctrl-C (SIGINT) is 2, a gone
  session is 4, and a start or prompt failure is 4. herdr unreachable is 4 and a missing herdr is
  127, as in preflight.
- **Prompt vars** — `Text` (the raw args joined), `Dir` (the working directory; argv paths are
  relative to it), `Root`, `Usage` (the parser's `PrintDefaults`), `Unattended` (the typed flag).

## Issues files (ADR-69, ADR-70)

- **Source** — `plan.Reader.Read` reads a file with no `### Phase` heading as an issues file
  (`internal/plan/backlog.go`) and sets `Plan.Backlog`. Item = column-0 `- [ ]`/`* [ ]`/`- `/`* `/
  `1. ` line plus the lines indented under it, or a `##`/`###` heading followed by prose; text
  before the first item is header. Done = `[x]`/`[X]`, `~~…~~`, `<!-- fixed: … -->`, or under a
  `Done|Completed|Fixed|Shipped|Archive` heading. `Phase.ID` = the item's place among all items, `"1"`..`"N"`;
  `Title` = its text verbatim; `Items` = its indented list lines, or the title when it has none;
  no `DependsOn`, `Files`, `DoneWhen`, `Milestone`. `Topic` = file name without extension. No items →
  error (exit 2).
- **Tick** — rewrites the item line's `[ ]` to `[x]` (box-less items keep their text) and appends
  `  <!-- fixed: r-loop/phase-N -->`; an item already done → `ErrNothingToTick`.
- **Checks** — `files-outside-plan` and `foreign-test-edit` are quiet when `Phase.Files` is empty.
- **Item gate** — `SessionManager.ItemGates` (= `Plan.Backlog`) sets `EvidenceContext.NeedGate` for a
  phase with no `Done when:`; `plan-file` then requires `## Gate` holding exactly one code span
  (`core.PlanGate`). Prompt var `ItemGate` turns the section on in `plan.md` and `implement.md`.
- **Suite** — `core.Suite{Command(ctx, Phase) (string, error)}`, implemented by `core.GateProbe`:
  reuse the last `gate-discovered` event, else run the `gate` step (prompt `gate`, check `report`,
  in the primary tree, `ReportPath` = `<runDir>/gate.md` relative to the root), take the first code
  span, run it on the base (exit 0 required), `ResetHard HEAD`, append the event. Failure →
  `ErrNoGate`. Config row `steps.gate` (default `claude/sonnet/medium/30m/report`).
- **Land** — `LandGate.Suite` set only for a backlog. For a phase with no `Done when:`: suite first
  (before the merge); after the merge, read `## Gate` from the phase plan in the primary tree
  (`ErrNoGate` if absent); red check: changed test files (`isTestPath`) must exist, are copied onto
  `.r-loop/wt/phase-N-red` at `HEAD` (the base, mid-merge) and the item command must exit non-zero
  there (`ErrGate` otherwise); gate = `<item> && <suite>`. `ErrGate` → gate-fix rounds; `ErrNoGate`
  → the phase blocks.
- **Item skip** — with item gates on, `plan-file` also accepts `status: already-done` or
  `status: not-work` with a `## Evidence` section holding at least one `path:line`; the plan must
  still change only itself. After the plan step (fresh or resumed), `core.PlanSkip` reads that
  status and the loop emits `item-skipped {phase, status, reason}` and moves on: no implement, no
  land, no tick, exit 0 if nothing else blocked. The report lists it with the other skips.
- **Review** — prompt var `ItemGate` adds the phase's open criteria to `review.md`: one test named
  per criterion, a criterion without one is a finding; a skip plan's citations are opened instead.
- **Preflight** — a backlog named `*-notes.md` → exit 2; dirty tree limited to the backlog and its
  `-notes.md` → the exit-4 message ends `; commit <paths> first`; a backlog phase whose only item
  is its title → `warning: phase N has no acceptance criteria…` in the dry run and the banner.

## The run's LLM (ADR-71, ADR-72)

- **Split** — the driver makes every step-state change, tick, commit and run-list change; the
  watchdog is a full session for judgement. It is always on (no `--no-watchdog`), and its row is
  `watchdog.{provider, model, effort}`.
- **Order in `Execute`** — `Ask.Serve` → `startWatchdog` (exit 4 on failure) → `startTUI` →
  `unblock` → `Loop.Run`, which starts `ServeQuestions`. `Preflight` creates and binds the run first; `recordRunList` is written by
  `unblock`, after deferral. `recordedRunList` reads the last `run-list` event.
- **Sorting** — `plan.classify(head, owner)` sets `Entry.Kind`: `person` when the owner matches
  legal|finance|procurement|hr|people|compliance|security council or a person pattern matches,
  else `decision` on a decision pattern, else `unclassified` (treated as a person's). `Entry` also
  carries `Alternative` and `Outstanding`.
- **Walk request** — `core.UnblockText(plan, entries, runList, donePath)`:
  `resolve first: <n> open entries in <plan> block this run's phases <list>…`, then per entry
  `## R<n> — <name>`, `kind · owner · blocks · timebox · output`, the entry text and each blocked
  phase's block, and last the line telling the watchdog to write an empty file at `donePath`
  (`<runDir>/unblock.done`) when the walk is done. The driver removes a stale `unblock.done`, sends
  the request with `Dog.Notify(text, wait=false, …)` — a prompt returns as soon as the watchdog
  starts asking the maintainer, so its return never means the walk is over — then polls every 100 ms for the
  done file and `Store.Aborted`, up to `watchdog.unblockTimeout`. On timeout it records a `warning`
  (`… did not finish within <timeout>`) and goes on; open entries are deferred.
- **After the walk** (`app.Wiring.walk`) — a stop → exit 4 and the run `halted`. Any dirty path
  other than the plan → exit 4. A plan change outside `## Resolve first`
  (`plan.OnlyResolveFirstChanged`) → the file is restored and exit 4. Otherwise one commit
  `docs: resolve <n> plan blockers`, an `entry-resolved {entry, resolved}` event per newly ticked
  entry, and the new plan swapped into `Loop`, `Gate.Boundary`, `Watch` and `Probe`.
- **Deferral** — `core.DeferBlocked(plan, list)` drops, for each still-open blocking entry, the
  phases it blocks and their dependents (`core.Dependents`), records `entry-deferred {entry,
  phases}` and writes the new run list. An empty result finishes the run with exit 0.
  `--unattended` skips the walk and defers.
- **Report** — `## Blockers` lists `<entry> → <resolved>` and `<entry> still open: phase <list>
  skipped`.
- **Deferred** — the watchdog managing the loop through MCP or a CLI (a non-goal in the spec).
- **Only the watchdog asks, in its own session (ADR-73)** — a step agent asks with
  `ask_watchdog`; `QuestionRouter.Route` sends the watchdog `question <id> from phase-<N>/<kind>:
  <text> options: <…> recommended: <…>` with `Dog.Notify`, which waits while the watchdog is
  blocked, and returns (ADR-76). The watchdog answers with `answer_question`, citing a `path:line` or `maintainer` —
  after asking the maintainer in its own session with AskUserQuestion, or as plain text — which
  delivers `AnsweredBy: maintainer` and counts as a human touch. An empty or invalid citation is
  refused and the question stays open. When the watchdog is not live as a question arrives, or the
  notice cannot reach it, `Watch.Route` accepts a driver `halt` signal `the watchdog
  is gone` for the asking step; the question is never answered, and `r-loop resume` starts a new
  watchdog. With `--unattended` the watchdog prompt says never to ask the maintainer: answer from
  the repository, or take the agent's recommended option citing the `path:line` that supports it.
  `propose_remedy` and `restart_step` take `maintainer_said`, the maintainer's reply, quoted; an
  off-list class without it returns `ask` and records nothing, and a non-fallback provider is
  refused with the same instruction.

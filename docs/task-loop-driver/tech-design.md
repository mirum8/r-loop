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
land gate gets a bounded fix round (ADR-64); an answer can come from any shell (ADR-67); and
`--unattended` pre-authorises the routine remedies, bounds restarts, lets an unanswered question
resolve to the asking agent's own recommendation, and restarts a step on its row's fallback
provider (ADR-61, ADR-63, ADR-66). Every automatic decision opens the run report. Every session
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
- **Types** — `StepKey{Run string; Phase int; Kind string; Attempt int}` ·
  `Phase{Number int; Title string; Implements []string; DependsOn []int; Files []string; Risk
  string; Items []Item; DoneWhen string; Milestone int; Block string}` (`Block` is the raw
  heading-to-next-heading text) · `Item{Text string; Done bool}` · `Milestone{Number int; Name
  string; Phases []int}` · `Entry{Name, Body string; Ticked, HasBox bool; Owner, Blocks, Timebox,
  Output, Resolved string; BlocksAll bool; BlocksPhases []int; Malformed []string}` ·
  `Signal{Seq int; Kind SignalKind; Source SignalSource; Step StepKey; Reason, Evidence string; At
  time.Time; Rejected bool; RejectReason string}` · `Question{ID string; Step StepKey; Text
  string; Options []string; Recommended string; AskedAt time.Time; Answer, AnsweredBy, Citation string; AnsweredAt
  time.Time}` · `Remedy{ID string; Step StepKey; Class, Command, Why, Consent string; ProposedAt,
  DecidedAt time.Time}` · `Landing{Phase int; MergeSHA string; GateSkipped bool; GateOutput
  string; Added, Deleted int}` — the merge's diff size, measured by the land gate before the gate
  runs; a failed measurement aborts the merge · `Event{At time.Time; Kind string; Phase int; Step string; Fields
  map[string]string}` — the one stream both faces render · `RunMeta{Todo string; ResolvedConfig
  []byte; Started time.Time}` · `Record{Kind string; At time.Time; Step *StepKey; State
  StepState; Run RunStatus; Reason string; Question *Question; Signal *Signal; Remedy *Remedy; Landing
  *Landing; Event *Event}` · `RunState{ID, Todo string; Started time.Time; Status RunStatus; Steps map[StepKey]StepState;
  LastStep *StepKey; Landed []Landing; Questions []Question; Signals []Signal; Remedies []Remedy;
  Events []Event; Warnings []string; Spans map[StepKey]StepSpan}` · `StepSpan{Started, Ended
  time.Time}` — a step's first `running` record (the moment `Watch.StepStarted` fires) to its `ok`/`failed` record, replayed from the step log.
- **Ports** (interfaces in `internal/core`, each with a fake in `internal/core/fakes_test.go`):
  - `PlanSource`: `Read(path) (Plan, error)` · `Tick(path, phase int) error` · `Stamp(path,
    entryName, resolvedLine string) error`; `Plan{Path, Topic string; Phases []Phase; Milestones
    []Milestone; ResolveFirst []Entry}`.
  - `SessionHost`: `Reachable() error` · `Open(OpenSpec{CWD, Label string; Env
    map[string]string}) (Workspace{ID, RootPane string}, error)` · `Start(pane, name, kind
    string, args []string) (Agent{Name, Pane string}, error)` · `Prompt(agent, text string, wait
    bool, timeout time.Duration) error` · `State(agent) (AgentState, error)` with `AgentState ∈
    {idle, working, blocked, done, unknown, gone}` · `Read(agent string, lines int) (string,
    error)` · `Interrupt(agent) error` · `Close(workspaceID) error` · `Split(pane, direction,
    cwd string) (pane string, error)` (an empty `pane` splits the driver's own).
  - `Repo`: `Root() string` · `Clean() ([]string, error)` · `HeadBranch() (string, error)` ·
    `HeadSHA(dir) (string, error)` · `AddWorktree(dir, branch, base string) error` ·
    `RemoveWorktree(dir) error` · `Dirty(dir) ([]string, error)` · `CommitAll(dir, message)
    (string, error)` · `DiffNonEmpty(dir, ref) (bool, error)` · `ChangedFiles(dir, ref)
    ([]string, error)` · `DiffStat(dir, ref) (added, deleted int, err error)` — **all three
    compare the working tree, untracked files included, against `ref`** · `Snapshot(dir)
    (tree string, err error)` — a tree object of the working tree, untracked files included,
    written through a throwaway index so no branch, commit or real index changes ·
    `TreeDiff(from, to string) ([]string, error)` — the paths that differ between two trees ·
    `MergeNoFF(branch) error` (`--no-commit`; a conflict returns `ErrMergeConflict` after `git
    merge --abort`) · `AbortMerge() error` · `Commit(message) (string, error)` (`git add -A`
    then commit, in the primary tree) · `CommitTouches(sha) ([]string, error)` · `ResetHard(ref)
    error` · `Run(dir, command string, timeout) (exit int, output string, err error)` (via `sh
    -c`).
  - `Store`: `Create(RunMeta) (runID string, err error)` · `Append(runID, Record) error` (a
    transition is appended **before** the action it describes) · `Load(runID) (RunState,
    error)` (reads every file of the run) · `Current() (runID string, pid int, ok bool)` ·
    `SetCurrent(runID, pid) error` · `ClearCurrent() error` · `Aborted(runID) bool` ·
    `MarkAbort(runID) error` · `Dir(runID) string`.
  - `Prompts`: `Render(name string, vars map[string]any) (text, source string, err error)`.
  - `AskChannel`: `Serve(ctx) (baseURL string, err error)` · `StepURL(StepKey) string` ·
    `Questions() <-chan Question` · `Answer(id, answer, by, citation string) error`.
  - `Face`: `Emit(Event)` · `Ask(Question) (string, error)` · `Close()`.
  - `Notifier`: `Fire(hook string, env map[string]string)` (never returns an error to the loop; a
    non-zero exit is an `Event{Kind: "notify-failed"}`).
- **Run directory** (`Store` adapter) — `.r-loop/runs/<runID>/` with `runID =
  <yyyymmdd-HHMMSS>`; files: `config.resolved.yaml`, `events.jsonl` (step, run, landing and
  display events, including `snapshot` and `review-round` events), `questions.jsonl`,
  `signals.jsonl`, `remedies.jsonl`, `report.md`, and `phase-<N>/` holding sentinels, step logs,
  findings and verdicts, and `answers/`, where `r-loop answer` drops one file per answer. A question's answer is a second line for the same id; `Load` keeps the
  last. `.r-loop/runs/current` holds `<runID> <pid>`; `.r-loop/runs/<runID>/abort` is the abort
  marker. `.r-loop/runs/` and `.r-loop/wt/` are appended to `<git-common-dir>/info/exclude` when
  absent, never to `.gitignore`.
- **Sentinel** — JSON in `.r-loop/runs/<runID>/phase-<N>/`:
  `{"outcome":"ok"|"failed","reason":"<text>","at":"<RFC3339>"}`. The author half writes
  `<kind>-a<attempt>.sentinel`; a reviewer `<kind>-rv-<provider>-r<round>-a<attempt>.sentinel`;
  the author's verify-and-apply half `<kind>-fix-r<round>-a<attempt>.sentinel`. Unreadable or
  malformed → the step is `failed` with reason `sentinel unreadable`.
- **Config resolution** — every key resolves CLI override → `.r-loop/config.yaml` →
  `~/.config/r-loop/config.yaml` → the embedded defaults, with provenance `<file>:<key>`,
  `flag:--provider`, `flag:--model`, `flag:--effort` or `default`. Step row keys: `prompt, check,
  provider, model, effort, timeout`, `fallback`, plus the review half on any row: `reviewers`,
  `rounds`, `reviewTimeout`. **A reviewer entry and a `fallback` are each a scalar provider name
  (model and effort left to the provider, flag omitted, banner prints `provider default`) or a
  block with `provider`, `model`, `effort`.** `--provider`, `--model` and `--effort` each take
  `<step>=<value>` and override only that row's own key, never its fallback or reviewers; a key
  `land.fix` inherits from the implement row is the overridden value. `land.fix` is an optional block with `provider`, `model`, `effort`, all optional,
  for the `gatefix` step: absent, the implement row's three; a missing key is the implement
  row's unless the block names another provider, in which case it stays empty (that provider's
  default). The reader resolves `land.fix` at load, so every later reader sees three plain
  values. `watchdog.effort` sits beside `watchdog.provider` and `watchdog.model`. `rounds: 0`
  or an empty `reviewers` list means the step has no review half. Rows: `plan`, `implement`,
  `milestone`. Defaults: pipeline `[plan, implement]`; plan `claude/opus/high/1h/plan-file`,
  reviewers `[codex]`, `rounds 2`, `reviewTimeout 20m`; implement
  `codex/gpt-5.6-sol/medium/4h/diff`, reviewers `[claude]`, `rounds 3`, `reviewTimeout 45m`;
  fallback plan `codex`, implement `claude` (scalars: provider defaults for model and effort);
  milestone `claude/opus/medium/1h/report`, no review; `land.fixRounds 1`, `land.gateTimeout 30m`, no `land.fix`;
  `unattended.allow [deps, ports, locks, restart, retry, provider]` and
  `unattended.questionTimeout 30m`, both applied only with `--unattended`;
  `watchdog.maxRestarts 2`; `watchdog.provider claude`, `watchdog.model sonnet`,
  `watchdog.effort ""` (provider default), `watchdog.allow []`, `watchdog.remedyWindow 10m`,
  `watchdog.answerWindow 5m`, `watchdog.checkTimeout 10m`, `watchdog.stallGrace 2m`,
  `watchdog.overtimeFactor 2`, `watchdog.diffFactor 3`; `notify.onHalt/onWarn/onDone ""`. A
  flow-style YAML node is rejected naming the line; an unknown key is rejected naming the key and
  file; a negative `rounds` is rejected; each is exit `2`.

## Milestone 2 — Sessions and providers

- **Provider block** — `providers.<name>`: `kind` (required), `modelFlag` (`{model}`),
  `effortFlag` (`{effort}`, may be empty → banner `effort n/a`), `askFlag` (`{url}` or
  `{mcpConfig}`), `doneSignal ∈ {sentinel}`, `ask ∈ {mcp, none}`, `review` (may be empty).
  **Precedence is whole-block**: the project config's block, else
  `~/.config/r-loop/providers/<name>.yaml`, else the shipped block. `Args(p, model, effort, askURL,
  mcpConfigPath)` expands the templates and omits a flag whose template or value is empty.
  Shipped: `claude` (`--model {model}`, `--effort {effort}`, `--mcp-config {mcpConfig}`, `review: /code-review`) and
  `codex` (`-c model={model}`, `-c model_reasoning_effort={effort}`, `-c
  mcp_servers.r-loop.url={url}`, `review: /review`). `{mcpConfig}` is a per-agent file
  `{"mcpServers":{"r-loop":{"type":"http","url":"<url>"}}}`. The core sees a provider only as
  `ProviderArgs{Kind string; Args []string; Ask bool; Review string}`.
- **Step kind** — `StepKind{Name, Prompt, Check string; Row StepRow}`; `StepRow{Provider, Model, Effort string; Fallback Fallback; Timeout time.Duration; Reviewers []Reviewer; Rounds int; ReviewTimeout time.Duration}`; `Reviewer{Provider, Model, Effort string}`, `Fallback{Provider, Model, Effort string}` and `GateFix{Provider, Model, Effort string}` — one type per role, the same three fields; an empty `Model` or `Effort` is the provider's default. A row's `check ∈ {plan-file,
  diff, report}` or one added with `RegisterCheck`; `findings` and `verdict` are the review
  half's own checks and are never named on a row. Evidence predicates take
  `EvidenceContext{Repo; Worktree, StartSHA, StartTree, PlanPath string; FindingsFiles []string;
  VerdictPath, RoundTree, ReportPath string; FS fs.FS}`. **`StartTree` is `Snapshot(worktree)`
  taken at spawn, so every check measures the step's own change, never the phase base or an
  earlier step's leftovers.**
- **Shipped checks** — `plan-file`: the plan exists; one of its first five lines is `status:
  planned`; it carries the four headings `## Summary`, `## Changes`, `## Tests`, `## Assumptions`;
  `## Tests` holds at least one list item; and `TreeDiff(StartTree, Snapshot(worktree))` names no
  path but the plan. `diff`: `TreeDiff(StartTree, Snapshot(worktree))` is non-empty. `report`: the
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

  The driver reads `## Assumptions` after the plan step ends `ok` and emits each list item as an
  `Event{Kind: "assumption"}`; the run report lists them per phase.
- **Two-signal rule** — `ok` only when the sentinel says `ok` **and** the evidence predicate
  passes **and** `HeadSHA(worktree)` still equals `StartSHA` (an agent commit is
  `failed(step committed before review)`); sentinel `failed` → `failed(<reason>)`; ok with
  evidence missing → `failed(evidence missing: <what>)`; `blocked`/`idle` for
  `watchdog.stallGrace` (default 2 min), no sentinel, no open question → `stalled`, and the
  driver sends one fixed nudge (below); `working` again → `running`; still quiet a further
  `stallGrace` after the nudge → `failed(stalled: no response to nudge)`; elapsed > timeout →
  `failed(backstop)`. The driver never kills a session: a failed step's session is left
  standing. For a step with a review
  half the same rule applies per half and per round.
- **Nudge** — fixed text in `internal/core`, never model output: `r-loop: no sentinel and no
  activity for <grace>. If your work is done, write the sentinel now. If you are blocked, call
  ask_user, or write a failed sentinel with the reason.` Sent once per stall, to step agents and
  reviewers alike, and recorded as `Event{Kind: "nudge"}`.
- **Names and paths** — phase branch `r-loop/phase-<N>`; worktree `.r-loop/wt/phase-<N>/` from
  the primary tree's HEAD branch (`base`); slug `phase-<N>-<kebab title>` (≤ 60 chars); workspace
  label and author agent `rloop-p<N>-<kind>`; reviewer agent `rloop-p<N>-<kind>-rv-<provider>-r<round>`
  truncated to fit; milestone `rloop-p<N>-ms`; `rloop-watchdog` — all within
  `[a-z][a-z0-9_-]{0,31}`; a new attempt whose old agent name is still live gets `-a<attempt>`
  appended. Findings `phase-<N>/<kind>-findings-<provider>-r<round>.json`; verdict
  `phase-<N>/<kind>-verdict-r<round>.json`; milestone report
  `docs/<topic>/reports/milestone-<M>-<slug>.md`.
- **Spawn** — `StepRef{Key StepKey; Kind StepKind; Phase Phase; InPrimary bool; Worktree,
  Branch, Base, RunDir, AskURL string; Vars map[string]any}`. Unless `InPrimary`, ensure the
  worktree; record `StartSHA = HeadSHA(dir)` and `StartTree = Snapshot(dir)`; append `spawned`;
  `Open` with `Env{R_LOOP_SENTINEL, R_LOOP_RUN, R_LOOP_PHASE, R_LOOP_STEP}`; `Start` in the root
  pane; render; `Prompt` without wait; append `running`. `InPrimary` spawns in `Repo.Root()` with
  no worktree and no review half (the milestone report). `Wait(ctx, s, Observer)` reports
  `Started`, `Stalled` and `Resumed` through the observer and returns only on a sentinel, a gone
  agent or the backstop; a stall never ends the wait. `Stop` is `Interrupt`; the driver never
  closes a workspace.
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
  `milestone`, `watchdog`, `gatefix`; `gatefix` adds `GateCommand` and `GateOutput`.

## Milestone 3 — The serial loop, landing and the plain face

- **Runners** — `StepRunner.Run(ctx, StepRef, Observer) Outcome`, run in a goroutine; the loop
  holds the live session from `Observer.Started`. `core.DefaultRunners(sm, kinds)` is keyed by a
  step kind's `Check`; any check without an entry uses the single-session runner: `Spawn`,
  `Wait`, then the review half when `Row.Reviewers` is non-empty and `Row.Rounds > 0`, then the
  one commit. Until Milestone 4 lands the review half is a no-op, so wiring never changes when it
  does. `core.StepVars` fills every template variable, empty where unused.
- **Run list** — unticked phases in numeric order, narrowed by `--from N` or `--phases n,n`; a
  listed phase ticked or absent is exit `2`. Phase state advances `planned → implemented →
  landed`.
- **Outcomes** — `failed` → wait `watchdog.remedyWindow` for a `Restart` (skipped with no
  watchdog; at most `watchdog.maxRestarts` restarts per step), then the phase is **blocked**:
  `Event{Kind: "phase-blocked", Fields{phase, reason}}` and `notify.onWarn` with
  `R_LOOP_STATUS=blocked`; every phase that depends on it, directly or through others, is
  `blocked` with `Event{Kind: "phase-skipped", Fields{phase, because}}`; the loop continues with
  the next phase that can run. A watchdog `halt` → `Interrupt`, `failed(watchdog: <reason>)`, and
  the phase is blocked the same way. When nothing is left to run: all landed → `finished`,
  `notify.onDone`, exit `0`; any phase blocked → `halted`, `notify.onHalt`, `r-loop resume`
  printed, and the exit code of the **first** block — `1` failed, `3` a failure that began as a
  stall, `5` a watchdog halt. Abort marker → exit `1` at once; preflight refusal `4`; usage, git
  state, config `2`; missing binary `127`.
- **Resume** — skips landed phases and `ok` steps, and re-runs the stopped step of **every**
  phase that halted, in phase order, as a new attempt on its own worktree; a phase blocked only
  because of a dependency simply runs. The worktree is **claimed** when it is clean or when
  `Snapshot(worktree)` equals the last `snapshot` event recorded for that step; anything else is
  an unclaimed tree, exit `2`. A step whose author half had passed resumes at its recorded round
  with a fresh author session. `--replan` re-runs the phase's `plan` step as a new attempt, with
  the failed step's reason as its addendum, before the step that failed.
- **Gate fix** — a red gate with `land.fixRounds` left runs one `gatefix` step in the phase
  worktree on the config's resolved `land.fix` provider, model and effort (the implement row's
  unless `land.fix` names its own), with `GateCommand` and `GateOutput`, `check: diff`, the
  implement row's timeout and its reviewers for one round; the gate command itself runs under
  `land.gateTimeout` (default 30m); its `ok` commits `r-loop: phase <N> gatefix`, and the
  landing starts again from the merge. A red gate with no fix round left blocks the phase.
- **Land** — in the primary tree: `MergeNoFF(r-loop/phase-<N>)` (`--no-commit`) → the merged tree
  is on disk, uncommitted → `Run(root, DoneWhen, gate timeout)`; exit ≠ 0 → `AbortMerge()`, halt
  with the output, nothing ticked, a gate-fix round when one is left; no `Done when:` → `gate-skipped` recorded, never a halt → `Tick`
  → `Commit("phase <N>: <title>")` → `CommitTouches` must include the todo and one other path, else
  `ResetHard("HEAD~1")` and halt. A conflict aborts before the gate. The gate therefore proves the
  phase's code, and no merge commit exists unless it passed.
- **Milestone boundary** — `LandGate` calls its `Boundary` after a landing; when phase N closed
  `## Milestone M`, one `InPrimary` session on the `milestone` row writes the report; `ok` →
  commit `docs(report): milestone <M>`; anything else → `report-skipped`, never a halt.
- **Report** — `report.md` opens with `human touches: <n>` (every answer a person gave, every
  consent, every resume) and an **Automatic decisions** section: restarts with their remedy,
  question timeouts with the answer the agent took, fallback restarts with the provider, model
  and effort used, gate-fix rounds,
  round-limit warnings, nudges, and blocked and skipped phases.
- **Banner** — one line per pipeline row and the milestone row, `<step> <provider> <model>
  <effort> <timeout> <check> ← <provenance>`; under a row with a review half, `review rounds <n>
  <reviewTimeout>` and one `reviewer <provider> <model|provider default> <effort|provider
  default>` line per reviewer and `fallback <provider> <model|provider default> <effort|provider
  default>` per row that has one; `gatefix <provider> <model|provider default> <effort|provider
  default>`; watchdog on/off with `<provider> <model> <effort|provider default>`. Every step,
  reviewer, fallback, gatefix and watchdog line ends `← <provenance>`: one source when its
  provider, model and effort share it, else `provider <src> model <src> effort <src>`. Overrides
  with the value replaced; prompt source per step; `mode: unattended` with the added allow-list
  and the question timeout, or `mode: attended`.
- **Plain lines / status / report / notify env** — as written in Phases 15–16: `HH:MM:SS phase
  <N> <kind> <state> <provider> <detail>` (during a review, `<detail>` is `review r<round>/<rounds>
  <half>`); `r-loop status --plain` lines; `report.md` rewritten on every transition; hook env
  `R_LOOP_RUN, R_LOOP_STATUS, R_LOOP_PHASE, R_LOOP_STEP, R_LOOP_REASON, R_LOOP_TODO,
  R_LOOP_REPORT`, `sh -c`, 60 s.

## Milestone 4 — The review half

- **Shape** — a review half runs after a step's author half ends `ok` and before the step's
  commit, inside the step's own workspace: the author stays in the root pane, and each reviewer
  gets a pane split to its right (`Split(rootPane, "right", worktree)`, stacked when there are several).
  The panes are made in round 1 and reused; **each round starts a fresh reviewer agent** in its
  pane, after interrupting the previous round's. The author is the same agent session through
  every round.
- **A round** — (1) `RoundTree = Snapshot(worktree)`, appended as `Event{Kind: "review-round",
  Fields{step, round, tree}}` before any reviewer starts; (2) resolve every reviewer (one with no
  `review` command fails the step before any pane opens); start every reviewer agent, then prompt
  each with the `review` template; (3) join on every reviewer sentinel; any `failed` or `stalled`
  reviewer fails the step naming it; every findings file must pass the `findings` check; then
  `TreeDiff(RoundTree, Snapshot(worktree))` must be empty, else `failed(reviewer modified the
  tree: <paths>)`; (4) no findings in any file → the review half ends clean; (5) prompt the author
  with the `fix` template; join on its sentinel; run the `verdict` check against `RoundTree`, then
  the step's own evidence check again; (6) no verdict `real` at P1 or P2 → the review half ends
  clean; otherwise the next round.
- **Clocks** — while reviewers run, the author's stall clock and backstop are suspended; each
  round's reviewers and the author's fix half share `Row.ReviewTimeout`. An open question
  suspends clocks as everywhere else.
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
  `<base>/<phase>/<kind>-rv-<provider>/<attempt>` — the path identifies the asking agent. Tool
  `ask_user(question, options?, recommended?) → {answer}` blocks until answered; ids `q<seq>`.
- **waiting-input** — a question moves the step `running → waiting-input`, freezes its backstop,
  and is recorded; the answer returns it to `running`. A backstop firing in `waiting-input` halts
  with `invariant: a question never expires`. `ask: none` omits the flag and records `ask-none`.
- **Routing** — the loop freezes the step first, then offers the question to `Watcher.Route`
  (the watchdog, Milestone 7); when that returns false, `Face.Ask`. Plain face:
  stdin when it is a terminal, else the question stays open. Whatever the face, an answer may
  also arrive as `<RunDir>/answers/<id>`, written by `r-loop answer <id> <text>` from any shell;
  the loop reads that directory each tick, and the first answer wins.
- **Unattended timeout** — only with `--unattended`: a question still open
  `unattended.questionTimeout` after it reached the face is answered by the driver with `No
  answer within <t>. Proceed with your recommendation: <recommended>` — or, with none given, `…
  Proceed with the option you judge safest and name it in your sentinel's reason` — recorded
  `AnsweredBy: timeout` and listed under Automatic decisions.

## Milestone 6 — The TUI

- **Model** — one Bubble Tea model fed by `Event`s; header, phase rail (24 columns), live-step
  panel, warnings, questions. Instrument tokens from `DESIGN.md`: surface `#0F1115`, raised
  `#171A20`, text `#D6DAE0`, dim `#8A929E`, primary `#6E9FC4`, secondary `#E0A458`, tertiary
  `#8FA87F`, error `#E0736A`, outline `#2E343D`. Both faces consume the same event log. During a
  review the live step reads `<kind> · review r<round>/<rounds>`.
- **Input** — `Face.Ask` in the questions region; a `yes`/`no` question is a consent line.

## Milestone 7 — The watchdog

- **Second MCP surface** — `<base>/watchdog/<wdToken>`; tools `signal(kind, step, reason,
  evidence) → {accepted, reason?}` · `propose_remedy(class, command, why) → {decision}` ·
  `restart_step(step, addendum?, provider?) → {accepted, reason?}` · `answer_question(id, answer,
  citation) → {accepted, reason?}`; `step` is `phase-<N>/<kind>`, resolved to the latest attempt.
  None of these is reachable from a step path.
- **Acceptance** — `Watch.Accept`: `warn` or `halt` naming the live step, or one that ended within
  the last poll tick, is accepted; anything else is recorded `Rejected` with its reason. A
  rejected watchdog `signal` halts the run (exit `5`). **During a phase check** the only
  accepted step is `phase-<N>/check`: `warn` is accepted, `halt` is rejected with `phase check
  may only warn` and does **not** halt the run.
- **Routing** — `warn` → event + `notify.onWarn`; `halt` → `Interrupt`, exit `5`. Driver checks
  emit the same `Signal` with `Source: driver`, all `warn`.
- **Phase check into the plan** — the accepted warnings of `phase-<N>/check` become the plan
  prompt's `PhaseWarnings`, so the planner addresses each one.
- **Remedies** — classes `deps · ports · containers · locks · restart · retry · provider`;
  allow-listed → authorised at once, else `Face.Ask` yes/no; the record is the command as
  proposed with `Consent ∈ {allow-list, maintainer, refused}`; the driver never runs it.
  `restart_step` is accepted only for a `failed` or `stalled` step inside its remedy window, after
  an authorised remedy for it (or an allow-listed restart class), and while the step has had fewer
  than `watchdog.maxRestarts` restarts; it queues a new attempt. With `--unattended`,
  `unattended.allow` is added to `watchdog.allow`, and an allow-listed `provider` restart may name
  only the row's `fallback` — any other provider still asks. A restart naming the fallback runs
  the new attempt on the fallback's provider, model and effort; one naming any other provider
  runs on that provider's defaults; neither carries the row's own model or effort.
- **Citations** — `path:line`, the path relative to the repository root, existing in the
  **primary tree** and not under `.r-loop/`. The primary tree holds the spec, the tech design, the
  todo, the committed phase plans and every landed phase, and never the current phase's
  uncommitted worktree.

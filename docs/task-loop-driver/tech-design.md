# r-loop — Tech design contracts

Read beside `todo.md`. Nothing here reaches an implementer: the leaf items repeat whatever they
need, because `/r:task-run` sees one leaf block and nothing else. Every contract below is shared by
at least two leaves of its milestone; what one leaf alone needs is in that leaf's items.

Stack, fixed by the spec and never re-decided here: Go 1.25.14 · module `r-loop` (no host in the
path) · Bubble Tea v1.3.10 · Lip Gloss v1.1.0 · MCP go-sdk v1.8.0
(`github.com/modelcontextprotocol/go-sdk`) · yaml.v3 v3.0.1 · herdr 0.9.0 · git 2.50.1. Nothing
from the skill-pack is read at run time; `todo.md` and `.task-plans/` are the two shared paths.

Four choices the spec left open were put to the maintainer on 2026-09-18 and the plan is built on
the answers: **the driver commits a step's leftover files itself** after the sentinel (the agent
may commit, never must); **a failed step waits a bounded `watchdog.remedyWindow` for a remedy
before the run halts**; **a `--plain` run reads an escalated answer from stdin when stdin is a
terminal**, and otherwise leaves the question open; **a reviewer entry is either a provider name
or a block with `provider`, `model` and `effort`**.

## Milestone 1 — Core, plan file, config and state

- **Layout** — `cmd/r-loop` (main) · `internal/core` (loop, state machines, step kinds, evidence
  checks, runners, session manager, land gate, watch, remedies; **imports only the standard
  library and itself**) · adapters, one package each: `internal/plan` (PlanSource) ·
  `internal/config` (LoopConfig) · `internal/store` (Store) · `internal/providers`
  (ProviderRegistry) · `internal/prompts` (Prompts) · `internal/herdr` (SessionHost) ·
  `internal/gitrepo` (Repo) · `internal/askmcp` (AskChannel and the watchdog surface) ·
  `internal/face/plain` and `internal/face/tui` (Face) · `internal/notify` (Notifier) ·
  `internal/app` (wiring, preflight, status, resume). A test in `internal/core` runs `go list
  -deps ./internal/core/...` and fails on any `r-loop/internal/` package other than
  `internal/core`.
- **Enums** — `StepState`: `queued · spawned · running · ok · failed · stalled · waiting-input`;
  `PhaseState`: `unticked · planned · implemented · reviewed · landed`; `RunStatus`: `created ·
  running · halted · finished`. Legal step transitions: queued→spawned→running; running→ok |
  failed | stalled | waiting-input; waiting-input→running; stalled→running | failed. `ok` and
  `failed` are terminal. **A re-run — by resume or by a watchdog restart — is a new attempt**:
  `StepKey.Attempt` increments and the new key starts at `queued`; the old attempt's record is
  never reopened. Any other transition returns `ErrIllegalTransition`.
- **Types** — `StepKey{Run string; Phase int; Kind string; Attempt int}` ·
  `Phase{Number int; Title string; Implements []string; DependsOn []int; Files []string; Risk
  string; Items []Item; DoneWhen string; Milestone int; Block string}` (`Block` is the raw
  heading-to-next-heading text) · `Item{Text string; Done bool}` · `Milestone{Number int; Name
  string; Phases []int}` · `Entry{Name, Body string; Ticked, HasBox bool; Owner, Blocks, Timebox,
  Output, Resolved string; BlocksAll bool; BlocksPhases []int; Malformed []string}` ·
  `Signal{Seq int; Kind SignalKind; Source SignalSource; Step StepKey; Reason, Evidence string; At
  time.Time; Rejected bool; RejectReason string}` · `Question{ID string; Step StepKey; Text
  string; Options []string; AskedAt time.Time; Answer, AnsweredBy, Citation string; AnsweredAt
  time.Time}` · `Remedy{ID string; Step StepKey; Class, Command, Why, Consent string; ProposedAt,
  DecidedAt time.Time}` · `Landing{Phase int; MergeSHA string; GateSkipped bool; GateOutput
  string}` · `Event{At time.Time; Kind string; Phase int; Step string; Fields
  map[string]string}` — the one stream both faces render · `RunMeta{Todo string; ResolvedConfig
  []byte; Started time.Time}` · `Record{Kind string; At time.Time; Step *StepKey; State
  StepState; Run RunStatus; Reason string; Question *Question; Signal *Signal; Remedy *Remedy; Landing
  *Landing; Event *Event}` · `RunState{ID, Todo string; Started time.Time; Status RunStatus; Steps map[StepKey]StepState;
  LastStep *StepKey; Landed []Landing; Questions []Question; Signals []Signal; Remedies []Remedy;
  Events []Event; Warnings []string}`.
- **Ports** (interfaces in `internal/core`, each with a fake in `internal/core/fakes_test.go`):
  - `PlanSource`: `Read(path) (Plan, error)` · `Tick(path, phase int) error` · `Stamp(path,
    entryName, resolvedLine string) error`; `Plan{Path, Topic string; Phases []Phase; Milestones
    []Milestone; ResolveFirst []Entry}`.
  - `SessionHost`: `Reachable() error` · `Open(OpenSpec{CWD, Label string; Env
    map[string]string}) (Workspace{ID, RootPane string}, error)` · `Start(pane, name, kind
    string, args []string) (Agent{Name, Pane string}, error)` · `Prompt(agent, text string, wait
    bool, timeout time.Duration) error` · `State(agent) (AgentState, error)` with `AgentState ∈
    {idle, working, blocked, done, unknown, gone}` · `Read(agent string, lines int) (string,
    error)` · `Interrupt(agent) error` · `Close(workspaceID) error` · `Split(direction, cwd
    string) (pane string, error)`.
  - `Repo`: `Root() string` · `Clean() ([]string, error)` · `HeadBranch() (string, error)` ·
    `HeadSHA(dir) (string, error)` · `AddWorktree(dir, branch, base string) error` ·
    `RemoveWorktree(dir) error` · `Dirty(dir) ([]string, error)` · `CommitAll(dir, message)
    (string, error)` · `DiffNonEmpty(dir, ref) (bool, error)` · `ChangedFiles(dir, ref)
    ([]string, error)` · `DiffStat(dir, ref) (added, deleted int, err error)` — **all three
    compare the working tree, untracked files included, against `ref`**, so they see
    uncommitted work during a step and committed work after it · `MergeNoFF(branch) error`
    (`--no-commit`; a conflict returns `ErrMergeConflict` after `git merge --abort`) ·
    `AbortMerge() error` · `Commit(message) (string, error)` (`git add -A` then commit, in the primary tree) · `CommitTouches(sha) ([]string,
    error)` · `ResetHard(ref) error` · `Run(dir, command string, timeout) (exit int, output
    string, err error)` (via `sh -c`).
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
  display events), `questions.jsonl`, `signals.jsonl`, `remedies.jsonl`, `report.md`, and
  `phase-<N>/` holding sentinels, step logs, findings and the verdict. A question's answer is a
  second line for the same id; `Load` keeps the last. `.r-loop/runs/current` holds `<runID>
  <pid>`; `.r-loop/runs/<runID>/abort` is the abort marker. `.r-loop/runs/` and `.r-loop/wt/` are
  appended to `<git-common-dir>/info/exclude` when absent, never to `.gitignore`.
- **Sentinel** — JSON at `.r-loop/runs/<runID>/phase-<N>/<kind>-a<attempt>.sentinel` (a
  reviewer's is `review-<provider>-a<attempt>.sentinel`):
  `{"outcome":"ok"|"failed","reason":"<text>","at":"<RFC3339>"}`. Unreadable or malformed → the
  step is `failed` with reason `sentinel unreadable`.
- **Config resolution** — every key resolves CLI override → `.r-loop/config.yaml` →
  `~/.config/r-loop/config.yaml` → the embedded defaults, with provenance `<file>:<key>`,
  `flag:--provider` or `default`. Step row keys: `prompt, check, provider, model, effort,
  timeout`, plus `reviewers` on `review`. **A reviewer entry is a scalar provider name (model and
  effort left to the provider, flag omitted, banner prints `provider default`) or a block with
  `provider`, `model`, `effort`.** Rows: `plan`, `implement`, `review`, `milestone`. Defaults:
  pipeline `[plan, implement, review]`; plan `claude/opus/high/1h/plan-file`; implement
  `codex/gpt-5.6-sol/medium/4h/diff`; review `reviewers [claude, codex]/2h/verdict`; milestone
  `claude/opus/medium/1h/report`; `watchdog.provider claude`, `watchdog.model sonnet`,
  `watchdog.allow []`, `watchdog.remedyWindow 10m`, `watchdog.answerWindow 5m`,
  `watchdog.checkTimeout 10m`, `watchdog.stallGrace 2m`, `watchdog.overtimeFactor 2`, `watchdog.diffFactor 3`; `notify.onHalt/onWarn/onDone ""`. A flow-style YAML node is
  rejected naming the line; an unknown key is rejected naming the key and file; either is exit
  `2`.

## Milestone 2 — Sessions and providers

- **Provider block** — `providers.<name>`: `kind` (required), `modelFlag` (`{model}`),
  `effortFlag` (`{effort}`, may be empty → banner `effort n/a`), `askFlag` (`{url}` or
  `{mcpConfig}`), `doneSignal ∈ {sentinel}`, `ask ∈ {mcp, none}`, `review` (may be empty).
  **Precedence is whole-block**: the project config's block, else
  `~/.config/r-loop/providers/<name>.yaml`, else the shipped block. `Args(p, model, effort, askURL,
  mcpConfigPath)` expands the templates and omits a flag whose template or value is empty.
  Shipped: `claude` (`--model {model}`, `--effort {effort}`, `--mcp-config {mcpConfig}`, `review: /code-review`) and
  `codex` (`-c model={model}`, `-c model_reasoning_effort={effort}`, `-c
  mcp_servers.r-loop.url={url}`, `review: /review`). `{mcpConfig}` is a per-step file
  `{"mcpServers":{"r-loop":{"type":"http","url":"<url>"}}}`. The core sees a provider only as
  `ProviderArgs{Kind string; Args []string; Ask bool; Review string}`.
- **Step kind** — `StepKind{Name, Prompt, Check string; Row StepRow}`; `StepRow{Provider, Model,
  Effort string; Timeout time.Duration; Reviewers []Reviewer}`; `Reviewer{Provider, Model, Effort
  string}`. `check ∈ {plan-file, diff, findings, verdict, report}`. Evidence predicates take
  `EvidenceContext{Repo; Worktree, Base, StartSHA, PlanPath string; FindingsFiles []string;
  VerdictPath, ReportPath string; FS fs.FS}`: `plan-file` → the plan exists with `status:` in its
  first five lines; `diff` → `DiffNonEmpty(worktree, StartSHA)` — **the step's own start, never
  the phase base**, so a planning commit cannot satisfy implement; `findings` → every findings
  file is valid findings JSON (Milestone 4); `verdict` → Milestone 4; `report` → non-empty report.
- **Two-signal rule** — `ok` only when the sentinel says `ok` **and** the evidence predicate
  passes; sentinel `failed` → `failed(<reason>)`; ok with evidence missing → `failed(evidence
  missing: <what>)`; `blocked`/`idle` for `watchdog.stallGrace` (default 2 min), no sentinel, no open question → `stalled`;
  elapsed > timeout → `failed(backstop)`. The driver never kills a stalled step.
- **Names and paths** — phase branch `r-loop/phase-<N>`; worktree `.r-loop/wt/phase-<N>/` from
  the primary tree's HEAD branch (`base`); slug `phase-<N>-<kebab title>` (≤ 60 chars); agent
  name `rloop-p<N>-<kind>` (`rloop-p<N>-rv-<provider>` for a reviewer, `rloop-p<N>-ms` for a
  milestone report, `rloop-watchdog`), all within `[a-z][a-z0-9_-]{0,31}`; a new attempt of a
  step whose old agent name is still live gets `-a<attempt>` appended. Findings
  `phase-<N>/findings-<provider>.json`; verdict `phase-<N>/verdict.json`; milestone report
  `docs/<topic>/reports/milestone-<M>-<slug>.md`.
- **Spawn** — `StepRef{Key StepKey; Kind StepKind; Phase Phase; InPrimary bool; Worktree,
  Branch, Base, RunDir, AskURL string; Vars map[string]any}`. Unless `InPrimary`, ensure the
  worktree; record `StartSHA = HeadSHA(dir)`; append `spawned`; `Open` with `Env{R_LOOP_SENTINEL,
  R_LOOP_RUN, R_LOOP_PHASE, R_LOOP_STEP}`; `Start`; render; `Prompt` without wait; append
  `running`. `InPrimary` spawns in `Repo.Root()` with no worktree (the milestone report). After an
  `ok` sentinel: `CommitAll(dir, "r-loop: phase <N> <kind>")` (not for `InPrimary`), then evidence.
  `Wait(ctx, s, Observer)` reports `Started`, `Stalled` and `Resumed` through the observer and
  returns only on a sentinel, a gone agent or the backstop; a stall never ends the wait. `Stop` is
  `Interrupt`; the driver never closes a workspace.
- **Prompt variables** — `PhaseNumber, PhaseTitle, PhaseBlock, Criteria, TodoPath, SpecDir,
  PlanPath, Branch, Base, Worktree, Sentinel, RunDir, AskURL, ReviewCommand, FindingsPath,
  FindingsFiles, VerdictPath, ReportPath, MilestoneName, MilestonePhases, Addendum`; override
  `.r-loop/prompts/<name>.md`, else embedded.

## Milestone 3 — The serial loop, landing and the plain face

- **Runners** — `StepRunner.Run(ctx, StepRef, Observer) Outcome`, run in a goroutine; the loop
  holds the live session from `Observer.Started`. `core.DefaultRunners(sm, kinds)` is keyed by a step
  kind's `Check`; `core.StepVars` fills every template variable, empty where unused; any check without an entry uses the single-session runner. The review runner is
  added to this map for `verdict` inside `internal/core`, so wiring never changes when it lands.
- **Run list** — unticked phases in numeric order, narrowed by `--from N` or `--phases n,n`; a
  listed phase ticked or absent is exit `2`. Phase state advances `planned → implemented →
  reviewed → landed`.
- **Outcomes** — `failed` → wait `watchdog.remedyWindow` for a `Restart` (skipped with no
  watchdog), then `halted`, `notify.onHalt`, `r-loop resume` printed, exit `1`; `stalled` → pause,
  name the workspace, exit `3` at backstop; watchdog `halt` → `Interrupt`, `failed(watchdog:
  <reason>)`, exit `5`; all landed → `notify.onDone`, exit `0`; abort marker → exit `1`;
  preflight refusal `4`; usage, git state, config `2`; missing binary `127`.
- **Land** — in the primary tree: `MergeNoFF(r-loop/phase-<N>)` (`--no-commit`) → the merged tree
  is on disk, uncommitted → `Run(root, DoneWhen, gate timeout)`; exit ≠ 0 → `AbortMerge()`, halt
  with the output, nothing ticked; no `Done when:` → `gate-skipped` recorded, never a halt → `Tick`
  → `Commit("phase <N>: <title>")` → `CommitTouches` must include the todo and one other path, else
  `ResetHard("HEAD~1")` and halt. A conflict aborts before the gate. The gate therefore proves the
  phase's code, and no merge commit exists unless it passed.
- **Milestone boundary** — `LandGate` calls its `Boundary` after a landing; when phase N closed
  `## Milestone M`, one `InPrimary` session on the `milestone` row writes the report; `ok` →
  commit `docs(report): milestone <M>`; anything else → `report-skipped`, never a halt.
- **Banner** — one line per pipeline row and the milestone row, `<step> <provider> <model>
  <effort> <timeout> <check> ← <provenance>`; one line per reviewer; overrides with the value
  replaced; prompt source per step; `fix half: implement row`; watchdog on/off.
- **Plain lines / status / report / notify env** — as written in Phases 15–16: `HH:MM:SS phase
  <N> <kind> <state> <provider> <detail>`; `r-loop status --plain` lines; `report.md` rewritten on
  every transition; hook env `R_LOOP_RUN, R_LOOP_STATUS, R_LOOP_PHASE, R_LOOP_STEP,
  R_LOOP_REASON, R_LOOP_TODO, R_LOOP_REPORT`, `sh -c`, 60 s.

## Milestone 4 — Review

- **Find half** — resolve every reviewer first; one with no `review` command fails the step
  before any spawn. Open every workspace, then start every agent, then prompt each; `CWD` = the
  phase worktree; reviewers write only outside it. Join on every sentinel; any `failed` or
  `stalled` reviewer fails the step naming it.
- **Findings file** — fixed JSON, written by the reviewer from its native output:
  `{"reviewer":"<provider>","findings":[{"id":"<provider>-<n>","title":"…","detail":"…","files":["…"]}]}`.
  This departs from ADR-54's "each file read as written", by the maintainer's choice on 2026-09-18,
  so the verdict check can match ids rather than trust a count.
- **Fix half** — one session on the **implement** row, sentinel `review-a<attempt>.sentinel`,
  `StartSHA` taken at its spawn. `verdict.json`: `{"findings":[{"id":"<provider>-<n>",
  "reviewer":"<provider>","title":"…","verdict":"real"|"not-real"|"out-of-scope",
  "severity":"P1"|"P2"|"P3"|"P4","fixed":true|false,"files":["…"]}]}`.
- **`verdict` check** — verdict ids equal finding ids across every findings file; every entry has
  verdict and severity; every `fixed: true` entry is `real` at P1/P2 and each of its `files` is in
  `ChangedFiles(worktree, fix StartSHA)`; every `real` P1/P2 is `fixed: true`.

## Milestone 5 — The ask channel

- **Server** — MCP go-sdk streamable HTTP on `127.0.0.1:<free port>`, base `/mcp/<runToken>`
  (32 hex chars, stored mode 0600). A step URL is `<base>/<phase>/<kind>/<attempt>`, a reviewer's
  `<base>/<phase>/review-<provider>/<attempt>` — the path identifies the asking step. Tool
  `ask_user(question, options?) → {answer}` blocks until answered; ids `q<seq>`.
- **waiting-input** — a question moves the step `running → waiting-input`, freezes its backstop,
  and is recorded; the answer returns it to `running`. A backstop firing in `waiting-input` halts
  with `invariant: a question never expires`. `ask: none` omits the flag and records `ask-none`.
- **Routing** — the loop freezes the step first, then offers the question to `Watcher.Route`
  (the watchdog, Milestone 7); when that returns false, `Face.Ask`. Plain face:
  stdin when it is a terminal, else the question stays open.

## Milestone 6 — The TUI

- **Model** — one Bubble Tea model fed by `Event`s; header, phase rail (24 columns), live-step
  panel, warnings, questions. Instrument tokens from `DESIGN.md`: surface `#0F1115`, raised
  `#171A20`, text `#D6DAE0`, dim `#8A929E`, primary `#6E9FC4`, secondary `#E0A458`, tertiary
  `#8FA87F`, error `#E0736A`, outline `#2E343D`. Both faces consume the same event log.
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
- **Remedies** — classes `deps · ports · containers · locks · restart · retry · provider`;
  allow-listed → authorised at once, else `Face.Ask` yes/no; the record is the command as
  proposed with `Consent ∈ {allow-list, maintainer, refused}`; the driver never runs it.
  `restart_step` is accepted only for a `failed` or `stalled` step inside its remedy window and
  after an authorised remedy for it (or an allow-listed restart class); it queues a new attempt.
- **Citations** — `path:line`, the path relative to the repository root, existing in the
  **primary tree** and not under `.r-loop/`. The primary tree holds the spec, the tech design, the
  todo, the committed phase plans and every landed phase, and never the current phase's
  uncommitted worktree.

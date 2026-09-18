# r-loop — Implementation Plan

Spec: `spec.html` · Sources: `spec.html`, `interview-notes.md` · Status: draft
Milestones 1–7 deliver v1: 19 of 21 stories. "Fan out reviewers using my own mechanism" is
deferred by the spec's v1 line and has no leaf. "Add a provider the driver has never seen" ships
its v1 half: the block is read and validated. Each leaf is scoped to roughly one Claude Code
session. Contracts live in `tech-design.md` beside this file. The repository holds no code yet, so
every `Files:` path is the first of its kind, and `Done when:` commands assume `go` 1.25 on `PATH`.
The first end-to-end run, `plan → implement → review → land` in `--plain`, is possible once
Phases 17 and 19 have both landed.

## Waves
<!-- generated from the Depends on edges — regenerate, never hand-edit -->
- Wave 0: Phase 1
- Wave 1: Phase 2, Phase 4, Phase 5, Phase 7, Phase 8, Phase 9, Phase 10
- Wave 2: Phase 3, Phase 6
- Wave 3: Phase 11
- Wave 4: Phase 12, Phase 20
- Wave 5: Phase 13, Phase 14, Phase 18
- Wave 6: Phase 15, Phase 19, Phase 24
- Wave 7: Phase 16, Phase 25, Phase 26
- Wave 8: Phase 17
- Wave 9: Phase 21
- Wave 10: Phase 22
- Wave 11: Phase 23, Phase 27
- Wave 12: Phase 28
- Wave 13: Phase 29
- Wave 14: Phase 30

## Milestone 1 — Core, plan file, config and state
Contracts: `tech-design.md#milestone-1-core-plan-file-config-and-state`

### Phase 1 — Module skeleton, core types, ports and the boundary test
**Implements:** Run every remaining phase of a plan
**Depends on:** —
**Files:** `go.mod` (new) · `cmd/r-loop/main.go` (new) · `internal/core/types.go` (new) · `internal/core/states.go` (new) · `internal/core/ports.go` (new) · `internal/core/fakes_test.go` (new) · `internal/core/boundary_test.go` (new) · `internal/core/states_test.go` (new)
- [ ] `go.mod` declares `module r-loop` and `go 1.25`; `cmd/r-loop/main.go` prints `r-loop <version>` on `--version` and exits 2 with a usage line on anything else
- [ ] `internal/core/types.go` defines the plan types: `StepKey{Run string; Phase int; Kind string; Attempt int}`, `Phase{Number int; Title string; Implements []string; DependsOn []int; Files []string; Risk string; Items []Item; DoneWhen string; Milestone int; Block string}`, `Item{Text string; Done bool}`, `Milestone{Number int; Name string; Phases []int}`, `Entry{Name, Body string; Ticked, HasBox bool; Owner, Blocks, Timebox, Output, Resolved string; BlocksAll bool; BlocksPhases []int; Malformed []string}`, `Plan{Path, Topic string; Phases []Phase; Milestones []Milestone; ResolveFirst []Entry}`
- [ ] `internal/core/types.go` also defines the traffic types: `Signal{Seq int; Kind SignalKind; Source SignalSource; Step StepKey; Reason, Evidence string; At time.Time; Rejected bool; RejectReason string}` with `SignalKind ∈ {warn, halt}` and `SignalSource ∈ {driver, watchdog}`, `Question{ID string; Step StepKey; Text string; Options []string; AskedAt time.Time; Answer, AnsweredBy, Citation string; AnsweredAt time.Time}`, `Remedy{ID string; Step StepKey; Class, Command, Why, Consent string; ProposedAt, DecidedAt time.Time}`, `Landing{Phase int; MergeSHA string; GateSkipped bool; GateOutput string}`, `Event{At time.Time; Kind string; Phase int; Step string; Fields map[string]string}`
- [ ] `internal/core/types.go` defines the store types: `RunMeta{Todo string; ResolvedConfig []byte; Started time.Time}`, `Record{Kind string; At time.Time; Step *StepKey; State StepState; Run RunStatus; Reason string; Question *Question; Signal *Signal; Remedy *Remedy; Landing *Landing; Event *Event}` with `Kind ∈ {step, run, landing, question, signal, remedy, event}` — a `step` record uses `State`, a `run` record uses `Run`, and an abort is a `run` record `halted` with reason `aborted`, and `RunState{ID, Todo string; Started time.Time; Status RunStatus; Steps map[StepKey]StepState; LastStep *StepKey; Landed []Landing; Questions []Question; Signals []Signal; Remedies []Remedy; Events []Event; Warnings []string}`
- [ ] `internal/core/states.go` defines `StepState` (`queued · spawned · running · ok · failed · stalled · waiting-input`), `PhaseState` (`unticked · planned · implemented · reviewed · landed`), `RunStatus` (`created · running · halted · finished`) and `func (s StepState) Next(to StepState) (StepState, error)` that allows exactly queued→spawned→running, running→ok|failed|stalled|waiting-input, waiting-input→running, stalled→running|failed, and returns `ErrIllegalTransition` naming both states for anything else; a re-run is a new `StepKey` with `Attempt+1` starting at `queued`, never a transition out of `failed`
- [ ] `internal/core/ports.go` declares `PlanSource{Read(path string) (Plan, error); Tick(path string, phase int) error; Stamp(path, entryName, resolvedLine string) error}`, `SessionHost{Reachable() error; Open(OpenSpec) (Workspace, error); Start(pane, name, kind string, args []string) (Agent, error); Prompt(agent, text string, wait bool, timeout time.Duration) error; State(agent string) (AgentState, error); Read(agent string, lines int) (string, error); Interrupt(agent string) error; Close(workspaceID string) error; Split(direction, cwd string) (string, error)}` with `OpenSpec{CWD, Label string; Env map[string]string}`, `Workspace{ID, RootPane string}`, `Agent{Name, Pane string}`, `AgentState ∈ {idle, working, blocked, done, unknown, gone}`
- [ ] `ports.go` also declares `Repo{Root() string; Clean() ([]string, error); HeadBranch() (string, error); HeadSHA(dir string) (string, error); AddWorktree(dir, branch, base string) error; RemoveWorktree(dir string) error; Dirty(dir string) ([]string, error); CommitAll(dir, message string) (string, error); DiffNonEmpty(dir, ref string) (bool, error); ChangedFiles(dir, ref string) ([]string, error); DiffStat(dir, ref string) (int, int, error); MergeNoFF(branch string) error; AbortMerge() error; Commit(message string) (string, error); CommitTouches(sha string) ([]string, error); ResetHard(ref string) error; Run(dir, command string, timeout time.Duration) (int, string, error)}`, `Store{Create(RunMeta) (string, error); Append(runID string, rec Record) error; Load(runID string) (RunState, error); Current() (string, int, bool); SetCurrent(runID string, pid int) error; ClearCurrent() error; Aborted(runID string) bool; MarkAbort(runID string) error; Dir(runID string) string}`, `Prompts{Render(name string, vars map[string]any) (string, string, error)}`, `AskChannel{Serve(ctx context.Context) (string, error); StepURL(StepKey) string; Questions() <-chan Question; Answer(id, answer, by, citation string) error}`, `Face{Emit(Event); Ask(Question) (string, error); Close()}`, `Notifier{Fire(hook string, env map[string]string)}`, and the errors `ErrMergeConflict`, `ErrNoInput`
- [ ] `internal/core/fakes_test.go` holds an in-memory fake for every port above, each recording the calls it received in order, so later core tests need no herdr, git or terminal
- [ ] `internal/core/boundary_test.go` runs `go list -deps ./internal/core/...` and fails naming any dependency under `r-loop/internal/` other than `r-loop/internal/core`
- [ ] `internal/core/states_test.go` proves every legal transition and that `ok→running`, `failed→running`, `failed→spawned` and `queued→ok` return `ErrIllegalTransition`
**Done when:** `go build ./... && go test ./internal/core/...` is green.

### Phase 2 — PlanReader: phases and milestones
**Implements:** Run every remaining phase of a plan
**Depends on:** Phase 1
**Files:** `internal/plan/reader.go` (new) · `internal/plan/reader_test.go` (new) · `internal/plan/testdata/todo.md` (new)
- [ ] `plan.Reader` implements `core.PlanSource.Read(path) (core.Plan, error)`; `Plan.Topic` is the todo's parent directory name
- [ ] a phase is a `### Phase N — title` heading (`—` or `-`, one or more spaces around) up to the next `###` or `##` heading; `Block` holds that raw text verbatim; `Number`, `Title` (with any `<!-- built: … -->` marker and `✅` removed), `Implements` (split on ` · `), `DependsOn` (`—`, `-`, `none` or absent mean none; otherwise every integer after `Phase`), `Files` (every backticked path on the `**Files:**` line), `Risk`, `Items` (`- [ ]` open, `- [x]`/`- [X]` done), `DoneWhen` (the text after `**Done when:**` up to the next `**` line or heading)
- [ ] a milestone is a `## Milestone N — name` heading; each phase's `Milestone` is the number of the nearest such heading above it, `0` when there is none; `Milestone.Phases` lists its phases in document order
- [ ] two headings with the same phase number, a phase number that skips a value, a `Depends on:` naming a phase that does not exist, or a `### Phase` heading without its dash return an error naming the line — the reader fails closed and never guesses
- [ ] `Plan.Unticked() []int` returns the phases with at least one open item, in numeric order; a phase whose every item is `[x]` is ticked
- [ ] `internal/plan/testdata/todo.md` is a real `/r:spec-design` plan with milestones, a `## Waves` block and a `<!-- built: … -->` marker; `reader_test.go` asserts the phase count, one phase's every field including `Block`, the milestone grouping, `Unticked`, and each fail-closed error
**Done when:** `go test ./internal/plan/...` is green.

### Phase 3 — PlanReader: Resolve first, tick and stamp
**Implements:** Close a blocking plan decision at startup · Run every remaining phase of a plan
**Depends on:** Phase 2
**Files:** `internal/plan/resolve.go` (new) · `internal/plan/write.go` (new) · `internal/plan/reader.go` (modify) · `internal/plan/resolve_test.go` (new) · `internal/plan/write_test.go` (new)
- [ ] `## Resolve first` is read up to the next heading of any level (`^#{1,6}\s`); each entry starts at a `- ` or `* ` bullet; `HasBox` says whether it carries `[ ]`/`[x]`; `Name` is the first `**…**` span; `Body` is the entry's full text; `Owner:`, `Blocks:`, `Timebox:`, `Output:`, `Resolved:`, `Alternative:`, `Outstanding:` are sliced by label position, and any other `Word:` label is listed in `Malformed`
- [ ] fail-closed rules: an entry with no checkbox is outstanding; a missing `Blocks:` or one naming no phase sets `BlocksAll: true`; a ticked entry with no `Resolved:` line counts as resolved for gating and is listed in `Malformed` as `ticked without Resolved`; `BlocksPhases` holds every integer after `Phase` in `Blocks:`
- [ ] `Plan.Blocking(phases []int) []core.Entry` returns the outstanding entries whose `BlocksAll` is true or whose `BlocksPhases` meets the given run list; a plan with no section returns none
- [ ] `Tick(path, phase)` rewrites only that phase's `- [ ]` lines to `- [x]`; a byte comparison of every other line before and after is part of the test; a phase with nothing open returns `ErrNothingToTick`
- [ ] `Stamp(path, entryName, resolvedLine)` turns the entry's `- [ ]` into `- [x]`, or turns a box-less `- **Name**` bullet into `- [x] **Name**`, and inserts one line `      Resolved: <resolvedLine>` after the entry's last line, leaving every other byte unchanged; an entry not found or already ticked is an error
- [ ] `resolve_test.go` covers: no section, an empty section, a box-less entry (outstanding, then stamped), a `Blocks:` naming two phases, a `Blocks:` with prose only (blocks all), a ticked entry without `Resolved:`, and an `Informs:` label reported as malformed rather than folded into `Blocks:`
**Done when:** `go test ./internal/plan/...` is green.

### Phase 4 — ConfigReader: block-style YAML, three-tier resolution and the banner
**Implements:** Choose the agent for each step · Try a different provider for one run · Add a step to every phase
**Depends on:** Phase 1
**Files:** `internal/config/reader.go` (new) · `internal/config/defaults.yaml` (new) · `internal/config/banner.go` (new) · `internal/config/reader_test.go` (new)
- [ ] `config.Load(projectDir, homeDir string, overrides []string) (LoopConfig, error)` resolves every key in the order CLI override → `<projectDir>/.r-loop/config.yaml` → `<homeDir>/.config/r-loop/config.yaml` → the embedded `defaults.yaml`, and records each key's provenance as `<file>:<key>`, `flag:--provider` or `default`
- [ ] `LoopConfig` holds `Pipeline []string`, `Steps map[string]StepRow` with `StepRow{Prompt, Check, Provider, Model, Effort string; Timeout time.Duration; Reviewers []Reviewer}` and `Reviewer{Provider, Model, Effort string}`, `Providers map[string]yaml.Node` (one raw block per name), `Watchdog{Provider, Model string; Allow []string; RemedyWindow, AnswerWindow, CheckTimeout, StallGrace time.Duration; OvertimeFactor, DiffFactor float64}`, `Notify{OnHalt, OnWarn, OnDone string}` and `Provenance map[string]string`
- [ ] a `reviewers` item is either a scalar provider name (model and effort empty, meaning the provider's own default) or a block mapping with `provider` (required), `model` and `effort`; any other shape is `ErrConfig` naming the line
- [ ] `defaults.yaml` (embedded) carries pipeline `[plan, implement, review]`; plan `claude/opus/high/1h/plan-file`; implement `codex/gpt-5.6-sol/medium/4h/diff`; review `reviewers: [claude, codex]`, `2h`, `verdict`; milestone `claude/opus/medium/1h/report`; watchdog `claude/sonnet`, `allow: []`, `remedyWindow: 10m`, `answerWindow: 5m`, `checkTimeout: 10m`, `stallGrace: 2m`, `overtimeFactor: 2`, `diffFactor: 3`; notify hooks empty; a factor below 1 is `ErrConfig`
- [ ] the file is parsed with `yaml.v3` at the node level; any node with `Style == FlowStyle` is rejected with `config.yaml:<line>: flow style is not accepted, write it block style`; an unknown key at any level, a timeout that does not parse as a `time.Duration`, a pipeline entry with no `steps` row, and a `check` outside `plan-file, diff, findings, verdict, report` are each `ErrConfig` naming the key, line or entry; the CLI maps `ErrConfig` to exit 2
- [ ] `--provider <step>=<name>` overrides one step's `provider` and records `flag:--provider`; an unknown step name or a missing `=` is `ErrConfig`
- [ ] `config.Banner(cfg) string` prints one line per pipeline row and the milestone row — `<step>  <provider>  <model>  <effort>  <timeout>  <check>  ← <provenance>` — one `reviewer <provider> <model|provider default> <effort|provider default>` line per reviewer, `override: implement provider codex (flag) replaces claude (.r-loop/config.yaml)` per override, `fix half: implement row` when `review` is in the pipeline, and `watchdog: <provider> <model> allow [<classes>]`
- [ ] `reader_test.go` proves a project file overriding one key of the machine file, a flow-style list rejected with its line, an unknown key rejected, a mixed reviewer list (one scalar, one block), the `--provider` override with its provenance, and the banner for a two-override config
**Done when:** `go test ./internal/config/...` is green.

### Phase 5 — StateStore: the run directory
**Implements:** Resume a run that stopped
**Depends on:** Phase 1
**Files:** `internal/store/store.go` (new) · `internal/store/store_test.go` (new)
**Risk:** persistence
- [ ] `store.New(repoRoot string) *Store` implements `core.Store` over `<repoRoot>/.r-loop/runs/`; `Create(meta)` makes `<runID>/` with `runID = <yyyymmdd-HHMMSS>` (a second run in the same second gets `-2`), writes `config.resolved.yaml` from `meta.ResolvedConfig` and `meta.json` holding `{"todo":…,"started":…}`, and creates empty `events.jsonl`, `questions.jsonl`, `signals.jsonl`, `remedies.jsonl`
- [ ] `Append(runID, rec)` writes one JSON line to the file for `rec.Kind`: `step`, `run`, `landing` and `event` to `events.jsonl`; `question` to `questions.jsonl`; `signal` to `signals.jsonl`; `remedy` to `remedies.jsonl`; each write uses `O_APPEND` and `fsync`, so a crash after the call leaves the record on disk
- [ ] `Load(runID) (core.RunState, error)` reads `meta.json` into `Todo` and `Started`, then **all four record files**: step records into `Steps` and `LastStep` (the last non-terminal step, else the last step), run records into `Status`, landings into `Landed`, events into `Events`, questions into `Questions` (a later line for the same id replaces the earlier one, so answers survive), signals into `Signals`, remedies into `Remedies`; a truncated last line in any file is skipped and named in `Warnings`, never an error
- [ ] `Current()` reads `.r-loop/runs/current` (`<runID> <pid>`), `SetCurrent` writes it atomically (temp file + rename), `ClearCurrent` removes it; `MarkAbort(runID)` creates `<runID>/abort` and `Aborted(runID)` reports its presence
- [ ] `store.EnsureExcluded(repoRoot)` appends `.r-loop/runs/` and `.r-loop/wt/` to `<git-common-dir>/info/exclude` when either line is absent, resolving the directory with `git rev-parse --git-common-dir`, and never touches `.gitignore`
- [ ] `store_test.go` proves durability by reading each file back after `Append`, a replay of a mixed log (steps, a question then its answer, a signal, a remedy, a landing) into the expected `RunState`, the truncated-line warning, the `current` round trip, and that a second `EnsureExcluded` adds no duplicate lines
**Done when:** `go test ./internal/store/...` is green.

## Milestone 2 — Sessions and providers
Contracts: `tech-design.md#milestone-2-sessions-and-providers`

### Phase 6 — ProviderRegistry and the shipped blocks
**Implements:** Add a provider the driver has never seen · Choose the agent for each step
**Depends on:** Phase 4
**Files:** `internal/providers/registry.go` (new) · `internal/providers/shipped/claude.yaml` (new) · `internal/providers/shipped/codex.yaml` (new) · `internal/providers/registry_test.go` (new)
- [ ] `providers.Provider{Name, Kind, ModelFlag, EffortFlag, AskFlag, DoneSignal, Ask, Review, Source string}`; `Registry.Resolve(name) (Provider, error)` takes the **whole block** from the first place that has one — the project config's `providers.<name>`, else `~/.config/r-loop/providers/<name>.yaml`, else the embedded shipped block — and sets `Source` to that place; blocks are never merged key by key
- [ ] validation on `Resolve`: `kind` required and non-empty; `modelFlag` contains `{model}` or is empty; `effortFlag` contains `{effort}` or is empty; `askFlag` contains `{url}` or `{mcpConfig}` or is empty; `doneSignal` is `sentinel`; `ask` is `mcp` or `none`; an unknown key is an error; every error names the field and the source it came from
- [ ] `Args(p Provider, model, effort, askURL, mcpConfigPath string) []string` expands the templates, splits each expanded flag on spaces, and omits a flag whose template or value is empty, so an empty model or effort (a reviewer on the provider default) passes no flag
- [ ] `shipped/claude.yaml`: `kind: claude`, `modelFlag: "--model {model}"`, `effortFlag: "--effort {effort}"`, `askFlag: "--mcp-config {mcpConfig}"`, `doneSignal: sentinel`, `ask: mcp`, `review: "/code-review"`
- [ ] `shipped/codex.yaml`: `kind: codex`, `modelFlag: "-c model={model}"`, `effortFlag: "-c model_reasoning_effort={effort}"`, `askFlag: "-c mcp_servers.r-loop.url={url}"`, `doneSignal: sentinel`, `ask: mcp`, `review: "/review"`
- [ ] `WriteMCPConfig(path, url string) error` writes `{"mcpServers":{"r-loop":{"type":"http","url":"<url>"}}}`; `ToCore(p Provider, model, effort, askURL, mcpConfigPath string) core.ProviderArgs` returns `{Kind, Args, Ask: p.Ask == "mcp", Review}` — the only shape the core sees
- [ ] `registry_test.go` proves a project block replacing the shipped `codex` block whole, a machine-level file adding `pdev` with only `kind` and `doneSignal`, each validation error with its field, the expanded args of both shipped blocks with and without model and effort, and that no non-test file under `internal/core` contains `claude`, `codex` or `opencode`
**Done when:** `go test ./internal/providers/...` is green.

### Phase 7 — PromptRenderer and the embedded prompts
**Implements:** Override the prompts for this project · Report a step's outcome so the driver can act
**Depends on:** Phase 1
**Files:** `internal/prompts/render.go` (new) · `internal/prompts/templates/plan.md` (new) · `internal/prompts/templates/implement.md` (new) · `internal/prompts/templates/review.md` (new) · `internal/prompts/templates/fix.md` (new) · `internal/prompts/templates/milestone.md` (new) · `internal/prompts/templates/watchdog.md` (new) · `internal/prompts/render_test.go` (new)
- [ ] `prompts.New(projectDir string) *Renderer` implements `core.Prompts.Render(name, vars) (text, source string, err error)`: `<projectDir>/.r-loop/prompts/<name>.md` when it exists (source = that path), else the embedded `templates/<name>.md` (source = `embedded`); an unknown name is an error
- [ ] templates are Go `text/template` with `missingkey=error`; the variable set is `PhaseNumber, PhaseTitle, PhaseBlock, Criteria, TodoPath, SpecDir, PlanPath, Branch, Base, Worktree, Sentinel, RunDir, AskURL, ReviewCommand, FindingsPath, FindingsFiles, VerdictPath, ReportPath, MilestoneName, MilestonePhases, Addendum`
- [ ] every step template — `plan`, `implement`, `review`, `fix`, `milestone`; not `watchdog`, which is a standing session and never writes a sentinel — ends with the same sentinel paragraph: write `{"outcome":"ok","reason":"","at":"<RFC3339>"}` to `{{.Sentinel}}` as the last action, or `{"outcome":"failed","reason":"<why>","at":"…"}` when the work cannot be done; never report completion only in the terminal; when `{{.AskURL}}` is non-empty, use the `ask_user` tool instead of guessing; and, when `{{.Addendum}}` is non-empty, a final `Note from the previous attempt:` section carrying it
- [ ] `plan.md` asks for a phase plan at `{{.PlanPath}}` whose first lines carry `status: planned`, derived from `{{.PhaseBlock}}` and `{{.Criteria}}` against the tree in `{{.Worktree}}`; `implement.md` asks for test-first implementation of `{{.PlanPath}}` with the build green, and forbids editing `{{.TodoPath}}`; both say the driver commits leftover files after the sentinel
- [ ] `review.md` asks the reviewer to run `{{.ReviewCommand}}` report-only over the diff of `{{.Branch}}` against `{{.Base}}`, convert what it reports into `{{.FindingsPath}}` as `{"reviewer":"<name>","findings":[{"id":"<name>-<n>","title":"…","detail":"…","files":["…"]}]}` with ids numbered from 1, and change no file in `{{.Worktree}}`; `fix.md` asks for a verdict (`real`, `not-real`, `out-of-scope`) and a severity (`P1`–`P4`) on every finding in `{{.FindingsFiles}}`, fixes of only those `real` at `P1` or `P2`, and `{{.VerdictPath}}` as `{"findings":[{"id","reviewer","title","verdict","severity","fixed","files"}]}`
- [ ] `milestone.md` asks for a Markdown report of what `{{.MilestoneName}}` (phases `{{.MilestonePhases}}`) built, written to `{{.ReportPath}}` from the landed commits and each phase's `.task-plans/` file; `watchdog.md` is a first draft stating the rule — it may warn and halt, it may propose remedies only with consent, it may never approve anything — and naming the tools `signal`, `propose_remedy`, `restart_step` and `answer_question`
- [ ] `render_test.go` proves the override wins with its source named, the embedded fallback, `missingkey` failing on an absent variable, the `Addendum` section present only when set, that each of the six templates renders with a full variable set, and that the five step templates carry the sentinel paragraph while `watchdog.md` does not
**Done when:** `go test ./internal/prompts/...` is green.

### Phase 8 — herdr adapter
**Implements:** See where a run is
**Depends on:** Phase 1
**Files:** `internal/herdr/client.go` (new) · `internal/herdr/client_test.go` (new) · `internal/herdr/testdata/herdr` (new) · `internal/herdr/live_test.go` (new)
- [ ] `herdr.Client{Bin string}` implements `core.SessionHost` by running the `herdr` CLI; every call parses stdout JSON on exit 0 and, on exit 1, the JSON error on stderr into `herdr.Error{Code, Message string}` so callers branch on `Code`; exit 2 is `herdr.Error{Code: "usage"}`; a missing binary is `ErrNoBinary` (the CLI maps it to exit 127)
- [ ] `Reachable()` runs `herdr workspace list` and returns the error code when the server is not up — never `herdr status`, which exits 0 with no server
- [ ] `Open(spec)` runs `herdr workspace create --cwd <spec.CWD> --label <spec.Label> --env K=V… --no-focus` and returns `.result.workspace.workspace_id` and `.result.root_pane.pane_id`; `Split(direction, cwd)` runs `herdr pane split --current --direction <d> --cwd <cwd> --no-focus` and returns `.result.pane.pane_id`
- [ ] `Start(pane, name, kind, args)` runs `herdr agent start <name> --kind <kind> --pane <pane> -- <args…>`, which returns once herdr reports the agent ready; `agent_not_ready` comes back as a `herdr.Error` and is never retried
- [ ] `Prompt(agent, text, wait, timeout)` runs `herdr agent prompt <agent> <text>` with `--wait --timeout <ms>` when `wait` is set; the text is one argv element, never shell-parsed; `agent_blocked` and `agent_prompt_stalled` come back as `herdr.Error` codes
- [ ] `State(agent)` runs `herdr agent get <agent>` and maps the lifecycle state `idle|working|blocked|done|unknown` to `core.AgentState`, a not-found code to `gone`; `Read(agent, lines)` runs `herdr agent read <agent> --source recent-unwrapped --lines <n>`; `Interrupt(agent)` runs `herdr agent send-keys <agent> esc` then `herdr agent send-keys <agent> ctrl+c`; `Close(id)` runs `herdr workspace close <id>` and never adds `--group`
- [ ] `client_test.go` points `Bin` at `testdata/herdr`, a script that records its argv and prints canned JSON, and proves every command's argv, each result field parsed, the error-code mapping, and `ErrNoBinary`
- [ ] `live_test.go` runs only when `R_LOOP_LIVE_HERDR=1`: it opens a workspace in `t.TempDir()`, splits a pane, reads `State` of a missing agent as `gone`, and closes the workspace — the check that the argv above matches the installed herdr 0.9.0
**Done when:** `go test ./internal/herdr/...` is green.

### Phase 9 — git adapter
**Implements:** Run every remaining phase of a plan · Stop a run without losing work
**Depends on:** Phase 1
**Files:** `internal/gitrepo/repo.go` (new) · `internal/gitrepo/repo_test.go` (new)
**Risk:** persistence
- [ ] `gitrepo.Open(dir string) (*Repo, error)` implements `core.Repo` over the `git` CLI; `Root()` is `git rev-parse --show-toplevel`; `HeadBranch()` is `git symbolic-ref --short HEAD` and a detached HEAD is an error; `HeadSHA(dir)` is `git -C <dir> rev-parse HEAD`; `Clean()` returns the paths from `git status --porcelain`, untracked included
- [ ] `AddWorktree(dir, branch, base)` creates `branch` from `base` when absent and runs `git worktree add <dir> <branch>`; an existing worktree on the same branch is reused, not an error; `RemoveWorktree(dir)` runs `git worktree remove --force <dir>` and `git worktree prune`
- [ ] `Dirty(dir)` lists `git -C <dir> status --porcelain` paths; `CommitAll(dir, message)` runs `git -C <dir> add -A` and `git -C <dir> commit -m <message>` with author `r-loop <r-loop@local>` and returns the new sha, or HEAD's sha with no commit when the tree is clean
- [ ] `DiffNonEmpty(dir, ref)`, `ChangedFiles(dir, ref)` and `DiffStat(dir, ref)` compare the **working tree against `ref`, untracked files included**: `git -C <dir> diff --name-only <ref>` plus `git -C <dir> ls-files --others --exclude-standard`, and `--numstat` summed for the stat, with each untracked file counted as its line count added
- [ ] `MergeNoFF(branch)` runs `git merge --no-ff --no-commit <branch>` in the primary tree; a non-zero exit runs `git merge --abort` and returns `core.ErrMergeConflict` naming the conflicting paths; `AbortMerge()` is `git merge --abort`; `Commit(message)` runs `git add -A` in the primary tree and commits, so the staged merge and the ticked todo land together; `CommitTouches(sha)` is `git show --name-only --format= <sha>` over the merge commit's first-parent diff; `ResetHard(ref)` is `git reset --hard <ref>`
- [ ] `Run(dir, command, timeout)` runs `sh -c <command>` in `dir`, returns the exit code and combined output, kills the process group on timeout and returns exit `-1` with `timed out after <d>` appended
- [ ] `repo_test.go` builds a repository under `t.TempDir()` and proves worktree add and reuse, `CommitAll` on dirty and clean trees, the three diff calls seeing an uncommitted edit and an untracked file, a no-ff merge left uncommitted then committed with both files touched, a conflict returning `ErrMergeConflict` with the tree restored, `AbortMerge` after a clean `MergeNoFF`, and `Run` timing out
**Done when:** `go test ./internal/gitrepo/...` is green.

### Phase 10 — StepKinds and the evidence checks
**Implements:** Add a step to every phase · Report a step's outcome so the driver can act
**Depends on:** Phase 1
**Files:** `internal/core/kinds.go` (new) · `internal/core/evidence.go` (new) · `internal/core/evidence_test.go` (new)
- [ ] `core.StepKind{Name, Prompt, Check string; Row StepRow}` with `StepRow{Provider, Model, Effort string; Timeout time.Duration; Reviewers []Reviewer}` and `Reviewer{Provider, Model, Effort string}`; `core.ProviderArgs{Kind string; Args []string; Ask bool; Review string}`; `core.Pipeline(entries []string, rows map[string]StepRow, prompts, checks map[string]string) ([]StepKind, error)` builds the ordered list and rejects an entry with no row, or a check not registered, naming the entry
- [ ] `type EvidenceCheck func(ctx EvidenceContext) (ok bool, missing string)` with `EvidenceContext{Repo Repo; Worktree, Base, StartSHA, PlanPath string; FindingsFiles []string; VerdictPath, ReportPath string; FS fs.FS}`; `RegisterCheck(name, fn)` adds a check without editing the loop, and a test registers `always-ok` and builds a pipeline entry `docs` on it
- [ ] shipped check `plan-file`: `PlanPath` exists and one of its first five lines starts with `status:`; the missing text names the path or `no status: header`
- [ ] shipped check `diff`: `Repo.DiffNonEmpty(Worktree, StartSHA)` — the step's own starting commit, never the phase base, so a plan commit made by the earlier step cannot satisfy it; the missing text is `no change since <StartSHA short>`
- [ ] shipped check `findings`: every path in `FindingsFiles` parses as `{"reviewer":string,"findings":[{"id":string,"title":string,"detail":string,"files":[string]}]}`, its `reviewer` matches the file name, and its ids are unique and start with `<reviewer>-`; `core.ReadFindings(path) (Findings, error)` is the one parser; shipped check `report`: `ReportPath` exists and is non-empty; `verdict` is registered as a stub returning `false, "verdict check not built yet"` so a default pipeline validates
- [ ] `core.ReadSentinel(path) (Sentinel, error)` parses `{"outcome":"ok"|"failed","reason":"…","at":"…"}`; an absent file is `ErrNoSentinel`; any other outcome or unreadable JSON is `ErrSentinelMalformed`
- [ ] `core.Judge(s Sentinel, sErr error, evidenceOK bool, missing string) (StepState, string)`: ok sentinel and evidence → `ok`; failed sentinel → `failed, <reason>`; ok sentinel and evidence missing → `failed, evidence missing: <missing>`; malformed → `failed, sentinel unreadable`
- [ ] `evidence_test.go` covers each shipped check passing and failing over `fstest.MapFS` and the fake `Repo`, the `diff` check failing when only the base differs but `StartSHA` equals the tree, every `Judge` branch, and `Pipeline` rejecting `check: bogus`
**Done when:** `go test ./internal/core/...` is green.

### Phase 11 — SessionManager: spawn, poll, judge and stop
**Implements:** Report a step's outcome so the driver can act · See where a run is
**Depends on:** Phase 6, Phase 7, Phase 8, Phase 9, Phase 10
**Files:** `internal/core/session.go` (new) · `internal/core/session_test.go` (new)
- [ ] `core.SessionManager{Host SessionHost; Repo Repo; Prompts Prompts; Store Store; Resolve func(provider, model, effort, askURL, mcpConfigPath string) (ProviderArgs, error); Now func() time.Time; Poll, StallGrace time.Duration}` (`StallGrace` from `watchdog.stallGrace`, default 2 min); `StepRef{Key StepKey; Kind StepKind; Phase Phase; InPrimary bool; Worktree, Branch, Base, RunDir, AskURL string; Vars map[string]any}`; `Spawn(ctx, ref) (*Session, error)`
- [ ] `Spawn`: unless `InPrimary`, `Repo.AddWorktree(.r-loop/wt/phase-<N>, r-loop/phase-<N>, base)`; the step's directory is the worktree, or `Repo.Root()` when `InPrimary`; `Session.StartSHA = Repo.HeadSHA(dir)`; append `spawned` **before** `Host.Open`
- [ ] then `Host.Open` with `Env{R_LOOP_SENTINEL, R_LOOP_RUN, R_LOOP_PHASE, R_LOOP_STEP}` and label = the agent name `rloop-p<N>-<kind>` (with `-a<attempt>` appended when `Attempt > 1`); `Host.Start` with the resolved kind and args; `Prompts.Render` with the step's variables plus `Sentinel` = `<RunDir>/phase-<N>/<kind>-a<attempt>.sentinel`; `Host.Prompt` without wait; append `running`
- [ ] any failure between `Open` and `running` returns `failed(spawn: <herdr code> <message>)`, leaves whatever opened standing and names it; nothing is retried
- [ ] `Wait(ctx, s, obs Observer) Outcome` with `Observer{Started(*Session); Stalled(*Session); Resumed(*Session)}` calls `obs.Started` once, then polls every `Poll` (default 10 s): a readable sentinel ends the wait; `gone` returns `failed(agent gone)`; `blocked` or `idle` held for `StallGrace` with `s.OpenQuestion == false` records `stalled` and calls `obs.Stalled` **without returning** — the session is left live and polling continues, and a later `working` state records `running` and calls `obs.Resumed`; elapsed beyond `Kind.Row.Timeout` returns `failed(backstop <timeout>)`, or `stalled(backstop)` when the step was stalled at that moment; while `s.OpenQuestion` is true neither the stall clock nor the backstop advances
- [ ] on an `ok` sentinel `Wait` first runs `Repo.CommitAll(dir, "r-loop: phase <N> <kind>")` — skipped for an `InPrimary` step, whose caller commits its own output — then the step's evidence check with `StartSHA`, then `Judge`; it appends the resulting state with its reason and returns `Outcome{State StepState; Reason string; Session *Session}` carrying the workspace id, the agent name and the directory
- [ ] `Stop(s)` calls `Host.Interrupt(agent)` and never `Host.Close`
- [ ] `session_test.go` with the fakes: the call order with `spawned` recorded before `Open`, an `InPrimary` spawn making no worktree, a spawn failure naming the opened workspace, ok sentinel plus evidence → `ok` with the leftover commit made, ok sentinel with no change since `StartSHA` → `failed`, `idle` for `StallGrace` → `obs.Stalled` with polling continuing, stalled until the backstop → `stalled(backstop)`, backstop while working → `failed`, an open question suspending both clocks, and attempt 2 getting the `-a2` name and sentinel
**Done when:** `go test ./internal/core/...` is green.

## Milestone 3 — The serial loop, landing and the plain face
Contracts: `tech-design.md#milestone-3-the-serial-loop-landing-and-the-plain-face`

### Phase 12 — RunLoop: phases, steps, halts and the report
**Implements:** Run every remaining phase of a plan · Resume a run that stopped
**Depends on:** Phase 5, Phase 11
**Files:** `internal/core/loop.go` (new) · `internal/core/runners.go` (new) · `internal/core/report.go` (new) · `internal/core/loop_test.go` (new)
- [ ] `core.RunLoop{Plan Plan; TodoPath string; Kinds []StepKind; Sessions *SessionManager; Store Store; Face Face; Notifier Notifier; Hooks Hooks; Lander Lander; Runners map[string]StepRunner; RunID string}` with `Hooks{OnHalt, OnWarn, OnDone string}`, `Lander{Land(ctx, Phase) (Landing, error)}` and `StepRunner{Run(ctx, StepRef, Observer) Outcome}`; the loop runs each step's `Run` in a goroutine and keeps the live `*Session` it receives through `Observer.Started`, so abort, signals and questions can act on the session while the step is running
- [ ] `runners.go`: `DefaultRunners(sm *SessionManager, kinds []StepKind) map[string]StepRunner` keyed by a step kind's `Check` (the kinds are passed so a runner can read another row, such as the review runner reading the `implement` row), plus `singleRunner` (`Spawn` then `Wait`) used for any check without its own entry; the loop picks `Runners[kind.Check]`, else the single runner
- [ ] `Run(ctx, RunOptions{From int; Phases []int; Resume bool}) int` builds the run list from `Plan.Unticked()`, narrowed by `From` (that number and above) or `Phases` (a listed phase ticked or absent returns 2 naming it); with `Resume`, every phase with a landing in the store is skipped, every step whose latest attempt is `ok` is skipped, and the first other step runs as a new attempt (`Attempt` = last + 1)
- [ ] per phase: emit `phase-start`; per pipeline entry: build the `StepRef` (branch `r-loop/phase-<N>`, worktree `.r-loop/wt/phase-<N>`, base = the primary tree's HEAD branch, `RunDir` = `Store.Dir`), run it, emit a `step` event on every state change; phase state advances `planned`, `implemented`, `reviewed` as the `plan`, `implement` and `review` entries end `ok`; then `Lander.Land` (a no-op lander until LandGate exists) and `landed`
- [ ] a `failed` outcome sets the run `halted`, fires `OnHalt` with `R_LOOP_STATUS=halted`, emits `halt` with the reason, the workspace, the worktree and the line `r-loop resume`, and returns 1; `Observer.Stalled` emits `stalled` naming the workspace and worktree while the session keeps running; a `stalled(backstop)` outcome halts and returns 3
- [ ] `core.StepVars(ref StepRef, plan Plan, todoPath, runDir string) map[string]any` builds **every** template variable for every step — `PhaseNumber`, `PhaseTitle`, `PhaseBlock` (`Phase.Block`), `Criteria` (the unticked item texts), `TodoPath`, `SpecDir` (the todo's directory), `PlanPath` (`.task-plans/phase-<N>-<kebab title>.md`, at most 60 characters), `Branch`, `Base`, `Worktree`, `Sentinel`, `RunDir`, `AskURL`, `ReviewCommand`, `FindingsPath`, `FindingsFiles`, `VerdictPath`, `ReportPath`, `MilestoneName`, `MilestonePhases`, `Addendum` — with `""` or an empty list where a step has no value, so `missingkey=error` only ever catches a template typo
- [ ] `Store.Aborted(RunID)` is checked every poll tick; when set, the loop stops scheduling, records a `run` record `halted` with reason `aborted`, emits the live session's workspace and worktree by name, and returns 1; when every listed phase is landed the run is `finished`, `OnDone` fires, and the loop returns 0
- [ ] `core.Report(state RunState, plan Plan) string` renders `report.md` with sections Landed (phase, merge SHA, `gate skipped` when so), Halt (reason, workspace, worktree, resume line), Questions, Signals, Remedies, Findings, Skips; empty sections are omitted; the loop rewrites `<RunDir>/report.md` after every transition
- [ ] `loop_test.go` drives a three-phase plan through fakes and proves: order and events of a clean run (exit 0, `OnDone` fired), a failed implement halting with the resume line (exit 1, `OnHalt` fired), resume skipping the `ok` plan step and re-running implement as attempt 2, stalled then backstop (exit 3), abort mid-step (exit 1), `--from` and `--phases` narrowing, and the report sections for the failed run
**Done when:** `go test ./internal/core/...` is green.

### Phase 13 — RunLoop: signals, questions and restarts
**Implements:** Answer an agent's question without leaving the loop · Halt a step that has gone the wrong way · Fix what is blocking a step
**Depends on:** Phase 12
**Files:** `internal/core/loop.go` (modify) · `internal/core/loop_events_test.go` (new)
- [ ] `RunLoop` gains `Watcher Watcher`, `Ask AskChannel` and `RemedyWindow time.Duration`, with `Watcher{BeforePhase(ctx, Phase); StepStarted(StepRef, *Session); StepEnded(StepRef, Outcome); Signals() <-chan Signal; Restarts() <-chan Restart; Route(ctx, Question) (answered bool)}` and `Restart{Step StepKey; Addendum, Provider string}`; a `nopWatcher` answers nothing and emits nothing
- [ ] the loop calls `BeforePhase` before a phase's first step, `StepStarted` after each spawn and `StepEnded` after each outcome
- [ ] a `warn` from `Signals()` is emitted as `warning` and fires `OnWarn` with `R_LOOP_STATUS=warning`; the run continues. A `halt` calls `Sessions.Stop`, records `failed(watchdog: <reason>)`, sets the run `halted`, fires `OnHalt` and returns 5
- [ ] a `failed` outcome waits up to `RemedyWindow` (0 when no watchdog runs) on `Restarts()` for a restart of that step; a restart runs the step again as a new attempt with `Vars["Addendum"]` set and, when `Provider` is given, that provider for this attempt only; the window expiring halts as before (exit 1)
- [ ] every question from `Ask.Questions()` **first** sets the asking session's `OpenQuestion`, moves the step to `waiting-input` and freezes its backstop, is appended to the store and emitted; only then is it offered to `Watcher.Route`, and when that returns false it goes to `Face.Ask`; the answer is returned with `Ask.Answer(id, answer, "maintainer", "")`, appended, and the step returns to `running`; `ErrNoInput` from the face leaves the question open, the step `waiting-input` and the run alive
- [ ] if a step's backstop ever fires while it is `waiting-input`, the run halts with `invariant: a question never expires` and exit 1 rather than failing the step
- [ ] `loop_events_test.go` with fakes proves: warn continues with `OnWarn` fired, halt stops the session and returns 5, a restart inside the window re-runs attempt 2 with the addendum, the window expiring returns 1, a question answered through the face returns to `running`, `ErrNoInput` keeping the run alive past the backstop, and `BeforePhase` called before the first spawn
**Done when:** `go test ./internal/core/...` is green.

### Phase 14 — LandGate and the milestone boundary
**Implements:** Run every remaining phase of a plan
**Depends on:** Phase 3, Phase 9, Phase 12
**Files:** `internal/core/land.go` (new) · `internal/core/milestone.go` (new) · `internal/core/land_test.go` (new)
**Risk:** persistence
- [ ] `core.LandGate{Repo Repo; Plan PlanSource; Store Store; Face Face; TodoPath string; GateTimeout time.Duration; Boundary *MilestoneBoundary}` implements `Lander`; `Land(ctx, phase)` works in the primary tree only
- [ ] step 1: `Repo.MergeNoFF("r-loop/phase-<N>")` — the merge is staged on disk and not committed; `ErrMergeConflict` returns at once, before the gate and before the todo is touched, and the loop halts naming the paths
- [ ] step 2: when `phase.DoneWhen` is non-empty, `Repo.Run(root, DoneWhen, GateTimeout)` over that merged, uncommitted tree — so the gate proves the phase's own code; a non-zero exit runs `Repo.AbortMerge()` and returns `ErrGate` carrying the output, with nothing merged or ticked; an empty `DoneWhen` records `Event{Kind: "gate-skipped"}` and `Landing.GateSkipped`, never a halt
- [ ] step 3: `Plan.Tick(TodoPath, N)` then `Repo.Commit("phase <N>: <title>")` — one merge commit carrying the code and the ticks; `Repo.CommitTouches(sha)` must include the todo and at least one other path, else `Repo.ResetHard("HEAD~1")` and `ErrLanding` naming `code and ticks land as one commit`
- [ ] `Landing{Phase, MergeSHA, GateSkipped, GateOutput}` is appended before `Land` returns; the phase worktree is left in place
- [ ] `core.MilestoneBoundary{Plan Plan; Sessions *SessionManager; Repo Repo; Kind StepKind; Topic string; RunDir string}`; `LandGate` calls `Boundary.After(ctx, phase)` after a landing; when phase N was the last unticked phase of its `## Milestone M`, it spawns one session with `InPrimary: true`, the `milestone` kind (`check: report`), `ReportPath` = `docs/<Topic>/reports/milestone-<M>-<kebab name>.md`, `MilestoneName` and `MilestonePhases`
- [ ] on `ok` the driver commits the report as `docs(report): milestone <M>`; on `failed`, `stalled` or backstop it records `Event{Kind: "report-skipped", reason}` and returns without error — the milestone's code is already merged
- [ ] `land_test.go` uses a real temporary repository through `gitrepo` and fakes for the rest: a gate that needs the phase's new file passes only because it runs after the merge, a red gate aborting the merge with the todo untouched, no `Done when:` recorded as a skip, a conflict leaving the todo untouched, the report session spawned only after a milestone's last phase and in the primary tree, and a failed report recorded as a skip
**Done when:** `go test ./internal/core/... ./internal/gitrepo/...` is green.

### Phase 15 — CLI, wiring, preflight and dry run
**Implements:** Run every remaining phase of a plan · Choose the agent for each step · Try a different provider for one run
**Depends on:** Phase 13, Phase 14
**Files:** `cmd/r-loop/main.go` (modify) · `internal/app/wire.go` (new) · `internal/app/preflight.go` (new) · `internal/face/plain/plain.go` (new) · `internal/app/app_test.go` (new)
- [ ] `r-loop <todo> [--from N] [--phases n,n] [--provider <step>=<name>]… [--no-watchdog] [--plain] [--dry-run]`; until the TUI exists every run uses the plain face and the banner says `face: plain`; an unknown flag or a missing todo exits 2 with one line on stderr
- [ ] `app.Wire(opts)` builds `plan.Reader`, `config.Load`, `store.New`, the provider registry, `prompts.New`, `herdr.Client`, `gitrepo.Open` and `plain.Face`; the `core.SessionManager` with `Resolve` mapping a provider name, model and effort to `core.ProviderArgs` through the registry; `core.DefaultRunners`; the `core.LandGate` with its `MilestoneBoundary` on the `milestone` row; and the `core.RunLoop`
- [ ] `app.Preflight` refuses with exit 4 naming the reason when the herdr server is unreachable, the primary tree is not clean (paths listed), or `.r-loop/runs/current` names a live pid; in `--plain` a blocking `## Resolve first` entry for the run list is also exit 4, naming the entry and suggesting `/r:plan-unblock <todo>`; a missing `herdr` or `git` binary exits 127; a config or provider error exits 2 with the reader's message
- [ ] every provider named by a pipeline row, a reviewer or the watchdog is resolved and validated in preflight, before any spawn, so a wrong block fails at start naming its field
- [ ] preflight then calls `store.EnsureExcluded`, creates the run with the resolved config, writes `current` with the pid, and prints the banner: the config banner, `prompt <step>: <source>` per step, `watchdog: on|off`
- [ ] `--dry-run` prints the banner and the run list with each phase's pipeline (`plan → implement → review → land`) and exits 0 without contacting herdr, without a run directory and without a worktree
- [ ] `plain.Face` implements `core.Face.Emit`: `HH:MM:SS  phase <N>  <kind>  <state>  <provider>  <detail>` for step events, `!  phase <N> <kind>: <reason>` for warnings, and `report: <path>` at the end; `Ask` prints `?  q<seq>  phase <N> <kind>: <text>` with numbered options and returns `core.ErrNoInput`
- [ ] `app_test.go` proves each preflight refusal with its exit code, a provider block missing `kind` refused in preflight, the `--dry-run` output for the plan fixture with a `--provider implement=claude` override in the banner, and the run list under `--from` and `--phases`
**Done when:** `go build ./cmd/r-loop && go test ./internal/app/... ./internal/face/...` is green and `go run ./cmd/r-loop docs/task-loop-driver/todo.md --dry-run --plain` prints the banner and run list.

### Phase 16 — Status command and notify hooks
**Implements:** See where a run is · Be told when a run halts
**Depends on:** Phase 15
**Files:** `cmd/r-loop/main.go` (modify) · `internal/app/wire.go` (modify) · `internal/app/status.go` (new) · `internal/notify/notify.go` (new) · `internal/app/status_test.go` (new) · `internal/notify/notify_test.go` (new)
- [ ] `r-loop status [--plain]` reads `.r-loop/runs/current` (else the newest run) and `Store.Load`s it; with no run it prints `no run` and exits 0
- [ ] `status --plain` prints `run <id> <status>`, one `phase <N> <state>` per phase of the plan, `live <kind> <provider> <workspace> <elapsed>` when a step is live, `question q<seq> <text>` per open question, and `warning <reason>` per warning
- [ ] `notify.Shell` implements `core.Notifier`: `Fire(hook, env)` runs `sh -c <hook>` with `R_LOOP_RUN`, `R_LOOP_STATUS` (`halted|finished|warning`), `R_LOOP_PHASE`, `R_LOOP_STEP`, `R_LOOP_REASON`, `R_LOOP_TODO`, `R_LOOP_REPORT` added to the environment and a 60 s timeout; an empty hook is a no-op
- [ ] a hook's non-zero exit or timeout is logged to `<RunDir>/notify.log` with its output and emitted as `Event{Kind: "notify-failed"}`; it never changes the run's outcome or exit code
- [ ] `app.Wire` passes `notify.Shell` and the configured `notify.onHalt`, `onWarn`, `onDone` into the loop's `Hooks`
- [ ] `status_test.go` proves the status lines for a stored run with a live step, an open question and a warning, and `no run`; `notify_test.go` proves the environment a hook sees and that a hook exiting 1 leaves a fake loop's exit code unchanged
**Done when:** `go test ./internal/app/... ./internal/notify/...` is green.

### Phase 17 — Resume and abort
**Implements:** Resume a run that stopped · Stop a run without losing work
**Depends on:** Phase 16
**Files:** `cmd/r-loop/main.go` (modify) · `internal/app/wire.go` (modify) · `internal/app/resume.go` (new) · `internal/app/resume_test.go` (new)
**Risk:** persistence
- [ ] `r-loop resume` reads `.r-loop/runs/current`, or the newest run directory when `current` is absent, `Load`s it, and exits 2 when the run is `finished` (`nothing to resume`) or its pid is alive (`run <id> is live in pid <n>`)
- [ ] before re-running, `Repo.Dirty(.r-loop/wt/phase-<N>)` for the last recorded step's worktree: any path means an unclaimed tree, because the driver commits a step's files after every `ok` sentinel; exit 2 naming the paths and the worktree with `commit or discard them, then resume`; the driver never commits or discards them itself
- [ ] otherwise the loop starts with `Resume: true`: landed phases and `ok` steps are skipped, the stopped step runs as a new attempt in a fresh session on the same worktree and branch, and a phase whose `plan` step is `ok` is never re-planned
- [ ] the resume banner names the previous attempt's session (`previous session rloop-p4-implement left in workspace w7`) above the normal banner; that session is left standing
- [ ] `r-loop abort` calls `Store.MarkAbort` for the current run and prints `abort requested for run <id>; the live step's session and worktree are left standing`; with no live run it exits 2
- [ ] a second `r-loop <todo>` while `current` names a live pid is a preflight refusal, exit 4, with `run <id> is live in pid <n>; use r-loop status, resume or abort`; a `current` whose pid is dead is cleared with a note and the new run proceeds
- [ ] `resume_test.go` proves: resume after a `failed` implement re-runs only implement as attempt 2, resume refused over an unclaimed tree naming the file, resume refused on a finished run, abort ending a fake run with the session named and `current` cleared, and the live-pid refusal
**Done when:** `go test ./internal/app/...` is green.

## Milestone 4 — Review
Contracts: `tech-design.md#milestone-4-review`

### Phase 18 — Reviewer workspaces
**Implements:** Review each phase with every configured reviewer
**Depends on:** Phase 12
**Files:** `internal/core/review.go` (new) · `internal/core/session.go` (modify) · `internal/core/runners.go` (modify) · `internal/core/review_test.go` (new)
**Risk:** concurrency
- [ ] `core.ReviewRunner{Sessions *SessionManager; Store Store}` implements `StepRunner`; `DefaultRunners` maps the check `verdict` to it, so no wiring changes
- [ ] before any spawn, every `Reviewer{Provider, Model, Effort}` in `Kind.Row.Reviewers` is resolved through `Sessions.Resolve`; an empty list, or a provider whose `ProviderArgs.Review` is empty, ends the step `failed(reviewer <name> declares no native reviewer)` or `failed(steps.review.reviewers is empty)` with nothing opened
- [ ] `SessionManager.SpawnMany(ctx, refs []StepRef) ([]*Session, error)` opens every workspace first, then starts every agent, then prompts each, so all reviewers are live before the first begins; a failure opening or starting any stops the rest, leaves what opened standing, and ends the step `failed(reviewer <name>: <herdr code>)`
- [ ] each reviewer runs in the phase worktree with agent name `rloop-p<N>-rv-<provider>`, sentinel `<RunDir>/phase-<N>/review-<provider>-a<attempt>.sentinel`, `FindingsPath` = `<RunDir>/phase-<N>/findings-<provider>.json`, and the `review` prompt rendered with that provider's `ReviewCommand`; a reviewer writes nothing inside the worktree, so no leftover commit follows its sentinel
- [ ] `WaitAll(ctx, sessions)` polls every session each tick with the single-step rules (sentinel, then the `StallGrace` stall rule, then the review row's backstop) and returns when every one has an outcome; the find half is `ok` only when every sentinel is `ok` and every findings file passes the `findings` check — valid JSON in the fixed shape `{"reviewer","findings":[{"id","title","detail","files"}]}`; the first `failed` or `stalled` reviewer names the half's outcome
- [ ] each reviewer's outcome is appended as a step record with key `(run, N, review-<provider>, attempt)` and emitted as `Event{Kind: "review-find", Fields{reviewer, state}}`
- [ ] `review_test.go` with fakes proves the open-all, start-all, prompt-all order, a reviewer with no review command failing before any open, a block reviewer passing its model and effort while a scalar reviewer passes none, two reviewers ok with both findings files, a findings file that is not valid findings JSON failing the half naming it, and one reviewer stalling while the other finishes
**Done when:** `go test ./internal/core/...` is green.

### Phase 19 — Fix session and the verdict check
**Implements:** Review each phase with every configured reviewer
**Depends on:** Phase 18
**Files:** `internal/core/review.go` (modify) · `internal/core/evidence.go` (modify) · `internal/prompts/templates/fix.md` (modify) · `internal/prompts/templates/review.md` (modify) · `internal/core/verdict_test.go` (new)
- [ ] `ReviewRunner` gains `FixRow StepRow`, set by `DefaultRunners` from the kind named `implement`; after the find half is `ok` it spawns one session on that row's provider, model and effort, in the phase worktree, with the `fix` prompt, `FindingsFiles` = `[{Reviewer, Path}]` for every reviewer, `VerdictPath` = `<RunDir>/phase-<N>/verdict.json`, sentinel `<RunDir>/phase-<N>/review-a<attempt>.sentinel`; the review row's timeout governs it, and its `StartSHA` is recorded at spawn
- [ ] `verdict.json` is `{"findings":[{"id":"<reviewer>-<n>","reviewer":"<name>","title":"…","verdict":"real"|"not-real"|"out-of-scope","severity":"P1"|"P2"|"P3"|"P4","fixed":true|false,"files":["…"]}]}`; `core.ReadVerdict(path) (Verdict, error)` rejects any other verdict or severity value naming the entry
- [ ] the `verdict` evidence check replaces the stub registered in `evidence.go`: the set of verdict ids equals the set of finding ids across every findings file — no finding without a verdict, no verdict for an unknown id, no id twice; every entry has a verdict and a severity; every `fixed: true` entry is `real` at `P1` or `P2` and each of its `files` is in `Repo.ChangedFiles(worktree, <fix StartSHA>)` — changed by the fix session itself, not by implement; every `real` entry at `P1` or `P2` is `fixed: true`; the first violation is the missing text
- [ ] the fix session's leftover files are committed as `r-loop: phase <N> review` after its `ok` sentinel, then the check runs; the review step is `ok` only when the sentinel is `ok` and the check passes
- [ ] every finding is appended as `Event{Kind: "finding", Fields{reviewer, id, title, verdict, severity, fixed}}`, which the run report lists under its reviewer
- [ ] `fix.md` names the exact JSON shape and that only `real` at `P1` or `P2` may be fixed; `review.md` names the findings JSON shape and the id rule the check relies on
- [ ] `verdict_test.go` proves the check passing on a two-reviewer fixture, failing on a finding with no verdict, on a verdict for an unknown id, on a missing severity, on a `fixed` entry whose file changed only before the fix session, and on a `real` P1 left unfixed
**Done when:** `go test ./internal/core/... ./internal/prompts/...` is green.

## Milestone 5 — The ask channel
Contracts: `tech-design.md#milestone-5-the-ask-channel`

### Phase 20 — AskServer
**Implements:** Ask the person a question
**Depends on:** Phase 11
**Files:** `internal/askmcp/server.go` (new) · `internal/core/session.go` (modify) · `internal/askmcp/server_test.go` (new)
**Risk:** concurrency
- [ ] `askmcp.Server{RunDir string}` implements `core.AskChannel` with the MCP go-sdk v1.8.0 streamable HTTP handler on `127.0.0.1:<free port>`; `Serve(ctx)` writes a 32-hex-char token to `<RunDir>/token` (mode 0600) and returns `http://127.0.0.1:<port>/mcp/<token>`
- [ ] `StepURL(key)` is `<base>/<phase>/<kind>/<attempt>`, and for a reviewer `<base>/<phase>/review-<provider>/<attempt>`; the handler resolves the asking `StepKey` from the path and answers 404 to a wrong token or an unknown path
- [ ] tool `ask_user(question: string, options?: string[]) → {answer: string}`: the call creates `Question{ID: "q<seq>", Step, Text, Options, AskedAt}`, sends it on `Questions()`, and blocks until `Answer(id, …)` or until the server's context ends, when the tool returns an error and never an invented answer
- [ ] `Answer(id, answer, by, citation)` completes the pending call and fills `Answer`, `AnsweredBy`, `Citation`, `AnsweredAt`; an unknown or already-answered id is an error
- [ ] `SessionManager` sets the step's `AskURL` from `StepURL` when the resolved provider's `Ask` is true, writing `<RunDir>/phase-<N>/<kind>-a<attempt>.mcp.json` first for a provider whose ask flag takes `{mcpConfig}`; with `ask: none` no flag is passed and `Event{Kind: "ask-none", Fields{provider}}` is emitted once per step
- [ ] `server_test.go` uses the go-sdk client against the real handler: `ask_user` blocks until `Answer` and returns the text, a wrong token is 404, two concurrent questions from two step paths get distinct ids and their own answers, and cancelling the context returns an error to the caller
**Done when:** `go test ./internal/askmcp/... ./internal/core/...` is green.

### Phase 21 — Questions in plain mode, end to end
**Implements:** Answer an agent's question without leaving the loop · Ask the person a question
**Depends on:** Phase 17, Phase 20
**Files:** `internal/app/wire.go` (modify) · `internal/face/plain/plain.go` (modify) · `internal/core/report.go` (modify) · `internal/app/ask_test.go` (new) · `internal/app/testdata/ask-agent.go` (new)
- [ ] `app.Wire` starts `askmcp.Server` for every run and hands it to the loop and the session manager; `--dry-run` starts no server
- [ ] `plain.Face.Ask` prints `?  q<seq>  phase <N> <kind>: <text>` and the numbered options, then, when stdin is a terminal, reads one line: a bare number picks that option, other text is the free answer, an empty line re-prompts; when stdin is not a terminal it prints `question q<seq> stays open — answer from the TUI or resume later` and returns `core.ErrNoInput`
- [ ] `r-loop status --plain` lists that open question, and the run keeps waiting: the step stays `waiting-input` with its backstop frozen
- [ ] the run report's Questions section lists each question with its step, the answer, who answered (`maintainer` or `watchdog`), the citation when there is one, and how long it waited; the Skips section has one `ask: none` line per step that ran without the channel
- [ ] `ask_test.go` runs the loop with a fake session host whose agent calls the real `askmcp` server: the step enters `waiting-input`, the fake clock passes the backstop with no failure, scripted stdin answers `2`, the agent receives the second option, and the step returns to `running`
- [ ] `testdata/ask-agent.go` is a stub agent that reads `R_LOOP_SENTINEL` and the step URL, calls `ask_user` with the go-sdk client, and writes the sentinel after the answer; a test runs it as a subprocess to prove the channel without herdr
**Done when:** `go test ./internal/app/... ./internal/face/...` is green.

## Milestone 6 — The TUI
Contracts: `tech-design.md#milestone-6-the-tui`

### Phase 22 — TUI: the run on one screen
**Implements:** See where a run is
**Depends on:** Phase 21
**Files:** `internal/face/tui/model.go` (new) · `internal/face/tui/view.go` (new) · `internal/face/tui/theme.go` (new) · `internal/app/wire.go` (modify) · `cmd/r-loop/main.go` (modify) · `internal/face/tui/model_test.go` (new) · `internal/face/tui/view_test.go` (new)
- [ ] `tui.Face` implements `core.Face` over a Bubble Tea v1.3.10 program; `Emit(Event)` sends the event to the program as a `tea.Msg`; without `--plain` and with a terminal on stdout the CLI uses it, otherwise the plain face; the banner's `face:` line names which
- [ ] the model keeps: run id, todo path, start time, watchdog on or off; every phase with its `PhaseState`; the live step (phase, kind, provider, model, effort, workspace id, start time, backstop remaining or `paused`); the last five warnings; the open questions
- [ ] `view.go` renders with Lip Gloss v1.1.0: a header; a 24-column phase rail with one row per phase and a glyph per state (`·` unticked, `p` planned, `i` implemented, `r` reviewed, `✓` landed); a live-step panel whose elapsed time ticks every second; a warnings and questions area; below 80 columns the rail stacks above the panel
- [ ] `theme.go` holds the Instrument colours — surface `#0F1115`, raised `#171A20`, text `#D6DAE0`, dim `#8A929E`, primary `#6E9FC4`, secondary `#E0A458`, tertiary `#8FA87F`, error `#E0736A`, outline `#2E343D` — and falls back to bold, dim and inverse only when `NO_COLOR` is set
- [ ] a `halted` or `finished` run leaves its final state on screen with the report path and, when halted, the resume line, until `q`; `ctrl+c` during a live step prints `use r-loop abort to stop the run` and does not exit
- [ ] `model_test.go` feeds one recorded event log to the plain face and to the TUI model and asserts both show the same phase states, live step and open question; `view_test.go` compares rendered frames at 120×40 and 70×30 with golden files
**Done when:** `go test ./internal/face/...` is green.

### Phase 23 — TUI input: questions, Resolve first and consent
**Implements:** Close a blocking plan decision at startup · Answer an agent's question without leaving the loop
**Depends on:** Phase 22
**Files:** `internal/face/tui/input.go` (new) · `internal/app/preflight.go` (modify) · `internal/face/tui/input_test.go` (new) · `internal/app/resolve_test.go` (new)
- [ ] `tui.Face.Ask(q)` shows the question in the questions area with numbered options; a digit picks an option, typed text is a free answer, `enter` submits, `esc` clears the draft; the answer returns to the caller and the question becomes an `answered` line naming who answered
- [ ] several open questions queue in arrival order; the area shows the count and the oldest first
- [ ] a question whose options are exactly `yes` and `no` renders as a consent line that `y` and `n` answer
- [ ] in TUI mode `app.Preflight` no longer refuses on a blocking `## Resolve first` entry: for each, in document order, it calls `Face.Ask` with the entry's `Name` and full `Body` as the text and a free-text answer; the plan format carries no recommendation field, so none is invented; `--plain` keeps exit 4
- [ ] each answer is written with `PlanSource.Stamp(todo, name, "<yyyy-mm-dd> — <answer>")`, and the stamped todo is committed in the primary tree as `plan: resolve <entry name>` before any phase is scheduled, so the run starts from a clean tree
- [ ] `input_test.go` proves an option picked by digit, a free-text answer, two queued questions answered in order, and the consent line; `resolve_test.go` proves a blocking entry stamped and committed before the loop starts, with fake plan source, repo and face
**Done when:** `go test ./internal/face/... ./internal/app/...` is green.

## Milestone 7 — The watchdog
Contracts: `tech-design.md#milestone-7-the-watchdog`

### Phase 24 — Watch core: signals, rejection and routing
**Implements:** Halt a step that has gone the wrong way
**Depends on:** Phase 13
**Files:** `internal/core/watch.go` (new) · `internal/core/watch_test.go` (new)
**Risk:** security
- [ ] `core.Watch{Store Store; Face Face; Checks []Check; Now func() time.Time; Poll time.Duration}` implements the loop's `Watcher`; `Signals()` is the channel the loop reads; `Accept(sig Signal) (Signal, error)` is the single entry point for every signal, from a driver check or from the watchdog
- [ ] `Accept` admits a signal only when `Kind` is `warn` or `halt` and `Step` names the live step or one that ended within the last `Poll`; any other kind, and any signal naming a step that is `ok`, landed or unknown, is appended with `Rejected: true` and `RejectReason` — an attempt is never dropped silently
- [ ] a rejected signal from the watchdog source is forwarded as a `halt` whose reason is `watchdog signal rejected: <reason>`, so the loop halts with exit 5 and the rejection is in the report; a rejected signal from a driver check becomes a `warn` naming the check
- [ ] an accepted `warn` is appended and forwarded; an accepted `halt` is appended and forwarded, and the loop stops the session at once — there is no confirmation step
- [ ] `type Check interface{ Name() string; Run(ctx CheckContext) []Signal }` with `CheckContext{Step StepRef; Session *Session; Started, Now time.Time; Repo Repo; Store Store; Plan Plan}`; `StepStarted` begins a ticker that runs every registered check each `Poll` until `StepEnded`, passing each signal through `Accept`; a test adds a fake check without editing any other file
- [ ] `watch_test.go` proves warn and halt forwarded, `kind: ok` and `kind: approve` rejected and recorded, a watchdog rejection turning into a halt, a signal for a landed phase rejected, and the fake check's warning arriving on a tick
**Done when:** `go test ./internal/core/...` is green.

### Phase 25 — Deterministic checks
**Implements:** Halt a step that has gone the wrong way
**Depends on:** Phase 24
**Files:** `internal/core/checks.go` (new) · `internal/core/checks_test.go` (new)
- [ ] five checks, each emitting `warn` with `Source: driver` and registered in `ShippedChecks(overtimeFactor, diffFactor float64)`: `files-outside-plan`, `step-overtime`, `diff-oversize`, `plan-touched`, `foreign-test-edit`; each fires at most once per step attempt
- [ ] `files-outside-plan`: `Repo.ChangedFiles(worktree, base)` — which includes uncommitted and untracked work — minus the phase's `Files:` paths (a directory entry covers its children; `.task-plans/` is always allowed) is non-empty → one warning listing up to ten paths
- [ ] `step-overtime`: once two phases of this run have landed, a step running longer than `overtimeFactor` (config `watchdog.overtimeFactor`, default 2) times the longest same-kind step among them → one warning naming both durations
- [ ] `diff-oversize`: once two phases have landed, `DiffStat(worktree, base)` added plus deleted beyond `diffFactor` (config `watchdog.diffFactor`, default 3) times the largest landed phase's → one warning naming both sizes
- [ ] `plan-touched`: the todo, or a `.task-plans/` file other than this phase's own plan, appears in `ChangedFiles(worktree, base)` during a step → one warning naming the file
- [ ] `foreign-test-edit`: a changed file matching `*_test.go`, `*.test.*`, `*_test.*` or `test/**` that existed at `base` and is not under a `Files:` path → one warning naming the file
- [ ] `checks_test.go` proves each check firing and not firing over the fake repo and a store holding two landed phases with durations and sizes, an uncommitted edit caught by `files-outside-plan`, and the once-per-attempt rule
**Done when:** `go test ./internal/core/...` is green.

### Phase 26 — The watchdog's MCP surface
**Implements:** Halt a step that has gone the wrong way
**Depends on:** Phase 20, Phase 24
**Files:** `internal/askmcp/watchdog.go` (new) · `internal/askmcp/server.go` (modify) · `internal/askmcp/watchdog_test.go` (new)
**Risk:** security
- [ ] `askmcp.Server` serves a second path `/mcp/watchdog/<wdToken>` on the same listener, with its own 32-hex-char token in `<RunDir>/wd-token` (mode 0600); `WatchdogURL()` returns it; this path also serves `ask_user`, as every session gets it; the step paths never list or accept the four tools below, and a call for them on a step path is 404
- [ ] tools: `signal(kind: string, step: string, reason: string, evidence: string) → {accepted: bool, reason?: string}` · `propose_remedy(class: string, command: string, why: string) → {decision: "authorised"|"refused"}` · `restart_step(step: string, addendum?: string, provider?: string) → {accepted: bool, reason?: string}` · `answer_question(id: string, answer: string, citation: string) → {accepted: bool, reason?: string}`; `step` is `phase-<N>/<kind>` and resolves to the latest attempt, except `phase-<N>/check`, which maps to `StepKey{Phase: N, Kind: "check", Attempt: 0}` with no attempt lookup
- [ ] the server delegates each tool to a handler set `WatchdogHandlers{Signal func(core.Signal) (bool, string); Propose func(class, command, why string) string; Restart func(step, addendum, provider string) (bool, string); Answer func(id, answer, citation string) (bool, string)}`; a handler left nil answers `accepted: false, reason: "not available"` (or `refused`)
- [ ] every call is appended to the run store as it arrives, before its handler runs
- [ ] `watchdog_test.go` uses the go-sdk client: `signal` on the watchdog path reaching its handler with a `core.Signal{Source: watchdog}`, the same tool on a step path answered 404, a wrong watchdog token 404, and a nil handler answering `not available`
**Done when:** `go test ./internal/askmcp/...` is green.

### Phase 27 — Watchdog session and wiring
**Implements:** Halt a step that has gone the wrong way
**Depends on:** Phase 22, Phase 25, Phase 26
**Files:** `internal/core/watchdog.go` (new) · `internal/prompts/templates/watchdog.md` (modify) · `internal/app/wire.go` (modify) · `internal/core/watchdog_test.go` (new)
- [ ] `core.Watchdog{Host SessionHost; Prompts Prompts; Provider ProviderArgs; Root, TodoPath, SpecDir, RunDir string; Allow []string}`: `Start(ctx)` calls `Host.Split("right", Root)`, starts agent `rloop-watchdog` with the watchdog provider's kind and args (its model flag and the ask flag pointed at the watchdog URL; no effort), renders `watchdog.md` with `TodoPath`, `SpecDir`, `RunDir` and the allow-list, and prompts it without wait
- [ ] `Watchdog.Notify(text string, wait bool, timeout time.Duration) error` is one `Host.Prompt`; a prompt refused with `agent_blocked` is retried once after 30 s, then recorded as `Event{Kind: "watchdog-unreachable"}` and the run continues without it
- [ ] `core.Watch` gains an optional `Dog *Watchdog`: `StepStarted` sends `step started phase-<N>/<kind> agent <name> worktree <dir> base <base>` and `StepEnded` sends `step ended phase-<N>/<kind> <state> <reason>`, both without wait
- [ ] `app.Wire` builds `core.Watch` with `ShippedChecks(watchdog.overtimeFactor, watchdog.diffFactor)` for every run and hands it to the loop as its `Watcher`; unless `--no-watchdog` it also starts the watchdog after preflight, sets `Watch.Dog`, sets the loop's `RemedyWindow` to `watchdog.remedyWindow`, and registers `Watch.Accept` as the `signal` handler of the watchdog MCP surface
- [ ] a watchdog that fails to start ends preflight with exit 4 naming the herdr code; `--no-watchdog` starts nothing, keeps the driver checks, and records `Event{Kind: "watchdog-skipped"}` once in the report
- [ ] `watchdog.md` gains the step-watching rule: after `step started`, read the agent every few minutes with `herdr agent read <name> --source recent-unwrapped --lines 200`, compare what it is doing with the phase block, confirm any suspected wrong turn with `git -C <worktree> diff <base>` before calling `signal` with `halt`, use `warn` for anything short of that, and stop watching on `step ended`
- [ ] `watchdog_test.go` proves the split-start-prompt order with the fake host, `StepStarted`/`StepEnded` prompts sent to the watchdog, the blocked retry then `watchdog-unreachable`, and that a `halt` reaching `Watch.Accept` from the MCP handler stops a fake loop with exit 5
**Done when:** `go test ./internal/core/... ./internal/app/...` is green.

### Phase 28 — Remedies and restart
**Implements:** Fix what is blocking a step · Pre-authorise the blockers worth fixing automatically
**Depends on:** Phase 27
**Files:** `internal/core/remedies.go` (new) · `internal/app/wire.go` (modify) · `internal/prompts/templates/watchdog.md` (modify) · `internal/core/remedies_test.go` (new)
**Risk:** security
- [ ] `core.Remedies{Allow []string; Face Face; Store Store; Window time.Duration; Now func() time.Time}`; classes are `deps · ports · containers · locks · restart · retry · provider`; any other class is refused naming it
- [ ] `Propose(class, command, why) string` attaches the remedy to the step the loop is holding in its remedy window, else the live step, else refuses with `no step to remedy`; a class in `Allow` is `authorised` at once with `Consent: allow-list`; otherwise `Face.Ask` with text `watchdog proposes (<class>): <command> — <why>` and options `yes`, `no`; `yes` → `authorised` with `Consent: maintainer`; `no`, `ErrNoInput` or no answer within `Window` → `refused`
- [ ] the `Remedy` record holds the command **exactly as proposed**, the class, the why, the consent and both times, and is appended before the decision returns to the watchdog; the driver never executes the command — the watchdog runs what was authorised
- [ ] `Restart(step, addendum, provider) (bool, string)` accepts only a step whose latest attempt is `failed` or `stalled` and whose loop remedy window is open, and only after an `authorised` remedy of class `restart`, `retry` (with an addendum) or `provider` (with a provider) for that step, or when that class is in `Allow`; it sends `core.Restart{Step, Addendum, Provider}` on the watcher's `Restarts()` channel; otherwise it answers `accepted: false` with the reason (`run halted`, `step is ok`, `no authorised remedy`)
- [ ] every accepted restart is `Event{Kind: "restart", Fields{step, attempt, addendum, provider, remedy}}`; the run report lists each remedy verbatim under its step with its consent and whether a restart followed
- [ ] `app.Wire` registers `Remedies.Propose` and `Remedies.Restart` as the `propose_remedy` and `restart_step` handlers
- [ ] `watchdog.md` gains the remedy rule: diagnose, propose the exact command with `propose_remedy`, run it only when `authorised`, then call `restart_step`; never edit code, tests or the plan; never merge, push, or delete anything that holds work
- [ ] `remedies_test.go` proves an allow-listed class authorised with no face call, an unlisted class asked and refused, the verbatim record, a restart accepted after an authorised `restart` remedy and re-running the step as a new attempt with the addendum, a restart refused on an `ok` step, and a refused remedy leading to no restart
**Done when:** `go test ./internal/core/... ./internal/app/...` is green.

### Phase 29 — Question routing through the watchdog
**Implements:** Answer from the whole run's context
**Depends on:** Phase 28
**Files:** `internal/core/questions.go` (new) · `internal/app/wire.go` (modify) · `internal/prompts/templates/watchdog.md` (modify) · `internal/core/questions_test.go` (new)
- [ ] `core.QuestionRouter{Dog *Watchdog; Ask AskChannel; Store Store; Repo Repo; AnswerWindow time.Duration}` implements the watcher's `Route(ctx, q) bool`, called only after the loop has already moved the step to `waiting-input` and frozen its backstop: when a watchdog is live, each question is sent to the watchdog as `question <id> from phase-<N>/<kind>: <text> options: <…>` without wait, and waits up to `AnswerWindow` (`watchdog.answerWindow`, default 5 min) for `answer_question`
- [ ] `Answer(id, answer, citation) (bool, string)` accepts only an open `id` with a citation matching `^[^\s:]+:\d+$` whose path, relative to the repository root, exists in the **primary tree** and is not under `.r-loop/`; the primary tree holds the spec, the tech design, the todo, the committed phase plans and every landed phase, never the current phase's worktree; an empty citation is an explicit escalation
- [ ] an accepted answer goes back through `AskChannel.Answer(id, answer, "watchdog", citation)`, is appended with `AnsweredBy: watchdog`, and is emitted as `Event{Kind: "question-answered", Fields{by, citation}}` so both faces show the citation beside the answer
- [ ] an escalation, a rejected citation or the window expiring makes `Route` return false, and the loop hands the question to `Face.Ask`; the step's backstop stays frozen throughout
- [ ] with `--no-watchdog` `Route` returns false at once
- [ ] `app.Wire` registers `QuestionRouter.Answer` as the `answer_question` handler; `watchdog.md` gains the answering rule: answer only with a `path:line` citation into the spec file, the tech-design file, the todo, a committed phase plan or code a landed phase wrote; when nothing cites it, call `answer_question` with an empty citation to escalate rather than guess
- [ ] `questions_test.go` proves a cited answer reaching the session with the citation logged, a citation to a file only in the worktree rejected then escalated, a citation under `.r-loop/` rejected, an empty citation escalating at once, the window expiring into the face, and the backstop frozen across the exchange
**Done when:** `go test ./internal/core/... ./internal/app/...` is green.

### Phase 30 — Phase check
**Implements:** Check a phase before it runs
**Depends on:** Phase 29
**Files:** `internal/core/phasecheck.go` (new) · `internal/core/watch.go` (modify) · `internal/core/watchdog.go` (modify) · `internal/core/report.go` (modify) · `internal/app/wire.go` (modify) · `internal/prompts/templates/watchdog.md` (modify) · `internal/core/phasecheck_test.go` (new)
- [ ] `core.PhaseCheck{Dog *Watchdog; Repo Repo; Timeout time.Duration}` runs from `Watch.BeforePhase`: it first ensures the phase worktree with `Repo.AddWorktree(.r-loop/wt/phase-<N>, r-loop/phase-<N>, base)` so the tree exists to read, then sends `check phase <N>` with the phase block, its `Files:` and `Risk:` lines, the worktree and the base, as `Notify(text, wait: true, Timeout)` (`watchdog.checkTimeout`, default 10 min)
- [ ] `Watch` gains a check mode: between `BeforePhase` start and return, the only step `Accept` admits is `phase-<N>/check`; a `warn` for it is accepted and forwarded; a `halt` for it is recorded `Rejected` with `phase check may only warn` and is **not** turned into a halt — the phase still runs; every other signal in that window is rejected as normal
- [ ] `app.Wire` sets `Watch.PhaseCheck` with `watchdog.checkTimeout` when the watchdog runs; an accepted warning reaches the face and the run report before the plan session is spawned, because `BeforePhase` returns only after the check prompt settles
- [ ] a timeout or `agent_blocked` on the check prompt records `Event{Kind: "phase-check-timeout"}`; with `--no-watchdog` the check records `Event{Kind: "phase-check-skipped"}`; neither halts, and the phase runs
- [ ] `core.Report` gains one `phase check` line per phase: `no disagreement`, the warning text, `timed out` or `skipped`, followed after the phase ends by whether it landed, failed or was halted — the rows the watchdog's threshold is tuned from
- [ ] `watchdog.md` gains the check: read the phase block and the tree, derive which files must change and how deep the cut is, compare with `Files:` and `Risk:`, call `signal` with `warn` and step `phase-<N>/check` once per disagreement, never rewrite the plan, never halt on this
- [ ] `phasecheck_test.go` proves the worktree created and the check prompt sent with wait before the plan spawn, a warn during the check shown ahead of the plan step's first event, a halt during the check rejected with the phase still running, and a timeout recorded without a halt
**Done when:** `go test ./internal/core/...` is green.

## Open questions

- **"Fan out reviewers using my own mechanism" has no leaf.** The spec's v1 line defers it, and
  ADR-54 supersedes the mechanism it describes. `check_todo.py --spec` still reports it as a story
  with no phase, by design.
- **A Resolve-first entry has no recommendation field.** The story "Close a blocking plan decision
  at startup" says the TUI shows each entry "with its recommendation", but the plan format written
  by `/r:spec-design` carries only Owner, Blocks, Timebox and Output. Phase 23 shows the entry's
  full text and takes a free answer. If the pack's format gains a recommendation line, Phase 23
  can offer it as the default option.
- **A remedy's result is not recorded.** The spec's domain model says a `Remedy` records "what it
  returned", but the watchdog runs the command and the spec's fixed tool list has no call that
  reports the result. The plan records the command as proposed and its consent only. A
  `remedy_result` tool would need the spec to widen the watchdog surface.

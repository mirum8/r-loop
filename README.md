# r-loop

r-loop runs a coding plan from start to finish with AI agents.

You give it a plan file. For each phase in the plan, r-loop starts fresh agent sessions
(`claude` or `codex`) in herdr panes. Each phase goes through three steps:

1. **plan** — an agent reads the phase and writes a short plan for it.
2. **implement** — an agent writes the code and the tests.
3. **land** — r-loop merges the work and runs the phase's check command. If the check passes,
   it ticks the phase in the plan file and commits.

After the plan and implement steps, other agents **review** the work in rounds. The author
agent checks each finding and fixes the real ones. r-loop commits a step's work itself, once,
after the last review round.

r-loop does not trust what an agent says. It checks real evidence on disk: the plan file, the
diff and the review verdict.

One more agent, the **watchdog**, runs for the whole run. Before the first phase it checks the
work against the code and shows you a table of what will run. Then it watches the steps, answers
the agents' questions, and asks you when it cannot answer. It is the only agent that talks to you.

## Requirements

- Go 1.25 or newer (only to build)
- git
- herdr 0.9, with its server running
- `claude` and/or `codex` CLI, logged in
- Run r-loop inside a herdr pane. The watchdog opens beside it.

## Install

```sh
git clone https://github.com/mirum8/r-loop.git
cd r-loop
./install.sh
```

`install.sh` builds the binary into `~/.local/bin/r-loop`. Set `PREFIX` to use another folder.
It also adds an r-loop block to herdr's `config.toml`, so the herdr sidebar shows each step's
state.

## Quick start

```sh
r-loop docs/my-feature/todo.md --dry-run   # check the plan and config, print the table, run nothing
r-loop docs/my-feature/todo.md             # run every open phase
```

Your git tree must be clean before a run. r-loop works in git worktrees under `.r-loop/wt/`
and keeps its run state under `.r-loop/runs/`. It adds both folders to `.git/info/exclude`,
not to `.gitignore`.

## Input files

r-loop reads two kinds of files.

**Plan file (`todo.md`)** — phases under `### Phase N — Title` headings. Each phase can have:

| Line | Meaning |
|---|---|
| `Depends on:` | Phases that must land first. If a phase is blocked, its dependents are skipped. |
| `Files:` | Files the phase may change. The watchdog warns about changes to other files. |
| `- [ ]` items | The work of the phase. |
| `Done when:` | The check command. Code spans in this line are joined with `&&` and run at land time. |

A `## Resolve first` section lists open questions and paperwork that block phases. At the start
of a run, the watchdog walks through these entries with you. Phases blocked by an entry that is
still open are skipped for this run.

r-loop checks the plan before it runs. A phase with no `- [ ]` items stops the run (exit 2).
Other problems are notes in the table: a phase with more than 12 open items, no `Done when:` or
one with no command, no `Implements:`, a risky topic with no `Risk:` line, no `Depends on:`, a
dependency on a later phase or a cycle, and two phases of one wave that change the same file.

Then the watchdog reads each phase against the code: still to **build**, **already done** (it
must cite the line that shows it), or **blocked**. It shows you the table in its pane and asks:
go, drop phases, or abort. Already-done and blocked phases are left out of the run, and so are the
phases that depend on a blocked one. r-loop does not tick an already-done phase.

**Issues file** — any markdown file with no `### Phase` heading, such as a backlog. Each
top-level list item is one issue, numbered by its place in the file. Items marked `[x]`, crossed
out or tagged `<!-- fixed: … -->` are done.

Before the first phase, the watchdog verifies each open issue against the code: fix it, or skip
it (stale, duplicate, not code work, …) with a reason. It groups the fixes that touch the same
code at a similar risk, and never puts a cosmetic fix with a deep one. Each group is one phase,
named after its lowest issue number, and lands in one commit that ticks every issue in it. You
see the groups in a table and can go, drop, split, merge or abort.

An issue has no `Done when:` line, so the plan step must write a test command for it (one for the
whole group). At land time this test must fail on the old code and pass on the new code, and the
project's full test suite must also pass.

## Command line

```
r-loop <todo.md> [flags]
r-loop <free text> [flags]
r-loop status [--plain] [<run-id>]
r-loop resume [--replan] [--unattended] [--yes] [--plain] [<run-id>]
r-loop abort
r-loop --create-config
r-loop --migrate-config
r-loop --version
```

### Run flags

| Flag | Meaning |
|---|---|
| `--from N` | Start at phase N. Skip open phases before it. |
| `--phases n,n` | Run only these phases. Each one must be open (not ticked). |
| `--provider <step>=<name>` | Use another provider for one step, for example `--provider implement=claude`. If you name the step's fallback, the fallback and the main provider swap. |
| `--model <step>=<model>` | Use another model for one step. |
| `--effort <step>=<level>` | Use another effort level for one step. |
| `--unattended` | Run with no person present. Skip the `## Resolve first` walk and the question after the triage table (the watchdog still verifies the work). Allow the remedies in `unattended.allow` without asking. |
| `--yes` | Start right after the triage without asking you. The watchdog still verifies the work, and a phase or issue it finds done or blocked is still left out. |
| `--plain` | Print plain text lines instead of the full-screen TUI. Plain is also used when stdin or stdout is not a terminal. |
| `--dry-run` | Check the plan, config, prompts and tools, print the banner, the run list, the plan-check notes and the table, then stop. It starts no session, so the table ends with `verification: not run`. |

A phase is named by its heading label: `10`, or `10a` for a phase inserted after 10 (`--phases 10a,10b`, `--from 10a`). Order is the order of the headings in the plan.

`<step>` is a row name under `steps:` in the config: `plan`, `implement`, `milestone` or `gate`,
or `intake` (see below). The three step flags change only that step, not its reviewers.

### Free text

If you don't remember the flags, write what you want instead:

```
r-loop "the task-loop plan, only phases 3 and 4, codex for implement"
```

Anything other than a single path ending in `.md` counts as free text. r-loop opens a short
intake session in herdr (the `intake:` row). The session finds the plan, works out the flags,
and shows you the full command. Say yes, and r-loop checks the command the same way as a typed
one. It prints `r-loop: resolved: r-loop <argv>` and starts that run. If the command is wrong
(a ticked phase, an unknown step), the session gets the reason and asks you again.
With `--unattended`, the intake session starts the command without asking you. Ctrl-C cancels
(exit 2).

### Subcommands

| Command | Meaning |
|---|---|
| `status` | Show the current or last run: phases, steps, questions. |
| `resume` | Continue the last halted run. Landed phases and finished steps are skipped. The step that stopped runs again as a new attempt. A run that stopped before you confirmed its table is triaged again; one that passed it keeps its run list and groups. |
| `resume --replan` | Also run the phase's `plan` step again before the step that failed. The failure reason goes into the new plan. |
| `abort` | Stop the live run. The run can be resumed later. |

### Exit codes

| Code | Meaning |
|---|---|
| `0` | All phases landed, or nothing was left to run. |
| `1` | A step failed, or the run was aborted (also at the triage question). |
| `2` | Bad usage, bad config or plan, or a git state problem. |
| `3` | A step stalled (no activity) and then failed. |
| `4` | Preflight refused to start: dirty tree, herdr not reachable, watchdog did not start, and similar. Also: the triage did not finish within `watchdog.triageTimeout`, or was interrupted. |
| `5` | The watchdog halted the run, or is gone (also during the triage). |
| `127` | `git` or `herdr` was not found. |

When a run halts, r-loop prints `r-loop resume`. The exit code comes from the first blocked
phase.

## Configuration

Each key is taken from the first place that has it:

1. a command-line flag
2. `.r-loop/config.yaml` in the project
3. `~/.config/r-loop/config.yaml`
4. the built-in defaults

`r-loop --create-config` writes the built-in defaults to `~/.config/r-loop/config.yaml` as a
starting point. It refuses to overwrite an existing file.

Every agent session names its provider, model and effort; nothing runs on a provider's own
defaults. A config that is missing one of the three is an error (exit 2).
`r-loop --migrate-config` updates configs written in the older format, where a `fallback` or a
reviewer was only a provider name. It rewrites `~/.config/r-loop/config.yaml` and the
`.r-loop/config.yaml` in the current directory, turning each bare name into a block and filling a
block's missing `model` or `effort`. The values come from the built-in defaults for that provider.
The original is kept as `config.yaml.bak`; an existing `.bak` is never overwritten. A provider with no built-in default is reported, and you have to set it by
hand (exit 1). `install.sh` runs it on the machine file.

The banner (see `--dry-run`) prints every value and where it came from. Use block-style YAML
only. Flow style (`[a, b]`, `{a: b}`) and unknown keys are errors (exit 2).

Durations use Go format: `30m`, `1h`, `2h30m`.

### `pipeline`

The steps run for each phase, in order. Default: `[plan, implement]`.

### `steps.<name>`

One row per step kind. Built-in rows: `plan`, `implement`, `milestone` (writes a report when a
milestone closes) and `gate` (finds the test suite command for an issues file).

| Key | Meaning |
|---|---|
| `prompt` | Prompt template name. |
| `check` | Evidence the step must leave: `plan-file` (a valid plan file, and no other change), `diff` (the worktree changed) or `report` (a non-empty report). |
| `provider` | Agent CLI: `claude` or `codex`, or your own provider. |
| `model` | Model name passed to the provider. |
| `effort` | Reasoning effort passed to the provider. |
| `timeout` | Hard limit for one attempt. After it, the step fails. |
| `fallback` | Session the watchdog may switch to when the step fails: a block with `provider`, `model`, `effort`. |
| `reviewers` | Review agents. See below. |
| `rounds` | Maximum review rounds. `0` turns review off. |
| `reviewTimeout` | Time limit for one review round. |

A **reviewer** is a block:

| Key | Meaning |
|---|---|
| `provider` | Required. The agent CLI. |
| `model`, `effort` | Required. |
| `name` | Reviewer id, for example `ui`. Default: the provider name. Must be unique in the row. |
| `prompt` | Prompt template. Default: `review`, which uses the provider's own review command. |
| `requires` | A path inside the repo. If the file is missing, this reviewer is skipped. |

Defaults:

| Step | Provider | Model | Effort | Timeout | Check | Reviewers | Rounds | Review timeout | Fallback |
|---|---|---|---|---|---|---|---|---|---|
| `plan` | claude | fable | medium | 1h | plan-file | codex | 2 | 20m | codex |
| `implement` | codex | gpt-5.6-sol | medium | 4h | diff | claude, ui | 3 | 45m | claude |
| `milestone` | claude | opus | medium | 1h | report | — | — | — | — |
| `gate` | claude | sonnet | medium | 30m | report | — | — | — | — |

The `ui` reviewer runs the project's `/test-app` skill (claude, opus, high). It needs
`.claude/skills/test-app/SKILL.md`, so it is skipped in projects without that file.

### `land`

| Key | Default | Meaning |
|---|---|---|
| `fixRounds` | `1` | How many times an agent may try to fix a red check command before the phase is blocked. |
| `gateTimeout` | `30m` | Time limit for the check command. |
| `fix` | implement row | Optional block with `provider`, `model`, `effort` for the fix agent. A key it leaves out comes from the implement row; naming another provider requires `model` and `effort` too. |

### `watchdog`

| Key | Default | Meaning |
|---|---|---|
| `provider`, `model`, `effort` | claude, opus, high | The watchdog agent. It has no fallback: if it does not start, the run does not start. |
| `allow` | `[]` | Remedy classes the watchdog may use without asking you. |
| `maxRestarts` | `2` | Most restarts for one step. |
| `blockerTimeout` | `10m` | How long a blocker (a failed step or reviewer, a land error, a failed gate fix or milestone report) waits for the watchdog before the phase is blocked. The clock stops while the watchdog is asking you. The old name `remedyWindow` still works. |
| `checkTimeout` | `10m` | Time limit for the watchdog's check of a phase before it starts. |
| `unblockTimeout` | `2h` | Time limit for the `## Resolve first` walk. Entries still open are deferred. |
| `triageTimeout` | `2h` | Time limit for the triage: the watchdog's check of the run list and your answer to the table. On timeout the run stops (exit 4) and `r-loop resume` triages again. |
| `stallGrace` | `2m` | Quiet time before a step counts as stalled and gets one nudge. If it is still quiet after one more `stallGrace`, it fails. |
| `overtimeFactor` | `2` | Warn when a step runs longer than this many times the longest landed step of the same kind. |
| `diffFactor` | `3` | Warn when a diff is bigger than this many times the largest landed phase. |
| `dialogs` | `[]` | Rules for answering a step's in-pane dialog (an approval or a choice menu), one sentence each. The watchdog answers a dialog a rule covers, citing the rule. Any other dialog it declines or asks you about; with `--unattended` it declines. |

Remedy classes: `deps`, `ports`, `containers`, `locks`, `restart`, `retry`, `provider`.

### `intake`

| Key | Default | Meaning |
|---|---|---|
| `provider`, `model`, `effort` | claude, sonnet, medium | The session that turns a free-text start into a command line. Any provider with `ask: mcp` works. There is no fallback: a bad value stops with exit 2. |

### `unattended`

| Key | Default | Meaning |
|---|---|---|
| `allow` | `[deps, ports, locks, restart, retry, provider]` | Added to `watchdog.allow`, only with `--unattended`. |

### `notify`

Shell commands run on run events (with `sh -c`, 60 s limit). Empty means off.

| Key | Runs when |
|---|---|
| `onHalt` | The run halts. |
| `onWarn` | A warning is raised, or a phase is blocked. |
| `onDone` | Every phase has landed. |

The command gets these environment variables: `R_LOOP_RUN`, `R_LOOP_STATUS` (`halted`,
`finished`, `warning` or `blocked`), `R_LOOP_PHASE`, `R_LOOP_STEP`, `R_LOOP_REASON`,
`R_LOOP_TODO`, `R_LOOP_REPORT`.

### `providers.<name>`

Tells r-loop how to start an agent CLI. `claude` and `codex` are built in. To change one or add a
new one, put a block under `providers:` in the project config, or a file at
`~/.config/r-loop/providers/<name>.yaml`. A block replaces the built-in one as a whole.

| Key | Meaning |
|---|---|
| `kind` | Required. The command herdr starts. |
| `flags` | Flags passed first on every start, split on spaces. No placeholders. |
| `modelFlag` | Flag template with `{model}`, for example `--model {model}`. |
| `effortFlag` | Flag template with `{effort}`. May be empty. |
| `askFlag` | Flag that connects the agent to r-loop's MCP server. Uses `{url}` or `{mcpConfig}`. |
| `dirFlag` | Flag that lets the agent write its phase's run folder, `.r-loop/runs/<run>/phase-<N>/`, where it writes its sentinel. Uses `{dir}`. Given only to sessions working in a phase worktree. Built in: `--add-dir {dir}` (claude), `-c sandbox_workspace_write.writable_roots=["{dir}"]` (codex). May be empty. |
| `doneSignal` | How the agent reports that it is done. Only `sentinel`. |
| `ask` | `mcp` (the agent can ask the watchdog) or `none`. Step agents need `mcp`. |
| `review` | The agent's own review command, for example `/code-review`. Plan reviewers never run it: they review the plan by prompt alone. |
| `reviewStart` | Screen text a review begins with, for a `review` that starts with `/`. r-loop then types `review` into the reviewer's pane itself and waits for this text, then for `reviewDone`, before it sends the prompt. Built in: `>> Code review started` (codex). Set both or neither. |
| `reviewDone` | Screen text a review ends with. Built in: `<< Code review finished` (codex). Set both or neither. |

### Example

```yaml
steps:
  implement:
    provider: claude
    model: opus
    effort: high
    fallback:
      provider: codex
      model: gpt-5.6-sol
      effort: high
    reviewers:
      - provider: codex
        model: gpt-5.6-sol
        effort: medium
    rounds: 2

watchdog:
  allow:
    - deps
    - ports

notify:
  onHalt: osascript -e 'display notification "r-loop halted" with title "r-loop"'
```

## Prompts

Every prompt is built in. To change one, copy it from `internal/prompts/templates/` to
`.r-loop/prompts/<name>.md` in your project. The banner shows which file each prompt came from.

## Run files

Each run lives in `.r-loop/runs/<runID>/`:

- `events.jsonl`, `questions.jsonl`, `signals.jsonl`, `remedies.jsonl` — append-only run state
- `config.resolved.yaml` — the config this run used
- `report.md` — what happened, updated after every change
- `triage.json`, `triage.md`, `gate.json` — the watchdog's verdicts, the table you saw, and the decision
- `phase-<N>/` — step logs, sentinels, review findings and verdicts

Phase plans are written to `.task-plans/phase-<N>-<title>.md` and committed with the phase.

## Development

```sh
go build ./...
go test ./...
go run ./cmd/r-loop docs/task-loop-driver/todo.md --dry-run --plain
```

The design is in `docs/task-loop-driver/`: `spec.html` (the spec and its decisions),
`tech-design.md` (contracts) and `todo.md` (the build plan). The TUI design system is in
`DESIGN.md`.

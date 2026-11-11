# Workflow

Two ways into r-loop: a **planned build** (idea → spec → plan → run) and a **backlog** (message → issues → run). Both end in the same `r-loop <file>` run.

```
idea ──/r:spec-brainstorm──▶ spec.html ──/r:spec-design──▶ todo.md + tech-design.md ──┐
                                                                                       ├──▶ r-loop <file>
message ──/r:issues-draft──▶ issues-<slug>-<date>.md (+ -notes.md) ────────────────────┘
```

## 1. Spec — `/r:spec-brainstorm`

Turns an idea into `docs/<topic>/spec.html`: requirements, domain, architecture, ADRs, technical details. It interviews you first, decides the rest by default, and writes nothing until you say yes.

| Variant | Use for |
|---|---|
| `/r:spec-brainstorm <idea>` | A new system or project |
| `--feature <text>` | A feature in an existing repo (reads the code first; see below) |
| `--explain` | An unfamiliar domain (adds narrative, glossary, industry research) |
| `--continue` | Resume an unfinished spec |
| `--change` | A changed requirement on a spec whose plan is partly built |

**Features.** A project keeps one **main spec**, which holds the current system and every decision. `--feature` doesn't write a second app spec. In one run, behind one approval, it writes:

- **a feature doc** `docs/<feature>/spec.html`. It is a delta with seven parts: problem & stories, baseline (`path:line` evidence), domain delta, behaviour, interface delta, fit check, and rollout & risks. It carries no ADRs.
- **changes to the main spec:**
  - the feature's ADRs, numbered after the existing ones;
  - its domain, component and API changes;
  - a `Features` link to the feature doc.

The main spec may get ahead of the code. If none exists yet, the first `--feature` run writes a thin one from the code. Then plan the feature itself: `/r:spec-design docs/<feature>/spec.html`.

## 2. Plan — `/r:spec-design`

Reads the spec and writes `todo.md` with `tech-design.md` beside it:

- `## Milestone` groups; `### Phase N — Title` leaves, each with `Depends on:`, `Files:`, `- [ ]` items and a `Done when:` command.
- `tech-design.md` holds the contracts a milestone's phases share (types, schema, signatures).
- Unknowns that need a decision go under `## Resolve first`, each with `Blocks:` naming the phases it holds up.

Optional before the run: `/r:plan-unblock` settles `## Resolve first` entries by hand. The r-loop watchdog also walks them with you at the start of a run.

## 3. Run — `r-loop`

Run it inside a herdr or herdr-app pane, from a clean git tree.

```sh
r-loop docs/<topic>/todo.md --dry-run   # check plan, config, tools; print the table; run nothing
r-loop docs/<topic>/todo.md             # run every open phase
```

What happens:

1. The watchdog opens beside r-loop, walks `## Resolve first`, then triages each phase against the code (**build**, **already done**, **blocked**) and asks: go, drop phases, or abort.
2. Each phase runs `plan → implement → land` (plus `design` first for phases that change what users see). Reviewer panes review each step in rounds; the step's own session fixes real P1/P2 findings.
3. Land runs the phase's `Done when:` command, ticks the phase in `todo.md` and commits.
4. Blockers go to the watchdog, which fixes the environment, config or plan and continues. At the end it writes `run-report.md` (finished) or `failure-report.md` (halted) under `.r-loop/runs/<runID>/`.

Useful flags: `--phases 3,4`, `--from 5`, `--provider implement=claude`, `--yes`, `--unattended`, `--plain`. Or describe the run in words: `r-loop "the task-loop plan, only phases 3 and 4"`.

After a halt:

```sh
r-loop status            # phases, steps, questions of the last run
r-loop resume            # continue; the failed step reruns as a new attempt
r-loop resume --replan   # also rerun the phase's plan step with the failure reason
r-loop abort             # stop the live run (resumable)
r-loop --cleanup         # remove leftover worktrees and phase branches (ends resumability)
```

## Issues workflow — `/r:issues-draft`

For a client email, chat dump, meeting notes or wishlist instead of a designed feature.

1. **Draft.** `/r:issues-draft` with the pasted message (or a path to it). It splits the message into asks, checks each against the code (already built? built differently? which files?), and writes:
   - `issues-<slug>-<yyyy-MM-dd>.md` — an unticked checklist, titles in the sender's wording and numbering;
   - `issues-<slug>-<yyyy-MM-dd>-notes.md` — questions for the sender, already-built findings, anything that moves architecture or the estimate.
2. **Review.** Read the notes; answer or strike the questions; edit the checklist.
3. **Fix.** Either:
   - `r-loop issues/issues-<slug>-<date>.md` — the watchdog verifies each open item (fix or skip with a reason), groups fixes that touch the same code at similar risk into phases, and shows the groups (go, drop, split, merge, abort). Each group lands in one commit that ticks its items. With no `Done when:`, the plan step writes a test that must fail on the old code and pass on the new, and the full suite must pass.
   - `/r:issues-fix <file>` — the same verify → group → fix loop inside one interactive session, for small backlogs.

An issues file is any markdown file with no `### Phase` heading; each top-level list item is one issue. Items marked `[x]`, struck out or tagged `<!-- fixed: … -->` are done.

## Which path

| You have | Start with |
|---|---|
| An idea or a feature nobody has specified | `/r:spec-brainstorm` → `/r:spec-design` → `r-loop todo.md` |
| A feature for an existing app | `/r:spec-brainstorm --feature` → `/r:spec-design docs/<feature>/spec.html` → `r-loop docs/<feature>/todo.md` |
| A written spec | `/r:spec-design` → `r-loop todo.md` |
| A list of asks, bugs or client changes | `/r:issues-draft` → `r-loop issues-….md` |
| A backlog that already exists | `r-loop <file>` or `/r:issues-fix <file>` |

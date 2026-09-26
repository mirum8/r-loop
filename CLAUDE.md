# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`r-loop` is a Go supervisor binary that runs a phased implementation plan (`todo.md`, the format written by `/r:spec-design`) or an issues file (the backlog `/r:issues-draft` writes and `/r:issues-fix` reads) to completion: for each phase it drives fresh, provider-agnostic agent sessions (claude, codex) in herdr panes through `plan → implement → land`, each step reviewed in rounds by reviewer panes beside it, with disk as the only hand-off between steps. It is run by one maintainer, usually in a herdr pane on a second screen.

**The repository has no code yet.** Everything is in the design docs; build it phase by phase from the plan.

## Source of truth

- `docs/task-loop-driver/spec.html` — the spec (stories, domain model, invariants, 82 ADRs). Decisions are settled there; don't re-decide them.
- `docs/task-loop-driver/todo.md` — the implementation plan: 17 milestones, 46 phases, each with `Depends on:`, `Files:`, checklist items and a `Done when:` command. The `## Waves` block is generated from the `Depends on` edges — regenerate, never hand-edit.
- `docs/task-loop-driver/tech-design.md` — contracts shared across phases of a milestone (types, enums, port signatures, run-dir layout, sentinel format, config resolution). Leaf items in `todo.md` repeat what they need, so an implementer working one phase can rely on that phase's block alone.
- `docs/task-loop-driver/interview-notes.md` — the interview log behind the spec.
- `DESIGN.md` — the TUI design system ("Instrument"): colour tokens, component states, layout in cells. `docs/design/variants/` holds the rejected alternatives and the layout mockups; `rail.txt` is the chosen arrangement.

## Stack (fixed by the spec)

Go 1.25.14 · module `r-loop` (no host in the path) · Bubble Tea v1.3.10 · Lip Gloss v1.1.0 · MCP go-sdk v1.8.0 (`github.com/modelcontextprotocol/go-sdk`) · yaml.v3 v3.0.1 · herdr 0.9.0 · git 2.50.1. No Python, no `claude -p`, nothing read from the skill-pack at run time.

## Commands

Each phase's `Done when:` line is its acceptance command. Typical forms:

```sh
go build ./...
go test ./internal/core/...                 # one package tree
go test ./internal/core/ -run TestName      # a single test
go run ./cmd/r-loop docs/task-loop-driver/todo.md --dry-run --plain
```

## Architecture

Hexagonal core inside one self-contained binary, supervising out-of-process agent workers.

- `internal/core` — loop, state machines, step kinds, evidence checks, session manager, review half, land gate, watch, remedies. **Imports only the standard library and itself**; a boundary test (`go list -deps`) fails on any other `r-loop/internal/` import. All ports are interfaces here, with in-memory fakes in `internal/core/fakes_test.go` so core tests need no herdr, git or terminal.
- Adapters, one package per port: `plan` (PlanSource), `config` (LoopConfig), `store` (Store), `providers`, `prompts`, `herdr` (SessionHost), `gitrepo` (Repo), `askmcp` (AskChannel: the steps' `ask_watchdog` tool + the watchdog MCP surface), `face/plain` and `face/tui` (Face), `notify`. `internal/app` does wiring, preflight, status and resume; `cmd/r-loop` is main.
- State machines: `StepState` queued→spawned→running→{ok|failed|stalled|waiting-input}; `ok`/`failed` are terminal. A re-run (resume or watchdog restart) is a new `StepKey` with `Attempt+1`, never a transition out of `failed`.
- A step signals completion via a JSON sentinel in `.r-loop/runs/<runID>/phase-<N>/`; the driver judges it against evidence (diff, plan file, verdict), never on the agent's word. After a step's work half, reviewer panes split beside it report findings and the step's own session verifies and fixes real P1/P2, in configurable rounds. The driver commits a step's work itself, once, after its last review round; nothing is committed before that.
- Run state is append-only JSONL under `.r-loop/runs/<runID>/`; a transition is appended **before** the action it describes. `.r-loop/runs/` and `.r-loop/wt/` go in `.git/info/exclude`, never `.gitignore`.
- Config resolves CLI flag → `.r-loop/config.yaml` → `~/.config/r-loop/config.yaml` → embedded defaults, with provenance per key. Block-style YAML only; flow-style nodes and unknown keys are rejected (exit 2).
- The driver is deterministic; the watchdog is the run's LLM (ADR-71): an always-on full session that does the judgement — watching steps, phase checks, answering questions, and walking `## Resolve first` like `/r:plan-unblock` (ADR-72). The driver commits and keeps the run list.
- Step agents ask the watchdog (`ask_watchdog`), which returns at once; the agent ends its turn and the driver types the answer into the pane that asked (ADR-76). Only the watchdog asks the maintainer, in its own session (ADR-73). No face asks anything, and the driver never answers a question itself: a gone watchdog halts the run.
- Both faces render the same `Event` stream. The TUI follows `DESIGN.md`: amber (`secondary`) means only "waiting for you", one row is bold (the live phase), one line per row down to 80 columns.

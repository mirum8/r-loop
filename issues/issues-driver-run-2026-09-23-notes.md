# Driver — notes on the live fix run of 23 Sep 2026

Read against `r-loop` @ `main` `feaf292` while run `20260923-153001` was live. The items are in
`issues-driver-run-2026-09-23.md`.

## Questions — need an answer before they can become work

**[#1] What should a plan review run?** Codex's `/review` reviews a diff against a base. A plan
round has only a new file under `.task-plans/`, so there may be nothing useful for it to run on.
The fix has to decide whether a plan review uses the provider's review command at all.

## Already built, or built differently than the message assumes

**[#1] The command is configured, but only mentioned in the prompt.**
The shipped codex block sets `review: "/review"` (`internal/providers/shipped/codex.yaml`), and
the round prompt says ``Run `{{.ReviewCommand}}` report-only over …``
(`internal/prompts/templates/review.md:3`, filled at `internal/core/review.go:284`). Codex handles
`/review` as a slash command only when it is typed as the whole input. Inside a long prompt it
looked for a skill by that name and reviewed by hand, and said so in its pane:

> The /review skill is not available in this session, so I'll perform the same report-only review
> directly: inspect the plan, phase requirements, tests, and relevant code, then write only the
> required findings and sentinel files outside the worktree.

The round still ended `review-find … findings=0 state=ok` and `review-clean` (phase 1 plan,
15:35). The same line showed up again in a codex reviewer of the nested `/test-app` run's phase 9
(sandbox `r-loop-sandbox-ioB9EL`), so it happens on every codex review, not just this one. Nothing in the TUI, `report.md` or the verdict shows that the configured command never ran.

**[#2] The warning comes from the check's input, not from the item.**
`PhaseCheck` formats `Files: <ph.Files>` and falls back to `none` when the list is empty
(`internal/core/phasecheck.go:43-50`). The watchdog prompt tells it to derive the real cut and
"compare that with its `Files:` and `Risk:` lines" (`internal/prompts/templates/watchdog.md:44-45`).
A backlog item never has either, because the `/r:issues-draft` format leaves them out on purpose. So
the comparison always fails. On phase 1 it raised an amber warning at 15:30:43, and the plan step
then spent an assumption answering it. Expect the same on phases 2 and 3.

**[#3] Nothing in a workspace or agent name says which run it belongs to.**
Step workspaces are labelled from the phase and kind alone (`stepLabel(key)`, used at
`internal/core/session.go:158`), and the sidebar token only says the step's state
(`internal/core/loop.go:1085-1104`). Agent names are `rloop-p<N>-<kind>` (`session.go:148`,
`review.go:271`, `internal/app/resume.go:158`). In this run, the implement step's `ui` reviewer ran
`/test-app`, which started a nested r-loop run in a sandbox. Its phase 1 plan asked herdr for
`rloop-p1-plan` while the fix run's plan agent had that name. The nested watchdog stopped on the
clash, and the reviewer aborted that run (sandbox `r-loop-sandbox-dyex60`, 15:40). It then renumbered
the sandbox plan so the phase became 9 and ran again (`r-loop-sandbox-ioB9EL`). The sidebar then
showed two `p1 plan` rows, one `done` and one `failed`, with nothing to say which run each came
from. The aborted run's workspace stays open by design, because an abort keeps it for resume.

It happened again in implement review round 2 (sandbox `r-loop-sandbox-zZKamo`, 15:56). This time
the nested watchdog **recommended stopping the other run's agent**:

> Stop that stale idle agent (`herdr agent stop rloop-p1-plan`) so the name frees up, then restart
> the step — recommended

That "stale" agent was the fix run's own phase 1 plan agent (`w9Z:p1`), and picking the
recommendation would have broken the real run. Nobody picked it: the reviewer aborted the nested
run at 15:57. A watchdog cannot tell another run's agent from a stale one of its own, because the
name carries no run. That is the strongest reason for unique names.

**[#4] Reviewers are split without the step's environment.**
A step session opens with `R_LOOP_SENTINEL`, `R_LOOP_RUN`, `R_LOOP_PHASE` and `R_LOOP_STEP`
(`internal/core/session.go:158-163`). A reviewer is split beside it with `Host.Split(target,
direction, worker.Dir)` (`internal/core/review.go:188`), which passes no environment. So the `ui`
reviewer's `/test-app` had no signal that a run was already live, and it started nested runs. The
skill now falls back to spotting a working directory under `.r-loop/wt/`, which works but relies on
a path.

**[#3] Partly done in `4077431`.** A `label` config key now marks a run's workspaces
(`◆ test p1 plan`) and agent names (`rloop-test-p1-plan`), and the sandbox config sets
`label: test`. What is left is uniqueness for runs that have no label.

**[#5] The guard cannot tell who moved `HEAD`.** `SessionManager.judge` compares `HEAD` with the
step's `StartSHA` and fails on any difference (`internal/core/session.go:408-413`). For a step that
runs in the primary checkout (the gate, the milestone), a commit by anyone, here the maintainer at
16:08:11, reads as the step's own. Phase 1's gate failed at 16:08:32, the phase was blocked, and
the watchdog's restart was refused because the phase was already blocked. Only `r-loop resume`,
after the run ended, landed it (`0b8472e`, 17:11).

**[#6] Seen in phase 3's implement step.** Codex ran `go test ./...` and reported "tests that open
local TCP listeners failed with 'bind: operation not permitted', and some app integration tests
stalled". Only the land gate, outside the sandbox, ran the whole suite. The shipped codex block
passes only `-c check_for_update_on_startup=false` (`internal/providers/shipped/codex.yaml`).

## Moves architecture or the estimate

Nothing does. #1 changes how a reviewer session is started (`internal/core/review.go`,
`internal/app/wire.go` spawn args, `review.md`), and may change what the `review` field means in
`tech-design.md`, from "a command named in the prompt" to "the session's first input". #4 adds an env map to the reviewer split (`review.go:188`, the herdr adapter's `Split`). #3 adds one config key and changes how workspace labels and agent names are built. Resume looks agents up by name (`resume.go:158`, `:285`), so the new scheme has to be what resume rebuilds. #2 is a
wording change in `phasecheck.go` and `watchdog.md`, plus the backlog flag the check already has
through `Plan.Backlog`.

## Second batch — notes on run `20260923-171431`

Read against `r-loop` @ `main` `cb62983`, after the first six landed.

**[#1/2] Four dismissals closed rounds this run.** Phase 1 implement: `claude-r1-1` (nested codex
blocked by its sandbox) and `claude-r2-1` (only half of `claude-r1-2` fixed), both `out-of-scope`.
Phase 3 implement: `claude-r1-1` (reviewer names lose `-rv-<name>`), `not-real`, citing the phase's
own plan. Phase 4: `claude-r1-6`, `not-real`. The only guard is format: a `not-real` verdict must
carry a `path:line` that exists (`internal/core/evidence.go:462-470`). Phase 6's first implement
attempt failed on exactly that, because its evidence carried prose after the `path:line`. Nothing
checks what the evidence says, and `out-of-scope` needs no evidence at all.

**[#2/2] Phase 3's first plan attempt.** The round-2 tree check failed with "reviewer modified the
tree: .task-plans/phase-3-…md" (`internal/core/review.go:360`). The watchdog found that the codex
reviewer's pane showed no edit. The plan's own session had made a late wording edit at 18:25,
after the driver took the round's snapshot. The step was restarted with a note not to edit after
the sentinel.

**[#3/2] The contract and the finding disagree.** `tech-design.md:264-267` now reads: a reviewer
agent "adds `-rv-<name>-r<round>[-a<attempt>]`… the suffix stays, while an overlong prefix is cut".
The reviewer found that `review.go` passes `-rv-<id>` into the part that gets cut
(`freeAgent(…, "-rv-"+rv.ID(), agentSuffix(…))`, `internal/core/review.go:184`). With a 5-character run token,
`rloop-<token>-p1-implement-rv-claude-r1` is already over 32 characters.

**[#4/2] Deferred in phase 1 as `claude-r2-1`.** `{args}` pastes the interactive start flags into the
shell command, now quoted (`claude-r1-2` fixed that half), but `codex exec review` rejects some of
them. Phase 6 added `-c sandbox_workspace_write.network_access=true` to the same `flags`, so the
set passed through `{args}` has grown. The first run after reinstalling will show whether codex
reviews work.

**[#5/2] Seen in phase 4's implement step.** Run by hand with `R_LOOP_LIVE_HERDR=1`, the new
environment check in `TestLiveHerdr` passed, and then the test failed in an older check:
`first agent still "idle" after interrupt`. The live test is not part of any gate, so nothing had
run it.

**[#6/2] Three so far.** `~/.codex/config.toml` holds `[projects."…/r-loop-sandbox-oirI70"]`,
`…-ioB9EL` and `…-P4x1W9`, each `trust_level = "trusted"`, one per sandbox a codex session entered.


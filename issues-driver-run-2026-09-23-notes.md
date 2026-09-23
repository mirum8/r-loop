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

## Moves architecture or the estimate

Nothing does. #1 changes how a reviewer session is started (`internal/core/review.go`,
`internal/app/wire.go` spawn args, `review.md`), and may change what the `review` field means in
`tech-design.md`, from "a command named in the prompt" to "the session's first input". #4 adds an env map to the reviewer split (`review.go:188`, the herdr adapter's `Split`). #3 adds one config key and changes how workspace labels and agent names are built. Resume looks agents up by name (`resume.go:158`, `:285`), so the new scheme has to be what resume rebuilds. #2 is a
wording change in `phasecheck.go` and `watchdog.md`, plus the backlog flag the check already has
through `Plan.Backlog`.

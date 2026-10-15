# Stability run — notes on the remaining-issues list of 15 Oct 2026

Read against `r-loop` @ `main` `81998cd` plus the uncommitted working tree; #28 and #29 were added
later, against `e80b114`. The 18 buildable asks are in `issues-stability-run-2026-10-15.md`. This file covers the 7 that did not become work, plus the
backlog items whose premise was off or that move architecture or the estimate.

## Questions — need an answer before they can become work

**[#9] Stall grace per step kind**
Not configurable today: there is one `watchdog.stallGrace` (`internal/config/reader.go:60`,
`defaults.yaml:103`, default 2m), wired once into `SessionManager.StallGrace`
(`internal/app/wire.go:534`). `core.StepRow` has only `Timeout` and `ReviewTimeout`
(`internal/core/kinds.go:13-20`). Since `run_in_background` detection now covers long Claude builds,
a per-kind grace mostly matters for codex and for foreground waits. Is there a concrete case where
2m is too short? If so, `steps.<kind>.stallGrace` with a fallback to `watchdog.stallGrace` is a small
local change.

**[#14] Triage was submitted twice**
The refusal is real and deliberate. `checkDesign` refuses any `design` flag, including `false`, on a
pipeline with no design step (`internal/core/triage.go:234-248`). ADR-92 and tech-design.md:1348-1349
require that, and `triage_test.go:738-740` pins it. The watchdog is never told to leave the field
out: neither the `submit_triage` description (`internal/askmcp/watchdog.go:439`) nor `watchdog.md`
(Design section) says so. There are two ways to go:
- keep the spec and add "omit design when there is no design step" to the prompt and the tool
  description, which is cheap and stays within the ADR;
- accept `false`, which amends ADR-92.

Either way, the refusal reason becomes visible once #8 lands.

**[#19] `gateIdle 10m` next to `gateTimeout 5m` in the banner**
They are not the same limit. `gateTimeout` caps the whole Done-when gate run. `gateIdle` kills the
gate after that long *with no output*, taking a JVM thread dump first (tech-design.md:487,
`internal/gitrepo/repo.go:805-850`). The shipped defaults are 30m / 10m (`defaults.yaml:76-77`). The
5m is the sandbox's own setting (`testdata/sandbox/.r-loop/config.yaml:52`). With `gateTimeout` ≤
`gateIdle` the idle limit simply never fires; that is not a contradiction. Should the banner mark
such an idle limit as "inert"? Otherwise, setting `gateIdle` in the sandbox config removes the
confusing line.

## Already built, by design, or built differently than the list assumed

**[#22] A codex reviewer sat idle ~28s before its prompt was typed — by design**
Implement reviewers use the `review` template, so codex there is an *exec* reviewer. `ReviewHalf.open`
launches every pane, prompts the non-exec reviewers, runs `codex review --uncommitted` itself
(`internal/core/nativereview.go`), and only then types into the codex pane
(`internal/core/review.go:377-455`). tech-design.md:593-598 and ADR-81 prescribe that order. A
logged run shows that gap can reach ~6.5 minutes. Shortening it means starting the exec run at round
start, or launching that pane later, which amends the tech-design. The plan reviewer's ~10s gap
matches codex prompt delivery (herdr's 10s `--until working` or the 10s rollout wait), but the
current logs cannot prove it.

**[#25] Dirty-tree refusal creates `.r-loop/runs/gate` and `lock` — by design**
Preflight takes the run lock (`internal/app/preflight.go:49`) before the tree check (`:70`), so no
second driver can start in between. The two files are persistent flock targets
(`internal/store/store.go:323-356`, tech-design.md:186). They are excluded from git and are left
empty. No run directory or `current` pointer is written. no_agent.sh allows them on purpose (commit
d0c665d).

**[#26] Two commits with identical subjects per landed phase — by design**
ADR-87 says "the land merge takes the implement step's subject", and rejects a separate merge
subject. The code follows it (`internal/core/land.go:508, 542-554`). `git log --first-parent` shows
one commit per phase. Changing this means amending a settled ADR.

**[#27] Attempt-scoped findings file names — already handled**
Stale findings can't be read:
- `launch` deletes each findings file before its reviewer starts (`internal/core/review.go:528`);
- reviewer sentinels, artifact dirs and agent names are attempt-scoped;
- an abort now closes the reviewer panes;
- a new attempt closes the earlier attempts' workspaces;
- resume interrupts any leftover agent.

Scoping the names would break the rebuild of earlier rounds' findings on resume
(`internal/core/review.go:60-76`) and the findings-name check (`internal/core/evidence.go:397`).

**[#7] The abort reason — narrower than the list said** *(in the backlog)*
The blocker part is already fixed. The spec does not ask for "aborted" on the step record: a
cancelled step is `failed(interrupted: <ctx error>)` (tech-design.md:396), and "aborted" belongs on
the run record and the `aborted` event, which already say so. The only defect is the
`reviewer codex:` prefix, which makes an abort read as a reviewer fault
(`internal/core/review.go:575-576, 602`).

**[#12] Background detection — the real defect is different** *(in the backlog)*
Detection is not limited to Agent/Bash: any tool call with `run_in_background: true` counts
(`internal/hooktap/hooktap.go:54`). The real problem is that the uncommitted stall fix made the flag
one-way. It clears only when the agent's next turn starts. Until then it suppresses nudges and
freezes quiet time (`internal/core/session.go:535, 556`). So an agent that starts a never-ending
background process (a dev server) and ends its turn without a sentinel sits until the step backstop,
which is **1h for plan and 4h for implement** (`defaults.yaml`), not 20m. This is a regression from
the uncommitted fix.

## Outside this repo

**[#29] `sleep 7200` orphans from the test-app terminal driver — skill-pack, not r-loop**
`tui-session.sh start` launches a TTL reaper as `nohup sh -c "sleep $ttl; tmux … kill-server; rm -rf …" &`
and stores the `sh` pid (`~/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh:181-184`).
`stop` kills that pid (`:406`), but killing the `sh` does not kill its `sleep` child. The child is
reparented to launchd and lives out the full TTL: 7200s for the live scripts that pass `--ttl 7200`
(`live_run_control.sh`, `live_full_plan.sh`). It is harmless once the tmux socket is gone, but it
piles up: five were alive after this session's live runs. The fix belongs in the skill-pack.
Either kill the reaper's whole process group (start it with `set -m`, then `kill -- -$pgid`), or run
`pkill -P <sh pid>` before killing the `sh`.

**[#28] The Gradle writer loops** are in the backlog. They came from
`internal/analyze/run_test.go:823`, whose stub detaches a `setpgrp` loop that pretends to be a Gradle
daemon. All seven leaked loops were 1–2 days old, and the many `go test ./...` runs of this session
left none behind, so the leak is intermittent. A likely cause is that Analyze lists the processes to
kill just once, at cancel time, and under load misses the stub's detached child.

## Moves architecture or the estimate

**[#4 / #5] Continue a stalled step in its own session** — in the backlog, flagged here.
Today a stall fails the step, and every remedy (retry, switch, continue) starts Attempt+1 in a fresh
session (`internal/core/loop.go:809-854`). In-flight subagents and background jobs are lost with it.
Re-nudging in place means raising the blocker while the step is still `stalled` rather than
`failed`. That reverses "a re-run is a new StepKey, never a transition out of failed" and ADR-62's
rejection of the watchdog writing to a session, so it needs ADR-62/ADR-97 amendments first. The
cheap half, the pane excerpt in the blocker, is #10, and it is only conformance with the existing
spec.

**[#6] Nudge cap** — in the backlog, flagged here.
The uncommitted stall fix already departs from ADR-62 ("nudged once, then fails"; "a stall costs at
most two grace periods") without a docs change. A cap brings the cost back close to the ADR, but the
ADR text and tech-design.md:337-342 have to be updated in the same change.

**[#21] Guard the first-prompt re-type** — in the backlog, flagged here.
`Host.Prompt` already waits for herdr to see codex start working (`internal/herdr/client.go:375-395`),
so whenever the rollout lookup is broken, every retry types into a working agent. The lookup can
break in several ways:
- a `.r-loop/prompts/` override that leaves out `{{.Sentinel}}`;
- `CODEX_HOME` being read from the driver's environment, not the pane's;
- a symlinked repo path;
- a change to codex's rollout format.

Guarding on herdr state is cheap. Checking for an empty composer means new per-provider screen
parsing, which costs more.

**[#16] Slow app tests** — in the backlog, flagged here.
One line in core (`hookStart` sleeping a full 200ms poll past its deadline, `internal/core/hooks.go:437`)
saves ~200ms per simulated session start. Making the package parallel-safe is a bigger refactor:
every fixture sets PATH for the whole process, so fake binaries need injecting through the wiring
instead.

**[#24] Live coverage for the nudge-reply and path-repair paths** — in the backlog, flagged here.
No lever is fully deterministic with real agents. Committed prompt overrides (`.r-loop/prompts/`) in
the sandbox are the most reliable. A scripted fake agent would be fully deterministic but is a
separate, larger piece of work. Each live script costs minutes per run.
